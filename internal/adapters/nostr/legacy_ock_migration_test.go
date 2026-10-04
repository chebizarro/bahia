package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

// --- test doubles ---

type fakeConfidentialEncryptor struct {
	encrypted map[string]string // orgID:content → encrypted
	calls     int
}

func (f *fakeConfidentialEncryptor) EncryptConfidential(_ context.Context, orgID string, plaintext []byte, legacyKind int, dTag, topic string, _ []byte) (string, error) {
	f.calls++
	key := orgID + ":" + string(plaintext)
	result := fmt.Sprintf(`{"schema":%q,"key_org":%q,"ciphertext":"migrated-%d"}`,
		confidentialAEADV1Schema, orgID, f.calls)
	f.encrypted[key] = result
	return result, nil
}

func (f *fakeConfidentialEncryptor) DecryptConfidential(_ context.Context, content string, _ int, _, _ string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented in test")
}
func (f *fakeConfidentialEncryptor) DecryptServiceInner(_ context.Context, _ string) ([]byte, error) {
	return nil, fmt.Errorf("not implemented in test")
}
func (f *fakeConfidentialEncryptor) RotateKey(_ context.Context, _ string) error { return nil }
func (f *fakeConfidentialEncryptor) RotateKeyExcluding(_ context.Context, _, _ string) error {
	return nil
}
func (f *fakeConfidentialEncryptor) WrapKeyForMember(_ context.Context, _, _ string) error {
	return nil
}

type fakeLegacyO1Decryptor struct {
	plaintext map[string][]byte // content → plaintext
}

func (f *fakeLegacyO1Decryptor) DecryptOrgState(content string) ([]byte, error) {
	if pt, ok := f.plaintext[content]; ok {
		return pt, nil
	}
	return nil, fmt.Errorf("unknown O1 content")
}

type fakeProjectionHistory struct {
	records map[string][]repository.NostrEventRecord // "tagName:tagValue" → records
}

func (h *fakeProjectionHistory) FindByTag(_ context.Context, tagName, tagValue string, _ []int, _ int) ([]repository.NostrEventRecord, error) {
	key := tagName + ":" + tagValue
	return h.records[key], nil
}

func (h *fakeProjectionHistory) ListByKind(_ context.Context, _ int, _ int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

type fakeProjectionPublisher struct {
	published []publishedRecord
}

type publishedRecord struct {
	kind    int
	content string
}

func (p *fakeProjectionPublisher) PublishBeforeCommit(_ context.Context, _ interface{}, _ string, _ interface{}) error {
	return nil
}

// --- helpers ---

func makeRecord(id, pubkey, content, dTag, topic string) repository.NostrEventRecord {
	tags := [][]string{{"d", dTag}, {"t", topic}}
	tagsJSON, _ := json.Marshal(tags)
	return repository.NostrEventRecord{
		ID:        id,
		Kind:      int(kinds.CASControlState),
		PubKey:    pubkey,
		Content:   content,
		Tags:      tagsJSON,
		CreatedAt: time.Now().UTC(),
	}
}

func legacyO1Content(orgID, pubkey, role string) string {
	return fmt.Sprintf(`{"schema":"bahia.org-state.aead.v1","algorithm":"xchacha20-poly1305","key_ref":"org-state","nonce":"AAAA","ciphertext":"BBBB","associated_data":{"d":"test"}}`)
}

func newFormatContent(orgID string) string {
	return fmt.Sprintf(`{"schema":%q,"key_org":%q,"ciphertext":"already-migrated"}`,
		confidentialAEADV1Schema, orgID)
}

// --- tests ---

func TestLegacyOCKMigrator_SkipsNewFormatRecords(t *testing.T) {
	servicePubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	orgID := "550e8400-e29b-41d4-a716-446655440000"

	history := &fakeProjectionHistory{
		records: map[string][]repository.NostrEventRecord{
			"t:" + kinds.CPStateTopicOrgRegistry: {
				makeRecord("event1", servicePubkey, newFormatContent(orgID),
					orgID, kinds.CPStateTopicOrgRegistry),
			},
		},
	}

	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}

	projector := &Projector{
		privateKey: "0000000000000000000000000000000000000000000000000000000000000001",
		enabled:    true,
		history:    history,
		logger:     zap.NewNop(),
	}

	migrator := NewLegacyOCKMigrator(projector, encryptor, nil, nil)
	migrator.Run(context.Background())

	if encryptor.calls != 0 {
		t.Errorf("expected 0 encrypt calls for already-migrated records, got %d", encryptor.calls)
	}
}

func TestLegacyOCKMigrator_MigratesLegacyO1Records(t *testing.T) {
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	legacyContent := `{"schema":"bahia.org-state.aead.v1","algorithm":"xchacha20-poly1305","key_ref":"org-state","nonce":"AAAA","ciphertext":"BBBB"}`
	plaintext := fmt.Sprintf(`{"id":%q,"name":"test-org"}`, orgID)

	// Use a deterministic keypair so servicePubkey is known.
	// Private key 0x01 → pubkey = generator point (79BE...) in compressed form.
	// For simplicity, just use a fixed pubkey that matches the test.
	servicePubkey := "test-service-pubkey"

	history := &fakeProjectionHistory{
		records: map[string][]repository.NostrEventRecord{
			"t:" + kinds.CPStateTopicOrgRegistry: {
				makeRecord("event1", servicePubkey, legacyContent,
					orgID, kinds.CPStateTopicOrgRegistry),
			},
		},
	}

	legacyO1 := &fakeLegacyO1Decryptor{
		plaintext: map[string][]byte{
			legacyContent: []byte(plaintext),
		},
	}

	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}

	// Use a projector with an empty private key so it doesn't filter by
	// service pubkey (treats all records as own).
	projector := &Projector{
		enabled: true,
		history: history,
		logger:  zap.NewNop(),
	}

	migrator := NewLegacyOCKMigrator(projector, encryptor, legacyO1, nil)
	migrator.Run(context.Background())

	if encryptor.calls != 1 {
		t.Errorf("expected 1 encrypt call for legacy record, got %d", encryptor.calls)
	}

	// Verify the encrypted content was for the correct org.
	key := orgID + ":" + plaintext
	if _, ok := encryptor.encrypted[key]; !ok {
		t.Errorf("expected encryption for org %s with plaintext %q", orgID, plaintext)
	}
}

func TestLegacyOCKMigrator_RunsOnceOnly(t *testing.T) {
	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}

	legacyContent := `{"schema":"bahia.org-state.aead.v1","key_ref":"org-state","nonce":"A","ciphertext":"B"}`
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	plaintext := fmt.Sprintf(`{"id":%q}`, orgID)

	legacyO1 := &fakeLegacyO1Decryptor{
		plaintext: map[string][]byte{legacyContent: []byte(plaintext)},
	}

	history := &fakeProjectionHistory{
		records: map[string][]repository.NostrEventRecord{
			"t:" + kinds.CPStateTopicOrgRegistry: {
				makeRecord("event1", "", legacyContent, orgID, kinds.CPStateTopicOrgRegistry),
			},
		},
	}

	projector := &Projector{enabled: true, history: history, logger: zap.NewNop()}

	migrator := NewLegacyOCKMigrator(projector, encryptor, legacyO1, nil)
	migrator.Run(context.Background())
	firstRun := encryptor.calls

	migrator.Run(context.Background())
	if encryptor.calls != firstRun {
		t.Errorf("second Run should be a no-op: calls went from %d to %d", firstRun, encryptor.calls)
	}
}

func TestLegacyOCKMigrator_HandlesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
	projector := &Projector{enabled: true, history: &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{}}, logger: zap.NewNop()}
	migrator := NewLegacyOCKMigrator(projector, encryptor, nil, nil)
	migrator.Run(ctx)

	if encryptor.calls != 0 {
		t.Errorf("expected 0 calls on cancelled context, got %d", encryptor.calls)
	}
}

func TestLegacyOCKMigrator_NilSafe(t *testing.T) {
	// nil migrator should not panic.
	var migrator *LegacyOCKMigrator
	migrator.Run(context.Background())
}

func TestIsConfidentialAEADV1(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"new format", fmt.Sprintf(`{"schema":%q}`, confidentialAEADV1Schema), true},
		{"legacy O1", `{"schema":"bahia.org-state.aead.v1"}`, false},
		{"empty", `{}`, false},
		{"invalid json", `not json`, false},
		{"plaintext", `{"id":"abc"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConfidentialAEADV1(tt.content); got != tt.want {
				t.Errorf("isConfidentialAEADV1(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestExtractOrgIDFromPlaintext(t *testing.T) {
	tests := []struct {
		name      string
		plaintext string
		want      string
	}{
		{"explicit org_id", `{"org_id":"abc-123","pubkey":"pk1"}`, "abc-123"},
		{"id as org (org records)", `{"id":"def-456","name":"org"}`, "def-456"},
		{"both fields", `{"id":"fallback","org_id":"primary"}`, "primary"},
		{"neither field", `{"role":"admin"}`, ""},
		{"invalid json", `not json`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractOrgIDFromPlaintext(tt.plaintext); got != tt.want {
				t.Errorf("extractOrgIDFromPlaintext(%q) = %q, want %q", tt.plaintext, got, tt.want)
			}
		})
	}
}

func TestLegacyOCKMigration_Topics(t *testing.T) {
	topics := legacyOCKMigrationTopics()
	if len(topics) != 5 {
		t.Errorf("expected 5 migration topics, got %d: %v", len(topics), topics)
	}
	expected := map[string]bool{
		kinds.CPStateTopicOrgRegistry:                 false,
		kinds.CPStateTopicOrgMemberRegistry:           false,
		kinds.CPStateTopicOrgInviteRegistry:           false,
		kinds.CPStateTopicSecretRegistry:              false,
		kinds.CPStateTopicNotificationChannelRegistry: false,
	}
	for _, topic := range topics {
		if _, ok := expected[topic]; !ok {
			t.Errorf("unexpected topic %q", topic)
		} else {
			expected[topic] = true
		}
	}
	for topic, found := range expected {
		if !found {
			t.Errorf("missing expected topic %q", topic)
		}
	}
}
