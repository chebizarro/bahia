package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip44"
	"github.com/google/uuid"
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

type testLegacyN1Decryptor struct {
	keyer  gonostr.Keyer
	pubkey gonostr.PubKey
	err    error
	calls  int
}

func (d *testLegacyN1Decryptor) ServiceDecrypt(ctx context.Context, content string) (string, error) {
	d.calls++
	if d.err != nil {
		return "", d.err
	}
	return d.keyer.Decrypt(ctx, content, d.pubkey)
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
	records        map[string][]repository.NostrEventRecord // "tagName:tagValue" → records
	err            error
	requestedLimit int
	errForTag      string
}

func (h *fakeProjectionHistory) FindByTag(_ context.Context, tagName, tagValue string, _ []int, limit int) ([]repository.NostrEventRecord, error) {
	h.requestedLimit = limit
	if h.err != nil && (h.errForTag == "" || h.errForTag == tagValue) {
		return nil, h.err
	}
	key := tagName + ":" + tagValue
	return h.records[key], nil
}

func (h *fakeProjectionHistory) ListByKind(_ context.Context, _ int, _ int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

type fakeProjectionPublisher struct {
	published []publishedRecord
	err       error
}

func (p *fakeProjectionPublisher) PublishProjection(_ context.Context, ev gonostr.Event, _ string, _ *uuid.UUID) error {
	p.published = append(p.published, publishedRecord{kind: int(ev.Kind), content: ev.Content})
	return p.err
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
	return fmt.Sprintf(`{"schema":%q,"algorithm":"xchacha20-poly1305","key_org":%q,"key_ref":"ock:%s","key_version":"v1","nonce":"nonce","ciphertext":"already-migrated","associated_data":{"schema":%q}}`,
		confidentialAEADV1Schema, orgID, orgID, confidentialAEADV1Schema)
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

	migrator := NewLegacyOCKMigrator(projector, encryptor, nil, nil, nil)
	migrator.Run(context.Background())

	if encryptor.calls != 0 {
		t.Errorf("expected 0 encrypt calls for already-migrated records, got %d", encryptor.calls)
	}
}

func TestLegacyOCKMigrator_DecryptsHistoricalN1ViaKeyer(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	secret, err := gonostr.SecretKeyFromHex(key)
	if err != nil {
		t.Fatal(err)
	}
	pubkey := secret.Public()
	conversation, err := nip44.GenerateConversationKey(pubkey, secret)
	if err != nil {
		t.Fatal(err)
	}
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	plaintext := fmt.Sprintf(`{"org_id":%q,"name":"secret"}`, orgID)
	content, err := nip44.Encrypt(plaintext, conversation)
	if err != nil {
		t.Fatal(err)
	}
	topic := kinds.CPStateTopicSecretRegistry
	history := &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
		"t:" + topic: {makeRecord("historical-n1", pubkey.Hex(), content, "secret-1", topic)},
	}}
	n1 := &testLegacyN1Decryptor{keyer: keyer.NewPlainKeySigner(secret), pubkey: pubkey}
	publisher := &fakeProjectionPublisher{}
	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey.Hex(),
		history: history, publisher: publisher, logger: zap.NewNop()}, encryptor, nil, n1, nil)
	report, err := m.RunChecked(context.Background())
	if err != nil || !report.Complete || report.Topics[topic].Migrated != 1 || n1.calls != 1 || len(publisher.published) != 1 {
		t.Fatalf("historical N1 keyer migration: %+v, %v, decrypts=%d, publishes=%d", report, err, n1.calls, len(publisher.published))
	}
	if _, ok := encryptor.encrypted[orgID+":"+plaintext]; !ok {
		t.Fatal("legacy plaintext was not re-encrypted for the same org")
	}
}

func TestLegacyOCKMigrator_N1UnavailableNeverFallsBackToRawKey(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	secret, err := gonostr.SecretKeyFromHex(key)
	if err != nil {
		t.Fatal(err)
	}
	pubkey := secret.Public()
	conversation, err := nip44.GenerateConversationKey(pubkey, secret)
	if err != nil {
		t.Fatal(err)
	}
	content, err := nip44.Encrypt(`{"org_id":"org-1"}`, conversation)
	if err != nil {
		t.Fatal(err)
	}
	topic := kinds.CPStateTopicSecretRegistry
	for _, tc := range []struct {
		name      string
		decryptor LegacyN1Decryptor
	}{
		{"missing", nil},
		{"denied", &testLegacyN1Decryptor{err: fmt.Errorf("writer epoch denied")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
				"t:" + topic: {makeRecord("legacy-n1", pubkey.Hex(), content, "secret-1", topic)},
			}}
			publisher := &fakeProjectionPublisher{}
			encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
			m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey.Hex(),
				history: history, publisher: publisher, logger: zap.NewNop()}, encryptor, nil, tc.decryptor, nil)
			report, err := m.RunChecked(context.Background())
			if err == nil || report.Complete || report.Topics[topic].Failed != 1 || encryptor.calls != 0 || len(publisher.published) != 0 {
				t.Fatalf("N1 failure used raw fallback or published: %+v, %v", report, err)
			}
		})
	}
}

func TestLegacyOCKMigrator_N1CanceledBeforeKeyer(t *testing.T) {
	n1 := &testLegacyN1Decryptor{err: fmt.Errorf("must not call")}
	m := NewLegacyOCKMigrator(nil, nil, nil, n1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.tryLegacyDecrypt(ctx, "ciphertext", legacyOCKTopic{decryptKind: legacyDecryptN1}); err == nil || n1.calls != 0 {
		t.Fatalf("canceled N1 invoked Keyer: calls=%d err=%v", n1.calls, err)
	}
}

func TestLegacyOCKMigrator_MigratesLegacyO1Records(t *testing.T) {
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	legacyContent := `{"schema":"bahia.org-state.aead.v1","algorithm":"xchacha20-poly1305","key_ref":"org-state","nonce":"AAAA","ciphertext":"BBBB"}`
	plaintext := fmt.Sprintf(`{"id":%q,"name":"test-org"}`, orgID)

	// Use a deterministic keypair so servicePubkey is known.
	// Private key 0x01 → pubkey = generator point (79BE...) in compressed form.
	// For simplicity, just use a fixed pubkey that matches the test.
	servicePubkey, _ := publicKeyHexFromPrivateKeyHex("0000000000000000000000000000000000000000000000000000000000000001")

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

	publisher := &fakeProjectionPublisher{}
	projector := &Projector{
		privateKey:    "0000000000000000000000000000000000000000000000000000000000000001",
		servicePubkey: servicePubkey,
		enabled:       true,
		history:       history,
		publisher:     publisher,
		logger:        zap.NewNop(),
	}

	migrator := NewLegacyOCKMigrator(projector, encryptor, legacyO1, nil, nil)
	report, err := migrator.RunChecked(context.Background())
	if err != nil || !report.Complete || report.Topics[kinds.CPStateTopicOrgRegistry].Migrated != 1 {
		t.Fatalf("unexpected migration result: report=%+v err=%v", report, err)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published %d events, want 1", len(publisher.published))
	}

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

	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	history := &fakeProjectionHistory{
		records: map[string][]repository.NostrEventRecord{
			"t:" + kinds.CPStateTopicOrgRegistry: {
				makeRecord("event1", pubkey, legacyContent, orgID, kinds.CPStateTopicOrgRegistry),
			},
		},
	}

	projector := &Projector{enabled: true, privateKey: key, servicePubkey: pubkey, history: history, publisher: &fakeProjectionPublisher{}, logger: zap.NewNop()}

	migrator := NewLegacyOCKMigrator(projector, encryptor, legacyO1, nil, nil)
	first, err := migrator.RunChecked(context.Background())
	if err != nil || !first.Complete {
		t.Fatalf("first run failed: %+v, %v", first, err)
	}
	firstRun := encryptor.calls
	first.Topics[kinds.CPStateTopicOrgRegistry] = LegacyOCKMigrationTopicReport{}
	second, err := migrator.RunChecked(context.Background())
	if err != nil || !second.Complete || second.Topics[kinds.CPStateTopicOrgRegistry].Migrated != 1 {
		t.Fatalf("cached report mutated: %+v, %v", second, err)
	}
	if encryptor.calls != firstRun {
		t.Errorf("second Run should be a no-op: calls went from %d to %d", firstRun, encryptor.calls)
	}
}

func TestLegacyOCKMigrator_HandlesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
	projector := &Projector{enabled: true, history: &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{}}, logger: zap.NewNop()}
	migrator := NewLegacyOCKMigrator(projector, encryptor, nil, nil, nil)
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

func TestLegacyOCKMigrator_CheckedPublishFailure(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	topic := kinds.CPStateTopicOrgRegistry
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	content := `{"schema":"bahia.org-state.aead.v1"}`
	history := &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
		"t:" + topic: {makeRecord("publish-fails", pubkey, content, orgID, topic)},
	}}
	publisher := &fakeProjectionPublisher{err: fmt.Errorf("relay rejected publish")}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey,
		history: history, publisher: publisher, logger: zap.NewNop()},
		&fakeConfidentialEncryptor{encrypted: map[string]string{}},
		&fakeLegacyO1Decryptor{plaintext: map[string][]byte{content: []byte(fmt.Sprintf(`{"id":%q}`, orgID))}}, nil, nil)
	report, err := m.RunChecked(context.Background())
	if err == nil || report.Complete || report.Topics[topic].Failed != 1 || report.Topics[topic].Migrated != 0 {
		t.Fatalf("failed publish certified: %+v, %v", report, err)
	}
}

func TestLegacyOCKMigrator_RetriesTransientQueryAndPublish(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	topic := kinds.CPStateTopicOrgRegistry
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	content := `{"schema":"bahia.org-state.aead.v1"}`
	history := &fakeProjectionHistory{err: fmt.Errorf("temporary read failure"), records: map[string][]repository.NostrEventRecord{
		"t:" + topic: {makeRecord("retry", pubkey, content, orgID, topic)},
	}}
	publisher := &fakeProjectionPublisher{err: fmt.Errorf("temporary publish failure")}
	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey,
		history: history, publisher: publisher, logger: zap.NewNop()}, encryptor,
		&fakeLegacyO1Decryptor{plaintext: map[string][]byte{content: []byte(fmt.Sprintf(`{"id":%q}`, orgID))}}, nil, nil)
	if report, err := m.RunChecked(context.Background()); err == nil || report.Complete {
		t.Fatalf("query failure certified: %+v", report)
	}
	history.err = nil
	if report, err := m.RunChecked(context.Background()); err == nil || report.Complete || report.Topics[topic].Failed != 1 {
		t.Fatalf("publish failure certified: %+v", report)
	}
	publisher.err = nil
	// The projector owns a bounded relay-backoff state after rejection; a
	// fresh projector instance models retry after that window has cleared.
	m.projector = &Projector{enabled: true, privateKey: key, servicePubkey: pubkey,
		history: history, publisher: publisher, logger: zap.NewNop()}
	report, err := m.RunChecked(context.Background())
	if err != nil || !report.Complete || report.Topics[topic].Migrated != 1 || len(publisher.published) != 2 {
		t.Fatalf("transient failures not retried: %+v, %v, publishes=%d", report, err, len(publisher.published))
	}
}

func TestLegacyOCKMigrator_RetryDoesNotRepublishPriorSuccess(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	topic := kinds.CPStateTopicOrgRegistry
	orgID := "550e8400-e29b-41d4-a716-446655440000"
	content := `{"schema":"bahia.org-state.aead.v1"}`
	history := &fakeProjectionHistory{err: fmt.Errorf("temporary later-topic failure"), errForTag: kinds.CPStateTopicSecretRegistry,
		records: map[string][]repository.NostrEventRecord{
			"t:" + topic: {makeRecord("already-published", pubkey, content, orgID, topic)},
		}}
	publisher := &fakeProjectionPublisher{}
	encryptor := &fakeConfidentialEncryptor{encrypted: map[string]string{}}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey,
		history: history, publisher: publisher, logger: zap.NewNop()}, encryptor,
		&fakeLegacyO1Decryptor{plaintext: map[string][]byte{content: []byte(fmt.Sprintf(`{"id":%q}`, orgID))}}, nil, nil)
	first, err := m.RunChecked(context.Background())
	if err == nil || first.Complete || first.Topics[topic].Migrated != 1 {
		t.Fatalf("partial attempt incorrect: %+v, %v", first, err)
	}
	history.err = nil
	second, err := m.RunChecked(context.Background())
	if err != nil || !second.Complete || second.Topics[topic].Migrated != 1 || encryptor.calls != 1 || len(publisher.published) != 1 {
		t.Fatalf("retry republished prior success: %+v, %v, encryptions=%d publishes=%d", second, err, encryptor.calls, len(publisher.published))
	}
}

type blockingMigrationHistory struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingMigrationHistory) FindByTag(_ context.Context, _, _ string, _ []int, _ int) ([]repository.NostrEventRecord, error) {
	h.once.Do(func() { close(h.entered); <-h.release })
	return nil, nil
}
func (h *blockingMigrationHistory) ListByKind(context.Context, int, int) ([]repository.NostrEventRecord, error) {
	return nil, nil
}

func TestLegacyOCKMigrator_ConcurrentCanceledCallerDoesNotPoisonRetry(t *testing.T) {
	history := &blockingMigrationHistory{entered: make(chan struct{}), release: make(chan struct{})}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, servicePubkey: "service", history: history, logger: zap.NewNop()},
		&fakeConfidentialEncryptor{encrypted: map[string]string{}}, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := m.RunChecked(ctx); first <- err }()
	<-history.entered
	go func() {
		report, err := m.RunChecked(context.Background())
		if !report.Complete && err == nil {
			err = fmt.Errorf("incomplete without error")
		}
		second <- err
	}()
	cancel()
	close(history.release)
	if err := <-first; err == nil {
		t.Fatal("canceled caller unexpectedly succeeded")
	}
	if err := <-second; err != nil {
		t.Fatalf("second caller inherited cancellation: %v", err)
	}
}

func TestLegacyOCKMigrator_CheckedFailureAccounting(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	topic := kinds.CPStateTopicOrgRegistry
	history := &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
		"t:" + topic: {
			makeRecord("unreadable", pubkey, `{"schema":"bahia.org-state.aead.v1"}`, "org-1", topic),
			makeRecord("foreign", "another-author", `{"schema":"bahia.org-state.aead.v1"}`, "org-2", topic),
		},
	}}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, privateKey: key, servicePubkey: pubkey, history: history, logger: zap.NewNop()},
		&fakeConfidentialEncryptor{encrypted: map[string]string{}}, &fakeLegacyO1Decryptor{}, nil, nil)
	report, err := m.RunChecked(context.Background())
	if err == nil || report.Complete {
		t.Fatalf("unreadable record certified: %+v, %v", report, err)
	}
	stats := report.Topics[topic]
	if stats.Scanned != 2 || stats.ForeignAuthor != 1 || stats.Failed != 1 || len(report.Failures) != 1 || report.Failures[0].EventID != "unreadable" {
		t.Fatalf("incorrect accounting: %+v", report)
	}
	second, secondErr := m.RunChecked(context.Background())
	if secondErr == nil || second.Complete || len(second.Failures) != 1 {
		t.Fatalf("second attempt hid failure: %+v, %v", second, secondErr)
	}
}

func TestLegacyOCKMigrator_CheckedPrerequisitesAndCancellation(t *testing.T) {
	var nilMigrator *LegacyOCKMigrator
	if report, err := nilMigrator.RunChecked(context.Background()); err == nil || report.Complete {
		t.Fatalf("nil migrator certified: %+v", report)
	}
	m := NewLegacyOCKMigrator(&Projector{enabled: true, history: &fakeProjectionHistory{}},
		&fakeConfidentialEncryptor{encrypted: map[string]string{}}, nil, nil, nil)
	if report, err := m.RunChecked(context.Background()); err == nil || report.Complete {
		t.Fatalf("missing service identity certified: %+v", report)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m = NewLegacyOCKMigrator(&Projector{enabled: true, servicePubkey: "service", history: &fakeProjectionHistory{}},
		&fakeConfidentialEncryptor{encrypted: map[string]string{}}, nil, nil, nil)
	if report, err := m.RunChecked(ctx); err == nil || report.Complete {
		t.Fatalf("cancelled scan certified: %+v", report)
	}
}

func TestLegacyOCKMigrator_CheckedQueryAndBoundedLimit(t *testing.T) {
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	pubkey, _ := publicKeyHexFromPrivateKeyHex(key)
	topic := kinds.CPStateTopicOrgRegistry
	for _, tc := range []struct {
		name    string
		history *fakeProjectionHistory
	}{
		{"query error", &fakeProjectionHistory{err: fmt.Errorf("store unavailable")}},
		{"full page", &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{"t:" + topic: make([]repository.NostrEventRecord, legacyOCKScanLimit)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewLegacyOCKMigrator(&Projector{enabled: true, servicePubkey: pubkey, history: tc.history, logger: zap.NewNop()},
				&fakeConfidentialEncryptor{encrypted: map[string]string{}}, nil, nil, nil)
			report, err := m.RunChecked(context.Background())
			if err == nil || report.Complete || len(report.Failures) == 0 || tc.history.requestedLimit != legacyOCKScanLimit {
				t.Fatalf("unproven scan certified or unbounded: %+v, %v", report, err)
			}
		})
	}
}

func TestIsConfidentialAEADV1(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"new format", newFormatContent("org-1"), true},
		{"schema-only is incomplete", fmt.Sprintf(`{"schema":%q}`, confidentialAEADV1Schema), false},
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
