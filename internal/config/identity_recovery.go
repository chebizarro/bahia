package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

var recoveryIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func (c *Config) ValidateIdentityRecovery() error {
	r := &c.IdentityRecovery
	if strings.TrimSpace(c.Nostr.ServiceID) == "" || strings.TrimSpace(r.LedgerPath) == "" || strings.TrimSpace(r.ReasonCode) == "" || len(r.ControlledRelays) == 0 || len(r.ConsumerRoster) == 0 || r.AcceptedEventIDs == nil {
		return errors.New("identity_recovery requires service_id, ledger_path, reason_code, controlled_relays, consumer_roster and reviewed accepted_event_ids")
	}
	if !recoveryIdentifierPattern.MatchString(c.Nostr.ServiceID) || !recoveryIdentifierPattern.MatchString(r.ReasonCode) {
		return errors.New("recovery service_id and reason_code must be stable machine identifiers")
	}
	seenIDs := map[string]struct{}{}
	for _, id := range r.AcceptedEventIDs {
		parsed, err := nostr.IDFromHex(id)
		if err != nil || parsed.Hex() != id {
			return errors.New("accepted_event_ids must be lowercase event IDs")
		}
		if _, dup := seenIDs[id]; dup {
			return errors.New("duplicate accepted event ID")
		}
		seenIDs[id] = struct{}{}
	}
	if c.Nostr.LocalStore.Path == "" {
		return errors.New("nostr.local_store.path required")
	}
	ledger, _ := filepath.Abs(r.LedgerPath)
	store, _ := filepath.Abs(c.Nostr.LocalStore.Path)
	if ledger == store || ledger == "" {
		return errors.New("recovery ledger must be separate from daemon store")
	}
	if _, err := nostr.PubKeyFromHex(r.OldPubkey); err != nil {
		return errors.New("identity_recovery.old_pubkey invalid")
	}
	if r.Operator.Signer.ClientSecretKey != "" || r.NewIdentity.Signer.ClientSecretKey != "" || r.OldSigner.Signer.ClientSecretKey != "" {
		return errors.New("recovery signer client keys must be mounted files, not config values")
	}
	if r.Operator.PublicKey == "" || r.Operator.PublicKey == r.OldPubkey {
		return errors.New("independent operator public key required")
	}
	if err := r.Operator.validate(); err != nil {
		return fmt.Errorf("identity_recovery.operator: %w", err)
	}
	if r.NewIdentity.PublicKey == "" || r.NewIdentity.PublicKey == r.OldPubkey || r.NewIdentity.PublicKey == r.Operator.PublicKey {
		return errors.New("distinct new identity public key required")
	}
	n := NostrConfig{PublicKey: r.NewIdentity.PublicKey, Signer: r.NewIdentity.Signer}
	if n.Signer.Method == NostrSignerLocal || n.Signer.Method == "" {
		return errors.New("new identity signer must not use raw local key in recovery config")
	}
	if err := n.validateServiceSigner(); err != nil {
		return fmt.Errorf("identity_recovery.new_identity: %w", err)
	}
	r.NewIdentity.PublicKey = n.PublicKey
	r.NewIdentity.Signer = n.Signer
	if r.OldSigner.PublicKey != "" || r.OldSigner.Signer.Method != "" {
		if r.OldSigner.PublicKey != r.OldPubkey {
			return errors.New("quarantined signer must match old identity")
		}
		if err := r.OldSigner.validate(); err != nil {
			return fmt.Errorf("identity_recovery.old_signer: %w", err)
		}
	}
	if slices.Contains(r.ControlledRelays, "") || slices.Contains(r.ConsumerRoster, "") {
		return errors.New("empty recovery relay or consumer id")
	}
	if !slices.Equal(r.ConsumerRoster, trustepoch.ConsumerIDs()) || len(r.AcknowledgmentAuthorities) != len(trustepoch.ConsumerIDs()) {
		return errors.New("complete trust-epoch roster and acknowledgment authority table required")
	}
	seenComponentKeys := map[string]bool{}
	for _, id := range r.ConsumerRoster {
		authority := r.AcknowledgmentAuthorities[id]
		parsed, parseErr := nostr.PubKeyFromHex(authority)
		if parseErr != nil || parsed.Hex() != authority {
			return fmt.Errorf("invalid acknowledgment authority for %s", id)
		}
		switch id {
		case "daemon":
			if authority != r.NewIdentity.PublicKey {
				return errors.New("daemon acknowledgment authority must be new service key")
			}
		case "cli", "web", "pinned-runtimes":
			if authority != r.Operator.PublicKey {
				return fmt.Errorf("%s acknowledgment authority must be operator root", id)
			}
		default:
			if authority == r.OldPubkey || authority == r.NewIdentity.PublicKey || authority == r.Operator.PublicKey || seenComponentKeys[authority] {
				return fmt.Errorf("%s requires an independent component acknowledgment authority", id)
			}
			seenComponentKeys[authority] = true
		}
	}
	operatorPubkey, err := nostr.PubKeyFromHex(r.Operator.PublicKey)
	if err != nil {
		return errors.New("operator public key invalid")
	}
	if _, err := relayguard.New(operatorPubkey, r.ControlledRelays); err != nil {
		return err
	}
	return nil
}
