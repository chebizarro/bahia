package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// SQLReviewRow is a non-secret projection identifier, never publication data.
// EmbeddedID identifies a deployment unit inside an environment head.
type SQLReviewRow struct {
	Family     string `json:"family"`
	Table      string `json:"table"`
	RowID      string `json:"row_id"`
	Kind       int    `json:"kind"`
	Coordinate string `json:"coordinate"`
	LegacyKind string `json:"legacy_kind,omitempty"`
	EmbeddedID string `json:"embedded_id,omitempty"`
}

type SQLReviewRows interface {
	ListReviewRows(context.Context) ([]SQLReviewRow, error)
}

// SQLUnsignedDraft intentionally contains no SQL payload or signer. An
// operator must review the source row and issue a separate signed intent.
type SQLUnsignedDraft struct {
	SQLReviewRow
	Reason         string `json:"reason"`
	RequiredAction string `json:"required_action"`
}

type SQLDraftReport struct {
	Schema          string             `json:"schema"`
	IncidentID      string             `json:"incident_id"`
	InventoryDigest string             `json:"inventory_digest"`
	ReviewedRows    int                `json:"reviewed_rows"`
	Drafts          []SQLUnsignedDraft `json:"unsigned_review_drafts"`
}

// SQLReviewDraftAdapter is report-only. It compares PG projection identifiers
// with accepted old-key and validated new-key canonical heads, then writes an
// unsigned, non-secret local artifact. Reassert never calls Source.Publish.
type SQLReviewDraftAdapter struct {
	OldPubkey    nostr.PubKey
	Source       EventSource
	Local        RecoveryLocalStore
	Ledger       *Ledger
	Rows         SQLReviewRows
	ArtifactPath string
}

func (a *SQLReviewDraftAdapter) state() (State, error) {
	if a == nil || a.OldPubkey == nostr.ZeroPK || a.Source == nil || a.Local == nil || a.Ledger == nil || a.Rows == nil || a.ArtifactPath == "" {
		return State{}, errors.New("SQL review draft reporter is incomplete")
	}
	s, err := a.Ledger.Load()
	if err != nil {
		return State{}, err
	}
	if s.OldPubkey != a.OldPubkey.Hex() || s.NewPubkey == "" || s.InventoryDigest == "" || s.ActivationBarrier <= 0 {
		return State{}, errors.New("accepted recovery inventory unavailable")
	}
	digest, count, err := InventoryDigest(s.Inventory)
	if err != nil || digest != s.InventoryDigest || count != len(s.Inventory) {
		return State{}, errors.New("accepted recovery inventory digest mismatch")
	}
	return s, nil
}

func sqlHeadKey(kind int, d, family string) string {
	return strconv.Itoa(kind) + ":" + d + ":" + family
}

func (a *SQLReviewDraftAdapter) canonicalHeads(ctx context.Context, s State) (map[string]nostr.Event, error) {
	newPub, err := nostr.PubKeyFromHex(s.NewPubkey)
	if err != nil || newPub == a.OldPubkey {
		return nil, errors.New("new identity unavailable")
	}
	oldRelay, err := a.Source.Read(ctx, nostr.Filter{Authors: []nostr.PubKey{a.OldPubkey}})
	if err != nil {
		return nil, fmt.Errorf("audited old-key relay read: %w", err)
	}
	oldLocal, err := a.Local.Events(ctx, a.OldPubkey)
	if err != nil {
		return nil, errors.New("audited old-key local read failed")
	}
	newRelay, err := a.Source.Read(ctx, nostr.Filter{Authors: []nostr.PubKey{newPub}})
	if err != nil {
		return nil, fmt.Errorf("audited new-key relay read: %w", err)
	}
	newLocal, err := a.Local.Events(ctx, newPub)
	if err != nil {
		return nil, errors.New("audited new-key local read failed")
	}
	accepted := map[nostr.ID]struct{}{}
	for _, item := range s.Inventory {
		id, err := nostr.IDFromHex(item.ID)
		if err != nil {
			return nil, errors.New("invalid accepted inventory ID")
		}
		accepted[id] = struct{}{}
	}
	seen := map[nostr.ID]struct{}{}
	heads := map[string]nostr.Event{}
	for _, ev := range append(oldRelay, oldLocal...) {
		if _, ok := accepted[ev.ID]; !ok {
			continue
		}
		if ev.PubKey != a.OldPubkey || !ev.CheckID() || !ev.VerifySignature() || int64(ev.CreatedAt) > s.ActivationBarrier {
			return nil, errors.New("invalid accepted old-key SQL comparison event")
		}
		seen[ev.ID] = struct{}{}
		key := sqlHeadKey(int(ev.Kind), tag(ev, "d"), tag(ev, "legacy_kind"))
		if ev.Kind == nostr.Kind(kinds.CASControlState) || tag(ev, "d") != "" {
			if prior, ok := heads[key]; !ok || newer(ev, prior) {
				heads[key] = ev
			}
		}
	}
	for id := range accepted {
		if _, ok := seen[id]; !ok {
			return nil, errors.New("accepted SQL comparison event no longer present in audited sources")
		}
	}
	for _, ev := range append(newRelay, newLocal...) {
		if ev.PubKey != newPub || !ev.CheckID() || !ev.VerifySignature() {
			continue
		}
		key := sqlHeadKey(int(ev.Kind), tag(ev, "d"), tag(ev, "legacy_kind"))
		if ev.Kind == nostr.Kind(kinds.CASControlState) || tag(ev, "d") != "" {
			if prior, ok := heads[key]; !ok || newer(ev, prior) {
				heads[key] = ev
			}
		}
	}
	return heads, nil
}

func rowHasCanonicalHead(row SQLReviewRow, heads map[string]nostr.Event) bool {
	if row.Coordinate == "" {
		return false
	}
	headsKey := sqlHeadKey(row.Kind, row.Coordinate, row.LegacyKind)
	head, ok := heads[headsKey]
	if !ok {
		return false
	}
	if tag(head, "deleted") == "true" {
		return false
	}
	if row.EmbeddedID == "" {
		return true
	}
	var body struct {
		DeploymentUnits []struct {
			ID string `json:"id"`
		} `json:"deployment_units"`
	}
	if json.Unmarshal([]byte(head.Content), &body) != nil {
		return false
	}
	for _, unit := range body.DeploymentUnits {
		if unit.ID == row.EmbeddedID {
			return true
		}
	}
	return false
}

func (a *SQLReviewDraftAdapter) report(ctx context.Context) (SQLDraftReport, error) {
	s, err := a.state()
	if err != nil {
		return SQLDraftReport{}, err
	}
	heads, err := a.canonicalHeads(ctx, s)
	if err != nil {
		return SQLDraftReport{}, err
	}
	rows, err := a.Rows.ListReviewRows(ctx)
	if err != nil {
		return SQLDraftReport{}, err
	}
	report := SQLDraftReport{Schema: "bahia.identity-recovery.sql-review-drafts.v1", IncidentID: s.IncidentID, InventoryDigest: s.InventoryDigest, ReviewedRows: len(rows), Drafts: []SQLUnsignedDraft{}}
	for _, row := range rows {
		if row.Family == "" || row.Table == "" || row.RowID == "" || row.Kind <= 0 {
			return SQLDraftReport{}, errors.New("SQL review row identifier invalid")
		}
		if rowHasCanonicalHead(row, heads) {
			continue
		}
		reason := "no accepted canonical head for SQL projection row"
		if row.Coordinate == "" {
			reason = "SQL projection lacks a canonical coordinate"
		}
		report.Drafts = append(report.Drafts, SQLUnsignedDraft{SQLReviewRow: row, Reason: reason, RequiredAction: "operator-signed intent after manual review"})
	}
	slices.SortFunc(report.Drafts, func(x, y SQLUnsignedDraft) int {
		return strings.Compare(x.Table+"\x00"+x.RowID+"\x00"+x.Coordinate, y.Table+"\x00"+y.RowID+"\x00"+y.Coordinate)
	})
	return report, nil
}

func (a *SQLReviewDraftAdapter) Inventory(ctx context.Context) ([]string, error) {
	report, err := a.report(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(report.Drafts))
	for _, draft := range report.Drafts {
		ids = append(ids, draft.Table+":"+draft.RowID)
	}
	return ids, nil
}

func writeSQLDraftReport(path string, report SQLDraftReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".sql-review-drafts-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *SQLReviewDraftAdapter) Reassert(ctx context.Context, _ nostr.Keyer) ([]string, error) {
	report, err := a.report(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeSQLDraftReport(a.ArtifactPath, report); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(report)
	h := sha256.Sum256(data)
	return []string{"sql-review-drafts:sha256:" + hex.EncodeToString(h[:])}, nil
}

func (a *SQLReviewDraftAdapter) Verify(ctx context.Context) ([]string, error) {
	want, err := a.report(ctx)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(a.ArtifactPath)
	if err != nil {
		return nil, fmt.Errorf("SQL draft artifact unavailable: %w", ErrIncomplete)
	}
	var got SQLDraftReport
	if json.Unmarshal(data, &got) != nil || !reflect.DeepEqual(got, want) {
		return nil, fmt.Errorf("SQL draft artifact no longer matches audited sources: %w", ErrIncomplete)
	}
	canonical, _ := json.Marshal(want)
	h := sha256.Sum256(canonical)
	return []string{"sql-review-drafts:sha256:" + hex.EncodeToString(h[:]), "sql-review-drafts:count:" + strconv.Itoa(len(want.Drafts))}, nil
}
