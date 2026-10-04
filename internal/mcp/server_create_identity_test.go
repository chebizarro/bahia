package mcp

import (
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// bahia-irsry.42: MCP create tools sign a client-minted entity id (the `id`
// argument, or a fresh UUIDv7) and derive their idempotency key from it, so
// a retry that passes the returned id back replays the create.
func TestCallTool_CreateToolsSendClientMintedIDs(t *testing.T) {
	ctx := authorizedMCPContext()
	commands := &captureServiceCommandPublisher{}
	policies := &capturePolicyCommandPublisher{}
	server := newTestServerWithOptions(nil, zap.NewNop(), ServerDeps{ServiceCommandPublisher: commands, PolicyCommandPublisher: policies})
	orgID := uuid.NewString()
	supplied := "0190f3b2-6a4e-7c1d-8e9f-0123456789ab"

	type call struct {
		tool string
		args map[string]interface{}
		sent func() (id uuid.UUID, key string)
	}
	calls := []call{
		{"bahia_create_service", map[string]interface{}{"name": "api", "artifact_repo": "registry.example/api"}, func() (uuid.UUID, string) {
			return commands.create.ID, commands.create.IdempotencyKey
		}},
		{"bahia_create_environment", map[string]interface{}{"name": "prod", "org_id": orgID, "protected": true}, func() (uuid.UUID, string) {
			return commands.envCreate.ID, commands.envCreate.IdempotencyKey
		}},
		{"bahia_create_policy", map[string]interface{}{"name": "sig", "enforcement": "block", "rules": []interface{}{map[string]interface{}{"type": "require_signature"}}}, func() (uuid.UUID, string) {
			return policies.create.ID, policies.create.IdempotencyKey
		}},
	}
	for _, c := range calls {
		t.Run(c.tool, func(t *testing.T) {
			res, err := server.CallTool(ctx, c.tool, c.args)
			if err != nil || res.IsError {
				t.Fatalf("%s without id: result=%#v err=%v", c.tool, res, err)
			}
			minted, firstKey := c.sent()
			if minted.Version() != 7 {
				t.Fatalf("%s minted id %s, want a UUIDv7", c.tool, minted)
			}

			withID := map[string]interface{}{"id": supplied}
			for k, v := range c.args {
				withID[k] = v
			}
			for attempt := 0; attempt < 2; attempt++ {
				res, err = server.CallTool(ctx, c.tool, withID)
				if err != nil || res.IsError {
					t.Fatalf("%s with id: result=%#v err=%v", c.tool, res, err)
				}
				id, key := c.sent()
				if id.String() != supplied {
					t.Fatalf("%s sent id %s, want the supplied %s", c.tool, id, supplied)
				}
				if c.tool != "bahia_create_policy" && (key == firstKey || key == "") {
					t.Fatalf("%s idempotency key %q does not depend on the id (first create used %q)", c.tool, key, firstKey)
				}
			}

			withID["id"] = "886313e1-3b8a-5372-9b90-0c9aee199e5d" // UUIDv5: name-derived, rejected
			if res, err = server.CallTool(ctx, c.tool, withID); err != nil || !res.IsError {
				t.Fatalf("%s accepted a v5 id: result=%#v err=%v", c.tool, res, err)
			}
		})
	}

	if commands.envCreate.OrgID.String() != orgID || commands.envCreate.Name != "prod" || !commands.envCreate.Protected {
		t.Fatalf("environment create command = %+v", commands.envCreate)
	}
	if res, _ := server.CallTool(ctx, "bahia_create_environment", map[string]interface{}{"name": "prod"}); res == nil || !res.IsError {
		t.Fatalf("environment create without org_id must fail: %#v", res)
	}
}
