package client

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func signedR3State(t *testing.T, sk nostr.SecretKey, family int, id string, content any, deleted bool) nostr.Event {
	t.Helper()
	_, tags := nostrpool.ControlStateEnvelope(family, id, deleted)
	body, err := json.Marshal(content)
	require.NoError(t, err)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(time.Now().Unix()), Tags: tags, Content: string(body)}
	require.NoError(t, ev.Sign(sk))
	return ev
}

func TestR3RegistryDecodersRoundTrip(t *testing.T) {
	sk := nostr.Generate()
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	service := uuid.New()
	buildID := uuid.New()
	artifactID := uuid.New()
	workerPub := nostr.GetPublicKey(nostr.Generate()).Hex()
	build := domain.Build{ID: buildID, ServiceID: service, Status: domain.BuildStatusSucceeded, GitSHA: "abcdef", CreatedAt: now}
	artifact := domain.Artifact{ID: artifactID, BuildID: buildID, ServiceID: service, ImageRepo: "repo", ImageDigest: "sha256:abc", CreatedAt: now}
	worker := domain.Worker{PubKey: workerPub, Name: "node", SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: now, Pricing: []domain.WorkerPricing{{PricePerSecond: 7}}}
	assignment := domain.WorkerAssignmentState{WorkerPubKey: workerPub, UpdatedAt: now}
	drain := domain.WorkerDrainStatus{WorkerPubKey: workerPub, SchedulingState: domain.WorkerSchedulingActive, UpdatedAt: now}
	eligibility := domain.WorkerEligibilityPreview{PreviewID: "preview", UpdatedAt: now}
	tests := []struct {
		name    string
		family  int
		id      string
		content any
		decode  func(nostr.Event) (any, error)
	}{
		{"build", kinds.BuildRegistry, buildID.String(), build, func(ev nostr.Event) (any, error) { return DecodeBuild(ev) }},
		{"artifact", kinds.ArtifactRegistry, artifactID.String(), artifact, func(ev nostr.Event) (any, error) { return DecodeArtifact(ev) }},
		{"worker-state", kinds.CPStateFamilyWorkerState.LegacyKind(), workerPub, worker, func(ev nostr.Event) (any, error) { return DecodeWorkerState(ev) }},
		{"worker-assignment", kinds.CPStateFamilyWorkerAssignment.LegacyKind(), workerPub, assignment, func(ev nostr.Event) (any, error) { return DecodeWorkerAssignment(ev) }},
		{"worker-drain", kinds.CPStateFamilyWorkerDrain.LegacyKind(), workerPub, drain, func(ev nostr.Event) (any, error) { return DecodeWorkerDrain(ev) }},
		{"worker-eligibility", kinds.CPStateFamilyWorkerEligibility.LegacyKind(), "preview", eligibility, func(ev nostr.Event) (any, error) { return DecodeWorkerEligibility(ev) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := signedR3State(t, sk, tc.family, tc.id, tc.content, false)
			got, err := tc.decode(ev)
			require.NoError(t, err)
			want, _ := json.Marshal(tc.content)
			actual, _ := json.Marshal(got)
			require.JSONEq(t, string(want), string(actual))
			tomb := signedR3State(t, sk, tc.family, tc.id, map[string]any{"deleted": true}, true)
			got, err = tc.decode(tomb)
			require.NoError(t, err)
			require.Nil(t, got)
		})
	}
}

func TestR3WorkerAdvertisementUsesWorkerAuthorAndCursor(t *testing.T) {
	serviceSK := nostr.Generate()
	workerSK := nostr.Generate()
	workerPub := nostr.GetPublicKey(workerSK)
	ad := nostr.Event{Kind: nostr.Kind(kinds.LoomWorkerAdvertisement), CreatedAt: nostr.Timestamp(time.Now().Unix()), Content: `{"name":"node"}`, Tags: nostr.Tags{{"S", "docker", "27", "/usr/bin/docker"}, {"price", "https://mint.example", "7", "sat"}, {"runtime", "vllm"}}}
	require.NoError(t, ad.Sign(workerSK))
	pool := &fakePool{events: []*nostr.Event{&ad}}
	nc, err := NewNostrClient(NostrClientConfig{StorePath: filepath.Join(t.TempDir(), "events.db"), ServicePubkey: nostr.GetPublicKey(serviceSK).Hex(), Pool: pool})
	require.NoError(t, err)
	defer nc.Close()
	events, result, err := nc.SyncAndQueryWorkerAdvertisements(t.Context(), []string{workerPub.Hex()})
	require.NoError(t, err)
	require.True(t, result.Fresh)
	require.Len(t, events, 1)
	worker, err := DecodeWorkerAdvertisement(events[0])
	require.NoError(t, err)
	require.Equal(t, workerPub.Hex(), worker.PubKey)
	require.Equal(t, "node", worker.Name)
	require.Equal(t, "docker", worker.Software[0].Name)
	require.Equal(t, "27", worker.Software[0].Version)
	require.Equal(t, 7, worker.Pricing[0].PricePerSecond)
	require.Equal(t, domain.WorkerStatusOnline, worker.Status)
	_, _, err = nc.SyncAndQueryWorkerAdvertisements(t.Context(), []string{"bad"})
	require.Error(t, err)
}
