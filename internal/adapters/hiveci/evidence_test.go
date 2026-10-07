package hiveci

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
)

// fixedScheduling stands in for the daemon's retained worker-state record.
type fixedScheduling struct{ state domain.WorkerSchedulingState }

func (s *fixedScheduling) WorkerSchedulingState(context.Context, string) (domain.WorkerSchedulingState, error) {
	return s.state, nil
}

func openTestStore(t *testing.T) *localstore.Store {
	t.Helper()
	store, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func signedWorkerAd(t *testing.T, key nostr.SecretKey, at time.Time) *nostr.Event {
	t.Helper()
	ad := &nostr.Event{
		Kind: kinds.LoomWorkerAdvertisement, CreatedAt: nostr.Timestamp(at.Unix()),
		Tags:    nostr.Tags{{"S", "docker", "1"}, {"A", "linux-amd64"}},
		Content: `{"name":"release-worker","max_concurrent_jobs":4}`,
	}
	require.NoError(t, ad.Sign(key))
	return ad
}

// worker admission is decided on the worker's own signed advertisement
// in the local event store. There is no SQL worker row to be missing or stale.
func TestLocalReleaseEvidenceAdmitsWorkerFromTheLocalStore(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := nostr.Generate()
	ad := signedWorkerAd(t, key, now.Add(-time.Minute))
	store := openTestStore(t)
	_, err := store.SaveEvent(*ad)
	require.NoError(t, err)
	scheduling := &fixedScheduling{state: domain.WorkerSchedulingActive}
	evidence := NewLocalReleaseEvidence(store, nil, scheduling, nil, service.WorkerPressureThresholds{})
	evidence.now = func() time.Time { return now }
	capabilityBytes, _ := json.Marshal(ad.Tags)

	admission, admitted, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
	require.NoError(t, err)
	require.True(t, admitted, "admission=%+v", admission)
	require.Equal(t, ad.ID.Hex(), admission.WorkerAdEventID)
	require.Equal(t, "eligible", admission.DecisionCode)

	t.Run("capability mismatch", func(t *testing.T) {
		_, _, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), `[["S","other"]]`, ad.ID.Hex())
		require.Error(t, err, "unsigned capability was admitted")
	})
	t.Run("unknown advertisement", func(t *testing.T) {
		other := signedWorkerAd(t, nostr.Generate(), now.Add(-time.Minute))
		_, _, err := evidence.AdmitWorker(context.Background(), other.PubKey.Hex(), string(capabilityBytes), other.ID.Hex())
		require.ErrorContains(t, err, "missing")
	})
	t.Run("cordoned worker", func(t *testing.T) {
		scheduling.state = domain.WorkerSchedulingCordoned
		defer func() { scheduling.state = domain.WorkerSchedulingActive }()
		_, admitted, err := evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
		require.NoError(t, err)
		require.False(t, admitted, "cordoned worker was admitted")
	})
	t.Run("superseded advertisement", func(t *testing.T) {
		newer := signedWorkerAd(t, key, now.Add(-30*time.Second))
		_, err := store.SaveEvent(*newer)
		require.NoError(t, err)
		_, _, err = evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
		require.Error(t, err, "superseded worker advertisement was admitted")
	})
}

// A stored event is a cache entry, not a trust decision: an advertisement
// whose signature does not verify is rejected even though the store holds it
// (the store does not verify on save; the subscriber does before saving).
func TestLocalReleaseEvidenceRejectsBadSignatureFromTheStore(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	ad := signedWorkerAd(t, nostr.Generate(), now.Add(-time.Minute))
	ad.Sig[0] ^= 0xff
	store := openTestStore(t)
	_, err := store.SaveEvent(*ad)
	require.NoError(t, err)
	evidence := NewLocalReleaseEvidence(store, nil, nil, nil, service.WorkerPressureThresholds{})
	evidence.now = func() time.Time { return now }
	capabilityBytes, _ := json.Marshal(ad.Tags)
	_, _, err = evidence.AdmitWorker(context.Background(), ad.PubKey.Hex(), string(capabilityBytes), ad.ID.Hex())
	require.ErrorContains(t, err, "signature")
}
