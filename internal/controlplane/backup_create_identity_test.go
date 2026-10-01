package controlplane

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// bahia-irsry.42: backup apply/register verbs are upserts, so a request may
// name the entity by id. A supplied id must be a canonical UUIDv7/v4; ids of
// referenced entities (a definition's repository_id, ...) are not checked
// here.
func TestBackupApplyParamsValidateTheEntityID(t *testing.T) {
	request := func(params map[string]any) ContextVMRequest {
		raw, _ := json.Marshal(params)
		return ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: raw}}
	}
	for _, valid := range []string{domain.NewEntityID().String(), "3b241101-e2bb-4255-8caf-4136c566a962"} {
		if _, err := backupApplyParams(request(map[string]any{"id": valid, "name": "archive"}), "id", "repository_id"); err != nil {
			t.Fatalf("id %s rejected: %v", valid, err)
		}
	}
	for _, invalid := range []map[string]any{
		{"id": "886313e1-3b8a-5372-9b90-0c9aee199e5d"},            // v5, name-derived
		{"repository_id": "3B241101-E2BB-4255-8CAF-4136C566A962"}, // not canonical
		{"id": "00000000-0000-0000-0000-000000000000"},
	} {
		if _, err := backupApplyParams(request(invalid), "id", "repository_id"); !errors.Is(err, domain.ErrInvalidEntityID) {
			t.Fatalf("params %v: err = %v, want ErrInvalidEntityID", invalid, err)
		}
	}
	// A definition apply references other entities by id; only its own id
	// is an entity id.
	if _, err := backupApplyParams(request(map[string]any{"name": "nightly", "repository_id": "not-checked-here"}), "id", "definition_id"); err != nil {
		t.Fatalf("a reference id was validated as the entity id: %v", err)
	}
}
