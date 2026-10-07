package hiveci

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
)

// releaseIndexFake is the SQL accepted-release table as an index: it applies
// the same first-writer-wins rules and records every write.
type releaseIndexFake struct {
	repository.HiveCIRepository
	mu         sync.Mutex
	byIdentity map[string]string
	commits    int
	fail       bool
}

func (f *releaseIndexFake) CommitAcceptedRelease(_ context.Context, release domain.HiveCIAcceptedRelease) (domain.HiveCIReleaseCommitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	if f.fail {
		return domain.HiveCIReleaseCommitResult{}, errors.New("database unavailable")
	}
	if f.byIdentity == nil {
		f.byIdentity = map[string]string{}
	}
	if digest, ok := f.byIdentity[release.Result.ReleaseIdentity]; ok {
		if digest == release.ContentDigest {
			return domain.HiveCIReleaseCommitResult{Release: release, Replay: true}, nil
		}
		return domain.HiveCIReleaseCommitResult{}, repository.ErrHiveCIReleaseReplayConflict
	}
	f.byIdentity[release.Result.ReleaseIdentity] = release.ContentDigest
	return domain.HiveCIReleaseCommitResult{Release: release}, nil
}

func (f *releaseIndexFake) ListPolicies(context.Context) ([]domain.HiveCIPipelinePolicy, error) {
	return nil, nil
}
func (f *releaseIndexFake) ListPendingResults(context.Context) ([]domain.HiveCIWorkflowResult, error) {
	return nil, nil
}
func (f *releaseIndexFake) EnsurePipelinePolicy(context.Context, domain.HiveCIPipelinePolicy) error {
	return nil
}

func (f *releaseIndexFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits
}

// the accepted-release ledger is canonical state. A release is
// accepted with no database, an exact replay is recognised after a restart
// from the ledger record in the local event store, and a conflicting
// attestation for the same identity is quarantined canonically and rejected.
func TestReleaseIngestionIsDBLessEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := newReleaseFixture(t)
	first := startCanonicalDaemon(t, dir, nil, nil)
	ingestor := NewReleaseIngestor(f.evidence, first.repo, []string{f.attestor.Public().Hex()}, []string{f.issuer.Public().Hex()})
	ingestor.now = func() time.Time { return f.now }

	commit, err := ingestor.Ingest(ctx, f.event)
	require.NoError(t, err)
	require.False(t, commit.Replay)
	require.Equal(t, f.result.ReleaseIdentity, commit.Release.Result.ReleaseIdentity)

	replay, err := ingestor.Ingest(ctx, f.event)
	require.NoError(t, err)
	require.True(t, replay.Replay, "the same attestation is an exact replay")

	accepted, err := first.repo.canonical.ListAcceptedReleases(ctx, "")
	require.NoError(t, err)
	require.Len(t, accepted, 1, "one ledger record per release identity")
	require.Equal(t, commit.Release.ContentDigest, accepted[0].ContentDigest)
	require.Equal(t, commit.Release.Policy.ID, accepted[0].Policy.ID, "the policy snapshot is retained")
	first.close()

	// A restarted daemon reads the ledger from its local event store.
	restarted := startCanonicalDaemon(t, dir, nil, nil)
	ingestor = NewReleaseIngestor(f.evidence, restarted.repo, []string{f.attestor.Public().Hex()}, []string{f.issuer.Public().Hex()})
	ingestor.now = func() time.Time { return f.now }
	replay, err = ingestor.Ingest(ctx, f.event)
	require.NoError(t, err)
	require.True(t, replay.Replay, "restart must recognise the replay from canonical state")

	conflictResult := f.result
	conflictResult.ImageTag = "release-conflict"
	conflict := releaseEventFromResult(t, conflictResult, f.attestor, f.now.Add(time.Second))
	_, err = ingestor.Ingest(ctx, conflict)
	require.ErrorIs(t, err, repository.ErrHiveCIReleaseReplayConflict)

	accepted, err = restarted.repo.canonical.ListAcceptedReleases(ctx, f.result.ReleaseIdentity)
	require.NoError(t, err)
	require.Len(t, accepted, 1)
	require.Equal(t, commit.Release.ContentDigest, accepted[0].ContentDigest, "a conflict never replaces the accepted release")
	// The quarantine is a canonical record on its own coordinate; its public
	// tags name the identity, the accepted digest and the rejected attestation.
	quarantined := restarted.ledgerRecords(t, "conflict")
	require.Len(t, quarantined, 1)
	require.Equal(t, f.result.ReleaseIdentity, releaseTag(quarantined[0], "release"))
	require.Equal(t, commit.Release.ContentDigest, releaseTag(quarantined[0], "accepted"))
	require.Equal(t, conflict.ID.Hex(), releaseTag(quarantined[0], "result"))
	require.NotEqual(t, commit.Release.ContentDigest, releaseTag(quarantined[0], "digest"))
	require.Len(t, restarted.ledgerRecords(t, "accepted"), 1)
}

// ledgerRecords returns the daemon's retained accepted-release ledger records
// carrying the status tag, read by their public tags.
func (d *canonicalDaemon) ledgerRecords(t *testing.T, status string) []nostr.Event {
	t.Helper()
	author, err := nostr.PubKeyFromHex(d.pubkey)
	require.NoError(t, err)
	var out []nostr.Event
	for ev := range d.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"t": []string{kinds.CPStateTopicHiveCIRelease}}}) {
		if releaseTag(ev, "legacy_kind") == kinds.CPStateFamilyHiveCIRelease.TagValue() && releaseTag(ev, "status") == status {
			out = append(out, ev)
		}
	}
	return out
}

func releaseTag(ev nostr.Event, key string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

// The SQL accepted-release table is an index written after the canonical
// commit: its failure never fails the commit, and RebuildIndex replays the
// ledger into it.
func TestAcceptedReleaseSQLIndexIsWrittenAfterCanonicalState(t *testing.T) {
	ctx := context.Background()
	f := newReleaseFixture(t)
	index := &releaseIndexFake{fail: true}
	d := startCanonicalDaemon(t, t.TempDir(), index, nil)
	ingestor := NewReleaseIngestor(f.evidence, d.repo, []string{f.attestor.Public().Hex()}, []string{f.issuer.Public().Hex()})
	ingestor.now = func() time.Time { return f.now }

	commit, err := ingestor.Ingest(ctx, f.event)
	require.NoError(t, err, "an unavailable index does not fail the canonical commit")
	require.False(t, commit.Replay)
	require.Equal(t, 1, index.count(), "the index was attempted")

	index.mu.Lock()
	index.fail = false
	index.mu.Unlock()
	require.NoError(t, d.repo.RebuildIndex(ctx))
	require.Equal(t, commit.Release.ContentDigest, index.byIdentity[f.result.ReleaseIdentity], "rebuild mirrors the canonical ledger")
	require.NoError(t, d.repo.RebuildIndex(ctx), "a mirrored release is an exact replay for the index")
}
