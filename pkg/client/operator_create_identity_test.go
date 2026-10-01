package client

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
)

// bahia-irsry.42: the operator client signs service/create and
// environment/create with a client-minted entity id, so a retry names the
// same entity and the control plane replays it instead of creating another.
func TestOperatorCreateRequestsCarryClientMintedIDs(t *testing.T) {
	supplied := "0190f3b2-6a4e-7c1d-8e9f-0123456789ab"
	creates := map[string]func(*OperatorControlPlaneClient, string) error{
		"service": func(c *OperatorControlPlaneClient, id string) error {
			_, err := c.CreateServiceNostr(context.Background(), CreateServiceNostrRequest{ID: id, Name: "api", ArtifactRepo: "registry.example/api"}, nil)
			return err
		},
		"environment": func(c *OperatorControlPlaneClient, id string) error {
			_, err := c.CreateEnvironmentNostr(context.Background(), CreateEnvironmentNostrRequest{ID: id, OrgID: uuid.NewString(), Name: "prod"}, nil)
			return err
		},
	}
	for entity, create := range creates {
		t.Run(entity, func(t *testing.T) {
			for _, tc := range []struct {
				name, id string
				check    func(*testing.T, string)
			}{
				{"minted when absent", "", func(t *testing.T, got string) {
					parsed, err := uuid.Parse(got)
					if err != nil || parsed.Version() != 7 || parsed.String() != got {
						t.Fatalf("minted id %q is not a canonical UUIDv7", got)
					}
				}},
				{"kept when supplied", supplied, func(t *testing.T, got string) {
					if got != supplied {
						t.Fatalf("id = %q, want the supplied %q", got, supplied)
					}
				}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					replyKey := nostr.Generate().Hex()
					transport := newFakeOperatorTransport()
					c := newTestOperatorClient(t, nostr.Generate().Hex(), transport)
					transport.publishFn = func(_ context.Context, event nostr.Event) (int, error) {
						transport.events <- signedContextVMResult(t, replyKey, event, map[string]any{"status": "created"})
						return 1, nil
					}
					if err := create(c, tc.id); err != nil {
						t.Fatalf("create: %v", err)
					}
					rpc := decodePublishedContextVMRequest(t, transport.onlyPublished(t))
					got, _ := rpc.Params["id"].(string)
					tc.check(t, got)
				})
			}
			for _, invalid := range []string{"0190F3B2-6A4E-7C1D-8E9F-0123456789AB", "886313e1-3b8a-5372-9b90-0c9aee199e5d", "not-a-uuid"} {
				transport := newFakeOperatorTransport()
				c := newTestOperatorClient(t, nostr.Generate().Hex(), transport)
				if err := create(c, invalid); err == nil {
					t.Fatalf("id %q was accepted", invalid)
				}
				transport.mu.Lock()
				published := len(transport.published)
				transport.mu.Unlock()
				if published != 0 {
					t.Fatalf("invalid id %q was published", invalid)
				}
			}
		})
	}
}
