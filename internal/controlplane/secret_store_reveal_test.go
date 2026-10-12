package controlplane

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"go.uber.org/zap"
)

// resolvingSecretStore releases one value per Resolve and keeps the Values
// it handed out so the test can prove the handler zeroized them.
type resolvingSecretStore struct {
	value   string
	lease   time.Duration
	err     error
	refs    []domain.SecretCredentialRef
	handed  []*secretstore.Value
	buffers [][]byte
}

func (s *resolvingSecretStore) Resolve(_ context.Context, ref domain.SecretCredentialRef) (*secretstore.Value, error) {
	s.refs = append(s.refs, ref)
	if s.err != nil {
		return nil, s.err
	}
	buf := []byte(s.value)
	v := secretstore.NewValue(buf, time.Now().Add(s.lease))
	s.handed = append(s.handed, v)
	s.buffers = append(s.buffers, buf)
	return v, nil
}

func (s *resolvingSecretStore) Inspect(context.Context, string) (secretstore.Metadata, error) {
	return secretstore.Metadata{}, secretstore.ErrNotFound
}

func (s *resolvingSecretStore) Health(context.Context) error { return nil }

func TestRevealSecretResolvesStoreBackedSecretAndZeroizes(t *testing.T) {
	serviceID, orgID, secretID := uuid.New(), uuid.New(), uuid.New()
	credRef := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("ef", 32), Version: 7}
	canonical := &memCanonicalSecrets{records: map[uuid.UUID]domain.SecretRef{
		secretID: {ID: secretID, ServiceID: serviceID, Name: "DB_PASSWORD", Version: 1, CredentialRef: &credRef},
	}, serviceOrg: map[uuid.UUID]string{}}
	store := &resolvingSecretStore{value: "s3cr3t-from-signet", lease: time.Minute}
	services, rbac := encryptedAuthDeps(t, serviceID, orgID, domain.RoleAdmin)
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{SecretRecords: canonical, SecretStore: store, Services: services, RBAC: rbac, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)

	reveal := func(service uuid.UUID) map[string]any {
		publisher.events = nil
		transport.HandleEvent(context.Background(), uniqueRevealRequest(t, service, secretID))
		response := contextVMResponse(t, publisher.events[len(publisher.events)-1])
		if response.Error != nil {
			return map[string]any{"error": response.Error.Message}
		}
		return response.Result.(map[string]any)
	}

	payload := reveal(serviceID)
	if payload["value"] != "s3cr3t-from-signet" {
		t.Fatalf("reveal payload = %#v", payload)
	}
	if len(store.refs) != 1 || store.refs[0] != credRef {
		t.Fatalf("resolved %v, want the pinned reference", store.refs)
	}
	for i, b := range store.buffers[0] {
		if b != 0 {
			t.Fatalf("byte %d of the resolved value was not zeroized after the reveal", i)
		}
	}

	// A secret is revealed only through its own service.
	if got := reveal(uuid.New()); got["error"] == nil {
		t.Fatalf("reveal through another service = %#v", got)
	}

	// The store failing closed is a refusal, never a stale or empty value.
	store.err = secretstore.ErrUnavailable
	if got := reveal(serviceID); got["error"] == nil || got["value"] != nil {
		t.Fatalf("reveal with the store unavailable = %#v", got)
	}

	// A lease that already ended is not used.
	store.err, store.lease = nil, -time.Second
	if got := reveal(serviceID); got["error"] == nil {
		t.Fatalf("reveal with an expired lease = %#v", got)
	}
}

// uniqueRevealRequest is a reveal request with its own JSON-RPC id, so
// repeated requests are distinct events rather than a deduplicated replay.
func uniqueRevealRequest(t *testing.T, serviceID, secretID uuid.UUID) *nostr.Event {
	t.Helper()
	params, err := json.Marshal(map[string]any{"service_id": serviceID.String(), "secret_id": secretID.String()})
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(ContextVMJSONRPCRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(uuid.NewString())), Method: ContextVMMethodServiceSecretsReveal, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return makeContextVMEvent(t, testRequesterKey, string(content))
}

func TestRevealSecretStoreBackedRequiresReadPermission(t *testing.T) {
	serviceID, orgID, secretID := uuid.New(), uuid.New(), uuid.New()
	credRef := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("ef", 32), Version: 1}
	canonical := &memCanonicalSecrets{records: map[uuid.UUID]domain.SecretRef{secretID: {ID: secretID, ServiceID: serviceID, CredentialRef: &credRef}}, serviceOrg: map[uuid.UUID]string{}}
	store := &resolvingSecretStore{value: "x", lease: time.Minute}
	services, _ := encryptedAuthDeps(t, serviceID, orgID, domain.RoleAdmin)
	_, outsider := encryptedAuthDeps(t, serviceID, uuid.New(), domain.RoleAdmin)
	h := NewEncryptedRouteHandlers(EncryptedRouteHandlersConfig{SecretRecords: canonical, SecretStore: store, Services: services, RBAC: outsider, Logger: zap.NewNop()})
	transport, publisher := encryptedRouteTransport(t, h)
	transport.HandleEvent(context.Background(), makeRouteRequest(t, ContextVMMethodServiceSecretsReveal, map[string]any{"service_id": serviceID.String(), "secret_id": secretID.String()}))
	if response := contextVMResponse(t, publisher.events[len(publisher.events)-1]); response.Error == nil {
		t.Fatalf("reveal without membership = %+v", response)
	}
	if len(store.refs) != 0 {
		t.Fatal("the store was asked for a value before authorization")
	}
}
