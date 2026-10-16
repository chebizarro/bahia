package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SBOMRecoveryAdapter reasserts reference and availability heads, not new
// attestation statements. The latter belong to bahia-4f10q.3.3. Availability
// links are the one author-dependent exception to byte-for-byte reassertion:
// old-author a tags and content coordinates are rewritten to new-key heads.
type SBOMRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

func (a *SBOMRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.SBOMReference), nostr.Kind(kinds.SBOMAvailabilityList)}, name: "sbom-references-and-availability",
		belongs: func(ev nostr.Event) bool {
			return ev.Kind == nostr.Kind(kinds.SBOMReference) && tag(ev, "t") == kinds.SBOMReferenceTopic || ev.Kind == nostr.Kind(kinds.SBOMAvailabilityList) && tag(ev, "t") == kinds.SBOMAvailabilityTopic
		},
		preflight: validateSBOMRecoveryHead,
		publish: func(context.Context, nostr.Keyer, nostr.Event, registryRecoverySink) error {
			return ErrAdapterNotRegistered
		},
	}
}

func sbomSubject(ev nostr.Event) domain.SBOMSubject {
	s := domain.SBOMSubject{Type: domain.SBOMSubjectType(tag(ev, "subject_type")), Digest: tag(ev, "subject")}
	switch s.Type {
	case domain.SBOMSubjectArtifact:
		s.ID = tag(ev, "artifact")
	case domain.SBOMSubjectDeployment:
		s.ID = tag(ev, "deployment")
	case domain.SBOMSubjectPackage:
		s.ID = tag(ev, "package")
	case domain.SBOMSubjectRepository:
		s.ID = tag(ev, "repo")
	}
	return s
}

func sbomReferenceRecord(old nostr.Event) (*nostr.Event, error) {
	s := sbomSubject(old)
	var statement domain.SBOMAttestation
	if json.Unmarshal([]byte(old.Content), &statement) != nil {
		return nil, errors.New("SBOM reference statement invalid")
	}
	at := time.Unix(int64(old.CreatedAt), 0).UTC()
	built, d, err := sbom.BuildSBOMReferenceEvent(sbom.BuildSBOMReferenceEventInput{Subject: s, Attestation: &statement, CreatedAt: &at})
	if err != nil || d != tag(old, "d") || built.Content != old.Content || !reflect.DeepEqual(built.Tags, old.Tags) {
		return nil, errors.New("SBOM typed reference differs from accepted head")
	}
	return built, nil
}

func sbomAvailabilityRecord(old nostr.Event, publisher string, refs map[string]nostr.Event) (*nostr.Event, error) {
	s := sbomSubject(old)
	s.DisplayName = strings.TrimPrefix(tag(old, "title"), "SBOMs for ")
	var index domain.SBOMIndex
	if json.Unmarshal([]byte(old.Content), &index) != nil || index.SubjectID != s.ID || index.SubjectType != string(s.Type) {
		return nil, errors.New("SBOM availability index invalid")
	}
	entries := slices.Clone(index.Entries)
	if refs != nil {
		for i := range entries {
			entry := &entries[i]
			if entry.ReferencePubkey != "" && entry.ReferencePubkey != old.PubKey.Hex() {
				return nil, errors.New("SBOM availability references unreviewed author")
			}
			if entry.ReferenceDTag == "" {
				return nil, errors.New("SBOM availability reference d tag missing")
			}
			ref, ok := refs[entry.ReferenceDTag]
			if !ok || ref.PubKey.Hex() != publisher || tag(ref, "d") != entry.ReferenceDTag || tag(ref, "subject") != entry.SubjectDigest {
				return nil, errors.New("SBOM availability reference linkage broken")
			}
			entry.ReferencePubkey = publisher
			entry.AttestationID = fmt.Sprintf("%d:%s:%s", sbom.KindSBOMReference, publisher, entry.ReferenceDTag)
		}
	}
	at := time.Unix(int64(old.CreatedAt), 0).UTC()
	built, d, err := sbom.BuildSBOMAvailabilityListEvent(sbom.BuildSBOMAvailabilityListEventInput{Subject: s, Entries: entries, PublisherPubkey: publisher, CreatedAt: &at})
	if err != nil || d != tag(old, "d") {
		return nil, errors.New("SBOM typed availability differs from coordinate")
	}
	return built, nil
}

func validateSBOMRecoveryHead(old nostr.Event) error {
	switch old.Kind {
	case nostr.Kind(kinds.SBOMReference):
		_, err := sbomReferenceRecord(old)
		return err
	case nostr.Kind(kinds.SBOMAvailabilityList):
		built, err := sbomAvailabilityRecord(old, old.PubKey.Hex(), nil)
		if err != nil || built.Content != old.Content || !reflect.DeepEqual(built.Tags, old.Tags) {
			return errors.New("SBOM typed availability cannot reproduce accepted head")
		}
		return nil
	}
	return errors.New("unsupported SBOM recovery kind")
}

func (a *SBOMRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}

func validateAcceptedSBOMLinks(heads map[string]nostr.Event) error {
	refs := map[string]nostr.Event{}
	for _, ev := range heads {
		if ev.Kind == nostr.Kind(kinds.SBOMReference) {
			refs[tag(ev, "d")] = ev
		}
	}
	for _, ev := range heads {
		if ev.Kind == nostr.Kind(kinds.SBOMAvailabilityList) {
			if _, err := sbomAvailabilityRecord(ev, ev.PubKey.Hex(), refs); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *SBOMRecoveryAdapter) expected(old nostr.Event, pub nostr.PubKey, refs map[string]nostr.Event) (*nostr.Event, error) {
	if old.Kind == nostr.Kind(kinds.SBOMReference) {
		return sbomReferenceRecord(old)
	}
	return sbomAvailabilityRecord(old, pub.Hex(), refs)
}

func (a *SBOMRecoveryAdapter) newReferenceHeads(ctx context.Context, pub nostr.PubKey, heads map[string]nostr.Event) (map[string]nostr.Event, error) {
	refs := map[string]nostr.Event{}
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		if old.Kind != nostr.Kind(kinds.SBOMReference) {
			continue
		}
		relay, local, err := a.core().newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil || relay.ID != local.ID || relay.PubKey != pub || !relay.CheckID() || !relay.VerifySignature() || !equivalent(*relay, old) {
			return nil, fmt.Errorf("SBOM reference %s linkage unavailable: %w", tag(old, "d"), ErrIncomplete)
		}
		refs[tag(old, "d")] = *relay
	}
	return refs, nil
}

func (a *SBOMRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	core := a.core()
	heads, err := core.heads(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptedSBOMLinks(heads); err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, fmt.Errorf("SBOM signer: %w", ErrAdapterNotRegistered)
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil || pub.Hex() != s.NewPubkey || pub == a.OldPubkey {
		return nil, errors.New("SBOM new signer mismatch")
	}
	sink := registryRecoverySink{source: a.Source, local: a.Local}
	proof := make([]string, 0, len(heads))
	for _, kind := range []nostr.Kind{nostr.Kind(kinds.SBOMReference), nostr.Kind(kinds.SBOMAvailabilityList)} {
		refs := map[string]nostr.Event{}
		if kind == nostr.Kind(kinds.SBOMAvailabilityList) {
			refs, err = a.newReferenceHeads(ctx, pub, heads)
			if err != nil {
				return nil, err
			}
		}
		for _, key := range sortedCoordinates(heads) {
			old := heads[key]
			if old.Kind != kind {
				continue
			}
			expected, buildErr := a.expected(old, pub, refs)
			if buildErr != nil {
				return nil, buildErr
			}
			relay, local, readErr := core.newHeads(ctx, pub, old)
			if readErr != nil {
				return nil, readErr
			}
			for _, head := range []*nostr.Event{relay, local} {
				if head != nil && (head.PubKey != pub || !head.CheckID() || !head.VerifySignature() || head.Content != expected.Content || !reflect.DeepEqual(head.Tags, expected.Tags)) {
					return nil, errors.New("SBOM new-key head differs from reconstructed record")
				}
			}
			switch {
			case relay != nil && local != nil:
			case relay != nil:
				err = a.Local.StoreEvent(ctx, *relay)
			case local != nil:
				err = a.Source.Publish(ctx, *local)
			default:
				expected.CreatedAt = nostr.Now()
				if err = signer.SignEvent(ctx, expected); err == nil {
					err = sink.publish(ctx, *expected)
				}
			}
			if err != nil {
				return nil, err
			}
			relay, local, err = core.newHeads(ctx, pub, old)
			if err != nil || relay == nil || local == nil || relay.ID != local.ID || relay.Content != expected.Content || !reflect.DeepEqual(relay.Tags, expected.Tags) {
				return nil, fmt.Errorf("SBOM coordinate %s: %w", key, ErrIncomplete)
			}
			proof = append(proof, relay.ID.Hex())
		}
	}
	slices.Sort(proof)
	return proof, nil
}

func (a *SBOMRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	core := a.core()
	heads, err := core.heads(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateAcceptedSBOMLinks(heads); err != nil {
		return nil, err
	}
	s, err := core.state()
	if err != nil {
		return nil, err
	}
	pub, err := nostr.PubKeyFromHex(s.NewPubkey)
	if err != nil || pub == a.OldPubkey {
		return nil, errors.New("SBOM new identity unavailable")
	}
	refs, err := a.newReferenceHeads(ctx, pub, heads)
	if err != nil {
		return nil, err
	}
	proof := make([]string, 0, len(heads))
	for _, key := range sortedCoordinates(heads) {
		old := heads[key]
		expected, err := a.expected(old, pub, refs)
		if err != nil {
			return nil, err
		}
		relay, local, err := core.newHeads(ctx, pub, old)
		if err != nil || relay == nil || local == nil || relay.ID != local.ID {
			return nil, fmt.Errorf("SBOM coordinate %s: %w", key, ErrIncomplete)
		}
		for _, head := range []*nostr.Event{relay, local} {
			if head.PubKey != pub || !head.CheckID() || !head.VerifySignature() || head.Content != expected.Content || !reflect.DeepEqual(head.Tags, expected.Tags) {
				return nil, errors.New("SBOM new-key head or author linkage invalid")
			}
		}
		proof = append(proof, relay.ID.Hex())
	}
	slices.Sort(proof)
	return proof, nil
}
