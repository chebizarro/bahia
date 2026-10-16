package identityrecovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// CredentialRecordSource supplies all canonical records that can carry a
// credential_ref. A nil source must fail the recovery phase closed.
type CredentialRecordSource interface {
	CredentialRefs(context.Context) ([]SecretRef, error)
}

// RotationWorkItem remains in the engine ledger across interrupted phases.
type RotationWorkItem struct {
	Ref                      SecretRef                   `json:"ref"`
	Replacement              *domain.SecretCredentialRef `json:"replacement,omitempty"`
	SourceRotationEvidenceID string                      `json:"source_rotation_evidence_id,omitempty"`
	WaiverEvidenceID         string                      `json:"waiver_evidence_id,omitempty"`
}

type RotationUpdate struct {
	RefID                    string                      `json:"ref_id"`
	NewRef                   *domain.SecretCredentialRef `json:"new_ref,omitempty"`
	SourceRotationEvidenceID string                      `json:"source_rotation_evidence_id,omitempty"`
	WaiverEvidenceID         string                      `json:"waiver_evidence_id,omitempty"`
}

// LedgerRotationTracker inventories references and records only operator-
// supplied replacement references and evidence. Rotation occurs at source.
type LedgerRotationTracker struct {
	Ledger      *Ledger
	Source      CredentialRecordSource
	SystemKeys  config.SystemKeysConfig
	UpdatesPath string
}

func rotationID(label string, ref domain.SecretCredentialRef) string {
	sum := sha256.Sum256([]byte(label + "\x00" + ref.Store + "\x00" + ref.CredentialID + "\x00" + strconv.Itoa(ref.Version)))
	return hex.EncodeToString(sum[:])
}

func (t *LedgerRotationTracker) Inventory(ctx context.Context) ([]SecretRef, error) {
	if t == nil || t.Ledger == nil || t.Source == nil {
		return nil, fmt.Errorf("rotation inventory: %w", ErrAdapterNotRegistered)
	}
	refs, err := t.Source.CredentialRefs(ctx)
	if err != nil {
		return nil, err
	}
	for purpose, keys := range map[string][]config.SystemKeyConfig{
		"assistant-transcript": t.SystemKeys.AssistantTranscript,
		"confidential-dedupe":  t.SystemKeys.ConfidentialDedupe,
	} {
		for _, key := range keys {
			label := "bahia-system/" + purpose + "/" + key.KeyID
			ref := domain.SecretCredentialRef{Store: secretstore.BackendSignet, CredentialID: key.CredentialID, Version: key.Version}
			refs = append(refs, SecretRef{Provider: ref.Store, Label: label, Credential: ref})
		}
	}
	existing, err := t.Ledger.RotationItems()
	if err != nil {
		return nil, err
	}
	// Index replacements by the same label/ref identity used for work items.
	// A source or ledger iteration order must not decide which refs are suppressed.
	replacementIDs := make(map[string]struct{}, len(existing))
	for id, item := range existing {
		if item.Ref.ID != id || rotationID(item.Ref.Label, item.Ref.Credential) != id || secretstore.ValidateRef(item.Ref.Credential) != nil {
			return nil, errors.New("rotation ledger reference invalid")
		}
		if item.Replacement != nil {
			if secretstore.ValidateRef(*item.Replacement) != nil || *item.Replacement == item.Ref.Credential {
				return nil, errors.New("rotation ledger replacement invalid")
			}
			replacementIDs[rotationID(item.Ref.Label, *item.Replacement)] = struct{}{}
		}
	}
	scanned := map[string]SecretRef{}
	for i := range refs {
		ref := &refs[i]
		if ref.Label == "" || secretstore.ValidateRef(ref.Credential) != nil || ref.Provider != ref.Credential.Store {
			return nil, errors.New("invalid rotation inventory reference")
		}
		expected := rotationID(ref.Label, ref.Credential)
		if ref.ID != "" && ref.ID != expected {
			return nil, errors.New("rotation inventory id mismatch")
		}
		ref.ID = expected
		if _, replacement := replacementIDs[ref.ID]; replacement {
			continue
		}
		scanned[ref.ID] = *ref
	}
	for id, item := range existing {
		scanned[id] = item.Ref
	}
	for id, ref := range scanned {
		if _, ok := existing[id]; !ok {
			if err := t.Ledger.SaveRotationItem(RotationWorkItem{Ref: ref}); err != nil {
				return nil, err
			}
		}
	}
	unique := make([]SecretRef, 0, len(scanned))
	for _, ref := range scanned {
		unique = append(unique, ref)
	}
	slices.SortFunc(unique, func(a, b SecretRef) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return unique, nil
}

func (t *LedgerRotationTracker) Rotate(ctx context.Context, refs []SecretRef) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t == nil || t.Ledger == nil || t.UpdatesPath == "" {
		return nil, fmt.Errorf("rotation updates: %w", ErrAdapterNotRegistered)
	}
	raw, err := os.ReadFile(t.UpdatesPath)
	if err != nil {
		return nil, errors.New("rotation updates unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var updates []RotationUpdate
	if err := decoder.Decode(&updates); err != nil {
		return nil, errors.New("invalid rotation updates")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing or invalid rotation updates")
	}
	items, err := t.Ledger.RotationItems()
	if err != nil {
		return nil, err
	}
	allowed := map[string]struct{}{}
	for _, ref := range refs {
		allowed[ref.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, update := range updates {
		item, ok := items[update.RefID]
		if _, permitted := allowed[update.RefID]; !ok || !permitted {
			return nil, errors.New("rotation update outside inventory")
		}
		if _, duplicate := seen[update.RefID]; duplicate {
			return nil, errors.New("duplicate rotation update")
		}
		seen[update.RefID] = struct{}{}
		waiver := update.WaiverEvidenceID != ""
		if waiver {
			if update.NewRef != nil || update.SourceRotationEvidenceID != "" || (!evidenceToken.MatchString(update.WaiverEvidenceID) || strings.Contains(strings.ToLower(update.WaiverEvidenceID), "nsec1")) {
				return nil, errors.New("invalid rotation waiver")
			}
		} else {
			if update.NewRef == nil || (!evidenceToken.MatchString(update.SourceRotationEvidenceID) || strings.Contains(strings.ToLower(update.SourceRotationEvidenceID), "nsec1")) || secretstore.ValidateRef(*update.NewRef) != nil || *update.NewRef == item.Ref.Credential {
				return nil, errors.New("new credential reference and source evidence required")
			}
		}
		item.Replacement = update.NewRef
		item.SourceRotationEvidenceID = update.SourceRotationEvidenceID
		item.WaiverEvidenceID = update.WaiverEvidenceID
		if err := t.Ledger.SaveRotationItem(item); err != nil {
			return nil, err
		}
	}
	items, err = t.Ledger.RotationItems()
	if err != nil {
		return nil, err
	}
	receipts := map[string]string{}
	for _, ref := range refs {
		item := items[ref.ID]
		if item.WaiverEvidenceID != "" {
			receipts[ref.ID] = "waiver:" + ref.ID + ":" + item.WaiverEvidenceID
		}
		if item.SourceRotationEvidenceID != "" && item.Replacement != nil {
			receipts[ref.ID] = "rotation:" + ref.ID + ":" + item.SourceRotationEvidenceID
		}
	}
	return receipts, nil
}
