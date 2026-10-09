package controlplane

import (
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestBackupReactorCoordinateCollisionDoesNotPublish(t *testing.T) {
	for _, family := range []string{"run", "retention", "restore"} {
		for _, collision := range []string{"sql-only", "same-event-tampered"} {
			t.Run(family+"/"+collision, func(t *testing.T) {
				key := nostr.Generate().Hex()
				actor := testNostrPubKeyHexFromPrivateKey(t, key)
				registry, recipe := newBackupRequestRegistryFixture()
				signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
				require.NoError(t, err)
				capture := &captureNostrPublisher{}
				reactor := NewReactor(Config{AuthorizedPubkeys: []string{actor}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture))
				reactor.backupRegistry = registry
				rowID := uuid.New()
				var invoke func()
				var assertNoWork func()
				switch family {
				case "run":
					executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
					responder := &recordingBackupRunResponder{}
					reactor.backupExecutor = executor
					reactor.backupResponder = responder
					event := signedLLMRequest(t, key, KindBackupRunRequest, `{"recipe":"recipe:daily:v1"}`,
						nostr.Tags{{"d", "collision:run"}, {"recipe", "recipe:daily:v1"}})
					registry.runs[rowID] = &domain.BackupRun{ID: rowID, RecipeID: recipe.ID, RepositoryID: recipe.RepositoryID,
						RequestedBy: actor, RequestKind: KindBackupRunRequest, RequestDTag: "collision:run",
						Status: domain.RunStatusSucceeded, Backend: recipe.Backend, TargetRef: recipe.TargetRef}
					if collision == "same-event-tampered" {
						registry.runs[rowID].RequestEventID = event.ID.Hex()
						registry.runs[rowID].TargetRef = "fs:/sql-overridden"
					}
					registry.coordinates[backupCoordinate(actor, KindBackupRunRequest, "collision:run")] = rowID
					invoke = func() { reactor.handleBackupRunRequest(t.Context(), event) }
					assertNoWork = func() {
						require.Empty(t, responder.statusSteps)
						require.Zero(t, responder.results)
						select {
						case id := <-executor.calls:
							t.Fatalf("SQL-only run %s executed", id)
						default:
						}
					}
				case "retention":
					executor := &recordingBackupRetentionExecutor{calls: make(chan uuid.UUID, 1)}
					responder := &recordingBackupRetentionResponder{}
					reactor.backupRetentionExecutor = executor
					reactor.backupRetentionResponder = responder
					policyID := registry.firstPolicyID()
					event := signedLLMRequest(t, key, KindBackupRetentionEnforce,
						fmt.Sprintf(`{"repository_id":%q,"policy_id":%q,"dry_run":true}`, recipe.RepositoryID, policyID),
						nostr.Tags{{"d", "collision:retention"}})
					registry.retentionRuns[rowID] = &domain.BackupRetentionRun{ID: rowID, RepositoryID: recipe.RepositoryID, PolicyID: &policyID,
						RequestedBy: actor, RequestKind: KindBackupRetentionEnforce, RequestDTag: "collision:retention",
						Status: domain.RunStatusSucceeded, DryRun: true, Backend: recipe.Backend}
					if collision == "same-event-tampered" {
						registry.retentionRuns[rowID].RequestEventID = event.ID.Hex()
						registry.retentionRuns[rowID].DryRun = false
					}
					registry.retentionCoords[backupCoordinate(actor, KindBackupRetentionEnforce, "collision:retention")] = rowID
					invoke = func() { reactor.handleBackupRetentionRequest(t.Context(), event) }
					assertNoWork = func() {
						require.Empty(t, responder.statusSteps)
						require.Zero(t, responder.results)
						select {
						case id := <-executor.calls:
							t.Fatalf("SQL-only retention %s executed", id)
						default:
						}
					}
				case "restore":
					executor := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
					responder := &recordingBackupRestoreResponder{}
					reactor.backupRestoreExecutor = executor
					reactor.backupRestoreResponder = responder
					source := registry.addRestoreEligibleRun()
					event := signedLLMRequest(t, key, KindBackupRestoreRequest,
						fmt.Sprintf(`{"backup_run_id":%q,"restore_target_ref":"fs:/restore"}`, source.ID),
						nostr.Tags{{"d", "collision:restore"}})
					registry.restores[rowID] = &domain.BackupRestoreRun{ID: rowID, BackupRunID: source.ID, RestoreTargetRef: "fs:/restore",
						RequestedBy: actor, RequestKind: KindBackupRestoreRequest, RequestDTag: "collision:restore",
						Status: domain.RunStatusSucceeded, ApprovalStatus: domain.BackupApprovalApproved}
					if collision == "same-event-tampered" {
						registry.restores[rowID].RequestEventID = event.ID.Hex()
						registry.restores[rowID].RestoreTargetRef = "fs:/sql-overridden"
					}
					registry.restoreCoords[backupCoordinate(actor, KindBackupRestoreRequest, "collision:restore")] = rowID
					invoke = func() { reactor.handleBackupRestoreRequest(t.Context(), event) }
					assertNoWork = func() {
						require.Empty(t, responder.statusSteps)
						require.Zero(t, responder.results)
						select {
						case id := <-executor.calls:
							t.Fatalf("SQL-only restore %s executed", id)
						default:
						}
					}
				}
				invoke()
				assertNoWork()
				require.Empty(t, capture.events, "collision signed a SQL-derived outcome")
			})
		}
	}
}

func TestBackupIntentCoordinateCollisionConflicts(t *testing.T) {
	for _, family := range []string{"run", "retention", "restore"} {
		for _, collision := range []string{"sql-only", "same-event-tampered"} {
			t.Run(family+"/"+collision, func(t *testing.T) {
				id, recipeID, repositoryID, policyID, sourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
				key := nostr.Generate().Hex()
				event := signedLLMRequest(t, key, 30900, `{}`, nil)
				actor := event.PubKey.Hex()
				coord := "collision:" + family
				registry := newFakeBackupIntentRegistry()
				runExec := recordingBackupRunExecutor{started: make(chan uuid.UUID, 1)}
				restoreExec := &recordingBackupRestoreExecutor{calls: make(chan uuid.UUID, 1)}
				retentionExec := &recordingBackupRetentionExecutor{calls: make(chan uuid.UUID, 1)}
				handler := NewBackupIntentHandler(BackupIntentHandlerConfig{
					Registry: registry, Logger: zap.NewNop(),
					Executors: BackupIntentExecutors{RunExecutor: runExec, RestoreExecutor: restoreExec, RetentionExecutor: retentionExec},
				})
				content := map[string]any{"id": id.String()}
				switch family {
				case "run":
					content["recipe_id"], content["repository_id"], content["backend"] = recipeID.String(), repositoryID.String(), "kopia"
					registry.runs[id] = &domain.BackupRun{ID: id, RecipeID: recipeID, RepositoryID: repositoryID, Backend: domain.BackupBackendKopia,
						RequestedBy: actor, RequestKind: int(event.Kind), RequestDTag: coord, Status: domain.RunStatusSucceeded}
					if collision == "same-event-tampered" {
						registry.runs[id].RequestEventID = event.ID.Hex()
						registry.runs[id].RecipeID = uuid.New()
					}
					registry.runCreatedAlready[id] = true
				case "retention":
					content["repository_id"], content["policy_id"], content["dry_run"] = repositoryID.String(), policyID.String(), true
					registry.retentionRuns[id] = &domain.BackupRetentionRun{ID: id, RepositoryID: repositoryID, PolicyID: &policyID, DryRun: true,
						RequestedBy: actor, RequestKind: int(event.Kind), RequestDTag: coord, Status: domain.RunStatusSucceeded}
					if collision == "same-event-tampered" {
						registry.retentionRuns[id].RequestEventID = event.ID.Hex()
						registry.retentionRuns[id].DryRun = false
					}
					registry.retentionCreatedAlready[id] = true
				case "restore":
					content["backup_run_id"], content["restore_target_ref"] = sourceID.String(), "fs:/restore"
					registry.restores[id] = &domain.BackupRestoreRun{ID: id, BackupRunID: sourceID, RestoreTargetRef: "fs:/restore",
						RequestedBy: actor, RequestKind: int(event.Kind), RequestDTag: coord, Status: domain.RunStatusSucceeded}
					if collision == "same-event-tampered" {
						registry.restores[id].RequestEventID = event.ID.Hex()
						registry.restores[id].RestoreTargetRef = "fs:/sql-overridden"
					}
					registry.restoreCreatedAlready[id] = true
				}
				intent := &Intent{Domain: "backup", Op: family, OrgID: testOrgID(), Actor: actor,
					IntentID: "collision-" + family, Coordinate: coord, Event: event, Content: content}
				statuses := &statusCollector{}
				proc := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()),
					openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
					IntentProcessorConfig{EnabledDomains: map[string]bool{"backup": true}}, zap.NewNop())
				proc.RegisterHandler("backup", handler)
				require.ErrorContains(t, proc.ProcessInProcess(t.Context(), intent), "coordinate conflicts with signed request")
				require.Len(t, statuses.events, 1)
				require.Equal(t, "conflict", tagValueNostr(statuses.events[0].Tags, "status"))
				select {
				case id := <-runExec.started:
					t.Fatalf("SQL-only run %s executed", id)
				default:
				}
				select {
				case id := <-restoreExec.calls:
					t.Fatalf("SQL-only restore %s executed", id)
				default:
				}
				select {
				case id := <-retentionExec.calls:
					t.Fatalf("SQL-only retention %s executed", id)
				default:
				}
			})
		}
	}
}

func TestBackupDuplicateEffectInputsCannotChangeWithSameEventID(t *testing.T) {
	id, sourceID, repoID, policyID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	source := "signed-event-id"
	common := func() (string, string, int, string) { return "operator", source, 30900, "backup:daily" }
	actor, eventID, kind, dTag := common()
	run := &domain.BackupRun{ID: id, RecipeID: sourceID, RepositoryID: repoID, PolicyID: &policyID,
		RequestedBy: actor, RequestEventID: eventID, RequestKind: kind, RequestDTag: dTag,
		Backend: domain.BackupBackendKopia, TargetRef: "fs:/signed", Metadata: map[string]any{"site": "signed"}}
	require.NoError(t, backupRunDuplicateMatches(run, run, true))
	runChanged := *run
	runChanged.TargetRef = "fs:/sql-overridden"
	require.Error(t, backupRunDuplicateMatches(&runChanged, run, true))
	restore := &domain.BackupRestoreRun{ID: id, BackupRunID: sourceID, RestoreTargetRef: "fs:/signed",
		RequestedBy: actor, RequestEventID: eventID, RequestKind: kind, RequestDTag: dTag,
		Metadata: map[string]any{"kopia_restore_source": "signed"}}
	require.NoError(t, backupRestoreDuplicateMatches(restore, restore, true))
	restoreChanged := *restore
	restoreChanged.Metadata = map[string]any{"kopia_restore_source": "sql-overridden"}
	require.Error(t, backupRestoreDuplicateMatches(&restoreChanged, restore, true))
	retention := &domain.BackupRetentionRun{ID: id, RepositoryID: repoID, PolicyID: &policyID, DryRun: true,
		RequestedBy: actor, RequestEventID: eventID, RequestKind: kind, RequestDTag: dTag}
	require.NoError(t, backupRetentionDuplicateMatches(retention, retention, true))
	retentionChanged := *retention
	retentionChanged.DryRun = false
	require.Error(t, backupRetentionDuplicateMatches(&retentionChanged, retention, true))
}
