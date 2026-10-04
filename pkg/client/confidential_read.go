package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// ConfidentialReader resolves OCKs from signed key-envelope events using the
// operator's NIP-44 signer. It never needs or exposes the signer's private key.
type ConfidentialReader struct {
	servicePub nostr.PubKey
	keys       map[string]controlplane.OrgContentKey
}

// NewConfidentialReader trial-decrypts the service's key envelopes. An envelope
// for another recipient is expected and is not an error.
func NewConfidentialReader(ctx context.Context, signer nostr.Keyer, servicePubkey string, events []nostr.Event) (*ConfidentialReader, error) {
	if signer == nil {
		return nil, fmt.Errorf("confidential reads require a NIP-44 capable signer")
	}
	sender, err := nostr.PubKeyFromHex(servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid service pubkey: %w", err)
	}
	recipient, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("get signer pubkey: %w", err)
	}
	r := &ConfidentialReader{servicePub: sender, keys: make(map[string]controlplane.OrgContentKey)}
	for _, ev := range events {
		if ev.PubKey != sender || !ev.CheckID() || !ev.VerifySignature() {
			return nil, fmt.Errorf("invalid signed OCK envelope %s", ev.GetID())
		}
		decoded, err := DecodeControlStateEvent(ev)
		if err != nil {
			return nil, err
		}
		if decoded.LegacyKind != kinds.OrgKeyEnvelope || decoded.Topic != kinds.CPStateTopicOrgKeyEnvelope || decoded.Deleted {
			continue
		}
		orgID, version, ok := parseOCKCoordinate(decoded.DTag)
		if !ok {
			return nil, fmt.Errorf("invalid OCK envelope coordinate %q", decoded.DTag)
		}
		plaintext, err := signer.Decrypt(ctx, ev.Content, sender)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		key, wrappedFor, err := controlplane.UnmarshalOCKWrap([]byte(plaintext))
		if err != nil {
			return nil, fmt.Errorf("decode OCK envelope %s: %w", ev.GetID(), err)
		}
		var wrap controlplane.OCKWrapPayload
		if err := json.Unmarshal([]byte(plaintext), &wrap); err != nil || wrap.KeyRef != key.KeyRef() {
			return nil, fmt.Errorf("invalid OCK key reference in envelope %s", ev.GetID())
		}
		if wrappedFor != recipient.Hex() || key.OrgID != orgID || key.Version != version {
			return nil, fmt.Errorf("OCK envelope %s does not match its signed coordinate or recipient", ev.GetID())
		}
		id := ockKeyID(orgID, version)
		if previous, exists := r.keys[id]; exists && previous.Key != key.Key {
			return nil, fmt.Errorf("conflicting OCK envelopes for %s", id)
		}
		r.keys[id] = key
	}
	return r, nil
}

// Decrypt returns readable=false when this signer has no envelope for the
// record's OCK version. A malformed or tampered record is an error instead.
func (r *ConfidentialReader) Decrypt(ev nostr.Event) (plaintext []byte, readable bool, err error) {
	if ev.PubKey != r.servicePub || !ev.CheckID() || !ev.VerifySignature() {
		return nil, false, fmt.Errorf("invalid signed confidential event %s", ev.GetID())
	}
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, false, err
	}
	envelope, err := controlplane.ParseConfidentialEnvelope(ev.Content)
	if err != nil {
		return nil, false, fmt.Errorf("parse confidential event %s: %w", ev.GetID(), err)
	}
	versionText, ok := strings.CutPrefix(envelope.KeyVersion, "v")
	if !ok {
		return nil, false, fmt.Errorf("invalid OCK version %q", envelope.KeyVersion)
	}
	version, err := strconv.Atoi(versionText)
	if err != nil || version <= 0 || envelope.KeyOrg == "" || envelope.KeyRef != "ock:"+envelope.KeyOrg {
		return nil, false, fmt.Errorf("invalid OCK reference in event %s", ev.GetID())
	}
	key, ok := r.keys[ockKeyID(envelope.KeyOrg, version)]
	if !ok {
		return nil, false, nil
	}
	plaintext, err = controlplane.DecryptConfidentialContent(key, ev.Content, controlplane.ConfidentialRecordContext{
		LegacyKind: decoded.LegacyKind, DTag: decoded.DTag, Topic: decoded.Topic,
	})
	if err != nil {
		return nil, false, fmt.Errorf("decrypt confidential event %s: %w", ev.GetID(), err)
	}
	return plaintext, true, nil
}

func ockKeyID(orgID string, version int) string { return orgID + ":v" + strconv.Itoa(version) }

func parseOCKCoordinate(d string) (string, int, bool) {
	rest, ok := strings.CutPrefix(d, "org-key:")
	if !ok {
		return "", 0, false
	}
	orgID, rest, ok := strings.Cut(rest, ":v")
	if !ok || orgID == "" {
		return "", 0, false
	}
	versionText, handle, ok := strings.Cut(rest, ":")
	version, err := strconv.Atoi(versionText)
	_, handleErr := hex.DecodeString(handle)
	return orgID, version, ok && err == nil && version > 0 && len(handle) == 32 && handleErr == nil
}
