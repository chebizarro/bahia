package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

var errSimulatedCrash = errors.New("simulated process loss")

type crashInitiationStore struct {
	InitiationStore
	stage  InitiationStage
	before bool
}

func (s *crashInitiationStore) Advance(ctx context.Context, from InitiationStage, rec *InitiationRecord) error {
	if rec.Stage == s.stage && s.before {
		return errSimulatedCrash
	}
	if err := s.InitiationStore.Advance(ctx, from, rec); err != nil {
		return err
	}
	if rec.Stage == s.stage {
		return errSimulatedCrash
	}
	return nil
}

type initiationRelay struct {
	mu            sync.Mutex
	signer        nostr.Signer
	events        map[nostr.ID]nostr.Event
	sends         map[nostr.Kind]int
	failKind      nostr.Kind
	acceptOnError bool
	entered       chan struct{}
	release       chan struct{}
}

func newInitiationRelay(t *testing.T) *initiationRelay {
	return &initiationRelay{signer: newTestSigner(t), events: map[nostr.ID]nostr.Event{}, sends: map[nostr.Kind]int{}}
}

func (r *initiationRelay) Publish(_ context.Context, event nostr.Event) (int, error) {
	if event.Kind == nostr.Kind(kinds.HiveCIWorkflowRun) && r.entered != nil {
		close(r.entered)
		<-r.release
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends[event.Kind]++
	if event.Kind == r.failKind {
		if r.acceptOnError {
			r.events[event.ID] = event
		}
		return 0, fmt.Errorf("relay acknowledgment lost")
	}
	r.events[event.ID] = event
	return 1, nil
}

func (r *initiationRelay) SubmitJob(ctx context.Context, job loomAdapter.JobRequest) (string, error) {
	args := append(nostr.Tag{"args"}, job.Args...)
	event := nostr.Event{Kind: nostr.Kind(kinds.LoomJobRequest), CreatedAt: nostr.Now(), Tags: nostr.Tags{
		{"e", job.ReferencedEventID}, {"cmd", job.Cmd}, {"p", job.AllowedWorkerPubkeys[0]}, args,
	}}
	if err := controlplane.SignGoNostrEvent(ctx, r.signer, &event); err != nil {
		return "", err
	}
	_, err := r.Publish(ctx, event)
	return event.ID.Hex(), err
}

func (r *initiationRelay) FindPublishedEvent(_ context.Context, filter nostr.Filter) (*nostr.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if filter.Matches(event) {
			return &event, nil
		}
	}
	return nil, nil
}

func (r *initiationRelay) assertOneBuild(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kind := range []nostr.Kind{nostr.Kind(kinds.HiveCIWorkflowRun), nostr.Kind(kinds.LoomJobRequest), nostr.Kind(kinds.CASControlState)} {
		require.Equal(t, 1, r.sends[kind], "kind %d must be sent exactly once", kind)
	}
}

func restartInitiator(t *testing.T, original *Initiator, store InitiationStore, relay *initiationRelay) *Initiator {
	t.Helper()
	// A fresh object, resolver and store connection: no process-local replay state.
	resolver := &fakeSecretResolver{known: original.secrets.(*fakeSecretResolver).known}
	return NewInitiator(original.client, resolver, relay, relay.signer, store, original.cfg, nil,
		WithLoomJobSubmitter(relay), WithPublicationInspector(relay))
}

func proveInitiationResume(t *testing.T, freshStore func(t *testing.T) (InitiationStore, func() InitiationStore)) {
	t.Helper()
	cuts := []struct {
		stage  InitiationStage
		before bool
	}{
		{StageRequestReady, true}, // Claim committed, no external publication.
		{StageRequestReady, false},
		{StageRequestPublished, true}, // Relay accepted; DB acknowledgment lost.
		{StageRequestPublished, false},
		{StageJobPublished, true},
		{StageJobPublished, false},
		{StageEvidenceReady, false},
		{StageEvidencePublished, true},
		{StageEvidencePublished, false},
	}
	for _, cut := range cuts {
		t.Run(fmt.Sprintf("%s/before=%t", cut.stage, cut.before), func(t *testing.T) {
			store, restartStore := freshStore(t)
			server := httptest.NewServer((&fakeGitea{}).handler(t))
			defer server.Close()
			original, _, _, credential, _ := newConformanceInitiator(t, server)
			req := arcanaStartRequest(credential)
			relay := newInitiationRelay(t)
			initiator := restartInitiator(t, original, &crashInitiationStore{store, cut.stage, cut.before}, relay)
			_, err := initiator.StartHiveCIBuild(context.Background(), req)
			require.ErrorIs(t, err, errSimulatedCrash)
			canonicalID := req.BuildID
			req.BuildID = uuid.New() // A caller cannot replace the durable identity.
			restarted := restartInitiator(t, original, restartStore(), relay)
			result, err := restarted.StartHiveCIBuild(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, canonicalID, result.BuildID)
			relay.assertOneBuild(t)
			replayed, err := restarted.StartHiveCIBuild(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, result, replayed)
			relay.assertOneBuild(t)
		})
	}
}

func TestInitiationResumeEveryDurableBoundary(t *testing.T) {
	proveInitiationResume(t, func(*testing.T) (InitiationStore, func() InitiationStore) {
		store := NewMemoryInitiationStore()
		return store, func() InitiationStore { return store }
	})
}

func TestInitiationUnconfirmedPublishNeverBecomesSuccessOrRedispatch(t *testing.T) {
	for _, kind := range []nostr.Kind{nostr.Kind(kinds.HiveCIWorkflowRun), nostr.Kind(kinds.LoomJobRequest), nostr.Kind(kinds.CASControlState)} {
		for _, accepted := range []bool{false, true} {
			t.Run(fmt.Sprintf("kind=%d/accepted=%t", kind, accepted), func(t *testing.T) {
				server := httptest.NewServer((&fakeGitea{}).handler(t))
				defer server.Close()
				original, _, _, credential, _ := newConformanceInitiator(t, server)
				relay := newInitiationRelay(t)
				relay.failKind, relay.acceptOnError = kind, accepted
				store := NewMemoryInitiationStore()
				req := arcanaStartRequest(credential)
				_, err := restartInitiator(t, original, store, relay).StartHiveCIBuild(context.Background(), req)
				require.ErrorIs(t, err, ErrPublishUnconfirmed)
				result, err := restartInitiator(t, original, store, relay).StartHiveCIBuild(context.Background(), req)
				if accepted {
					require.NoError(t, err)
					require.NotNil(t, result)
					relay.assertOneBuild(t)
				} else {
					require.ErrorIs(t, err, ErrPublishUnconfirmed)
					require.Nil(t, result)
					require.Equal(t, 1, relay.sends[kind])
				}
			})
		}
	}
}

func proveConcurrentInitiation(t *testing.T, newStore func() InitiationStore) {
	t.Helper()
	server := httptest.NewServer((&fakeGitea{}).handler(t))
	defer server.Close()
	original, _, _, credential, _ := newConformanceInitiator(t, server)
	relay := newInitiationRelay(t)
	relay.entered, relay.release = make(chan struct{}), make(chan struct{})
	req := arcanaStartRequest(credential)
	const callers = 32
	start, results := make(chan struct{}), make(chan error, callers)
	for range callers {
		initiator := restartInitiator(t, original, newStore(), relay)
		go func() {
			<-start
			_, err := initiator.StartHiveCIBuild(context.Background(), req)
			results <- err
		}()
	}
	close(start)
	<-relay.entered
	for range callers - 1 {
		err := <-results
		require.True(t, errors.Is(err, ErrInitiationConflict) || errors.Is(err, ErrPublishUnconfirmed), "unexpected concurrent result: %v", err)
	}
	close(relay.release)
	require.NoError(t, <-results)
	relay.assertOneBuild(t)
	result, err := restartInitiator(t, original, newStore(), relay).StartHiveCIBuild(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, req.BuildID, result.BuildID)
	relay.assertOneBuild(t)
}

func TestConcurrentInitiationSingleDispatch(t *testing.T) {
	store := NewMemoryInitiationStore()
	proveConcurrentInitiation(t, func() InitiationStore { return store })
}
