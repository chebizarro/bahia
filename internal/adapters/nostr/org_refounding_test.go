package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"go.uber.org/zap"
)

type refoundingEncryptor struct {
	version   int
	rotations int
}

func (e *refoundingEncryptor) RotateKey(context.Context, string) error {
	e.version++
	e.rotations++
	return nil
}
func (e *refoundingEncryptor) CurrentKeyVersion(context.Context, string) (string, error) {
	return fmt.Sprintf("v%d", e.version), nil
}
func (e *refoundingEncryptor) WrapKeyForMember(context.Context, string, string) error { return nil }
func (e *refoundingEncryptor) EncryptConfidential(_ context.Context, org string, plaintext []byte, _ int, _, _ string, inner []byte) (string, error) {
	content, err := json.Marshal(map[string]any{"schema": confidentialAEADV1Schema, "key_org": org,
		"key_version": fmt.Sprintf("v%d", e.version), "plaintext": string(plaintext), "service_inner": string(inner)})
	return string(content), err
}
func (e *refoundingEncryptor) DecryptConfidential(_ context.Context, content string, _ int, _, _ string) ([]byte, error) {
	var value struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return nil, err
	}
	return []byte(value.Plaintext), nil
}
func (e *refoundingEncryptor) DecryptServiceInner(_ context.Context, content string) ([]byte, error) {
	var value struct {
		Inner string `json:"service_inner"`
	}
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return nil, err
	}
	return []byte(value.Inner), nil
}

func refoundingRecord(t *testing.T, pubkey, org, version, d, topic, inner string) repository.NostrEventRecord {
	t.Helper()
	content, err := json.Marshal(map[string]any{"schema": confidentialAEADV1Schema, "key_org": org,
		"key_version": version, "plaintext": d, "service_inner": inner})
	if err != nil {
		t.Fatal(err)
	}
	return makeRecord("event-"+d+version, pubkey, string(content), d, topic)
}

func TestOrgRefoundingRepublishesTwoOldVersionsOnSameCoordinates(t *testing.T) {
	ctx := context.Background()
	org := uuid.New().String()
	other := uuid.New().String()
	sink := &captureProjectionPublisher{}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
	pubkey, err := publicKeyHexFromPrivateKeyHex(projector.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	history := &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
		"t:" + kinds.CPStateTopicOrgMemberRegistry: {
			refoundingRecord(t, pubkey, org, "v2", "member-1", kinds.CPStateTopicOrgMemberRegistry, ""),
			refoundingRecord(t, pubkey, org, "v1", "member-2", kinds.CPStateTopicOrgMemberRegistry, ""),
			refoundingRecord(t, pubkey, org, "v1", "member-1", kinds.CPStateTopicOrgMemberRegistry, ""),
			refoundingRecord(t, pubkey, other, "v1", "other-member", kinds.CPStateTopicOrgMemberRegistry, ""),
		},
		"t:" + kinds.CPStateTopicNotificationChannelRegistry: {
			refoundingRecord(t, pubkey, org, "v1", "channel-1", kinds.CPStateTopicNotificationChannelRegistry, "secret"),
		},
	}}
	projector.history = history
	encryptor := &refoundingEncryptor{version: 2}
	publisher := NewOrgCanonicalPublisher(projector, encryptor, zap.NewNop())
	version, count, err := publisher.Rekey(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if version != "v3" || count != 3 || encryptor.rotations != 1 {
		t.Fatalf("version=%s count=%d rotations=%d", version, count, encryptor.rotations)
	}
	coordinates := map[string]bool{}
	for _, event := range sink.snapshot() {
		d := tagValue(event.Tags, "d")
		if coordinates[d] {
			t.Fatalf("duplicate coordinate %q", d)
		}
		coordinates[d] = true
		var content struct {
			Version string `json:"key_version"`
			Inner   string `json:"service_inner"`
		}
		if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
			t.Fatal(err)
		}
		if content.Version != "v3" {
			t.Fatalf("%s version %q", d, content.Version)
		}
		if d == "channel-1" && content.Inner != "secret" {
			t.Fatalf("service inner lost: %q", content.Inner)
		}
	}
	if len(coordinates) != 3 {
		t.Fatalf("coordinates = %v", coordinates)
	}
}

func TestOrgPublishMemberStrictRevocationToggle(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, removed := range []bool{false, true} {
			t.Run(fmt.Sprintf("strict=%t/removed=%t", strict, removed), func(t *testing.T) {
				ctx := context.Background()
				org := uuid.New()
				sink := &captureProjectionPublisher{}
				projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, nil, zap.NewNop())
				pubkey, err := publicKeyHexFromPrivateKeyHex(projector.privateKey)
				if err != nil {
					t.Fatal(err)
				}
				projector.history = &fakeProjectionHistory{records: map[string][]repository.NostrEventRecord{
					"t:" + kinds.CPStateTopicOrgRegistry: {refoundingRecord(t, pubkey, org.String(), "v1", org.String(), kinds.CPStateTopicOrgRegistry, "")},
				}}
				encryptor := &refoundingEncryptor{version: 1}
				publisher := NewOrgCanonicalPublisher(projector, encryptor, zap.NewNop())
				publisher.SetStrictRevocationLookup(func(context.Context, uuid.UUID) (bool, error) { return strict, nil })
				member := &domain.OrgMember{OrgID: org, Pubkey: "member", Role: domain.RoleViewer}
				if err := publisher.PublishMember(ctx, member, removed, domain.RoleAdmin); err != nil {
					t.Fatal(err)
				}
				if encryptor.rotations != 1 {
					t.Fatalf("rotations=%d", encryptor.rotations)
				}
				want := 1
				if strict {
					want = 2
				}
				if got := len(sink.snapshot()); got != want {
					t.Fatalf("published=%d want=%d", got, want)
				}
			})
		}
	}
}

func TestOrgRefoundingReplacesStoredCoordinate(t *testing.T) {
	ctx := context.Background()
	org := uuid.New().String()
	sink := &captureProjectionPublisher{}
	repo := NewLocalEventRepository(openTestLocalStore(t, ""), nil)
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), sink, repo, zap.NewNop())
	pubkey, err := publicKeyHexFromPrivateKeyHex(projector.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	projector.history = repo.Authored(pubkey)
	encryptor := &refoundingEncryptor{version: 1}
	content, err := encryptor.EncryptConfidential(ctx, org, []byte(`{"id":"record"}`), KindOrgRegistry, org, kinds.CPStateTopicOrgRegistry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := projector.publishControlState(ctx, KindOrgRegistry, org, false, nil, content, "org.projection", nil); err != nil {
		t.Fatal(err)
	}
	publisher := NewOrgCanonicalPublisher(projector, encryptor, zap.NewNop())
	version, count, err := publisher.Rekey(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if version != "v2" || count != 1 {
		t.Fatalf("version=%s count=%d", version, count)
	}
	records, err := projector.history.FindByTag(ctx, "t", kinds.CPStateTopicOrgRegistry, []int{KindCASControlState}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || extractDTagFromRecord(records[0]) != org {
		t.Fatalf("latest-coordinate records = %#v", records)
	}
	var current struct {
		Version string `json:"key_version"`
	}
	if err := json.Unmarshal([]byte(records[0].Content), &current); err != nil {
		t.Fatal(err)
	}
	if current.Version != "v2" {
		t.Fatalf("stored version = %q", current.Version)
	}
}
