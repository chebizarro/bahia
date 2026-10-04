package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func isDefaultWorkerBuildArtifactRead(cmd *cobra.Command) bool {
	if useHTTPReadFallback(cmd) || cmd.Parent() == nil {
		return false
	}
	switch cmd.Parent().Name() + "/" + cmd.Name() {
	case "workers/list", "workers/show", "builds/get", "builds/list", "artifacts/get", "artifacts/list":
		return true
	}
	return false
}

func readCLIWorkerEvents(cmd *cobra.Command, extraAuthor string) ([]nostr.Event, []nostr.Event, error) {
	pubkey := resolveOperatorServicePubkey(cmd)
	if pubkey == "" {
		return nil, nil, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for Nostr reads")
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve relays: %w", err)
	}
	path, err := nostrServiceStorePath(pubkey)
	if err != nil {
		return nil, nil, err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return nil, nil, err
	}
	pool, closePool, err := newCLIReadPool(cmd.Context(), relays)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to relays: %w", err)
	}
	defer closePool()
	nc, err := client.NewNostrClient(client.NostrClientConfig{StorePath: path, ServicePubkey: pubkey, Pool: pool, EOSETimeout: timeout})
	if err != nil {
		return nil, nil, fmt.Errorf("create Nostr client: %w", err)
	}
	defer nc.Close()
	states, stateFresh, err := nc.SyncAndQueryWorkerFamilies(cmd.Context())
	if err != nil {
		return nil, nil, fmt.Errorf("sync worker state: %w", err)
	}
	authors := make([]string, 0)
	for _, ev := range states {
		decoded, err := client.DecodeControlStateEvent(ev)
		if err != nil {
			return nil, nil, err
		}
		if decoded.LegacyKind == kinds.CPStateFamilyWorkerState.LegacyKind() && !decoded.Deleted {
			state, err := client.DecodeWorkerState(ev)
			if err != nil {
				return nil, nil, err
			}
			if state != nil {
				authors = append(authors, state.PubKey)
			}
		}
	}
	if extraAuthor != "" {
		authors = append(authors, extraAuthor)
	}
	adverts, adFresh, err := nc.SyncAndQueryWorkerAdvertisements(cmd.Context(), authors)
	if err != nil {
		return nil, nil, fmt.Errorf("sync worker advertisements: %w", err)
	}
	if !stateFresh.Fresh || !adFresh.Fresh {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: relay data may be stale (no EOSE within timeout)")
	}
	return states, adverts, nil
}

func listCLIWorkers(cmd *cobra.Command) ([]domain.Worker, error) {
	return listCLIWorkersForAuthor(cmd, "")
}

func listCLIWorkersForAuthor(cmd *cobra.Command, extraAuthor string) ([]domain.Worker, error) {
	if useHTTPReadFallback(cmd) {
		return apiClient.ListWorkerStates(cmd.Context())
	}
	states, adverts, err := readCLIWorkerEvents(cmd, extraAuthor)
	if err != nil {
		return nil, err
	}
	workers := make(map[string]domain.Worker)
	for _, ev := range states {
		decoded, err := client.DecodeControlStateEvent(ev)
		if err != nil {
			return nil, err
		}
		switch decoded.LegacyKind {
		case kinds.CPStateFamilyWorkerState.LegacyKind():
			state, err := client.DecodeWorkerState(ev)
			if err != nil {
				return nil, err
			}
			if state != nil {
				workers[state.PubKey] = *state
			}
		case kinds.CPStateFamilyWorkerAssignment.LegacyKind():
			if _, err := client.DecodeWorkerAssignment(ev); err != nil {
				return nil, err
			}
		case kinds.CPStateFamilyWorkerDrain.LegacyKind():
			if _, err := client.DecodeWorkerDrain(ev); err != nil {
				return nil, err
			}
		case kinds.CPStateFamilyWorkerEligibility.LegacyKind():
			if _, err := client.DecodeWorkerEligibility(ev); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(adverts, func(i, j int) bool { return adverts[i].CreatedAt < adverts[j].CreatedAt })
	for _, ev := range adverts {
		ad, err := client.DecodeWorkerAdvertisement(ev)
		if err != nil {
			return nil, err
		}
		if _, exists := workers[ad.PubKey]; !exists {
			workers[ad.PubKey] = *ad
		}
	}
	result := make([]domain.Worker, 0, len(workers))
	for _, worker := range workers {
		result = append(result, worker)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PubKey < result[j].PubKey })
	return result, nil
}

func getCLIWorker(cmd *cobra.Command, pubkey string) (*domain.Worker, error) {
	if useHTTPReadFallback(cmd) {
		return apiClient.GetWorkerState(cmd.Context(), pubkey)
	}
	if _, err := nostr.PubKeyFromHex(pubkey); err != nil {
		return nil, fmt.Errorf("invalid worker pubkey %q: %w", pubkey, err)
	}
	workers, err := listCLIWorkersForAuthor(cmd, pubkey)
	if err != nil {
		return nil, err
	}
	for i := range workers {
		if strings.EqualFold(workers[i].PubKey, pubkey) {
			return &workers[i], nil
		}
	}
	return nil, fmt.Errorf("worker %s not found", pubkey)
}

func renderWorkers(workers []domain.Worker) error {
	return output(workers, []string{"PUBKEY", "NAME", "PRICE/SEC", "CAPABILITIES"}, func(w domain.Worker) []string {
		price := 0
		if len(w.Pricing) > 0 {
			price = w.Pricing[0].PricePerSecond
		}
		caps := make([]string, 0, len(w.Software))
		for _, sw := range w.Software {
			caps = append(caps, sw.Name)
		}
		joined := strings.Join(caps, ", ")
		if len(joined) > 30 {
			joined = joined[:27] + "..."
		}
		return []string{truncate(w.PubKey, 16), w.Name, fmt.Sprintf("%d sats", price), joined}
	})
}

func getCLIBuild(cmd *cobra.Command, id string) (*client.BuildDetailsResult, error) {
	want, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid build ID %q: %w", id, err)
	}
	if useHTTPReadFallback(cmd) {
		build, err := apiClient.GetBuild(cmd.Context(), id)
		return &client.BuildDetailsResult{Build: build}, err
	}
	events, err := readNostrEvents(cmd, "build", kinds.BuildRegistry)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		build, err := client.DecodeBuild(ev)
		if err != nil {
			return nil, err
		}
		if build != nil && build.ID == want {
			return &client.BuildDetailsResult{Build: build}, nil
		}
	}
	return nil, fmt.Errorf("build %s not found", id)
}

func listCLIBuilds(cmd *cobra.Command, serviceID string, limit, offset int) (*client.BuildListResult, error) {
	service, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service ID %q: %w", serviceID, err)
	}
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, fmt.Errorf("limit must be 1..200 and offset must be nonnegative")
	}
	if useHTTPReadFallback(cmd) {
		builds, err := apiClient.ListBuilds(cmd.Context(), serviceID, limit, offset)
		return &client.BuildListResult{Builds: builds, Count: len(builds), Limit: limit, Offset: offset}, err
	}
	events, err := readNostrEvents(cmd, "build", kinds.BuildRegistry)
	if err != nil {
		return nil, err
	}
	builds := make([]domain.Build, 0)
	for _, ev := range events {
		build, err := client.DecodeBuild(ev)
		if err != nil {
			return nil, err
		}
		if build != nil && build.ServiceID == service {
			builds = append(builds, *build)
		}
	}
	sort.Slice(builds, func(i, j int) bool {
		if builds[i].CreatedAt.Equal(builds[j].CreatedAt) {
			return builds[i].ID.String() > builds[j].ID.String()
		}
		return builds[i].CreatedAt.After(builds[j].CreatedAt)
	})
	count := len(builds)
	if offset >= count {
		builds = []domain.Build{}
	} else {
		builds = builds[offset:]
		if len(builds) > limit {
			builds = builds[:limit]
		}
	}
	return &client.BuildListResult{Builds: builds, Count: count, Limit: limit, Offset: offset}, nil
}

func getCLIArtifact(cmd *cobra.Command, id string) (*domain.Artifact, error) {
	want, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid artifact ID %q: %w", id, err)
	}
	if useHTTPReadFallback(cmd) {
		return apiClient.GetArtifact(cmd.Context(), id)
	}
	events, err := readNostrEvents(cmd, "artifact", kinds.ArtifactRegistry)
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		artifact, err := client.DecodeArtifact(ev)
		if err != nil {
			return nil, err
		}
		if artifact != nil && artifact.ID == want {
			return artifact, nil
		}
	}
	return nil, fmt.Errorf("artifact %s not found", id)
}

func listCLIArtifacts(cmd *cobra.Command, serviceID string, limit, offset int) ([]domain.Artifact, error) {
	service, err := uuid.Parse(serviceID)
	if err != nil {
		return nil, fmt.Errorf("invalid service ID %q: %w", serviceID, err)
	}
	if limit < 1 || limit > 200 || offset < 0 {
		return nil, fmt.Errorf("limit must be 1..200 and offset must be nonnegative")
	}
	if useHTTPReadFallback(cmd) {
		return apiClient.ListArtifacts(cmd.Context(), serviceID, limit, offset)
	}
	events, err := readNostrEvents(cmd, "artifact", kinds.ArtifactRegistry)
	if err != nil {
		return nil, err
	}
	artifacts := make([]domain.Artifact, 0)
	for _, ev := range events {
		artifact, err := client.DecodeArtifact(ev)
		if err != nil {
			return nil, err
		}
		if artifact != nil && artifact.ServiceID == service {
			artifacts = append(artifacts, *artifact)
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].CreatedAt.Equal(artifacts[j].CreatedAt) {
			return artifacts[i].ID.String() > artifacts[j].ID.String()
		}
		return artifacts[i].CreatedAt.After(artifacts[j].CreatedAt)
	})
	if offset >= len(artifacts) {
		return []domain.Artifact{}, nil
	}
	artifacts = artifacts[offset:]
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	return artifacts, nil
}

func renderArtifacts(artifacts []domain.Artifact) error {
	return output(artifacts, []string{"ID", "BUILD", "IMAGE", "DIGEST", "CREATED"}, func(a domain.Artifact) []string {
		created := ""
		if !a.CreatedAt.IsZero() {
			created = a.CreatedAt.UTC().Format(time.RFC3339)
		}
		return []string{a.ID.String(), a.BuildID.String(), a.ImageRepo + ":" + a.ImageTag, truncate(a.ImageDigest, 20), created}
	})
}
