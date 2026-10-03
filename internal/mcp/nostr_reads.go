package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"sort"
	"time"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
)

// StateEventStore is the daemon's local event store read surface. The daemon
// shares the same store with inbound sync and the projector warm start.
type StateEventStore interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

// ConfidentialStateReader opens the service-wrapped OCK envelope using the
// daemon's own key. The signed event tags supply the AEAD record identity.
type ConfidentialStateReader interface {
	DecryptConfidential(context.Context, string, int, string, string) ([]byte, error)
	DecryptServiceInner(context.Context, string) ([]byte, error)
}

var _ ConfidentialStateReader = (*controlplane.ConfidentialEncryptor)(nil)

type stateRecord struct {
	Event   nostr.Event
	Content json.RawMessage
	Fields  map[string]any
}

// readStateFamily validates the event before interpreting either its tags or
// content. The family table is the projector's sole source of topic mappings.
func (s *Server) readStateFamily(ctx context.Context, legacyKind int) ([]stateRecord, error) {
	if s.stateStore == nil || s.servicePubkey == "" {
		return nil, fmt.Errorf("MCP local state store is not configured")
	}
	pubkey, err := nostr.PubKeyFromHex(s.servicePubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP service pubkey: %w", err)
	}
	var topic string
	for _, family := range nostrpool.CPStateFamilyTopics() {
		if family.LegacyKind == legacyKind {
			topic = family.Topic
			break
		}
	}
	if topic == "" {
		return nil, fmt.Errorf("unknown cp-state family %d", legacyKind)
	}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{pubkey},
		Tags:    nostr.TagMap{"t": {topic}},
	}
	records := make([]stateRecord, 0)
	for ev := range s.stateStore.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ev.PubKey != pubkey || !ev.CheckID() || !ev.VerifySignature() {
			return nil, fmt.Errorf("invalid signed cp-state event %s", ev.ID.Hex())
		}
		decoded, err := client.DecodeControlStateEvent(ev)
		if err != nil {
			return nil, err
		}
		if decoded.LegacyKind != legacyKind || decoded.Topic != topic {
			return nil, fmt.Errorf("cp-state event %s has mismatched family or topic", ev.ID.Hex())
		}
		if decoded.Deleted {
			continue
		}
		content := decoded.Content
		var envelope struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(content, &envelope); err != nil {
			return nil, fmt.Errorf("decode cp-state event %s: %w", ev.ID.Hex(), err)
		}
		if envelope.Schema == controlplane.ConfidentialSchema {
			if s.confidentialReader == nil {
				return nil, fmt.Errorf("confidential MCP state requires the daemon OCK decryptor")
			}
			plaintext, err := s.confidentialReader.DecryptConfidential(ctx, ev.Content, legacyKind, decoded.DTag, topic)
			if err != nil {
				return nil, fmt.Errorf("decrypt cp-state event %s: %w", ev.ID.Hex(), err)
			}
			content = plaintext
			// The daemon owns the service key. Prefer the full service-only
			// payload when present (notification credentials, fleet channels).
			serviceContent, err := s.confidentialReader.DecryptServiceInner(ctx, ev.Content)
			if err != nil {
				return nil, fmt.Errorf("decrypt service inner of %s: %w", ev.ID.Hex(), err)
			}
			if len(serviceContent) != 0 {
				content = serviceContent
			}
		}
		fields := make(map[string]any)
		if err := json.Unmarshal(content, &fields); err != nil {
			return nil, fmt.Errorf("decode cp-state fields of %s: %w", ev.ID.Hex(), err)
		}
		if fields["deleted"] == true {
			continue
		}
		records = append(records, stateRecord{Event: ev, Content: content, Fields: fields})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Event.CreatedAt != records[j].Event.CreatedAt {
			return records[i].Event.CreatedAt > records[j].Event.CreatedAt
		}
		return records[i].Event.ID.Hex() > records[j].Event.ID.Hex()
	})
	return records, nil
}

func (s *Server) readStateOne(ctx context.Context, family int, key, value string) (*stateRecord, error) {
	records, err := s.readStateFamily(ctx, family)
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].Fields[key] == value {
			return &records[i], nil
		}
	}
	return nil, nil
}

func stringFromRecord(fields map[string]any, key string) string {
	value, _ := fields[key].(string)
	return value
}

func recordTime(fields map[string]any, key string) (time.Time, bool) {
	value := stringFromRecord(fields, key)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}
