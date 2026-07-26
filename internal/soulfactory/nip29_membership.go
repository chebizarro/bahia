package soulfactory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

const nip29KindPutUser nostr.Kind = 9000

// NIP29Group identifies one relay-hosted group assigned during provisioning.
type NIP29Group struct {
	Relay string
	ID    string
}

type nip29MembershipAssigner interface {
	Assign(context.Context, string) ([]string, error)
}

type nip29Membership struct {
	signer relayAuthSigner
	groups []NIP29Group
	buses  map[string]*SoulFactoryRelayBus
}

func newNIP29Membership(groups []NIP29Group, signer relayAuthSigner) (*nip29Membership, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	if signer == nil {
		return nil, fmt.Errorf("NIP-29 group assignment requires a signer")
	}

	membership := &nip29Membership{
		signer: signer,
		buses:  make(map[string]*SoulFactoryRelayBus),
	}
	seen := make(map[string]struct{}, len(groups))
	for i, group := range groups {
		group.Relay = strings.TrimRight(strings.TrimSpace(group.Relay), "/")
		group.ID = strings.TrimSpace(group.ID)
		if group.Relay == "" || group.ID == "" {
			return nil, fmt.Errorf("NIP-29 group %d requires relay and id", i)
		}
		key := group.Relay + "\x00" + group.ID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if membership.buses[group.Relay] == nil {
			bus, err := NewSoulFactoryRelayBus(
				[]string{group.Relay},
				WithRelayBusSigner(signer),
			)
			if err != nil {
				return nil, fmt.Errorf("configure NIP-29 relay %s: %w", group.Relay, err)
			}
			membership.buses[group.Relay] = bus
		}
		membership.groups = append(membership.groups, group)
	}
	return membership, nil
}

// Assign publishes NIP-29 put-user events signed by Soul Factory's
// Signet-custodied controller. The provisioned agent never receives key
// material. Relay OK is required for every configured group.
func (m *nip29Membership) Assign(ctx context.Context, pubkey string) ([]string, error) {
	if m == nil || len(m.groups) == 0 {
		return nil, nil
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if _, err := nostr.PubKeyFromHex(pubkey); err != nil {
		return nil, fmt.Errorf("invalid provisioned agent pubkey: %w", err)
	}

	assigned := make([]string, 0, len(m.groups))
	authenticated := make(map[string]struct{}, len(m.buses))
	for _, group := range m.groups {
		if _, ok := authenticated[group.Relay]; !ok {
			if err := m.buses[group.Relay].Authenticate(ctx); err != nil {
				return assigned, fmt.Errorf("authenticate NIP-29 relay %s: %w", group.Relay, err)
			}
			authenticated[group.Relay] = struct{}{}
		}
		event := nostr.Event{
			Kind:      nip29KindPutUser,
			CreatedAt: nostr.Now(),
			Tags: nostr.Tags{
				{"h", group.ID},
				{"p", pubkey},
			},
		}
		if err := m.signer.Sign(ctx, &event); err != nil {
			return assigned, fmt.Errorf("sign NIP-29 membership %s on %s: %w", group.ID, group.Relay, err)
		}
		if _, err := m.buses[group.Relay].Publish(ctx, event); err != nil {
			if !strings.Contains(err.Error(), "auth-required:") {
				return assigned, fmt.Errorf("assign NIP-29 membership %s on %s: %w", group.ID, group.Relay, err)
			}
			// Some relays acknowledge NIP-42 AUTH asynchronously. If the
			// immediately-following write races that acknowledgement, wait
			// briefly for authenticated connection state and retry once.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return assigned, ctx.Err()
			case <-timer.C:
			}
			if _, retryErr := m.buses[group.Relay].Publish(ctx, event); retryErr != nil {
				return assigned, fmt.Errorf("assign NIP-29 membership %s on %s after auth: %w", group.ID, group.Relay, retryErr)
			}
		}
		assigned = append(assigned, group.Relay+"'"+group.ID)
	}
	return assigned, nil
}
