package domain

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// RunSecretAccessesKey is the deployment run apply-metadata key listing the
// secrets a direct runtime run injected. Its presence is authoritative: an
// empty list means the run injected none. A run without it predates the
// record, so what it injected is unknown.
const RunSecretAccessesKey = "secret_accesses"

// RunSecretAccess is one secret a run injected: the secret and the revision
// of its canonical record at resolve time (re-pointing increments it). It
// names no credential and carries no value.
type RunSecretAccess struct {
	SecretID      uuid.UUID
	RecordVersion int
}

// RunSecretAccesses reads the run's record from apply metadata, as written or
// after a JSON round trip. ok is false when the run has no record. An entry
// that cannot be read is returned with a nil secret id, so a reader that
// requires every entry fails closed.
func RunSecretAccesses(applyMetadata map[string]any) (accesses []RunSecretAccess, ok bool) {
	raw, present := applyMetadata[RunSecretAccessesKey]
	if !present {
		return nil, false
	}
	var items []any
	switch list := raw.(type) {
	case []map[string]any:
		for _, entry := range list {
			items = append(items, entry)
		}
	case []any:
		items = list
	default:
		return []RunSecretAccess{{}}, true
	}
	for _, item := range items {
		entry, isMap := item.(map[string]any)
		if !isMap {
			accesses = append(accesses, RunSecretAccess{})
			continue
		}
		id, err := uuid.Parse(fmt.Sprint(entry["secret_id"]))
		if err != nil {
			id = uuid.Nil
		}
		version := 0
		switch v := entry["record_version"].(type) {
		case int:
			version = v
		case float64:
			version = int(v)
		}
		accesses = append(accesses, RunSecretAccess{SecretID: id, RecordVersion: version})
	}
	return dedupeRunSecretAccesses(accesses), true
}

// EncodeRunSecretAccesses is the apply-metadata form of accesses.
func EncodeRunSecretAccesses(accesses []RunSecretAccess) []map[string]any {
	accesses = dedupeRunSecretAccesses(accesses)
	out := make([]map[string]any, 0, len(accesses))
	for _, access := range accesses {
		out = append(out, map[string]any{"secret_id": access.SecretID.String(), "record_version": access.RecordVersion})
	}
	return out
}

func dedupeRunSecretAccesses(accesses []RunSecretAccess) []RunSecretAccess {
	seen := make(map[RunSecretAccess]struct{}, len(accesses))
	out := make([]RunSecretAccess, 0, len(accesses))
	for _, access := range accesses {
		if _, dup := seen[access]; dup {
			continue
		}
		seen[access] = struct{}{}
		out = append(out, access)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SecretID != out[j].SecretID {
			return out[i].SecretID.String() < out[j].SecretID.String()
		}
		return out[i].RecordVersion < out[j].RecordVersion
	})
	return out
}
