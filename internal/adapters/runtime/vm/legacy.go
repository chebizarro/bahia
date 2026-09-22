package vm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// LegacyProof is created only with a new definition. Old unmarked directories
// remain observable but are never mutation or garbage-collection authority.
type LegacyProof struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	DefinitionDigest string    `json:"definition_digest"`
}

const LegacyProofFile = "legacy-ownership.json"

type LegacyVerifier interface {
	VerifyLegacy(context.Context, string, uuid.UUID) error
}

func WriteLegacyProof(ctx context.Context, dir string, proof LegacyProof) error {
	if proof.ID == uuid.Nil || proof.Name != filepath.Base(dir) || !sha256DigestPattern.MatchString(proof.DefinitionDigest) {
		return ProviderError(domain.VMErrorIntegrity, nil)
	}
	return writeJSON(ctx, filepath.Join(dir, LegacyProofFile), proof)
}
func ReadLegacyProof(root, name string) (*LegacyProof, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return nil, ProviderError(domain.VMErrorForeign, nil)
	}
	path := filepath.Join(root, name, LegacyProofFile)
	if err := CheckContainedPath(root, path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ProviderError(domain.VMErrorForeign, err)
	}
	var p LegacyProof
	if json.Unmarshal(data, &p) != nil || p.ID == uuid.Nil || p.Name != name || !sha256DigestPattern.MatchString(p.DefinitionDigest) {
		return nil, ProviderError(domain.VMErrorForeign, nil)
	}
	return &p, nil
}

func (r *Runtime) verifyLegacy(ctx context.Context, md *InstanceMetadata) error {
	if md == nil || md.OwnershipID == uuid.Nil || md.RuntimeType != string(r.cfg.RuntimeType) {
		return ProviderError(domain.VMErrorForeign, nil)
	}
	env := uuid.Nil
	if md.EnvironmentID != "" {
		var err error
		env, err = uuid.Parse(md.EnvironmentID)
		if err != nil {
			return ProviderError(domain.VMErrorForeign, err)
		}
	}
	if md.Name != InstanceName(env, md.ServiceName) {
		return ProviderError(domain.VMErrorForeign, nil)
	}
	if err := CheckContainedPath(r.instancesDir(), filepath.Join(r.instancesDir(), md.Name)); err != nil {
		return err
	}
	verifier, ok := r.hv.(LegacyVerifier)
	if !ok {
		return ProviderError(domain.VMErrorUnsupported, nil)
	}
	return verifier.VerifyLegacy(ctx, md.Name, md.OwnershipID)
}
