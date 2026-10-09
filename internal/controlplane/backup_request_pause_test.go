package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupReactorRequestIntakePausedBeforeSQL(t *testing.T) {
	for _, family := range []string{"run", "retention", "restore"} {
		for _, stored := range []string{"empty", "sql-only"} {
			t.Run(family+"/"+stored, func(t *testing.T) {
				key := nostr.Generate().Hex()
				actor := testNostrPubKeyHexFromPrivateKey(t, key)
				registry, recipe := newBackupRequestRegistryFixture()
				signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
				require.NoError(t, err)
				capture := &captureNostrPublisher{published: 1}
				reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
				reactor.backupRegistry = registry
				runExecutor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
				restoreExecutor := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
				retentionExecutor := &recordingBackupRetentionExecutor{calls: make(chan uuid.UUID, 1)}
				runResponder := &recordingBackupRunResponder{}
				restoreResponder := &recordingBackupRestoreResponder{}
				retentionResponder := &recordingBackupRetentionResponder{}
				reactor.backupExecutor, reactor.backupRestoreExecutor, reactor.backupRetentionExecutor = runExecutor, restoreExecutor, retentionExecutor
				reactor.backupResponder, reactor.backupRestoreResponder, reactor.backupRetentionResponder = runResponder, restoreResponder, retentionResponder
				coord := "paused:" + family
				var request *nostr.Event
				var resultKind int
				var invoke func(*Reactor)
				switch family {
				case "run":
					resultKind = KindBackupRunResult
					request = signedLLMRequest(t, key, KindBackupRunRequest, `{"recipe":"recipe:daily:v1"}`, nostr.Tags{{"d", coord}, {"recipe", "recipe:daily:v1"}})
					invoke = func(r *Reactor) { r.handleBackupRunRequest(t.Context(), request) }
					if stored == "sql-only" {
						id := uuid.New()
						registry.runs[id] = &domain.BackupRun{ID: id, RequestedBy: actor, RequestKind: KindBackupRunRequest, RequestDTag: coord, Status: domain.RunStatusSucceeded}
						registry.coordinates[backupCoordinate(actor, KindBackupRunRequest, coord)] = id
					}
				case "retention":
					resultKind = KindBackupRetentionResult
					policyID := registry.firstPolicyID()
					request = signedLLMRequest(t, key, KindBackupRetentionEnforce,
						fmt.Sprintf(`{"repository_id":%q,"policy_id":%q,"dry_run":true}`, recipe.RepositoryID, policyID), nostr.Tags{{"d", coord}})
					invoke = func(r *Reactor) { r.handleBackupRetentionRequest(t.Context(), request) }
					if stored == "sql-only" {
						id := uuid.New()
						registry.retentionRuns[id] = &domain.BackupRetentionRun{ID: id, RequestedBy: actor, RequestKind: KindBackupRetentionEnforce, RequestDTag: coord, Status: domain.RunStatusSucceeded}
						registry.retentionCoords[backupCoordinate(actor, KindBackupRetentionEnforce, coord)] = id
					}
				case "restore":
					resultKind = KindBackupRestoreResult
					sourceID := uuid.New()
					request = signedLLMRequest(t, key, KindBackupRestoreRequest,
						fmt.Sprintf(`{"backup_run_id":%q,"restore_target_ref":"fs:/restore"}`, sourceID), nostr.Tags{{"d", coord}})
					invoke = func(r *Reactor) { r.handleBackupRestoreRequest(t.Context(), request) }
					if stored == "sql-only" {
						id := uuid.New()
						registry.restores[id] = &domain.BackupRestoreRun{ID: id, RequestedBy: actor, RequestKind: KindBackupRestoreRequest, RequestDTag: coord, Status: domain.RunStatusSucceeded}
						registry.restoreCoords[backupCoordinate(actor, KindBackupRestoreRequest, coord)] = id
					}
				}
				runCount, restoreCount, retentionCount := len(registry.runs), len(registry.restores), len(registry.retentionRuns)
				invoke(reactor)
				invoke(reactor)
				require.Zero(t, registry.sqlCalls, "paused request read or wrote SQL")
				require.Equal(t, runCount, len(registry.runs))
				require.Equal(t, restoreCount, len(registry.restores))
				require.Equal(t, retentionCount, len(registry.retentionRuns))
				require.Empty(t, runResponder.statusSteps)
				require.Zero(t, runResponder.results)
				require.Empty(t, restoreResponder.statusSteps)
				require.Zero(t, restoreResponder.results)
				require.Empty(t, retentionResponder.statusSteps)
				require.Zero(t, retentionResponder.results)
				select {
				case id := <-runExecutor.calls:
					t.Fatalf("run %s executed", id)
				default:
				}
				select {
				case id := <-restoreExecutor.calls:
					t.Fatalf("restore %s executed", id)
				default:
				}
				select {
				case id := <-retentionExecutor.calls:
					t.Fatalf("retention %s executed", id)
				default:
				}
				require.Len(t, capture.events, 1, "same request emitted repeated refusal")
				refusal := capture.events[0]
				require.Equal(t, nostr.Kind(resultKind), refusal.Kind)
				require.Equal(t, request.ID.Hex(), tagValueNostr(refusal.Tags, "e"))
				require.Equal(t, "rejected", tagValueNostr(refusal.Tags, "status"))
				require.Equal(t, "backup_request_paused", tagValueNostr(refusal.Tags, "result"))
				require.True(t, refusal.VerifySignature())
				var body map[string]any
				require.NoError(t, json.Unmarshal([]byte(refusal.Content), &body))
				require.Equal(t, request.ID.Hex(), body["request_event_id"])
				require.Equal(t, "backup_request_paused", body["error"].(map[string]any)["code"])

				// A process restart may resend a refusal, but it cannot mint a
				// different signed event for the same incoming request.
				restartCapture := &captureNostrPublisher{published: 1}
				restarted := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(restartCapture))
				invoke(restarted)
				require.Len(t, restartCapture.events, 1)
				require.Equal(t, refusal.ID, restartCapture.events[0].ID)
				require.Zero(t, registry.sqlCalls)
			})
		}
	}
}

func TestBackupIntentRequestIntakePausedBeforeSQL(t *testing.T) {
	for _, family := range []string{"run", "retention", "restore"} {
		for _, stored := range []string{"empty", "sql-only"} {
			t.Run(family+"/"+stored, func(t *testing.T) {
				key := nostr.Generate().Hex()
				event := signedLLMRequest(t, key, 30900, `{}`, nil)
				actor := event.PubKey.Hex()
				id := uuid.New()
				registry := newFakeBackupIntentRegistry()
				if stored == "sql-only" {
					switch family {
					case "run":
						registry.runs[id] = &domain.BackupRun{ID: id, Status: domain.RunStatusSucceeded}
					case "retention":
						registry.retentionRuns[id] = &domain.BackupRetentionRun{ID: id, Status: domain.RunStatusSucceeded}
					case "restore":
						registry.restores[id] = &domain.BackupRestoreRun{ID: id, Status: domain.RunStatusSucceeded}
					}
				}
				runExecutor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
				restoreExecutor := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
				retentionExecutor := &recordingBackupRetentionExecutor{calls: make(chan uuid.UUID, 1)}
				publisher := &fakeBackupIntentPublisher{}
				handler := NewBackupIntentHandler(BackupIntentHandlerConfig{Registry: registry, Publisher: publisher,
					Executors: BackupIntentExecutors{RunExecutor: runExecutor, RestoreExecutor: restoreExecutor, RetentionExecutor: retentionExecutor}, Logger: zap.NewNop()})
				statuses := &statusCollector{}
				proc := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()), openTestStore(t),
					NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
					IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
				proc.RegisterHandler("backup", handler)
				intent := &Intent{Domain: "backup", Op: family, OrgID: testOrgID(), Actor: actor,
					IntentID: "paused-" + family, Coordinate: family + ":" + id.String(), Event: event,
					Content: map[string]any{"id": id.String()}}
				require.ErrorContains(t, proc.ProcessInProcess(t.Context(), intent), "request intake paused")
				require.Len(t, statuses.events, 1)
				require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
				require.Zero(t, registry.workflowCreates)
				require.Zero(t, publisher.recipeCount+publisher.policyCount+publisher.repositoryCount+publisher.definitionCount+publisher.deletedCount)
				select {
				case id := <-runExecutor.calls:
					t.Fatalf("run %s executed", id)
				default:
				}
				select {
				case id := <-restoreExecutor.calls:
					t.Fatalf("restore %s executed", id)
				default:
				}
				select {
				case id := <-retentionExecutor.calls:
					t.Fatalf("retention %s executed", id)
				default:
				}
			})
		}
	}
}

func TestBackupPausedRefusalRequiresValidSignedRequest(t *testing.T) {
	key := nostr.Generate().Hex()
	actor := testNostrPubKeyHexFromPrivateKey(t, key)
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	require.NoError(t, err)
	capture := &captureNostrPublisher{published: 1}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
	request := signedLLMRequest(t, key, KindBackupRunRequest, `{"recipe":"recipe:daily:v1"}`, nostr.Tags{{"d", "invalid:request"}})
	request.Content = `{"recipe":"tampered-after-signing"}`
	reactor.handleBackupRunRequest(t.Context(), request)
	require.Empty(t, capture.events, "invalid request caused a service-signed refusal")

	// An authentic but unauthorized sender cannot make the service mint a
	// distinct signed outcome on every replay either.
	unauthorized := signedLLMRequest(t, nostr.Generate().Hex(), KindBackupRunRequest,
		`{"recipe":"recipe:daily:v1"}`, nostr.Tags{{"d", "unauthorized:request"}})
	reactor.handleBackupRunRequest(t.Context(), unauthorized)
	reactor.handleBackupRunRequest(t.Context(), unauthorized)
	require.Empty(t, capture.events, "unauthorized request caused a service-signed refusal")
}

func TestBackupPausedRefusalRequiresWellFormedPayload(t *testing.T) {
	tests := []struct {
		name    string
		kind    int
		content string
		invoke  func(*Reactor, context.Context, *nostr.Event)
	}{
		{"run/malformed-json", KindBackupRunRequest, `{`, (*Reactor).handleBackupRunRequest},
		{"run/missing-recipe", KindBackupRunRequest, `{}`, (*Reactor).handleBackupRunRequest},
		{"run/invalid-recipe-id", KindBackupRunRequest, `{"recipe_id":"not-a-uuid"}`, (*Reactor).handleBackupRunRequest},
		{"run/invalid-recipe-coordinate", KindBackupRunRequest, `{"recipe":"invalid"}`, (*Reactor).handleBackupRunRequest},
		{"restore/malformed-json", KindBackupRestoreRequest, `{`, (*Reactor).handleBackupRestoreRequest},
		{"restore/missing-fields", KindBackupRestoreRequest, `{}`, (*Reactor).handleBackupRestoreRequest},
		{"restore/invalid-run-id", KindBackupRestoreRequest, `{"backup_run_id":"not-a-uuid","restore_target_ref":"fs:/restore"}`, (*Reactor).handleBackupRestoreRequest},
		{"retention/malformed-json", KindBackupRetentionEnforce, `{`, (*Reactor).handleBackupRetentionRequest},
		{"retention/missing-fields", KindBackupRetentionEnforce, `{}`, (*Reactor).handleBackupRetentionRequest},
		{"retention/invalid-repository-id", KindBackupRetentionEnforce, `{"repository_id":"not-a-uuid","policy_id":"00000000-0000-0000-0000-000000000001"}`, (*Reactor).handleBackupRetentionRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := nostr.Generate().Hex()
			actor := testNostrPubKeyHexFromPrivateKey(t, key)
			signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
			require.NoError(t, err)
			capture := &captureNostrPublisher{published: 1}
			registry, _ := newBackupRequestRegistryFixture()
			reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
			reactor.backupRegistry = registry
			request := signedLLMRequest(t, key, tt.kind, tt.content, nostr.Tags{{"d", "invalid:payload"}})
			tt.invoke(reactor, t.Context(), request)
			tt.invoke(reactor, t.Context(), request)
			require.Empty(t, capture.events, "invalid payload caused a service-signed refusal")
			require.Zero(t, registry.sqlCalls, "invalid payload reached SQL")
		})
	}
}
