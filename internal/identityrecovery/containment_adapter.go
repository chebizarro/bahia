package identityrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// DurableContainmentAdapter inspects the marker written by the daemon's
// nostrout runtime kill switch. Declaration publishes the trigger; this
// adapter never publishes or activates a second containment path.
type DurableContainmentAdapter struct {
	Ledger                   *Ledger
	MarkerPath               string
	ConfirmDaemonUnreachable bool
}

func (a DurableContainmentAdapter) Contain(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Ledger == nil || a.MarkerPath == "" {
		return nil, fmt.Errorf("containment inspection: %w", ErrAdapterNotRegistered)
	}
	state, err := a.Ledger.Load()
	if err != nil {
		return nil, err
	}
	if state.IncidentID == "" {
		return nil, errors.New("declared incident unavailable for containment inspection")
	}
	raw, err := os.ReadFile(a.MarkerPath)
	if errors.Is(err, os.ErrNotExist) && a.ConfirmDaemonUnreachable {
		return []string{"containment:daemon-unreachable:operator-confirmed:" + state.IncidentID}, nil
	}
	if err != nil {
		return nil, errors.New("daemon containment marker unavailable")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var marker struct {
		IncidentID string `json:"incident_id"`
	}
	if dec.Decode(&marker) != nil || dec.Decode(new(any)) != io.EOF || marker.IncidentID != state.IncidentID {
		return nil, errors.New("daemon containment marker does not prove this incident")
	}
	return []string{"containment:runtime-kill-switch:" + state.IncidentID}, nil
}
