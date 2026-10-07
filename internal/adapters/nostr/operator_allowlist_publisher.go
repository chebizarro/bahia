package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// KindOperatorAllowlistRecord is the operator allowlist cp-state family
// It is a 30900-only family with no catalog kind.
const KindOperatorAllowlistRecord = int(kinds.CPStateFamilyOperatorAllowlist)

// OperatorAllowlistRecord is the plaintext of one operator allowlist record.
// It is the only place the operator pubkeys appear: the event tags carry the
// scope (in d) and the family envelope, never a pubkey.
type OperatorAllowlistRecord struct {
	Scope     string    `json:"scope"`
	Pubkeys   []string  `json:"pubkeys"`
	UpdatedAt time.Time `json:"updated_at"`
}

// OperatorAllowlistPublisher publishes the daemon's operator allowlists as
// fleet-OCK encrypted cp-state (family 32029, t=operator-allowlist): one
// replaceable record per scope on "operators:<scope>". Holders of the fleet
// OCK (fleet operators, bootstrap owners and the service) decrypt the list and
// can trust the other authorized operators' documents; relays and everyone
// else see only the scope and an opaque ciphertext. Records go through the
// canonical-first path, so a publish is durable in the outbox before the
// caller sees a result and an unchanged list is not republished.
type OperatorAllowlistPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
	logger    *zap.Logger
	now       func() time.Time
}

// NewOperatorAllowlistPublisher creates a publisher backed by projector.
// encryptor is required: an allowlist is never published in plaintext.
func NewOperatorAllowlistPublisher(projector *Projector, encryptor ConfidentialStateEncryptor, logger *zap.Logger) *OperatorAllowlistPublisher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &OperatorAllowlistPublisher{projector: projector, encryptor: encryptor, logger: logger.Named("operator-allowlist"), now: time.Now}
}

func (p *OperatorAllowlistPublisher) available() error {
	if p == nil || p.projector == nil || !p.projector.Enabled() {
		return fmt.Errorf("operator allowlist publisher is unavailable")
	}
	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext publish of operator allowlist")
	}
	return nil
}

var operatorAllowlistScopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// NormalizeOperatorPubkeys lower-cases, deduplicates and sorts the 64-hex
// pubkeys of an allowlist; anything else is dropped. The result is stable, so
// an unchanged configuration yields an unchanged record.
func NormalizeOperatorPubkeys(pubkeys []string) []string {
	seen := make(map[string]struct{}, len(pubkeys))
	out := make([]string, 0, len(pubkeys))
	for _, raw := range pubkeys {
		pk := strings.ToLower(strings.TrimSpace(raw))
		if len(pk) != 64 || !hexOnly(pk) {
			continue
		}
		if _, dup := seen[pk]; dup {
			continue
		}
		seen[pk] = struct{}{}
		out = append(out, pk)
	}
	sort.Strings(out)
	return out
}

// PublishOperatorAllowlist publishes scope's allowlist. An empty list (after
// normalisation) replaces the record with a tombstone, so a scope that was
// disabled or emptied in config stops authorizing anyone.
func (p *OperatorAllowlistPublisher) PublishOperatorAllowlist(ctx context.Context, scope string, pubkeys []string) error {
	if err := p.available(); err != nil {
		return err
	}
	scope = strings.TrimSpace(scope)
	if !operatorAllowlistScopePattern.MatchString(scope) {
		return fmt.Errorf("operator allowlist scope %q is invalid", scope)
	}
	dTag := kinds.OperatorAllowlistDTag(scope)
	family := cpStateFamilies[KindOperatorAllowlistRecord]
	normalized := NormalizeOperatorPubkeys(pubkeys)
	deleted := len(normalized) == 0
	var plaintext []byte
	if deleted {
		plaintext = []byte("{}")
	} else {
		var err error
		plaintext, err = json.Marshal(OperatorAllowlistRecord{Scope: scope, Pubkeys: normalized, UpdatedAt: p.now().UTC()})
		if err != nil {
			return err
		}
	}
	encrypted, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, plaintext, KindOperatorAllowlistRecord, dTag, family.topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt operator allowlist %s: %w", scope, err)
	}
	// No extra tags: a pubkey in a tag would disclose the operators to relays.
	if err := p.projector.publishCanonicalFirst(ctx, KindOperatorAllowlistRecord, dTag, deleted, nil, string(plaintext), encrypted, "operator_allowlist.projection", nil); err != nil {
		return fmt.Errorf("publish operator allowlist %s: %w", scope, err)
	}
	p.logger.Info("operator allowlist published", zap.String("scope", scope), zap.Int("operators", len(normalized)), zap.Bool("tombstone", deleted))
	return nil
}
