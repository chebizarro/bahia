package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	gonostr "fiatjaf.com/nostr"
)

// Rekey rotates the org content key, then replaces every current confidential
// cp-state coordinate in that key scope. A failed publish leaves the old
// coordinate readable with its old, still-distributed key version.
func (p *OrgCanonicalPublisher) Rekey(ctx context.Context, orgID string) (version string, republished int, err error) {
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.projector.history == nil || p.encryptor == nil {
		return "", 0, fmt.Errorf("refounding requires projector, history, and confidential encryptor")
	}
	if orgID == "" {
		return "", 0, fmt.Errorf("refounding requires an org key scope")
	}
	p.rekeyMu.Lock()
	defer p.rekeyMu.Unlock()
	if err := p.encryptor.RotateKey(ctx, orgID); err != nil {
		return "", 0, fmt.Errorf("rotate OCK for %s: %w", orgID, err)
	}
	return p.refoundCurrent(ctx, orgID)
}

// refoundCurrent republishes under the already-rotated key. Membership
// revocation rotates and refounds before committing its canonical event.
// The caller holds rekeyMu across the complete operation.
func (p *OrgCanonicalPublisher) refoundCurrent(ctx context.Context, orgID string) (version string, republished int, err error) {
	if p == nil || p.projector == nil || !p.projector.Enabled() || p.projector.history == nil || p.encryptor == nil {
		return "", 0, fmt.Errorf("refounding requires projector, history, and confidential encryptor")
	}
	if orgID == "" {
		return "", 0, fmt.Errorf("refounding requires an org key scope")
	}
	versions, ok := p.encryptor.(interface {
		CurrentKeyVersion(context.Context, string) (string, error)
	})
	if !ok {
		return "", 0, fmt.Errorf("confidential encryptor cannot report current key version")
	}
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(p.projector.privateKey)
	if err != nil {
		return "", 0, fmt.Errorf("resolve service publisher: %w", err)
	}
	version, err = versions.CurrentKeyVersion(ctx, orgID)
	if err != nil {
		return "", 0, fmt.Errorf("read rotated OCK version for %s: %w", orgID, err)
	}

	// The cp-state family table is authoritative. Non-confidential families are
	// ignored after inspecting their envelope schema; new confidential families
	// are therefore included without adding another topic list.
	kinds := make([]int, 0, len(cpStateFamilies))
	for kind := range cpStateFamilies {
		kinds = append(kinds, kind)
	}
	sort.Ints(kinds)
	for _, kind := range kinds {
		family := cpStateFamilies[kind]
		const scanLimit = 1000000
		records, queryErr := p.projector.history.FindByTag(ctx, "t", family.topic, []int{KindCASControlState}, scanLimit)
		if queryErr != nil {
			return version, republished, fmt.Errorf("scan %s after %d publishes: %w", family.topic, republished, queryErr)
		}
		if len(records) == scanLimit {
			return version, republished, fmt.Errorf("scan %s reached history limit after %d publishes", family.topic, republished)
		}
		seen := make(map[string]struct{}, len(records))
		for start := 0; start < len(records); start += 128 {
			end := min(start+128, len(records))
			for _, rec := range records[start:end] {
				if err := ctx.Err(); err != nil {
					return version, republished, fmt.Errorf("refounding interrupted after %d publishes: %w", republished, err)
				}
				if rec.PubKey != servicePubkey {
					continue
				}
				tags := recordTags(rec)
				dTag := tagValue(tags, "d")
				if dTag == "" {
					return version, republished, fmt.Errorf("%s record %s missing d-tag", family.topic, rec.ID)
				}
				if _, exists := seen[dTag]; exists {
					continue
				}
				seen[dTag] = struct{}{}
				var envelope struct {
					Schema     string `json:"schema"`
					KeyOrg     string `json:"key_org"`
					KeyVersion string `json:"key_version"`
				}
				if json.Unmarshal([]byte(rec.Content), &envelope) != nil || envelope.Schema != confidentialAEADV1Schema || envelope.KeyOrg != orgID || envelope.KeyVersion == version {
					continue
				}
				plaintext, decErr := p.encryptor.DecryptConfidential(ctx, rec.Content, kind, dTag, family.topic)
				if decErr != nil {
					return version, republished, fmt.Errorf("decrypt %s/%s after %d publishes: %w", family.topic, dTag, republished, decErr)
				}
				inner, innerErr := p.encryptor.DecryptServiceInner(ctx, rec.Content)
				if innerErr != nil {
					return version, republished, fmt.Errorf("decrypt service fields for %s/%s: %w", family.topic, dTag, innerErr)
				}
				ciphertext, encErr := p.encryptor.EncryptConfidential(ctx, orgID, plaintext, kind, dTag, family.topic, inner)
				if encErr != nil {
					return version, republished, fmt.Errorf("encrypt %s/%s: %w", family.topic, dTag, encErr)
				}
				extra := make(gonostr.Tags, 0, len(tags))
				for _, tag := range tags {
					if len(tag) < 2 || refoundingEnvelopeTag(tag[0]) {
						continue
					}
					extra = append(extra, tag)
				}
				if pubErr := p.projector.publishControlState(ctx, kind, dTag, tagValue(tags, "deleted") == "true", extra, ciphertext, "org_refounding", nil); pubErr != nil {
					return version, republished, fmt.Errorf("publish %s/%s after %d publishes: %w", family.topic, dTag, republished, pubErr)
				}
				republished++
			}
		}
	}
	return version, republished, nil
}

func refoundingEnvelopeTag(name string) bool {
	switch name {
	case "d", "domain", "schema", "legacy_kind", "deleted", "t":
		return true
	default:
		return false
	}
}
