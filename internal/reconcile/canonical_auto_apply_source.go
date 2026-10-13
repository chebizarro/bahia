package reconcile

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// CanonicalAutoApplySource never consults a repository or PostgreSQL. It
// binds the particular signed local winners it read; another winner seen on
// the next read invalidates this decision. It makes no global absence claim.
type CanonicalAutoApplySource struct {
	Runtime *CanonicalRuntimeSource
	Records secretstore.Records
	Secrets *secretstore.Resolver
}

func (s *CanonicalAutoApplySource) one(ctx context.Context, family int, coordinate string) (nostr.Event, error) {
	events, err := s.Runtime.records(ctx, family, coordinate)
	if err != nil {
		return nostr.Event{}, err
	}
	if len(events) == 0 {
		return nostr.Event{}, fmt.Errorf("%w: canonical %d coordinate %s is absent", ErrFreshDecisionRequired, family, coordinate)
	}
	return events[0], nil
}

func (s *CanonicalAutoApplySource) LoadAutoApplySnapshot(ctx context.Context, serviceID, envID uuid.UUID) (ApplySnapshot, error) {
	if s == nil || s.Runtime == nil || s.Runtime.store == nil || serviceID == uuid.Nil || envID == uuid.Nil {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: canonical snapshot source is unavailable")
	}
	svcEvent, err := s.one(ctx, kinds.ServiceRegistry, serviceID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	svcRecord, ok := decodeCanonical[domain.Service](svcEvent, serviceID.String())
	if !ok || svcRecord.Deleted || svcRecord.Value.ID != serviceID || svcRecord.Value.OrgID == uuid.Nil {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	if svcRecord.Value.RuntimeConfig != nil && svcRecord.Value.RuntimeConfig.Adopted != nil {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: adopted runtime configuration is not a credential-reference-only auto-apply input")
	}
	envEvent, err := s.one(ctx, kinds.EnvironmentRegistry, envID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	envRecord, ok := decodeCanonical[canonicalEnvironment](envEvent, envID.String())
	if !ok || envRecord.Deleted || envRecord.Value.ID != envID || envRecord.Value.OrgID != svcRecord.Value.OrgID {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	stateEvent, err := s.one(ctx, kinds.ServiceState, kinds.ServiceStateDTag(serviceID.String(), envID.String()))
	if err != nil {
		return ApplySnapshot{}, err
	}
	stateRecord, ok := decodeCanonical[canonicalState](stateEvent, kinds.ServiceStateDTag(serviceID.String(), envID.String()))
	if !ok || stateRecord.Deleted || stateRecord.Value.ServiceID != serviceID || stateRecord.Value.EnvironmentID != envID ||
		stateRecord.Value.DesiredIntentID == nil || stateRecord.Value.DesiredRuntimeState == nil ||
		validateCanonicalDesiredState(&stateRecord.Value.EnvironmentServiceState) != nil {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	spec := *stateRecord.Value.DesiredRuntimeState
	if spec.ContainsPlaintextSecret() {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: signed desired state contains a secret value")
	}
	if ext := spec.DockerExtension; ext != nil && (ext.ContainerName != "" || len(ext.HostConfig) != 0 ||
		len(ext.NetworkingConfig) != 0 || len(ext.VolumeOptions) != 0 || len(ext.Healthcheck) != 0) {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: raw Docker extensions lack credential-reference binding")
	}
	if spec.UnitRuntimeType != domain.RuntimeTypeDocker || spec.PublicRoute != nil {
		// TODO(bahia-4f10q.2.1): tool approvals need a separate effect adapter.
		// TODO(bahia-4f10q.2.2): LLM effects need a separate effect adapter.
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: only route-free Docker runtime effects have a replay-safe adapter")
	}
	unit, err := domain.NewImplicitDefaultDeploymentUnit(&envRecord.Value.Environment)
	if err != nil {
		return ApplySnapshot{}, err
	}
	if spec.DeploymentUnitID == nil &&
		(unit.Key != spec.DeploymentUnitKey || unit.RuntimeType != spec.UnitRuntimeType) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	if spec.DeploymentUnitID != nil {
		found := false
		for _, candidate := range envRecord.Value.DeploymentUnits {
			if candidate.ID == *spec.DeploymentUnitID {
				if found || candidate.EnvironmentID != envID || candidate.RuntimeType != domain.RuntimeTypeDocker ||
					candidate.Key != spec.DeploymentUnitKey {
					return ApplySnapshot{}, ErrFreshDecisionRequired
				}
				unit, found = &candidate, true
			}
		}
		if !found {
			return ApplySnapshot{}, ErrFreshDecisionRequired
		}
	}
	if unit.ReconcileMode != domain.ReconcileModeAutoApply || unit.OwnershipMode != domain.OwnershipModeBahiaManaged {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: signed deployment unit is not Bahia-managed auto_apply")
	}
	intentID := *stateRecord.Value.DesiredIntentID
	intentEvent, err := s.one(ctx, kinds.DeploymentIntentRegistry, intentID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	intentRecord, ok := decodeCanonical[domain.DeploymentIntent](intentEvent, intentID.String())
	if !ok || intentRecord.Deleted || intentRecord.Value.ID != intentID ||
		intentRecord.Value.ServiceID != serviceID || intentRecord.Value.EnvironmentID != envID ||
		intentRecord.Value.ArtifactID != spec.ArtifactID || intentRecord.Value.DesiredHash != spec.DesiredHash ||
		intentRecord.Value.DesiredState == nil || intentRecord.Value.DesiredState.ComputeDesiredHash() != spec.DesiredHash ||
		!reflect.DeepEqual(intentRecord.Value.DesiredState, &spec) ||
		(intentRecord.Value.Status != domain.IntentStatusApproved &&
			intentRecord.Value.Status != domain.IntentStatusDeploying &&
			intentRecord.Value.Status != domain.IntentStatusDeployed) ||
		(intentRecord.Value.ApprovalStatus != domain.ApprovalStatusNotRequired &&
			intentRecord.Value.ApprovalStatus != domain.ApprovalStatusApproved) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	if intentRecord.Value.RuntimeReleaseID != nil {
		return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: runtime-release effects need a canonical release/binding adapter")
	}
	artifactEvent, err := s.one(ctx, kinds.ArtifactRegistry, spec.ArtifactID.String())
	if err != nil {
		return ApplySnapshot{}, err
	}
	artifactRecord, ok := decodeCanonical[domain.Artifact](artifactEvent, spec.ArtifactID.String())
	if !ok || artifactRecord.Deleted || artifactRecord.Value.ID != spec.ArtifactID || artifactRecord.Value.ServiceID != serviceID ||
		!artifactImageMatchesSpec(artifactRecord.Value, spec.ImageRef) {
		return ApplySnapshot{}, ErrFreshDecisionRequired
	}
	decision, err := s.acceptedDecision(ctx, intentRecord.Value, svcRecord.Value.OrgID)
	if err != nil {
		return ApplySnapshot{}, err
	}
	pins := make([]ApplySecretPin, len(spec.SecretRefs))
	for i, desired := range spec.SecretRefs {
		if desired.CredentialRef == nil || s.Records == nil || s.Secrets == nil {
			return ApplySnapshot{}, fmt.Errorf("bahia-4f10q.2: secret %s has no signed Signet version pin", desired.SecretID)
		}
		if err := secretstore.ValidateRef(*desired.CredentialRef); err != nil {
			return ApplySnapshot{}, err
		}
		current, err := s.Records.CurrentSecretRef(ctx, desired.SecretID)
		if err != nil || current == nil || current.CredentialRef == nil || current.ServiceID != serviceID ||
			current.Name != desired.Name || (current.EnvironmentID != nil && *current.EnvironmentID != envID) ||
			*current.CredentialRef != *desired.CredentialRef {
			return ApplySnapshot{}, fmt.Errorf("%w: secret %s credential/version moved", ErrFreshDecisionRequired, desired.SecretID)
		}
		pins[i] = ApplySecretPin{SecretID: desired.SecretID, EnvVar: desired.EnvVar,
			CredentialID: desired.CredentialRef.CredentialID, Version: desired.CredentialRef.Version}
	}
	return ApplySnapshot{IntentID: intentID, DecisionEventID: decision.ID.Hex(),
		DecisionCoordinate: tagValue(decision.Tags, "d"), DecisionActor: decision.PubKey.Hex(),
		ServiceEventID: svcEvent.ID.Hex(), StateEventID: stateEvent.ID.Hex(), IntentEventID: intentEvent.ID.Hex(),
		EnvironmentEventID: envEvent.ID.Hex(), ArtifactEventID: artifactEvent.ID.Hex(),
		ReleaseEventID: artifactEvent.ID.Hex(), Service: svcRecord.Value, Environment: envRecord.Value.Environment,
		Unit: *unit, Spec: spec, SecretPins: pins}, nil
}

func artifactImageMatchesSpec(artifact domain.Artifact, image string) bool {
	base := strings.TrimSpace(artifact.ImageRepo)
	if base == "" || artifact.ImageDigest == "" {
		return false
	}
	if image == base+"@"+artifact.ImageDigest {
		return true
	}
	return artifact.ImageTag != "" && image == base+":"+artifact.ImageTag
}

func (s *CanonicalAutoApplySource) acceptedDecision(ctx context.Context, intent domain.DeploymentIntent, orgID uuid.UUID) (nostr.Event, error) {
	var selected nostr.Event
	var selectedStatusAt nostr.Timestamp
	for status := range s.Runtime.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{30315}, Authors: []nostr.PubKey{s.Runtime.author},
		Tags: nostr.TagMap{"status": {"accepted"}}}) {
		if !status.CheckID() || !status.VerifySignature() || tagValue(status.Tags, "domain") != "intent" ||
			tagValue(status.Tags, "t") != "intent-status" {
			continue
		}
		id, err := nostr.IDFromHex(tagValue(status.Tags, "e"))
		if err != nil {
			continue
		}
		for event := range s.Runtime.store.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}) {
			if !event.CheckID() || !event.VerifySignature() || event.ID != id ||
				tagValue(status.Tags, "p") != event.PubKey.Hex() {
				continue
			}
			parsed, err := controlplane.ParseIntent(&event)
			if err != nil || parsed.Domain != "deployment" || parsed.OrgID != orgID {
				continue
			}
			if intent.ApprovalStatus == domain.ApprovalStatusApproved {
				target := fmt.Sprint(parsed.Content["deployment_intent_id"])
				if target == "<nil>" {
					target = fmt.Sprint(parsed.Content["target_intent_id"])
				}
				if parsed.Op != "approve" || target != intent.ID.String() {
					continue
				}
			} else {
				if parsed.Op != "create" && parsed.Op != "rollback" && parsed.Op != "route-attach" {
					continue
				}
				if fmt.Sprint(intent.Metadata["nostr_event_id"]) != id.Hex() ||
					parsed.Event.PubKey.Hex() != intent.RequestedBy ||
					fmt.Sprint(parsed.Content["service_id"]) != intent.ServiceID.String() ||
					fmt.Sprint(parsed.Content["environment_id"]) != intent.EnvironmentID.String() ||
					(parsed.Op == "create" && fmt.Sprint(parsed.Content["artifact_id"]) != intent.ArtifactID.String()) {
					continue
				}
			}
			if selected.ID == nostr.ZeroID || status.CreatedAt > selectedStatusAt {
				selected = event
				selectedStatusAt = status.CreatedAt
			}
		}
	}
	if selected.ID == nostr.ZeroID {
		return nostr.Event{}, fmt.Errorf("bahia-4f10q.2: no exact accepted signed deployment decision; fresh decision required")
	}
	return selected, nil
}

func (s *CanonicalAutoApplySource) ResolvePinnedSecrets(ctx context.Context, snapshot ApplySnapshot) (map[string]string, func(), error) {
	values := make(map[string]string, len(snapshot.SecretPins))
	if len(snapshot.SecretPins) == 0 {
		return values, func() {}, nil
	}
	if s == nil || s.Secrets == nil {
		return nil, nil, fmt.Errorf("bahia-4f10q.2: Signet resolver is unavailable")
	}
	ids := make([]uuid.UUID, len(snapshot.SecretPins))
	for i, pin := range snapshot.SecretPins {
		ids[i] = pin.SecretID
	}
	resolved, err := s.Secrets.ResolveAll(ctx, ids)
	if err != nil {
		if errors.Is(err, secretstore.ErrVersionMismatch) || errors.Is(err, secretstore.ErrRevoked) ||
			errors.Is(err, secretstore.ErrExpired) || errors.Is(err, secretstore.ErrNoRecord) ||
			errors.Is(err, secretstore.ErrNotFound) {
			return nil, nil, fmt.Errorf("%w: pinned Signet credential/version is no longer current: %v", ErrFreshDecisionRequired, err)
		}
		return nil, nil, fmt.Errorf("pinned Signet secret resolution deferred: %w", err)
	}
	release := func() {
		clear(values)
		secretstore.ZeroizeResolved(resolved)
	}
	for i, item := range resolved {
		pin := snapshot.SecretPins[i]
		if item.Secret.CredentialRef == nil || item.Secret.CredentialRef.CredentialID != pin.CredentialID ||
			item.Secret.CredentialRef.Version != pin.Version || item.Value == nil {
			release()
			return nil, nil, ErrFreshDecisionRequired
		}
		values[pin.EnvVar] = string(item.Value.Bytes())
	}
	return values, release, nil
}

// DockerAutoApplyEffect covers only one managed Docker service. Compose,
// routes, tool approvals, and LLM effects remain explicit fail-closed seams.
type DockerAutoApplyEffect struct {
	Resolver interface {
		runtime.RuntimeResolver
		runtime.DeploymentUnitRuntimeResolver
	}
}

func (e DockerAutoApplyEffect) docker(snapshot ApplySnapshot) (*runtime.DockerObserver, error) {
	if e.Resolver == nil || snapshot.Unit.RuntimeType != domain.RuntimeTypeDocker {
		return nil, fmt.Errorf("bahia-4f10q.2: unsupported auto-apply runtime")
	}
	rt, err := e.Resolver.ResolveDeploymentUnit(&snapshot.Service, &snapshot.Environment, &snapshot.Unit)
	if err != nil {
		return nil, err
	}
	docker, ok := rt.(*runtime.DockerObserver)
	if !ok {
		return nil, fmt.Errorf("bahia-4f10q.2: only Docker has an effect-observation fence")
	}
	return docker, nil
}

func (e DockerAutoApplyEffect) Observe(ctx context.Context, snapshot ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	docker, err := e.docker(snapshot)
	if err != nil {
		return false, nil, err
	}
	container, err := docker.ControlClient().FindManagedContainer(ctx, &snapshot.Spec)
	if err != nil {
		return false, nil, err
	}
	if container == nil || !strings.EqualFold(container.State, "running") ||
		!runtime.ContainerDesiredHashMatches(container, &snapshot.Spec) {
		return false, nil, nil
	}
	obs, err := docker.Observe(ctx, snapshot.Spec.ServiceID, snapshot.Spec.EnvironmentID, snapshot.Spec.StableServiceKey)
	if err != nil || obs == nil || obs.ObservedContainerID != container.ID {
		return false, nil, fmt.Errorf("docker observation did not confirm the exact managed container")
	}
	return true, obs, nil
}

func (e DockerAutoApplyEffect) Apply(ctx context.Context, snapshot ApplySnapshot, secrets map[string]string) error {
	docker, err := e.docker(snapshot)
	if err != nil {
		return err
	}
	plan := &domain.DesiredEnvironmentPlan{EnvironmentID: snapshot.Environment.ID,
		Services: []domain.DesiredServiceSpec{snapshot.Spec}}
	plan.ComputeRevisionHash()
	_, err = docker.ApplyDesiredState(ctx, runtime.DesiredStateApplyRequest{
		EnvironmentPlan: plan, TargetService: &snapshot.Spec, Secrets: secrets, PullPolicy: snapshot.Spec.PullPolicy,
	})
	return err
}

var _ AutoApplySnapshotSource = (*CanonicalAutoApplySource)(nil)
var _ AutoApplyEffect = DockerAutoApplyEffect{}
