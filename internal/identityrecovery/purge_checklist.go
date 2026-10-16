package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
)

var PurgeChecklistItems = []string{"archive-artifacts", "pg-backups", "wal", "controlled-re-encryption"}
var evidenceToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)

// FilePurgeReceipts reads operator-supplied evidence identifiers. It never
// performs a destructive purge or accesses backup contents.
type FilePurgeReceipts struct{ Path string }

func (p FilePurgeReceipts) Purge(ctx context.Context, ids []nostr.ID) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, errors.New("purge evidence file required")
	}
	raw, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, errors.New("purge evidence file unavailable")
	}
	var supplied map[string]string
	if err := json.Unmarshal(raw, &supplied); err != nil {
		return nil, errors.New("invalid purge evidence file")
	}
	if len(supplied) != len(PurgeChecklistItems) {
		return nil, ErrIncomplete
	}
	out := make([]string, 0, len(PurgeChecklistItems))
	for _, item := range PurgeChecklistItems {
		id := supplied[item]
		if !evidenceToken.MatchString(id) || strings.Contains(strings.ToLower(id), "nsec1") {
			return nil, ErrIncomplete
		}
		out = append(out, "purge:"+item+":"+id)
	}
	// The inventory is supplied to the seam; the engine publishes scoped
	// deletions before it invokes this operator-executed checklist.
	_ = ids
	return out, nil
}

func validatePurgeEvidence(receipts []string) error {
	if len(receipts) != len(PurgeChecklistItems) {
		return ErrIncomplete
	}
	seen := map[string]struct{}{}
	for _, receipt := range receipts {
		matched := false
		for _, item := range PurgeChecklistItems {
			prefix := "purge:" + item + ":"
			if len(receipt) > len(prefix) && receipt[:len(prefix)] == prefix && evidenceToken.MatchString(receipt[len(prefix):]) && !strings.Contains(strings.ToLower(receipt[len(prefix):]), "nsec1") {
				if _, duplicate := seen[item]; duplicate {
					return ErrIncomplete
				}
				seen[item] = struct{}{}
				matched = true
				break
			}
		}
		if !matched {
			return ErrIncomplete
		}
	}
	if len(seen) != len(PurgeChecklistItems) {
		return ErrIncomplete
	}
	return nil
}

func hasPurgeEvidence(receipt Receipt, deletionIDs []string, relays []string) bool {
	var items []string
	for _, id := range receipt.EvidenceIDs {
		if len(id) >= 6 && id[:6] == "purge:" {
			items = append(items, id)
		}
	}
	if validatePurgeEvidence(items) != nil || !slices.Contains(receipt.EvidenceIDs, "deletion-complete") {
		return false
	}
	for _, id := range deletionIDs {
		if !slices.Contains(receipt.EvidenceIDs, id) {
			return false
		}
		for _, relay := range relays {
			sum := sha256.Sum256([]byte(relay))
			if !slices.Contains(receipt.EvidenceIDs, "delete-ok:"+hex.EncodeToString(sum[:])+":"+id) {
				return false
			}
		}
	}
	return true
}
