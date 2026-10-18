package identityrecovery

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

// SnapshotArtifact pins an immutable nostrarchive gzip JSONL artifact.
type SnapshotArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// SnapshotReceipt contains metadata only; raw events never enter signed receipts.
type SnapshotReceipt struct {
	Basename       string   `json:"basename"`
	PinnedSHA256   string   `json:"pinned_sha256"`
	ComputedSHA256 string   `json:"computed_sha256"`
	EventCount     int      `json:"event_count"`
	AdmittedIDs    []string `json:"admitted_ids"`
}

type VerifiedSnapshotSource struct{ Artifacts []SnapshotArtifact }

func validateSnapshotArtifacts(artifacts []SnapshotArtifact) error {
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		path := filepath.Clean(artifact.Path)
		digest, err := hex.DecodeString(artifact.SHA256)
		if !filepath.IsAbs(artifact.Path) || !strings.HasSuffix(path, ".jsonl.gz") || strings.ContainsAny(path, "\r\n") || strings.Contains(strings.ToLower(path), "nsec1") || seen[path] || err != nil || len(digest) != sha256.Size || artifact.SHA256 != strings.ToLower(artifact.SHA256) {
			return errors.New("invalid verified snapshot artifact configuration")
		}
		seen[path] = true
	}
	return nil
}

// SnapshotVerification is a read-only digest check for plan/status.
type SnapshotVerification struct {
	Basename       string `json:"basename"`
	PinnedSHA256   string `json:"pinned_sha256"`
	ComputedSHA256 string `json:"computed_sha256,omitempty"`
	State          string `json:"state"`
}

func (v VerifiedSnapshotSource) Verification() []SnapshotVerification {
	out := make([]SnapshotVerification, 0, len(v.Artifacts))
	for _, a := range v.Artifacts {
		item := SnapshotVerification{Basename: filepath.Base(a.Path), PinnedSHA256: a.SHA256, State: "unavailable"}
		raw, err := os.ReadFile(a.Path)
		if err == nil {
			sum := sha256.Sum256(raw)
			item.ComputedSHA256 = hex.EncodeToString(sum[:])
			item.State = "mismatch"
			if item.ComputedSHA256 == a.SHA256 {
				item.State = "verified"
			}
		}
		out = append(out, item)
	}
	return out
}

// ReadMissing verifies every configured artifact before returning any event.
// The caller supplies only approved IDs absent from relay/local/by-ID reads.
func (v VerifiedSnapshotSource) ReadMissing(ctx context.Context, old nostr.PubKey, declaredAt string, missing []string) (map[string]nostr.Event, []SnapshotReceipt, error) {
	if err := validateSnapshotArtifacts(v.Artifacts); err != nil {
		return nil, nil, err
	}
	deadline, err := time.Parse(time.RFC3339, declaredAt)
	if err != nil {
		return nil, nil, errors.New("invalid incident declaration timestamp")
	}
	wanted := make(map[string]bool, len(missing))
	for _, id := range missing {
		wanted[id] = true
	}
	found := make(map[string]nostr.Event)
	receipts := make([]SnapshotReceipt, 0, len(v.Artifacts))
	for _, a := range v.Artifacts {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		raw, err := os.ReadFile(a.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("verified snapshot %s unavailable", filepath.Base(a.Path))
		}
		sum := sha256.Sum256(raw)
		actual := hex.EncodeToString(sum[:])
		if actual != a.SHA256 {
			return nil, nil, fmt.Errorf("verified snapshot %s digest mismatch", filepath.Base(a.Path))
		}
		// A byte reader exposes unread trailing bytes after the gzip member.
		input := bytes.NewReader(raw)
		zr, err := gzip.NewReader(input)
		if err != nil {
			return nil, nil, fmt.Errorf("verified snapshot %s invalid gzip: %w", filepath.Base(a.Path), err)
		}
		zr.Multistream(false)
		reader := bufio.NewReader(zr)
		receipt := SnapshotReceipt{Basename: filepath.Base(a.Path), PinnedSHA256: a.SHA256, ComputedSHA256: actual, AdmittedIDs: []string{}}
		for {
			if err := ctx.Err(); err != nil {
				_ = zr.Close()
				return nil, nil, err
			}
			line, readErr := reader.ReadBytes('\n')
			if readErr == io.EOF && len(line) == 0 {
				break
			}
			if readErr != nil || len(line) == 0 || line[len(line)-1] != '\n' {
				_ = zr.Close()
				return nil, nil, fmt.Errorf("verified snapshot %s malformed JSONL", receipt.Basename)
			}
			ev, err := strictSnapshotEvent(line[:len(line)-1])
			if err != nil || !ev.CheckID() || !ev.VerifySignature() || ev.PubKey != old || int64(ev.CreatedAt) > deadline.Unix() {
				_ = zr.Close()
				return nil, nil, fmt.Errorf("verified snapshot %s invalid event at line %d", receipt.Basename, receipt.EventCount+1)
			}
			receipt.EventCount++
			id := ev.ID.Hex()
			if wanted[id] {
				if _, exists := found[id]; !exists {
					found[id] = ev
					receipt.AdmittedIDs = append(receipt.AdmittedIDs, id)
				}
			}
		}
		if err := zr.Close(); err != nil {
			return nil, nil, fmt.Errorf("verified snapshot %s invalid gzip trailer: %w", receipt.Basename, err)
		}
		if input.Len() != 0 {
			return nil, nil, fmt.Errorf("verified snapshot %s trailing gzip data", receipt.Basename)
		}
		slices.Sort(receipt.AdmittedIDs)
		receipts = append(receipts, receipt)
	}
	return found, receipts, nil
}

func completeAcceptedHistory(ctx context.Context, s State, old nostr.PubKey, accepted map[nostr.ID]struct{}, seen map[nostr.ID]nostr.Event) error {
	missing := make([]string, 0)
	for id := range accepted {
		if _, ok := seen[id]; !ok {
			missing = append(missing, id.Hex())
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if len(s.VerifiedSnapshots) == 0 {
		return errors.New("accepted inventory event no longer present in audited sources")
	}
	recovered, _, err := (VerifiedSnapshotSource{Artifacts: s.VerifiedSnapshots}).ReadMissing(ctx, old, s.DeclaredAt, missing)
	if err != nil {
		return err
	}
	for _, id := range missing {
		ev, ok := recovered[id]
		if !ok {
			return errors.New("accepted inventory event no longer present in audited sources or verified snapshots")
		}
		if int64(ev.CreatedAt) > s.ActivationBarrier {
			return errors.New("accepted old-key event crosses trust activation barrier")
		}
		seen[ev.ID] = ev
	}
	return nil
}

// strictSnapshotEvent accepts exactly the seven NIP-01 event fields, once each.
func strictSnapshotEvent(line []byte) (nostr.Event, error) {
	var ev nostr.Event
	allowed := map[string]bool{"id": true, "pubkey": true, "created_at": true, "kind": true, "tags": true, "content": true, "sig": true}
	dec := json.NewDecoder(bytes.NewReader(line))
	start, err := dec.Token()
	if err != nil || start != json.Delim('{') {
		return ev, errors.New("snapshot event must be an object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return ev, errors.New("snapshot event has unknown or duplicate field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ev, err
		}
	}
	end, err := dec.Token()
	if err != nil || end != json.Delim('}') || len(seen) != len(allowed) {
		return ev, errors.New("snapshot event fields incomplete")
	}
	if _, err := dec.Token(); err != io.EOF {
		return ev, errors.New("snapshot event trailing JSON")
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// rejectSupersededSnapshotHead prevents an old snapshot revision from becoming
// the new-key head when a newer old-key revision is still observable but was
// not approved. The narrow d-filter is used only for snapshot-selected heads.
func rejectSupersededSnapshotHead(ctx context.Context, s State, source EventSource, local []nostr.Event, old nostr.PubKey, head nostr.Event) error {
	if !strings.HasPrefix(s.InventorySources[head.ID.Hex()], "snapshot:") {
		return nil
	}
	d := tag(head, "d")
	if d == "" {
		return errors.New("snapshot canonical head lacks coordinate")
	}
	relay, err := source.Read(ctx, nostr.Filter{Authors: []nostr.PubKey{old}, Kinds: []nostr.Kind{head.Kind}, Tags: nostr.TagMap{"d": {d}}})
	if err != nil {
		return fmt.Errorf("read current old-key coordinate head: %w", err)
	}
	for _, ev := range append(relay, local...) {
		if ev.PubKey != old || ev.Kind != head.Kind || tag(ev, "d") != d {
			continue
		}
		if !ev.CheckID() || !ev.VerifySignature() {
			return errors.New("invalid observed old-key coordinate head")
		}
		if newer(ev, head) {
			return errors.New("snapshot revision superseded by a newer unapproved old-key head")
		}
	}
	return nil
}
