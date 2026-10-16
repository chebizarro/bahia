package trustepoch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// EventQuery is satisfied by the daemon's durable local event store. Its
// caller must separately keep an operator-root subscription live; a stale
// cache cannot establish that the relays have no newer epoch.
type EventQuery interface {
	QueryEvents(nostr.Filter) iter.Seq[nostr.Event]
}

type LocalSource struct{ Events EventQuery }

func (s LocalSource) Head(ctx context.Context, serviceID, operatorRoot string) (*nostr.Event, bool, error) {
	if s.Events == nil || !validHex64(operatorRoot) {
		return nil, false, fmt.Errorf("trust epoch source or operator root is missing")
	}
	root, err := nostr.PubKeyFromHex(operatorRoot)
	if err != nil {
		return nil, false, err
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{root}, Tags: nostr.TagMap{"d": []string{"trust-epoch:" + serviceID}}}
	var head *nostr.Event
	for ev := range s.Events.QueryEvents(filter) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if head == nil || ev.CreatedAt > head.CreatedAt || (ev.CreatedAt == head.CreatedAt && ev.ID.Hex() < head.ID.Hex()) {
			copy := ev
			head = &copy
		}
	}
	return head, head != nil, nil
}

// FileInventory loads one recovery-engine export. Its envelope pins the
// service id and digest; CheckInventory verifies the full ID list itself.
type FileInventory struct{ Path string }

func (f FileInventory) IDs(ctx context.Context, serviceID, digest string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.Path == "" {
		return nil, ErrInventory
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, fmt.Errorf("read inventory artifact: %w", err)
	}
	var artifact struct {
		ServiceID string   `json:"service_id"`
		Digest    string   `json:"inventory_digest"`
		IDs       []string `json:"ids"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&artifact); err != nil {
		return nil, ErrInventory
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, ErrInventory
	}
	if artifact.ServiceID != serviceID || artifact.Digest != digest || artifact.IDs == nil {
		return nil, ErrInventory
	}
	return artifact.IDs, nil
}
