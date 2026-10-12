package nostr

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

type observedStateRecord struct {
	Deleted              bool   `json:"deleted"`
	ServiceID            string `json:"service_id"`
	EnvironmentID        string `json:"environment_id"`
	DeploymentUnitID     string `json:"deployment_unit_id"`
	CurrentObservationID string `json:"current_observation_id"`
	ObservationID        string `json:"observation_id"`
	ObservedVersion      string `json:"observed_version"`
	ObservedImageRepo    string `json:"observed_image_repo"`
	ObservedImageDigest  string `json:"observed_image_digest"`
	ObservedContainerID  string `json:"observed_container_id"`
	ObservedHost         string `json:"observed_host"`
	ObservationSource    string `json:"observation_source"`
	HealthStatus         string `json:"health_status"`
	DriftStatus          string `json:"drift_status"`
	ObservedAt           string `json:"observed_at"`
}

// observedDeployments joins retained service, environment and runtime-state
// records from the daemon's canonical event store. SQL projection rows are
// never consulted. An absent store is distinguishable from an empty fleet.
func (p *Projector) observedDeployments(ctx context.Context) ([]observedDeploymentDiscovery, string) {
	deployments := []observedDeploymentDiscovery{}
	if p.observedState == nil || p.servicePubkey == "" {
		return deployments, "unavailable"
	}
	pubkey, err := gonostr.PubKeyFromHex(p.servicePubkey)
	if err != nil {
		p.logger.Warn("observed deployments: invalid service pubkey", zap.Error(err))
		return deployments, "unavailable"
	}
	read := func(topic string, family int, visit func(gonostr.Event)) bool {
		filter := gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}, Authors: []gonostr.PubKey{pubkey}, Tags: gonostr.TagMap{"t": {topic}}}
		for ev := range p.observedState.QueryEvents(filter) {
			if ctx.Err() != nil {
				return false
			}
			if !ev.CheckID() || !ev.VerifySignature() || ev.PubKey != pubkey || !hasEventTag(ev.Tags, "t", topic) || !hasEventTag(ev.Tags, "legacy_kind", strconv.Itoa(family)) {
				continue
			}
			visit(ev)
		}
		return true
	}
	services := map[string]domain.Service{}
	if !read(kinds.CPStateTopicServiceRegistry, KindServiceRegistry, func(ev gonostr.Event) {
		var record struct {
			domain.Service
			Deleted bool `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &record); err != nil {
			p.logger.Warn("observed deployments: invalid service record", zap.Error(err))
			return
		}
		if record.ID != uuid.Nil && !record.Deleted {
			services[record.ID.String()] = record.Service
		}
	}) {
		return deployments, "unavailable"
	}
	environments := map[string]domain.Environment{}
	if !read(kinds.CPStateTopicEnvironmentRegistry, KindEnvironmentRegistry, func(ev gonostr.Event) {
		var record struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Deleted bool   `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(ev.Content), &record); err != nil {
			p.logger.Warn("observed deployments: invalid environment record", zap.Error(err))
			return
		}
		if record.ID != "" && !record.Deleted {
			environments[record.ID] = domain.Environment{Name: record.Name}
		}
	}) {
		return deployments, "unavailable"
	}
	if !read(kinds.CPStateTopicServiceState, KindServiceState, func(ev gonostr.Event) {
		var state observedStateRecord
		if err := json.Unmarshal([]byte(ev.Content), &state); err != nil {
			p.logger.Warn("observed deployments: invalid runtime-state record", zap.Error(err))
			return
		}
		if state.Deleted || state.CurrentObservationID == "" || state.CurrentObservationID != state.ObservationID {
			return
		}
		svc, svcOK := services[state.ServiceID]
		env, envOK := environments[state.EnvironmentID]
		if !svcOK || !envOK {
			return
		}
		deployments = append(deployments, observedDeploymentDiscovery{
			ServiceID: state.ServiceID, ServiceName: svc.Name,
			EnvironmentID: state.EnvironmentID, EnvironmentName: env.Name,
			DeploymentUnitID: state.DeploymentUnitID,
			RuntimeType:      string(svc.RuntimeType), RuntimeTarget: svc.RuntimeTargetName(),
			ObservationID: state.ObservationID, ObservedVersion: state.ObservedVersion,
			ObservedImageRepo: state.ObservedImageRepo, ObservedImageDigest: state.ObservedImageDigest,
			ObservedContainerID: state.ObservedContainerID, ObservedHost: state.ObservedHost,
			ObservationSource: state.ObservationSource, HealthStatus: state.HealthStatus,
			DriftStatus: state.DriftStatus, ObservedAt: state.ObservedAt,
		})
	}) {
		return []observedDeploymentDiscovery{}, "unavailable"
	}
	sort.Slice(deployments, func(i, j int) bool {
		left := strings.ToLower(deployments[i].EnvironmentName) + "\x00" + strings.ToLower(deployments[i].ServiceName) + "\x00" + deployments[i].EnvironmentID + "\x00" + deployments[i].ServiceID + "\x00" + deployments[i].DeploymentUnitID
		right := strings.ToLower(deployments[j].EnvironmentName) + "\x00" + strings.ToLower(deployments[j].ServiceName) + "\x00" + deployments[j].EnvironmentID + "\x00" + deployments[j].ServiceID + "\x00" + deployments[j].DeploymentUnitID
		return left < right
	})
	return deployments, "available"
}

func hasEventTag(tags gonostr.Tags, key, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key && tag[1] == value {
			return true
		}
	}
	return false
}
