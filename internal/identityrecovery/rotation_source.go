package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/secretstore"
)

// SecretRegistrySource inventories the canonical, OCK-decrypted secret view.
// It never resolves a Signet value or consults PostgreSQL.
type SecretRegistrySource struct {
	View interface {
		StoreBackedSecrets(context.Context) ([]domain.SecretRef, error)
		ServiceOrgID(context.Context, uuid.UUID) (string, error)
	}
}

func (s SecretRegistrySource) CredentialRefs(ctx context.Context) ([]SecretRef, error) {
	if s.View == nil {
		return nil, ErrAdapterNotRegistered
	}
	secrets, err := s.View.StoreBackedSecrets(ctx)
	if err != nil {
		return nil, err
	}
	refs := make([]SecretRef, 0, len(secrets))
	for _, secret := range secrets {
		if secret.CredentialRef == nil || secret.ServiceID == uuid.Nil || secret.Name == "" {
			return nil, errors.New("canonical secret reference incomplete")
		}
		if err := secretstore.ValidateRef(*secret.CredentialRef); err != nil {
			return nil, errors.New("canonical secret reference invalid")
		}
		org, err := s.View.ServiceOrgID(ctx, secret.ServiceID)
		if err != nil {
			return nil, err
		}
		orgID, err := uuid.Parse(org)
		if err != nil || orgID == uuid.Nil {
			return nil, errors.New("canonical service organization invalid")
		}
		label := orgID.String() + "/" + secret.ServiceID.String() + "/" + secret.Name
		refs = append(refs, SecretRef{Provider: secret.CredentialRef.Store, Label: label, Credential: *secret.CredentialRef})
	}
	return refs, nil
}

// CanonicalCredentialSource scans accepted current heads from both audited
// relays and the durable local store. It covers every nested credential_ref
// triple, including backup snapshots and runtime desired state. Opaque UUID
// references (HiveCI mirror-read, LLM/ML provider, notification and deployment
// settings) resolve through their canonical secret-registry records instead.
type CanonicalCredentialSource struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
	OldSigner func(context.Context) (nostr.Keyer, error)
}

func (s CanonicalCredentialSource) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{old: s.OldPubkey, source: s.Source, local: s.Local, ledger: s.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, name: "canonical-credential-refs",
		belongs: func(ev nostr.Event) bool {
			return ev.Kind == nostr.Kind(kinds.CASControlState) && tag(ev, "legacy_kind") != ""
		},
		preflight: func(ev nostr.Event) error {
			if tag(ev, "d") == "" || tag(ev, "legacy_kind") == "" || tag(ev, "deleted") != "false" && tag(ev, "deleted") != "true" {
				return errors.New("canonical credential record envelope invalid")
			}
			return nil
		},
		publish: func(context.Context, nostr.Keyer, nostr.Event, registryRecoverySink) error {
			return ErrAdapterNotRegistered
		},
	}
}

type recoveryOCKHistory []domain.KeyEnvelopeRecord

func (h recoveryOCKHistory) FindKeyEnvelopes(_ context.Context, orgID string) ([]domain.KeyEnvelopeRecord, error) {
	out := make([]domain.KeyEnvelopeRecord, 0)
	for _, r := range h {
		if strings.HasPrefix(r.DTag, "org-key:"+orgID+":") {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s CanonicalCredentialSource) CredentialRefs(ctx context.Context) ([]SecretRef, error) {
	heads, err := s.core().heads(ctx)
	if err != nil {
		return nil, err
	}
	var history recoveryOCKHistory
	for _, ev := range heads {
		if tag(ev, "legacy_kind") == strconv.Itoa(adapter.KindOrgKeyEnvelope) {
			history = append(history, domain.KeyEnvelopeRecord{DTag: tag(ev, "d"), Content: ev.Content})
		}
	}
	var decryptor *controlplane.OCKManager
	var oldSigner nostr.Keyer
	for _, key := range sortedCoordinates(heads) {
		ev := heads[key]
		if tag(ev, "deleted") == "true" {
			continue
		}
		if tag(ev, "legacy_kind") == strconv.Itoa(adapter.KindOrgKeyEnvelope) {
			continue
		}
		if !strings.HasPrefix(ev.Content, "{") {
			continue
		}
		var envelope controlplane.ConfidentialEnvelope
		if json.Unmarshal([]byte(ev.Content), &envelope) != nil || envelope.Schema != controlplane.ConfidentialSchema {
			continue
		}
		if s.OldSigner == nil {
			return nil, errors.New("quarantined decryptor required for confidential rotation inventory")
		}
		oldSigner, err = s.OldSigner(ctx)
		if err != nil {
			return nil, errors.New("quarantined decryptor unavailable")
		}
		pub, err := oldSigner.GetPublicKey(ctx)
		if err != nil || pub != s.OldPubkey {
			return nil, errors.New("quarantined decryptor identity mismatch")
		}
		decryptor = controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: oldSigner, ServicePubkey: s.OldPubkey.Hex(), History: history})
		break
	}
	refs := map[string]SecretRef{}
	for _, key := range sortedCoordinates(heads) {
		ev := heads[key]
		if tag(ev, "deleted") == "true" {
			continue
		}
		if tag(ev, "legacy_kind") == strconv.Itoa(adapter.KindOrgKeyEnvelope) {
			continue
		}
		payloads := [][]byte{[]byte(ev.Content)}
		var envelope controlplane.ConfidentialEnvelope
		if json.Unmarshal([]byte(ev.Content), &envelope) == nil && envelope.Schema == controlplane.ConfidentialSchema {
			if decryptor == nil {
				return nil, errors.New("confidential rotation inventory decryptor unavailable")
			}
			version, parseErr := strconv.Atoi(strings.TrimPrefix(envelope.KeyVersion, "v"))
			if parseErr != nil || version <= 0 {
				return nil, errors.New("confidential rotation OCK version invalid")
			}
			ock, keyErr := decryptor.GetKeyByVersion(ctx, envelope.KeyOrg, version)
			if keyErr != nil {
				return nil, errors.New("confidential rotation OCK unavailable")
			}
			plain, decErr := controlplane.DecryptConfidentialContent(ock, ev.Content, controlplane.ConfidentialRecordContext{LegacyKind: mustFamily(ev), DTag: tag(ev, "d"), Topic: tag(ev, "t")})
			if decErr != nil {
				return nil, errors.New("confidential rotation payload invalid")
			}
			payloads = [][]byte{plain}
			if envelope.ServiceInner != "" {
				inner, decErr := oldSigner.Decrypt(ctx, envelope.ServiceInner, s.OldPubkey)
				if decErr != nil {
					return nil, errors.New("confidential rotation service inner unavailable")
				}
				payloads = append(payloads, []byte(inner))
			}
		}
		for i, raw := range payloads {
			var value any
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.UseNumber()
			if dec.Decode(&value) != nil {
				return nil, errors.New("canonical rotation record JSON invalid")
			}
			label := "canonical/" + tag(ev, "legacy_kind") + "/" + tag(ev, "d") + "/" + strconv.Itoa(i)
			if err := scanCredentialRefs(value, label, refs); err != nil {
				return nil, err
			}
		}
	}
	out := make([]SecretRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b SecretRef) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func mustFamily(ev nostr.Event) int { family, _ := strconv.Atoi(tag(ev, "legacy_kind")); return family }

func scanCredentialRefs(value any, label string, refs map[string]SecretRef) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			path := label + "/" + key
			if key == "credential_ref" && item != nil {
				body, ok := item.(map[string]any)
				if !ok {
					return errors.New("canonical credential_ref is not a triple")
				}
				raw, _ := json.Marshal(body)
				var ref domain.SecretCredentialRef
				dec := json.NewDecoder(strings.NewReader(string(raw)))
				dec.DisallowUnknownFields()
				if dec.Decode(&ref) != nil || secretstore.ValidateRef(ref) != nil {
					return errors.New("canonical credential_ref invalid")
				}
				id := rotationID(path, ref)
				refs[id] = SecretRef{ID: id, Provider: ref.Store, Label: path, Credential: ref}
				continue
			}
			if err := scanCredentialRefs(item, path, refs); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range v {
			if err := scanCredentialRefs(item, fmt.Sprintf("%s/%d", label, i), refs); err != nil {
				return err
			}
		}
	}
	return nil
}
