package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"fiatjaf.com/nostr"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// HostnameRecoveryAdapter only re-signs canonical ownership and withdrawal
// records. It never constructs a HostnameIntentHandler or HostnameRouteManager,
// so no DNS, tunnel, TLS or Caddy effect is replayed during recovery.
type HostnameRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

func (a *HostnameRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{
		old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, name: "hostname-reservations-and-recovery",
		belongs: func(ev nostr.Event) bool {
			return strings.HasPrefix(tag(ev, "d"), kinds.HostnameReservationDPrefix) || strings.HasPrefix(tag(ev, "d"), kinds.HostnameRecoveryDPrefix) ||
				tag(ev, "legacy_kind") == kinds.CPStateFamilyHostnameReservation.TagValue() || tag(ev, "legacy_kind") == kinds.CPStateFamilyHostnameRecovery.TagValue()
		},
		preflight: a.preflight,
		publish:   a.publish,
	}
}

func hostnameRecoveryPayload(old nostr.Event) (kinds.CPStateFamily, string, bool, string, error) {
	var family kinds.CPStateFamily
	var hostname string
	switch {
	case strings.HasPrefix(tag(old, "d"), kinds.HostnameReservationDPrefix):
		family = kinds.CPStateFamilyHostnameReservation
		hostname = strings.TrimPrefix(tag(old, "d"), kinds.HostnameReservationDPrefix)
	case strings.HasPrefix(tag(old, "d"), kinds.HostnameRecoveryDPrefix):
		family = kinds.CPStateFamilyHostnameRecovery
		hostname = strings.TrimPrefix(tag(old, "d"), kinds.HostnameRecoveryDPrefix)
	default:
		return 0, "", false, "", errors.New("hostname coordinate missing")
	}
	deleted := tag(old, "deleted") == "true"
	if tag(old, "deleted") != "true" && tag(old, "deleted") != "false" {
		return 0, "", false, "", errors.New("hostname deletion marker invalid")
	}
	var content []byte
	if family == kinds.CPStateFamilyHostnameReservation {
		var record domain.HostnameReservation
		if err := json.Unmarshal([]byte(old.Content), &record); err != nil {
			return 0, "", false, "", err
		}
		if err := record.Validate(); err != nil {
			return 0, "", false, "", err
		}
		if record.Hostname != hostname || record.Deleted != deleted {
			return 0, "", false, "", errors.New("hostname reservation coordinate or deletion differs from content")
		}
		content, _ = json.Marshal(record)
	} else {
		var record controlplane.HostnameRecoveryRecord
		if err := json.Unmarshal([]byte(old.Content), &record); err != nil {
			return 0, "", false, "", err
		}
		if deleted || record.Hostname != hostname || record.IntentID == "" || record.IntentEventID == "" || record.RequestedBy == "" || record.UpdatedAt.IsZero() || record.Phase == "" {
			return 0, "", false, "", errors.New("hostname recovery receipt identity or phase invalid")
		}
		if normalized, err := domain.NormalizePublicHostname(record.Hostname); err != nil || normalized != record.Hostname {
			return 0, "", false, "", errors.New("hostname recovery receipt name invalid")
		}
		if err := domain.ValidateDesiredPublicRoute(&record.Plan); err != nil {
			return 0, "", false, "", err
		}
		validPhase := record.Phase == "prepared" || record.Phase == "internal_withdrawn"
		switch record.Plan.Provider {
		case "cloudflare_tunnel":
			validPhase = validPhase || record.Phase == "dns_withdrawn" || record.Phase == "tunnel_withdrawn"
		case "caddy":
			validPhase = validPhase || record.Phase == "route_withdrawn" || record.Phase == "tls_withdrawn"
		}
		if !validPhase {
			return 0, "", false, "", errors.New("hostname recovery receipt phase invalid")
		}
		content, _ = json.Marshal(record)
	}
	return family, tag(old, "d"), deleted, string(content), nil
}

func (a *HostnameRecoveryAdapter) preflight(old nostr.Event) error {
	family, coordinate, deleted, content, err := hostnameRecoveryPayload(old)
	if err != nil {
		return err
	}
	kind, tags := adapter.ControlStateEnvelope(int(family), coordinate, deleted)
	if old.Kind != nostr.Kind(kind) || !reflect.DeepEqual(old.Tags, tags) || old.Content != content {
		return errors.New("typed hostname publisher cannot reproduce accepted head")
	}
	return nil
}

func (a *HostnameRecoveryAdapter) publish(ctx context.Context, signer nostr.Keyer, old nostr.Event, sink registryRecoverySink) error {
	family, coordinate, deleted, content, err := hostnameRecoveryPayload(old)
	if err != nil {
		return err
	}
	pub, err := signer.GetPublicKey(ctx)
	if err != nil {
		return err
	}
	projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
	publisher := adapter.NewRelayFirstStatePublisher(projector, sink)
	_, err = publisher.PublishHostnameRecord(ctx, family, coordinate, deleted, content)
	return err
}

func (a *HostnameRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}
func (a *HostnameRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	return a.core().Reassert(ctx, signer)
}
func (a *HostnameRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	return a.core().Verify(ctx)
}
