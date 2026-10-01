package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/pkg/client"
)

// bahia-irsry.42: `services create` and `environments create` sign a
// client-minted id. A minted id is reported before publishing so a create
// that timed out can be retried with --id (and is then replayed).
func TestCreateCommandsMintOrPassClientIDs(t *testing.T) {
	const supplied = "0190f3b2-6a4e-7c1d-8e9f-0123456789ab"
	cases := map[string]func(t *testing.T, id string) (sent string, stderr string, err error){
		"services": func(t *testing.T, id string) (string, string, error) {
			var captured client.CreateServiceNostrRequest
			args := []string{"services", "create", "--name", "api", "--artifact-repo", "registry.example/api"}
			stderr, err := runCreateCommand(t, id, args, fakeCLIOperatorClient{serviceCreate: func(req client.CreateServiceNostrRequest) (*client.ServiceCommandResult, error) {
				captured = req
				return &client.ServiceCommandResult{Status: "created", ServiceID: req.ID}, nil
			}})
			return captured.ID, stderr, err
		},
		"environments": func(t *testing.T, id string) (string, string, error) {
			var captured client.CreateEnvironmentNostrRequest
			args := []string{"create", "--name", "prod", "--org", uuid.NewString()}
			stderr, err := runCreateCommand(t, id, args, fakeCLIOperatorClient{environmentCreate: func(req client.CreateEnvironmentNostrRequest) (*client.EnvironmentCommandResult, error) {
				captured = req
				return &client.EnvironmentCommandResult{Status: "created", EnvironmentID: req.ID}, nil
			}})
			return captured.ID, stderr, err
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			sent, stderr, err := run(t, "")
			if err != nil {
				t.Fatalf("create without --id: %v", err)
			}
			if parsed, parseErr := uuid.Parse(sent); parseErr != nil || parsed.Version() != 7 {
				t.Fatalf("minted id %q is not a UUIDv7", sent)
			}
			if !strings.Contains(stderr, "--id "+sent) {
				t.Fatalf("stderr %q does not tell the operator how to retry with --id %s", stderr, sent)
			}

			sent, stderr, err = run(t, supplied)
			if err != nil || sent != supplied || stderr != "" {
				t.Fatalf("create --id %s: sent %q, stderr %q, err %v", supplied, sent, stderr, err)
			}

			if _, _, err := run(t, strings.ToUpper(supplied)); err == nil || !strings.Contains(err.Error(), "invalid --id") {
				t.Fatalf("non-canonical --id accepted: %v", err)
			}
		})
	}
}

func runCreateCommand(t *testing.T, id string, args []string, fake fakeCLIOperatorClient) (string, error) {
	t.Helper()
	resetOperatorGlobals(t)
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	restore := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) { return fake, nil })
	defer restore()

	root := newOperatorFlagTestCommand(t).Root()
	if args[0] == "services" {
		root.AddCommand(servicesCommands())
	} else {
		root.AddCommand(newEnvironmentCreateCommand())
	}
	if err := root.PersistentFlags().Set("relay", "wss://relay.example"); err != nil {
		t.Fatalf("set relay: %v", err)
	}
	if id != "" {
		args = append(args, "--id", id)
	}
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return stderr.String(), err
}
