package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
)

func TestDNSSubcommandsBuildTypedRequests(t *testing.T) {
	setupDNSCLIEnv(t)
	policyPath := writeDNSPolicyFile(t, `{"name":"edge-routing","rules":[{"match":{"environment":"prod"},"action":{"visibility":"edge"}}],"enabled":true}`)
	expiresAt := "2026-09-04T12:00:00Z"

	var zoneRequest client.DNSZoneCreateRequest
	var policyRequest client.DNSPolicyApplyRequest
	var recordRequest client.DNSRecordSetRequest
	fake := fakeCLIOperatorClient{
		dnsZoneCreate: func(req client.DNSZoneCreateRequest) (*client.DNSCommandResult, error) {
			zoneRequest = req
			return &client.DNSCommandResult{Status: "success"}, nil
		},
		dnsPolicyApply: func(req client.DNSPolicyApplyRequest) (*client.DNSCommandResult, error) {
			policyRequest = req
			return &client.DNSCommandResult{Status: "success"}, nil
		},
		dnsRecordSet: func(req client.DNSRecordSetRequest) (*client.DNSCommandResult, error) {
			recordRequest = req
			return &client.DNSCommandResult{Status: "success"}, nil
		},
	}
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) { return fake, nil })
	defer restoreFactory()

	executeDNSCommand(t, "zone-create", "--name", "prod.example", "--visibility", "external", "--backend-ref", "powerdns-prod", "--ttl", "300")
	executeDNSCommand(t, "policy-apply", "--file", policyPath)
	executeDNSCommand(t, "record-set", "--zone", "prod.example", "--name", "api", "--type", "A", "--value", "192.0.2.10", "--ttl", "60", "--reason", "incident pin", "--expires-at", expiresAt)

	if zoneRequest.Name != "prod.example" || zoneRequest.Visibility != domain.ZoneVisibilityExternal || zoneRequest.BackendRef != "powerdns-prod" || zoneRequest.TTL != 300 {
		t.Fatalf("zone request = %#v", zoneRequest)
	}
	if policyRequest.Name != "edge-routing" || !policyRequest.Enabled || len(policyRequest.Rules) != 1 || policyRequest.Rules[0].Action.Visibility != domain.ZoneVisibilityEdge {
		t.Fatalf("policy request = %#v", policyRequest)
	}
	wantExpiry, _ := time.Parse(time.RFC3339, expiresAt)
	if recordRequest.ZoneName != "prod.example" || recordRequest.RecordName != "api" || recordRequest.RecordType != domain.DNSRecordTypeA || recordRequest.Value != "192.0.2.10" || recordRequest.TTL != 60 || recordRequest.Reason != "incident pin" || recordRequest.ExpiresAt == nil || !recordRequest.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("record request = %#v", recordRequest)
	}
}

func TestDNSDriftRemediateWithAndWithoutZone(t *testing.T) {
	setupDNSCLIEnv(t)
	var requests []client.DNSDriftRemediateRequest
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{dnsDriftRemediate: func(req client.DNSDriftRemediateRequest) (*client.DNSCommandResult, error) {
			requests = append(requests, req)
			return &client.DNSCommandResult{Status: "success"}, nil
		}}, nil
	})
	defer restoreFactory()

	executeDNSCommand(t, "drift-remediate", "--zone", "prod.example")
	executeDNSCommand(t, "drift-remediate")
	if len(requests) != 2 || requests[0].Zone != "prod.example" || requests[1].Zone != "" {
		t.Fatalf("drift requests = %#v", requests)
	}
}

func TestDNSCommandReturnsNonZeroForFailureStatusError(t *testing.T) {
	setupDNSCLIEnv(t)
	want := errors.New(`dns/record-set failed with status "error": unknown DNS zone prod.example`)
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		return fakeCLIOperatorClient{dnsRecordSet: func(client.DNSRecordSetRequest) (*client.DNSCommandResult, error) {
			return nil, want
		}}, nil
	})
	defer restoreFactory()

	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(dnsCommands())
	root.SetArgs([]string{"dns", "record-set", "--zone", "prod.example", "--name", "api", "--type", "A", "--value", "192.0.2.10", "--ttl", "60", "--reason", "incident pin"})
	if err := root.ExecuteContext(context.Background()); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestDNSPolicyApplyRejectsInvalidJSONAndPolicy(t *testing.T) {
	setupDNSCLIEnv(t)
	called := false
	restoreFactory := replaceOperatorFactory(func(client.OperatorControlPlaneConfig) (cliOperatorClient, error) {
		called = true
		return fakeCLIOperatorClient{}, nil
	})
	defer restoreFactory()

	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "invalid JSON", content: `{"name":`, want: "read DNS policy"},
		{name: "invalid policy", content: `{"name":"no-rules","rules":[],"enabled":true}`, want: "rules must not be empty"},
		{name: "unknown field", content: `{"name":"policy","rules":[],"secret":"not-allowed"}`, want: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newOperatorFlagTestCommand(t).Root()
			root.AddCommand(dnsCommands())
			root.SetArgs([]string{"dns", "policy-apply", "--file", writeDNSPolicyFile(t, test.content)})
			err := root.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
	if called {
		t.Fatal("operator client was built for an invalid policy file")
	}
}

func setupDNSCLIEnv(t *testing.T) {
	t.Helper()
	outputFormat = "json"
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", nostr.Generate().Hex())
	t.Setenv("BAHIA_NOSTR_RELAYS", "wss://relay.example")
	t.Cleanup(func() { outputFormat = "table" })
}

func executeDNSCommand(t *testing.T, args ...string) {
	t.Helper()
	root := newOperatorFlagTestCommand(t).Root()
	root.AddCommand(dnsCommands())
	root.SetArgs(append([]string{"dns"}, args...))
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute dns %s: %v", strings.Join(args, " "), err)
	}
}

func writeDNSPolicyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dns-policy.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write DNS policy file: %v", err)
	}
	return path
}
