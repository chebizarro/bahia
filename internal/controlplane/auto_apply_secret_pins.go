package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// pinDecisionSecrets binds only credential references, never values, in the
// signed desired state. A decision created without this binding cannot later
// be upgraded to auto-apply by inspecting a newer credential version.
func pinDecisionSecrets(ctx context.Context, spec *domain.DesiredServiceSpec, records secretstore.Records, store secretstore.SecretStore) error {
	if spec == nil {
		return fmt.Errorf("desired state is required")
	}
	if len(spec.SecretRefs) == 0 {
		return nil
	}
	if records == nil || store == nil {
		return fmt.Errorf("auto-apply secret pinning requires canonical secret records and Signet")
	}
	org, err := records.ServiceOrgID(ctx, spec.ServiceID)
	if err != nil {
		return fmt.Errorf("resolve canonical service organization: %w", err)
	}
	orgID, err := uuid.Parse(org)
	if err != nil || orgID == uuid.Nil {
		return fmt.Errorf("canonical service organization is invalid")
	}
	scope := secretstore.Scope{OrgID: orgID, ServiceID: spec.ServiceID}
	pins := make([]domain.SecretCredentialRef, len(spec.SecretRefs))
	for i, desired := range spec.SecretRefs {
		ref, err := records.CurrentSecretRef(ctx, desired.SecretID)
		if err != nil {
			return fmt.Errorf("read canonical secret %s: %w", desired.SecretID, err)
		}
		if ref == nil || ref.ServiceID != spec.ServiceID || ref.Name != desired.Name ||
			(ref.EnvironmentID != nil && *ref.EnvironmentID != spec.EnvironmentID) ||
			ref.CredentialRef == nil {
			return fmt.Errorf("secret %s is not a matching store-backed canonical reference", desired.SecretID)
		}
		pin := *ref.CredentialRef
		if err := secretstore.ValidateRef(pin); err != nil {
			return fmt.Errorf("secret %s: %w", desired.SecretID, err)
		}
		metadata, err := store.Inspect(ctx, pin.CredentialID, scope)
		if err != nil {
			return fmt.Errorf("inspect secret %s: %w", desired.SecretID, err)
		}
		if metadata.Status != secretstore.StatusActive || metadata.Version != pin.Version ||
			!scope.Admits(metadata.Label) || !strings.HasSuffix(metadata.Label, "/"+ref.Name) {
			return fmt.Errorf("secret %s changed before the decision; fresh decision required", desired.SecretID)
		}
		pins[i] = pin
	}
	for i := range spec.SecretRefs {
		pin := pins[i]
		spec.SecretRefs[i].CredentialRef = &pin
	}
	spec.ComputeDesiredHash()
	return nil
}

// verifyDecisionSecrets is used on explicit approval: a proposal's pinned
// versions must still be active at the moment the signed decision is made.
func verifyDecisionSecrets(ctx context.Context, spec *domain.DesiredServiceSpec, records secretstore.Records, store secretstore.SecretStore) error {
	if spec == nil {
		return fmt.Errorf("desired state is required")
	}
	for _, ref := range spec.SecretRefs {
		if ref.CredentialRef == nil {
			return fmt.Errorf("secret %s has no decision-time credential pin; fresh decision required", ref.SecretID)
		}
	}
	copySpec := *spec
	copySpec.SecretRefs = append([]domain.DesiredSecretRef(nil), spec.SecretRefs...)
	if err := pinDecisionSecrets(ctx, &copySpec, records, store); err != nil {
		return err
	}
	for i := range spec.SecretRefs {
		if *copySpec.SecretRefs[i].CredentialRef != *spec.SecretRefs[i].CredentialRef {
			return fmt.Errorf("secret %s changed before approval; fresh decision required", spec.SecretRefs[i].SecretID)
		}
	}
	return nil
}
