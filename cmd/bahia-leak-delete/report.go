package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/atomicfile"
)

const (
	reportVersion = 1
	// maxReportBytes bounds the report read back; a scan of a few hundred
	// thousand ids fits comfortably.
	maxReportBytes = 256 << 20
)

// report is the JSON state file shared by scan and delete. It never holds a d
// value or a leaked event: scans record ids, kinds and created_at, plus
// fingerprints of the d values for the deletion guard; deletions record the
// signed kind-5 requests (e and k tags only) and each relay's answer.
type report struct {
	Version   int    `json:"version"`
	OldPubkey string `json:"old_pubkey"`
	Kinds     []int  `json:"kinds"`
	// DFingerprints are fingerprintOf every d value the scans saw.
	DFingerprints []string    `json:"d_fingerprints"`
	Scans         []relayScan `json:"scans"`
	Deletions     []deletion  `json:"deletions,omitempty"`
}

// relayScan is the latest scan of one relay.
type relayScan struct {
	Relay string `json:"relay"`
	// Status is scanComplete when every page ended in EOSE.
	Status string `json:"status"`
	// Reason says why an incomplete scan stopped (redacted).
	Reason string `json:"reason,omitempty"`
	Pages  int    `json:"pages"`
	// Truncated is set when more events shared one created_at than a page
	// returned, so paging had to step past that second.
	Truncated bool `json:"truncated,omitempty"`
	// Invalid counts events the relay sent that failed validation (wrong
	// author or kind, bad id or signature) and were dropped.
	Invalid   int            `json:"invalid_events_dropped,omitempty"`
	ScannedAt time.Time      `json:"scanned_at"`
	Events    []scannedEvent `json:"events"`
}

const (
	scanComplete   = "complete"
	scanIncomplete = "incomplete"
)

type scannedEvent struct {
	ID        string `json:"id"`
	Kind      int    `json:"kind"`
	CreatedAt int64  `json:"created_at"`
}

// deletion is one signed NIP-09 request, the scanned events it names and
// each target relay's answer.
type deletion struct {
	Event   nostr.Event              `json:"event"`
	Targets []scannedEvent           `json:"targets"`
	Relays  map[string]*relayOutcome `json:"relays"`
}

type relayOutcome struct {
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	Attempts  int       `json:"attempts"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// Relay outcome statuses. accepted and duplicate are done; the others are
// retried by the next delete --execute.
const (
	outcomePending      = "pending"
	outcomeAccepted     = "accepted"
	outcomeDuplicate    = "duplicate"
	outcomeRejected     = "rejected"
	outcomeAuthRequired = "auth-required"
	outcomeRateLimited  = "rate-limited"
	outcomeError        = "error"
)

func (o *relayOutcome) done() bool {
	return o != nil && (o.Status == outcomeAccepted || o.Status == outcomeDuplicate)
}

func newReport(oldPubkey nostr.PubKey) *report {
	kindValues := make([]int, 0, len(leakedKinds))
	for _, kind := range leakedKinds {
		kindValues = append(kindValues, int(kind))
	}
	sort.Ints(kindValues)
	return &report{Version: reportVersion, OldPubkey: oldPubkey.Hex(), Kinds: kindValues}
}

// loadReport reads the report at path. A missing file is os.ErrNotExist.
func loadReport(path string, oldPubkey nostr.PubKey) (*report, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read report %s: %w", path, err)
	}
	if len(data) > maxReportBytes {
		return nil, fmt.Errorf("report %s exceeds %d bytes", path, maxReportBytes)
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode report %s: %w", path, err)
	}
	if r.Version != reportVersion {
		return nil, fmt.Errorf("report %s has version %d; this tool reads version %d", path, r.Version, reportVersion)
	}
	if r.OldPubkey != oldPubkey.Hex() {
		return nil, fmt.Errorf("report %s is for pubkey %s, not --old-pubkey %s", path, r.OldPubkey, oldPubkey.Hex())
	}
	if _, err := parseFingerprints(r.DFingerprints); err != nil {
		return nil, fmt.Errorf("report %s: %w", path, err)
	}
	validEvents := func(where string, events []scannedEvent) error {
		for _, ev := range events {
			if id, err := nostr.IDFromHex(ev.ID); err != nil || id.Hex() != ev.ID || !isLeakedKind(nostr.Kind(ev.Kind)) {
				return fmt.Errorf("report %s: %s lists invalid event %q kind %d", path, where, ev.ID, ev.Kind)
			}
		}
		return nil
	}
	for _, scan := range r.Scans {
		if err := validEvents("scan of "+scan.Relay, scan.Events); err != nil {
			return nil, err
		}
	}
	for i := range r.Deletions {
		del := &r.Deletions[i]
		where := "deletion " + del.Event.ID.Hex()
		if err := validEvents(where, del.Targets); err != nil {
			return nil, err
		}
		if !namesExactly(del.Event, del.Targets) {
			return nil, fmt.Errorf("report %s: %s does not name exactly its recorded targets", path, where)
		}
		if del.Relays == nil {
			del.Relays = map[string]*relayOutcome{}
		}
	}
	return &r, nil
}

// loadOrNewReport reads the report at path, or starts one when it does not
// exist yet.
func loadOrNewReport(path string, oldPubkey nostr.PubKey) (*report, error) {
	r, err := loadReport(path, oldPubkey)
	if errors.Is(err, os.ErrNotExist) {
		return newReport(oldPubkey), nil
	}
	return r, err
}

// save writes the report atomically with owner-only permissions. It ignores
// cancellation so the progress of an interrupted run is still recorded.
func (r *report) save(ctx context.Context, path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if err := atomicfile.WriteFile(context.WithoutCancel(ctx), path, ".bahia-leak-delete-report-*", append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write report %s: %w", path, err)
	}
	return nil
}

func (r *report) fingerprints() (fingerprintSet, error) {
	return parseFingerprints(r.DFingerprints)
}

func (r *report) mergeFingerprints(set fingerprintSet) error {
	merged, err := r.fingerprints()
	if err != nil {
		return err
	}
	for fp := range set {
		merged[fp] = struct{}{}
	}
	r.DFingerprints = merged.hexes()
	sort.Strings(r.DFingerprints)
	return nil
}

// putScan replaces the previous scan of the same relay.
func (r *report) putScan(scan relayScan) {
	for i := range r.Scans {
		if r.Scans[i].Relay == scan.Relay {
			r.Scans[i] = scan
			return
		}
	}
	r.Scans = append(r.Scans, scan)
	sort.Slice(r.Scans, func(i, j int) bool { return r.Scans[i].Relay < r.Scans[j].Relay })
}

// targets maps every event any scan or planned deletion names to its kind
// and creation time. An id stays deletable after a later scan stops seeing it
// (because a relay honoured the deletion): the deletion still names it.
func (r *report) targets() map[nostr.ID]scannedEvent {
	out := make(map[nostr.ID]scannedEvent)
	add := func(events []scannedEvent) {
		for _, ev := range events {
			id, err := nostr.IDFromHex(ev.ID)
			if err == nil {
				out[id] = ev
			}
		}
	}
	for _, scan := range r.Scans {
		add(scan.Events)
	}
	for _, del := range r.Deletions {
		add(del.Targets)
	}
	return out
}

// guard builds the deletion guard over everything the report names.
func (r *report) guard(oldPubkey nostr.PubKey) (deletionGuard, error) {
	forbidden, err := r.fingerprints()
	if err != nil {
		return deletionGuard{}, err
	}
	targets := make(map[nostr.ID]nostr.Kind)
	for id, ev := range r.targets() {
		targets[id] = nostr.Kind(ev.Kind)
	}
	return deletionGuard{author: oldPubkey, targets: targets, forbidden: forbidden}, nil
}

// namesExactly reports whether the e tags of ev are exactly targets' ids.
func namesExactly(ev nostr.Event, targets []scannedEvent) bool {
	want := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		want[target.ID] = struct{}{}
	}
	named := 0
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "e" {
			if _, ok := want[tag[1]]; !ok {
				return false
			}
			named++
		}
	}
	return named == len(want) && named == len(targets)
}

// relayURLs lists every relay a scan covered or a deletion was sent to, so a
// resumed run keeps delivering to relays added with --relay earlier.
func (r *report) relayURLs() []string {
	var urls []string
	add := func(url string) {
		if !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	for _, scan := range r.Scans {
		add(scan.Relay)
	}
	for _, del := range r.Deletions {
		relays := make([]string, 0, len(del.Relays))
		for relay := range del.Relays {
			relays = append(relays, relay)
		}
		sort.Strings(relays)
		for _, relay := range relays {
			add(relay)
		}
	}
	return urls
}

func normalizeRelays(raw []string) ([]string, error) {
	var out []string
	for _, value := range raw {
		url := nostr.NormalizeURL(strings.TrimSpace(value))
		if url == "" || !(strings.HasPrefix(url, "wss://") || strings.HasPrefix(url, "ws://")) {
			return nil, fmt.Errorf("invalid relay URL %q", value)
		}
		if !slices.Contains(out, url) {
			out = append(out, url)
		}
	}
	return out, nil
}
