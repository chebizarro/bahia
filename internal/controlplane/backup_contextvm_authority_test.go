package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

func TestBackupContextVMAuthorizedRequesterCarriesAuditableDelegation(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	rbac := backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, rbac)

	request := backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"tenant_id":       tenantID.String(),
		"recipe_id":       uuid.NewString(),
		"idempotency_key": "authority-success",
	})
	transport.HandleEvent(context.Background(), request)

	command := backupPublishedCommand(t, publisher.events, KindBackupRunRequest)
	if !command.VerifySignature() {
		t.Fatal("service-signed backup command signature is invalid")
	}
	if command.PubKey.Hex() != servicePubkey {
		t.Fatalf("command signer = %s, want Bahia service %s", command.PubKey.Hex(), servicePubkey)
	}
	authority, delegated, err := backupRequestAuthorityFromEvent(&command)
	if err != nil {
		t.Fatalf("parse signed delegation: %v", err)
	}
	if !delegated {
		t.Fatal("backup command is missing its delegation record")
	}
	if authority.RequesterPubkey != requesterPubkey ||
		authority.RequestEventID != request.ID.Hex() ||
		authority.RequestEventKind != int(KindContextVMMessage) ||
		authority.TenantID != tenantID.String() ||
		authority.Capability != string(domain.PermManageBackups) ||
		authority.ServicePubkey != servicePubkey {
		t.Fatalf("delegation = %+v", authority)
	}

	metadata := backupNostrMetadata(&command, nil, nil)
	if metadata["nostr_request_pubkey"] != requesterPubkey ||
		metadata["nostr_service_pubkey"] != servicePubkey ||
		metadata["nostr_request_event_id"] != request.ID.Hex() ||
		metadata["nostr_tenant_id"] != tenantID.String() ||
		metadata["nostr_capability"] != string(domain.PermManageBackups) {
		t.Fatalf("audit metadata = %#v", metadata)
	}
	if metadata["nostr_delegated"] != true {
		t.Fatalf("audit metadata did not identify delegated authority: %#v", metadata)
	}
	if strings.Contains(command.Content, testServiceKey) || strings.Contains(command.Content, testRequesterKey) {
		t.Fatal("delegation audit content leaked a private signing key")
	}
}

func TestBackupContextVMRequesterAuthorityFailsClosed(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	otherPubkey := testNostrPubKeyHexFromPrivateKey(t, testOtherKey)

	tests := []struct {
		name       string
		requestKey string
		params     map[string]any
		rbac       *auth.RBAC
		wantError  string
	}{
		{
			name:       "unauthorized requester denied although service can sign",
			requestKey: testOtherKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "recipe_id": uuid.NewString(), "idempotency_key": "unauthorized",
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleAdmin),
			wantError: "not a member",
		},
		{
			name:       "service key cannot replace requester authority",
			requestKey: testServiceKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "recipe_id": uuid.NewString(), "idempotency_key": "service-substitution",
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleAdmin),
			wantError: "service signer cannot supply backup requester authority",
		},
		{
			name:       "forged delegated identity is ignored",
			requestKey: testOtherKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "recipe_id": uuid.NewString(), "idempotency_key": "forged",
				"requester_pubkey": requesterPubkey,
				"service_pubkey":   servicePubkey,
				"capability":       string(domain.PermManageBackups),
				"request_authority": map[string]any{
					"version": backupDelegationVersion, "requester_pubkey": requesterPubkey,
					"tenant_id": tenantA.String(), "capability": string(domain.PermManageBackups),
				},
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleAdmin),
			wantError: "not a member",
		},
		{
			name:       "cross tenant request is denied",
			requestKey: testRequesterKey,
			params: map[string]any{
				"tenant_id": tenantB.String(), "recipe_id": uuid.NewString(), "idempotency_key": "cross-tenant",
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleAdmin),
			wantError: "not a member",
		},
		{
			name:       "conflicting tenant aliases are denied",
			requestKey: testRequesterKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "org_id": tenantB.String(), "recipe_id": uuid.NewString(), "idempotency_key": "tenant-alias-conflict",
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleAdmin),
			wantError: "must identify the same tenant",
		},
		{
			name:       "missing tenant RBAC fails closed",
			requestKey: testRequesterKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "recipe_id": uuid.NewString(), "idempotency_key": "no-rbac",
			},
			rbac:      nil,
			wantError: "tenant RBAC is not configured",
		},
		{
			name:       "insufficient capability is denied",
			requestKey: testRequesterKey,
			params: map[string]any{
				"tenant_id": tenantA.String(), "recipe_id": uuid.NewString(), "idempotency_key": "viewer",
			},
			rbac:      backupAuthorityTestRBAC(tenantA, requesterPubkey, domain.RoleViewer),
			wantError: "requires backups:manage permission",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &mockEncryptedPublisher{}
			transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey, otherPubkey, servicePubkey}, zap.NewNop())
			RegisterBackupAliasContextVMHandlers(transport, tc.rbac)
			request := backupAuthorityRequest(t, tc.requestKey, ContextVMMethodBackupRun, tc.params)

			transport.HandleEvent(context.Background(), request)

			if got := countBackupPublishedCommands(publisher.events, KindBackupRunRequest); got != 0 {
				t.Fatalf("unauthorized request published %d downstream backup command(s), want 0", got)
			}
			response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
			if response.Error == nil || !strings.Contains(strings.ToLower(response.Error.Message), strings.ToLower(tc.wantError)) {
				t.Fatalf("response error = %+v, want substring %q", response.Error, tc.wantError)
			}
		})
	}
}

func TestBackupContextVMReplayDoesNotRepublishDelegatedCommand(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin))

	params := map[string]any{
		"tenant_id": tenantID.String(), "recipe_id": uuid.NewString(), "idempotency_key": "backup-authority-replay",
		"_meta": map[string]any{"progressToken": "backup-authority-replay"},
	}
	first := backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, params)
	second := backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, params)
	second.CreatedAt = first.CreatedAt + 1
	second.ID = nostr.ID{}
	second.Sig = [64]byte{}
	if err := second.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatalf("sign distinct replay event: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("replay fixture must use a distinct event ID")
	}
	transport.HandleEvent(context.Background(), first)
	transport.HandleEvent(context.Background(), second)

	if got := countBackupPublishedCommands(publisher.events, KindBackupRunRequest); got != 1 {
		t.Fatalf("delegated backup commands published = %d, want 1 across replay", got)
	}
}

func TestBackupContextVMAmbiguousTenantRequiresExplicitBinding(t *testing.T) {
	tenantA, tenantB := uuid.New(), uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	members := &encryptedMemberRepo{members: []domain.OrgMember{
		{OrgID: tenantA, Pubkey: requesterPubkey, Role: domain.RoleAdmin},
		{OrgID: tenantB, Pubkey: requesterPubkey, Role: domain.RoleOwner},
	}}
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, auth.NewRBAC(members))

	request := backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"recipe_id": uuid.NewString(), "idempotency_key": "ambiguous-tenant",
	})
	transport.HandleEvent(context.Background(), request)

	if countBackupPublishedCommands(publisher.events, KindBackupRunRequest) != 0 {
		t.Fatal("ambiguous multi-tenant request published a backup command")
	}
	response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
	if response.Error == nil || !strings.Contains(response.Error.Message, "tenant_id is required") {
		t.Fatalf("response error = %+v", response.Error)
	}
}

func TestBackupRequestAuthorityRejectsTamperedSignedDelegation(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin))
	transport.HandleEvent(context.Background(), backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"tenant_id": tenantID.String(), "recipe_id": uuid.NewString(), "idempotency_key": "tamper",
	}))
	command := backupPublishedCommand(t, publisher.events, KindBackupRunRequest)

	var body map[string]any
	if err := json.Unmarshal([]byte(command.Content), &body); err != nil {
		t.Fatalf("decode command: %v", err)
	}
	authority := body["request_authority"].(map[string]any)
	authority["requester_pubkey"] = command.PubKey.Hex()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode tampered command: %v", err)
	}
	command.Content = string(encoded)
	command.ID = nostr.ID{}
	command.Sig = [64]byte{}
	if err := command.Sign(testNostrSecretKey(t, testServiceKey)); err != nil {
		t.Fatalf("re-sign tampered command: %v", err)
	}

	if _, _, err := backupRequestAuthorityFromEvent(&command); err == nil || !strings.Contains(err.Error(), "requester") {
		t.Fatalf("tampered delegation error = %v", err)
	}
}

func TestBackupRequestAuthorityRejectsDuplicateTags(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin))
	transport.HandleEvent(context.Background(), backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"tenant_id": tenantID.String(), "recipe_id": uuid.NewString(), "idempotency_key": "duplicate-tag",
	}))
	command := backupPublishedCommand(t, publisher.events, KindBackupRunRequest)
	command.Tags = append(command.Tags, nostr.Tag{"requester", requesterPubkey})
	command.ID = nostr.ID{}
	command.Sig = [64]byte{}
	if err := command.Sign(testNostrSecretKey(t, testServiceKey)); err != nil {
		t.Fatalf("re-sign duplicate-tag command: %v", err)
	}

	if _, _, err := backupRequestAuthorityFromEvent(&command); err == nil || !strings.Contains(err.Error(), "one value") {
		t.Fatalf("duplicate delegation tag error = %v", err)
	}
}

func TestDelegatedBackupCommandUsesRequesterForDurableAttribution(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	registry, recipe := newBackupRequestRegistryFixture()
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin))
	request := backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"tenant_id": tenantID.String(), "recipe_id": recipe.ID.String(), "idempotency_key": "durable-attribution",
	})
	transport.HandleEvent(context.Background(), request)
	command := backupPublishedCommand(t, publisher.events, KindBackupRunRequest)

	signer, err := NewPrivateKeySigner(testServiceKey)
	if err != nil {
		t.Fatalf("service signer: %v", err)
	}
	executor := &recordingBackupExecutor{calls: make(chan uuid.UUID, 1)}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{servicePubkey}}, nil, nil, signer, zap.NewNop())
	reactor.backupRegistry = registry
	reactor.backupExecutor = executor
	reactor.handleBackupRunRequest(context.Background(), &command)

	select {
	case runID := <-executor.calls:
		run := registry.runs[runID]
		if run == nil || run.RequestedBy != requesterPubkey {
			t.Fatalf("durable requester attribution = %#v, want %s", run, requesterPubkey)
		}
		if run.Metadata["nostr_service_pubkey"] != servicePubkey ||
			run.Metadata["nostr_request_event_id"] != request.ID.Hex() ||
			run.Metadata["nostr_tenant_id"] != tenantID.String() ||
			run.Metadata["nostr_capability"] != string(domain.PermManageBackups) {
			t.Fatalf("durable delegation audit = %#v", run.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("delegated backup command was not executed")
	}
}

func TestDelegatedBackupCommandRejectsUnexpectedIssuer(t *testing.T) {
	tenantID := uuid.New()
	requesterPubkey := testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	publisher := &mockEncryptedPublisher{}
	transport := NewEncryptedRequestTransport(nil, newResponder(t, publisher), []string{requesterPubkey}, zap.NewNop())
	RegisterBackupAliasContextVMHandlers(transport, backupAuthorityTestRBAC(tenantID, requesterPubkey, domain.RoleAdmin))
	transport.HandleEvent(context.Background(), backupAuthorityRequest(t, testRequesterKey, ContextVMMethodBackupRun, map[string]any{
		"tenant_id": tenantID.String(), "recipe_id": uuid.NewString(), "idempotency_key": "issuer-mismatch",
	}))
	command := backupPublishedCommand(t, publisher.events, KindBackupRunRequest)

	otherSigner, err := NewPrivateKeySigner(testOtherKey)
	if err != nil {
		t.Fatalf("other signer: %v", err)
	}
	registry, _ := newBackupRequestRegistryFixture()
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{servicePubkey}}, nil, nil, otherSigner, zap.NewNop())
	reactor.backupRegistry = registry
	if reactor.authorizeBackupCommandRequest(context.Background(), &command, "backup_run", KindBackupRunResult) {
		t.Fatal("delegated command from an unexpected issuer was authorized")
	}
}

func backupAuthorityTestRBAC(tenantID uuid.UUID, pubkey string, role domain.Role) *auth.RBAC {
	return auth.NewRBAC(&encryptedMemberRepo{members: []domain.OrgMember{{OrgID: tenantID, Pubkey: pubkey, Role: role}}})
}

func backupAuthorityRequest(t *testing.T, privateKey, method string, params map[string]any) *nostr.Event {
	t.Helper()
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	rpc := ContextVMJSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(`"backup-authority"`), Method: method, Params: rawParams}
	content, err := json.Marshal(rpc)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return makeContextVMEvent(t, privateKey, string(content))
}

func backupPublishedCommand(t *testing.T, events []nostr.Event, kind int) nostr.Event {
	t.Helper()
	for _, event := range events {
		if event.Kind == nostr.Kind(kind) {
			return event
		}
	}
	t.Fatalf("no backup command kind %d among %d published events", kind, len(events))
	return nostr.Event{}
}

func countBackupPublishedCommands(events []nostr.Event, kind int) int {
	count := 0
	for _, event := range events {
		if event.Kind == nostr.Kind(kind) {
			count++
		}
	}
	return count
}
