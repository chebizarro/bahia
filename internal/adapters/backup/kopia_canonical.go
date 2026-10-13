package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
	"github.com/openagentsinc/bahia/internal/service"
)

// CanonicalSnapshot is the observable result of the one mutating Kopia step.
// Its identity is a Kopia manifest ID, never a SQL operation row.
type CanonicalSnapshot struct {
	ID string
}

// CreateCanonicalSnapshot performs the tagged external effect. The caller owns
// the resolved, version-pinned Signet value and must zeroize it after return.
// The value is a JSON object containing config (Kopia repository config JSON)
// and password (Kopia repository password), never a filesystem path.
func (b *KopiaBackend) CreateCanonicalSnapshot(ctx context.Context, repo *domain.BackupRepository, recipe *domain.BackupRecipe, runID uuid.UUID, credential *secretstore.Value) (CanonicalSnapshot, error) {
	if runID == uuid.Nil || recipe == nil || repo == nil || recipe.RepositoryID != repo.ID || credential == nil {
		return CanonicalSnapshot{}, fmt.Errorf("%w: canonical Kopia snapshot inputs are incomplete", service.ErrBackupBackendConfiguration)
	}
	effectArgs, err := CanonicalSnapshotEffectArgs(recipe, runID)
	if err != nil {
		return CanonicalSnapshot{}, err
	}
	var result CanonicalSnapshot
	err = b.withCanonicalConfig(ctx, repo, credential, func(cfg *kopiaCommandConfig) error {
		args := append(cfg.baseArgs(), effectArgs...)
		stdout, err := b.runCanonical(ctx, cfg, args...)
		if err != nil {
			return err
		}
		id, _, err := parseKopiaSnapshotCreate(stdout)
		if err != nil {
			return fmt.Errorf("%w: Kopia create did not return an identifiable snapshot", service.ErrBackupBackendExecution)
		}
		result.ID = id
		return nil
	})
	return result, err
}

// ObserveCanonicalSnapshot queries Kopia's own tagged manifests, including
// incomplete ones. Only a successful listing with no tagged manifest allows
// the executor to retry an interrupted create.
func (b *KopiaBackend) ObserveCanonicalSnapshot(ctx context.Context, repo *domain.BackupRepository, runID uuid.UUID, credential *secretstore.Value) (CanonicalSnapshot, bool, error) {
	if runID == uuid.Nil || repo == nil || credential == nil {
		return CanonicalSnapshot{}, false, fmt.Errorf("%w: canonical Kopia observation inputs are incomplete", service.ErrBackupBackendConfiguration)
	}
	var result CanonicalSnapshot
	var found bool
	err := b.withCanonicalConfig(ctx, repo, credential, func(cfg *kopiaCommandConfig) error {
		args := append(cfg.baseArgs(), "snapshot", "list", "--json", "--all", "--incomplete", "--tags", canonicalRunTag(runID), "--tags", canonicalStepTag)
		stdout, err := b.runCanonical(ctx, cfg, args...)
		if err != nil {
			return err
		}
		var manifests []struct {
			ID         string            `json:"id"`
			Tags       map[string]string `json:"tags"`
			Incomplete string            `json:"incomplete"`
		}
		if err := json.Unmarshal([]byte(stdout), &manifests); err != nil {
			return fmt.Errorf("%w: Kopia tagged snapshot listing is invalid", service.ErrBackupBackendExecution)
		}
		for _, manifest := range manifests {
			if manifest.Tags["tag:bahia-run"] != runID.String() || manifest.Tags["tag:bahia-step"] != "snapshot-create" || strings.TrimSpace(manifest.ID) == "" || manifest.Incomplete != "" {
				return fmt.Errorf("%w: Kopia tagged snapshot is incomplete or mismatched", service.ErrBackupBackendExecution)
			}
			if found {
				return fmt.Errorf("%w: multiple Kopia snapshots have the same run coordinate", service.ErrBackupBackendExecution)
			}
			result.ID = manifest.ID
			found = true
		}
		return nil
	})
	return result, found, err
}

const canonicalStepTag = "bahia-step:snapshot-create"

func canonicalRunTag(runID uuid.UUID) string { return "bahia-run:" + runID.String() }

// CanonicalSnapshotEffectArgs is the nonsecret exact effect argument vector
// bound into the local receipt. The ephemeral --config-file path is added only
// at invocation time and is never persisted.
func CanonicalSnapshotEffectArgs(recipe *domain.BackupRecipe, runID uuid.UUID) ([]string, error) {
	if recipe == nil || runID == uuid.Nil {
		return nil, fmt.Errorf("%w: canonical snapshot inputs are incomplete", service.ErrBackupBackendConfiguration)
	}
	source, err := kopiaSourcePath(recipe.TargetRef)
	if err != nil {
		return nil, err
	}
	if err := validateRecipePathScope(recipe); err != nil {
		return nil, err
	}
	return []string{"snapshot", "create", "--json", "--tags", canonicalRunTag(runID), "--tags", canonicalStepTag, source}, nil
}

func (b *KopiaBackend) withCanonicalConfig(ctx context.Context, repo *domain.BackupRepository, credential *secretstore.Value, effect func(*kopiaCommandConfig) error) error {
	if b == nil || b.runner == nil || strings.TrimSpace(b.config.BinaryPath) == "" || repo == nil || repo.Backend != domain.BackupBackendKopia {
		return fmt.Errorf("%w: canonical Kopia backend is unavailable", service.ErrBackupBackendConfiguration)
	}
	for _, key := range []string{"kopia_config_file", "config_file", "kopia_password_env", "password_env"} {
		if _, configured := repo.Metadata[key]; configured {
			return fmt.Errorf("%w: canonical Kopia runs require a Signet-held config, not a path or password environment reference", service.ErrBackupBackendConfiguration)
		}
	}
	secretID, err := uuid.Parse(repo.CredentialProfile)
	if err != nil || secretID == uuid.Nil {
		return fmt.Errorf("%w: canonical Kopia repository requires a secret UUID", service.ErrBackupBackendConfiguration)
	}
	value := credential.Bytes()
	var held struct {
		Config   json.RawMessage `json:"config"`
		Password string          `json:"password"`
	}
	if err := json.Unmarshal(value, &held); err != nil || len(held.Config) == 0 || held.Config[0] != '{' || !json.Valid(held.Config) || held.Password == "" || strings.ContainsRune(held.Password, 0) {
		return fmt.Errorf("%w: Signet-held Kopia credential requires config JSON and password", service.ErrBackupBackendConfiguration)
	}
	defer clear(held.Config)
	dir, err := os.MkdirTemp("", "bahia-kopia-")
	if err != nil {
		return fmt.Errorf("%w: create private Kopia config directory", service.ErrBackupBackendExecution)
	}
	// Cleanup is best-effort; do not turn an otherwise successful backup into a failed one.
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("%w: restrict Kopia config directory", service.ErrBackupBackendExecution)
	}
	path := filepath.Join(dir, "repository.config")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("%w: create private Kopia config file", service.ErrBackupBackendExecution)
	}
	if _, err := file.Write(held.Config); err != nil {
		// The write error takes precedence over closing the failed file.
		_ = file.Close()
		return fmt.Errorf("%w: write private Kopia config file", service.ErrBackupBackendExecution)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close private Kopia config file", service.ErrBackupBackendExecution)
	}
	defer func() {
		// Best-effort overwrite before unlink; no credential bytes enter durable
		// Bahia storage, events, logs or returned errors.
		if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			_, _ = f.Write(make([]byte, len(held.Config)))
			_ = f.Sync()
			_ = f.Close()
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return effect(&kopiaCommandConfig{binary: b.config.BinaryPath, configFile: path,
		extraEnv: []string{"KOPIA_PASSWORD=" + held.Password, "KOPIA_USE_KEYRING=false", "KOPIA_LOG_DIR=" + dir}})
}

func (b *KopiaBackend) runCanonical(ctx context.Context, cfg *kopiaCommandConfig, args ...string) (string, error) {
	stdout, _, err := b.runner.Run(ctx, cfg.binary, args, cfg.extraEnv)
	if err == nil {
		return stdout, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: Kopia binary is unavailable", service.ErrBackupBackendConfiguration)
	}
	// CLI stdout, stderr and error text can echo repository credentials.
	return "", fmt.Errorf("%w: canonical Kopia invocation failed; inspect the repository out of band", service.ErrBackupBackendExecution)
}
