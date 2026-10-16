package identityrecovery

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func backupFixture(t *testing.T) (*BackupRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer, []nostr.Event) {
	t.Helper()
	oldKey := signer(t)
	id, org, repoID, policyID, recipeID, definitionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Unix(1_700_000_000, 0).UTC()
	recipe := domain.BackupRecipe{ID: recipeID, OrgID: org, Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: repoID, PolicyID: &policyID, TargetRef: "service:db", CreatedAt: now, UpdatedAt: now}
	policy := domain.BackupPolicy{ID: policyID, OrgID: org, Name: "verified", RequireVerification: true, CreatedAt: now, UpdatedAt: now}
	repo := domain.BackupRepository{ID: repoID, OrgID: org, Name: "main", Backend: domain.BackupBackendKopia, RepositoryURI: "s3://backup", CreatedAt: now, UpdatedAt: now}
	definition := domain.BackupDefinition{ID: definitionID, OrgID: org, Name: "db", RepositoryID: repoID, PolicyID: policyID, RecipeID: recipeID, ScheduleEnabled: true, CreatedAt: now, UpdatedAt: now}
	run := domain.BackupRun{ID: id, OrgID: org, DefinitionID: definitionID, RecipeID: recipeID, RepositoryID: repoID, PolicyID: &policyID, Backend: domain.BackupBackendKopia, Status: domain.RunStatusSucceeded, CreatedAt: now, UpdatedAt: now}
	verification := &domain.BackupVerificationRecord{ID: uuid.New(), Mode: domain.BackupVerificationMode("integrity"), Status: domain.BackupVerificationSucceeded, Verified: true}
	var heads []nostr.Event
	for _, item := range []struct {
		family  int
		d       string
		tags    nostr.Tags
		content string
	}{
		func() struct {
			family  int
			d       string
			tags    nostr.Tags
			content string
		} {
			tags, content := adapter.BackupRecipeRegistryRecord(&recipe, false)
			return struct {
				family  int
				d       string
				tags    nostr.Tags
				content string
			}{adapter.KindBackupRecipeRegistry, adapter.BackupRecipeDTag(recipeID), tags, content}
		}(),
		func() struct {
			family  int
			d       string
			tags    nostr.Tags
			content string
		} {
			tags, content := adapter.BackupPolicyRegistryRecord(&policy, false)
			return struct {
				family  int
				d       string
				tags    nostr.Tags
				content string
			}{adapter.KindBackupPolicyRegistry, adapter.BackupPolicyDTag(policyID), tags, content}
		}(),
		func() struct {
			family  int
			d       string
			tags    nostr.Tags
			content string
		} {
			tags, content := adapter.BackupRepositoryRegistryRecord(&repo, false)
			return struct {
				family  int
				d       string
				tags    nostr.Tags
				content string
			}{adapter.KindBackupRepositoryRegistry, adapter.BackupRepositoryDTag(repoID), tags, content}
		}(),
		func() struct {
			family  int
			d       string
			tags    nostr.Tags
			content string
		} {
			tags, content := adapter.BackupDefinitionRegistryRecord(&definition, false)
			return struct {
				family  int
				d       string
				tags    nostr.Tags
				content string
			}{adapter.KindBackupDefinitionRegistry, adapter.BackupDefinitionDTag(definitionID), tags, content}
		}(),
		func() struct {
			family  int
			d       string
			tags    nostr.Tags
			content string
		} {
			tags, content := adapter.BackupRunStateRecord(&run, verification)
			return struct {
				family  int
				d       string
				tags    nostr.Tags
				content string
			}{adapter.KindBackupRunState, adapter.BackupRunDTag(id), tags, content}
		}(),
	} {
		kind, envelope := adapter.ControlStateEnvelope(item.family, item.d, false)
		heads = append(heads, signedRegistryHead(t, oldKey, kind, append(envelope, item.tags...), item.content, int64(100+len(heads))))
	}
	base, relay, local, key := registryFixture(t, adapter.KindServiceRegistry, heads)
	return &BackupRecoveryAdapter{OldPubkey: heads[0].PubKey, Source: relay, Local: local, Ledger: base.Ledger}, relay, local, key, heads
}

func TestBackupRecoveryRoundTripIdempotenceAndEquivalence(t *testing.T) {
	a, relay, local, key, heads := backupFixture(t)
	ctx := context.Background()
	ids, err := a.Inventory(ctx)
	if err != nil || len(ids) != len(heads) || !slices.Contains(ids, heads[0].ID.Hex()) {
		t.Fatalf("inventory %v: %v", ids, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != len(heads) || local.writes != len(heads) {
		t.Fatalf("writes relay=%d local=%d", relay.publishes, local.writes)
	}
	if proof, err := a.Verify(ctx); err != nil || len(proof) != 2 {
		t.Fatalf("verify %v: %v", proof, err)
	}
	if _, err := a.Reassert(ctx, key); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != len(heads) || local.writes != len(heads) {
		t.Fatal("idempotent rerun wrote heads")
	}
	bad := signedRegistryHead(t, key, int(heads[0].Kind), heads[0].Tags, `{"changed":true}`, time.Now().Unix()+100)
	relay.events = append(relay.events, bad)
	local.events = append(local.events, bad)
	if _, err := a.Verify(ctx); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent verify: %v", err)
	}
	if _, err := a.Reassert(ctx, key); err == nil || relay.publishes != len(heads) {
		t.Fatalf("divergent reassert: %v", err)
	}
}

func TestBackupRecoveryPreservesOwnerAndRefusesEOSEFailure(t *testing.T) {
	a, relay, _, key, heads := backupFixture(t)
	if tag(heads[0], "org") == "" || tag(heads[0], "org") != tag(heads[1], "org") {
		t.Fatal("backup owner tags missing")
	}
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil {
		t.Fatal("EOSE failure accepted as absence")
	}
	if _, err := a.Reassert(context.Background(), key); err == nil || relay.publishes != 0 {
		t.Fatalf("EOSE failure published: %v", err)
	}
}

func TestBackupRecoveryRejectsOwnerMismatch(t *testing.T) {
	_, _, _, _, heads := backupFixture(t)
	bad := heads[0]
	bad.Tags = slices.Clone(bad.Tags)
	for i := range bad.Tags {
		if len(bad.Tags[i]) >= 2 && bad.Tags[i][0] == "org" {
			bad.Tags[i] = nostr.Tag{"org", uuid.NewString()}
		}
	}
	if _, err := buildBackupRecovery(bad); err == nil {
		t.Fatal("mismatched owner tag accepted")
	}
}

func TestBackupRecoveryRecipeTombstone(t *testing.T) {
	oldKey := signer(t)
	policyID := uuid.New()
	recipe := &domain.BackupRecipe{ID: uuid.New(), OrgID: uuid.New(), Name: "retired", Version: "v2", Backend: domain.BackupBackendKopia, RepositoryID: uuid.New(), PolicyID: &policyID, TargetRef: "service:retired"}
	tags, content := adapter.BackupRecipeRegistryRecord(recipe, true)
	kind, envelope := adapter.ControlStateEnvelope(adapter.KindBackupRecipeRegistry, adapter.BackupRecipeDTag(recipe.ID), true)
	old := signedRegistryHead(t, oldKey, kind, append(envelope, tags...), content, 100)
	base, relay, local, key := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	a := &BackupRecoveryAdapter{OldPubkey: old.PubKey, Source: relay, Local: local, Ledger: base.Ledger}
	if _, err := a.Reassert(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}
