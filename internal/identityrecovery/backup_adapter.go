package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// BackupRecoveryAdapter reasserts backup configuration and run-state heads.
// It publishes signed state only; it never invokes backup executors, restore
// providers, schedules or retention effects. Existing org ownership remains
// the signed org tag and content org_id as required by backup_ownership.go.
type BackupRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

type backupRecoveredRecord struct {
	family       int
	coordinate   string
	deleted      bool
	tags         nostr.Tags
	content      string
	recipe       *domain.BackupRecipe
	policy       *domain.BackupPolicy
	repository   *domain.BackupRepository
	definition   *domain.BackupDefinition
	run          *domain.BackupRun
	verification *domain.BackupVerificationRecord
}

func backupRecoveryFamily(family int) bool {
	switch family {
	case adapter.KindBackupRecipeRegistry, adapter.KindBackupPolicyRegistry, adapter.KindBackupRepositoryRegistry, adapter.KindBackupDefinitionRegistry, adapter.KindBackupRunState:
		return true
	}
	return false
}

func (a *BackupRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{
		old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, name: "backup-canonical-families",
		belongs: func(ev nostr.Event) bool {
			family, _ := strconv.Atoi(tag(ev, "legacy_kind"))
			return backupRecoveryFamily(family) || strings.HasPrefix(tag(ev, "d"), "backup-recipe:") || strings.HasPrefix(tag(ev, "d"), "backup-policy:") || strings.HasPrefix(tag(ev, "d"), "backup-repository:") || strings.HasPrefix(tag(ev, "d"), "backup-definition:") || strings.HasPrefix(tag(ev, "d"), "backup-run:")
		},
		preflight: a.preflight, publish: a.publish,
	}
}

func parseBackupID(body string) (uuid.UUID, uuid.UUID, bool, error) {
	var base struct {
		ID      uuid.UUID `json:"id"`
		OrgID   uuid.UUID `json:"org_id"`
		Deleted bool      `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(body), &base); err != nil {
		return uuid.Nil, uuid.Nil, false, err
	}
	if base.ID == uuid.Nil || base.OrgID == uuid.Nil {
		return uuid.Nil, uuid.Nil, false, errors.New("backup canonical owner or ID missing")
	}
	return base.ID, base.OrgID, base.Deleted, nil
}

func optionalBackupID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return nil, errors.New("invalid backup policy ID tag")
	}
	return &id, nil
}

func buildBackupRecovery(old nostr.Event) (backupRecoveredRecord, error) {
	r := backupRecoveredRecord{}
	r.family, _ = strconv.Atoi(tag(old, "legacy_kind"))
	if !backupRecoveryFamily(r.family) {
		return r, errors.New("unsupported backup canonical family")
	}
	id, org, deleted, err := parseBackupID(old.Content)
	if err != nil {
		return r, err
	}
	r.deleted = deleted
	if tag(old, "deleted") != strconv.FormatBool(deleted) || tag(old, "org") != org.String() {
		return r, errors.New("backup canonical ownership or deletion tag mismatch")
	}
	switch r.family {
	case adapter.KindBackupRecipeRegistry:
		var v domain.BackupRecipe
		if err := json.Unmarshal([]byte(old.Content), &v); err != nil {
			return r, err
		}
		if deleted {
			v.Name = tag(old, "name")
			v.Backend = domain.BackupBackendKind(tag(old, "backend"))
			v.TargetRef, v.Version = tag(old, "target"), tag(old, "version")
			v.RepositoryID, err = uuid.Parse(tag(old, "repository_id"))
			if err != nil {
				return r, err
			}
			v.PolicyID, err = optionalBackupID(tag(old, "policy_id"))
			if err != nil {
				return r, err
			}
		}
		r.coordinate = adapter.BackupRecipeDTag(id)
		r.tags, r.content = adapter.BackupRecipeRegistryRecord(&v, deleted)
		r.recipe = &v
	case adapter.KindBackupPolicyRegistry:
		var v domain.BackupPolicy
		if err := json.Unmarshal([]byte(old.Content), &v); err != nil {
			return r, err
		}
		if deleted {
			v.Name = tag(old, "name")
			v.RequireVerification, err = strconv.ParseBool(tag(old, "require_verification"))
			if err != nil {
				return r, err
			}
			v.VerificationMode = domain.BackupVerificationMode(tag(old, "verification"))
		}
		r.coordinate = adapter.BackupPolicyDTag(id)
		r.tags, r.content = adapter.BackupPolicyRegistryRecord(&v, deleted)
		r.policy = &v
	case adapter.KindBackupRepositoryRegistry:
		var v domain.BackupRepository
		if err := json.Unmarshal([]byte(old.Content), &v); err != nil {
			return r, err
		}
		if deleted {
			v.Name, v.Backend = tag(old, "name"), domain.BackupBackendKind(tag(old, "backend"))
		}
		r.coordinate = adapter.BackupRepositoryDTag(id)
		r.tags, r.content = adapter.BackupRepositoryRegistryRecord(&v, deleted)
		r.repository = &v
	case adapter.KindBackupDefinitionRegistry:
		var v domain.BackupDefinition
		if err := json.Unmarshal([]byte(old.Content), &v); err != nil {
			return r, err
		}
		if deleted {
			v.Name, v.Group = tag(old, "name"), tag(old, "group")
			v.RepositoryID, err = uuid.Parse(tag(old, "repository_id"))
			if err != nil {
				return r, err
			}
			v.PolicyID, err = uuid.Parse(tag(old, "policy_id"))
			if err != nil {
				return r, err
			}
			v.RecipeID, err = uuid.Parse(tag(old, "recipe_id"))
			if err != nil {
				return r, err
			}
			v.ScheduleEnabled, err = strconv.ParseBool(tag(old, "schedule_enabled"))
			if err != nil {
				return r, err
			}
		}
		r.coordinate = adapter.BackupDefinitionDTag(id)
		r.tags, r.content = adapter.BackupDefinitionRegistryRecord(&v, deleted)
		r.definition = &v
	case adapter.KindBackupRunState:
		if deleted {
			return r, errors.New("backup run tombstone has no production publisher")
		}
		var v domain.BackupRun
		if err := json.Unmarshal([]byte(old.Content), &v); err != nil {
			return r, err
		}
		if v.DefinitionID == uuid.Nil || v.RecipeID == uuid.Nil || v.RepositoryID == uuid.Nil {
			return r, errors.New("backup run ownership references missing")
		}
		var nested struct {
			Verification *domain.BackupVerificationRecord `json:"verification"`
		}
		if err := json.Unmarshal([]byte(old.Content), &nested); err != nil {
			return r, err
		}
		r.verification = nested.Verification
		r.coordinate = adapter.BackupRunDTag(id)
		r.tags, r.content = adapter.BackupRunStateRecord(&v, r.verification)
		r.run = &v
	}
	if r.coordinate != tag(old, "d") {
		return r, errors.New("backup canonical coordinate mismatch")
	}
	kind, envelope := adapter.ControlStateEnvelope(r.family, r.coordinate, deleted)
	if old.Kind != nostr.Kind(kind) || !reflect.DeepEqual(old.Tags, append(envelope, r.tags...)) || old.Content != r.content {
		return r, errors.New("typed backup builder cannot reproduce accepted head")
	}
	return r, nil
}

func (a *BackupRecoveryAdapter) preflight(old nostr.Event) error {
	_, err := buildBackupRecovery(old)
	return err
}

type backupRecoveryVerifier struct {
	record *domain.BackupVerificationRecord
}

func (v backupRecoveryVerifier) GetBackupVerificationByRunID(context.Context, uuid.UUID) (*domain.BackupVerificationRecord, error) {
	return v.record, nil
}

func (a *BackupRecoveryAdapter) publish(ctx context.Context, signer nostr.Keyer, old nostr.Event, sink registryRecoverySink) error {
	r, err := buildBackupRecovery(old)
	if err != nil {
		return err
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil {
		return err
	}
	projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
	publisher := adapter.NewBackupCanonicalPublisher(projector, zap.NewNop())
	if r.deleted {
		id, _, _, err := parseBackupID(old.Content)
		if err != nil {
			return err
		}
		return publisher.PublishDeleted(ctx, r.family, r.coordinate, r.tags, r.content, "backup.recovery", &id)
	}
	switch r.family {
	case adapter.KindBackupRecipeRegistry:
		return publisher.PublishRecipe(ctx, r.recipe)
	case adapter.KindBackupPolicyRegistry:
		return publisher.PublishPolicy(ctx, r.policy)
	case adapter.KindBackupRepositoryRegistry:
		return publisher.PublishRepository(ctx, r.repository)
	case adapter.KindBackupDefinitionRegistry:
		return publisher.PublishDefinition(ctx, r.definition)
	case adapter.KindBackupRunState:
		publisher.SetRunVerifier(backupRecoveryVerifier{record: r.verification})
		return publisher.PublishRun(ctx, r.run)
	}
	return fmt.Errorf("unsupported backup recovery family")
}

func (a *BackupRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}
func (a *BackupRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	return a.core().Reassert(ctx, signer)
}
func (a *BackupRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	return a.core().Verify(ctx)
}
