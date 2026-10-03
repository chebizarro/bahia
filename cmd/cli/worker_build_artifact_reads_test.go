package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestR3ReadRESTNostrGolden(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	serviceSK := nostr.Generate()
	workerSK := nostr.Generate()
	servicePub := nostr.GetPublicKey(serviceSK).Hex()
	workerPub := nostr.GetPublicKey(workerSK).Hex()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	serviceID := uuid.New()
	buildID := uuid.New()
	artifactID := uuid.New()
	worker := domain.Worker{PubKey: workerPub, Name: "node", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: now, Software: []domain.WorkerSoftware{{Name: "docker"}}, Pricing: []domain.WorkerPricing{{PricePerSecond: 7, Unit: "sat"}}, CreatedAt: now, UpdatedAt: now}
	build := domain.Build{ID: buildID, ServiceID: serviceID, Status: domain.BuildStatusSucceeded, GitSHA: "abcdef1234567890", GitRef: "main", CIRunID: "run-1", CreatedAt: now}
	artifact := domain.Artifact{ID: artifactID, BuildID: buildID, ServiceID: serviceID, ImageRepo: "ghcr.io/acme/api", ImageTag: "v1", ImageDigest: "sha256:abc", CreatedAt: now}
	stamp := nostr.Timestamp(time.Now().Unix())
	state := makeCLIReadEvent(t, serviceSK, kinds.CPStateFamilyWorkerState.LegacyKind(), uuid.New(), worker, stamp)
	// Worker coordinates are keyed by pubkey, not the helper's UUID d-tag.
	for i := range state.Tags {
		if len(state.Tags[i]) > 1 && state.Tags[i][0] == "d" {
			state.Tags[i][1] = kinds.WorkerStateDPrefix + workerPub
		}
	}
	require.NoError(t, state.Sign(serviceSK))
	ad := nostr.Event{Kind: nostr.Kind(kinds.LoomWorkerAdvertisement), CreatedAt: stamp, Tags: nostr.Tags{{"S", "docker"}}, Content: `{"name":"node"}`}
	require.NoError(t, ad.Sign(workerSK))
	pool := &cliReadPool{events: []nostr.Event{state, ad, makeCLIReadEvent(t, serviceSK, kinds.BuildRegistry, buildID, build, stamp), makeCLIReadEvent(t, serviceSK, kinds.ArtifactRegistry, artifactID, artifact, stamp)}}
	installCLIReadPool(t, pool)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var data any
		switch r.URL.Path {
		case "/api/v1/workers":
			data = []domain.Worker{worker}
		case "/api/v1/workers/" + workerPub:
			data = worker
		case "/api/v1/builds/" + buildID.String():
			data = build
		case "/api/v1/services/" + serviceID.String() + "/builds":
			data = []domain.Build{build}
		case "/api/v1/artifacts/" + artifactID.String():
			data = artifact
		case "/api/v1/services/" + serviceID.String() + "/artifacts":
			data = []domain.Artifact{artifact}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer server.Close()
	cases := [][]string{{"workers", "list"}, {"workers", "show", workerPub}, {"builds", "get", "--build", buildID.String()}, {"builds", "list", "--service", serviceID.String()}, {"artifacts", "get", "--artifact", artifactID.String()}, {"artifacts", "list", "--service", serviceID.String()}}
	for _, command := range cases {
		for _, format := range []string{"table", "json"} {
			t.Run(strings.Join(command, "-")+"-"+format, func(t *testing.T) {
				base := []string{"--server", server.URL, "--service-pubkey", servicePub, "--relay", "wss://fixture.invalid", "--output", format}
				rest, _, err := runReadCLI(t, append(append([]string{}, base...), append([]string{"--http-fallback"}, command...)...)...)
				require.NoError(t, err)
				got, stderr, err := runReadCLI(t, append(append([]string{}, base...), command...)...)
				require.NoError(t, err)
				require.Empty(t, stderr)
				require.Equal(t, rest, got)
			})
		}
	}
	require.Equal(t, 12, calls, "default reads must not hit HTTP")
	adFilters := 0
	for _, filter := range pool.filters {
		if len(filter.Kinds) == 1 && filter.Kinds[0] == nostr.Kind(kinds.LoomWorkerAdvertisement) {
			adFilters++
			require.Equal(t, []nostr.PubKey{nostr.GetPublicKey(workerSK)}, filter.Authors)
			require.NotContains(t, filter.Authors, nostr.GetPublicKey(serviceSK))
		}
	}
	require.Equal(t, 4, adFilters)
	adSeen := 0
	for _, filter := range pool.filters {
		if len(filter.Kinds) == 1 && filter.Kinds[0] == nostr.Kind(kinds.LoomWorkerAdvertisement) {
			adSeen++
			if adSeen > 1 {
				require.Equal(t, stamp, filter.Since, "worker advert cursor must resume")
			}
		}
	}
	for _, filter := range pool.filters {
		if len(filter.Kinds) == 1 && filter.Kinds[0] == nostr.Kind(kinds.CASControlState) && len(filter.Tags["t"]) == 4 {
			require.Contains(t, filter.Tags["t"], kinds.WorkerStateTopic)
			require.Contains(t, filter.Tags["t"], kinds.WorkerAssignmentTopic)
			require.Contains(t, filter.Tags["t"], kinds.WorkerDrainTopic)
			require.Contains(t, filter.Tags["t"], kinds.WorkerEligibilityTopic)
		}
	}
}

func TestR3BuildCursorAndStaleExit(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	sk := nostr.Generate()
	service := uuid.New()
	build := domain.Build{ID: uuid.New(), ServiceID: service, Status: domain.BuildStatusQueued, CreatedAt: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}
	stamp := nostr.Timestamp(time.Now().Unix())
	pool := &cliReadPool{events: []nostr.Event{makeCLIReadEvent(t, sk, kinds.BuildRegistry, build.ID, build, stamp)}}
	installCLIReadPool(t, pool)
	args := []string{"--service-pubkey", nostr.GetPublicKey(sk).Hex(), "--relay", "wss://fixture.invalid", "--output", "json", "builds", "list", "--service", service.String()}
	first, stderr, err := runReadCLI(t, args...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	pool.events = nil
	second, stderr, err := runReadCLI(t, args...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, first, second)
	require.Len(t, pool.filters, 2)
	require.Equal(t, stamp, pool.filters[1].Since)
	pool.stale = true
	third, stderr, err := runReadCLI(t, append([]string{"--eose-timeout", "1ms"}, args...)...)
	require.NoError(t, err, "stale cache reads exit 0")
	require.Equal(t, first, third)
	require.Contains(t, stderr, "relay data may be stale")
}

func TestR3ReadMissingAndInvalidIDs(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	sk := nostr.Generate()
	pub := nostr.GetPublicKey(sk).Hex()
	installCLIReadPool(t, &cliReadPool{})
	base := []string{"--service-pubkey", pub, "--relay", "wss://fixture.invalid"}
	for _, cmd := range [][]string{{"builds", "get", "--build", "bad"}, {"artifacts", "get", "--artifact", "bad"}, {"workers", "show", "bad"}} {
		_, _, err := runReadCLI(t, append(append([]string{}, base...), cmd...)...)
		require.Error(t, err)
	}
	_, _, err := runReadCLI(t, append(append([]string{}, base...), "artifacts", "get", "--artifact", uuid.New().String())...)
	require.ErrorContains(t, err, "not found")
}
