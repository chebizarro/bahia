package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var cliEOSETimeout = client.DefaultEOSETimeout

func registerNostrReadFlags(root *cobra.Command) {
	root.PersistentFlags().DurationVar(&cliEOSETimeout, "eose-timeout", client.DefaultEOSETimeout, "Maximum wait for relay EOSE before reading stale local state (env BAHIA_EOSE_TIMEOUT)")
}

func readEOSETimeout(cmd *cobra.Command) (time.Duration, error) {
	if cmd.Root().PersistentFlags().Changed("eose-timeout") {
		if cliEOSETimeout <= 0 {
			return 0, fmt.Errorf("--eose-timeout must be positive")
		}
		return cliEOSETimeout, nil
	}
	if raw := strings.TrimSpace(os.Getenv("BAHIA_EOSE_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid BAHIA_EOSE_TIMEOUT %q: expected a positive duration", raw)
		}
		return d, nil
	}
	return client.DefaultEOSETimeout, nil
}

func useHTTPReadFallback(cmd *cobra.Command) bool {
	flags := cmd.Root().PersistentFlags()
	return flags.Changed("http-fallback") && operatorHTTPFallback
}

func isDefaultStatePolicyRead(cmd *cobra.Command) bool {
	if useHTTPReadFallback(cmd) || cmd.Parent() == nil {
		return false
	}
	switch cmd.Parent().Name() + "/" + cmd.Name() {
	case "state/list", "state/drifted", "policies/list", "policies/get", "orgs/list", "orgs/get", "members/list", "secrets/list", "channels/list", "channels/get":
		return true
	default:
		return false
	}
}

// readCLIStateEvents owns the pool and local store for one command invocation.
func readCLIStateEvents(cmd *cobra.Command, domain string) ([]nostr.Event, error) {
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, fmt.Errorf("resolve relays: %w", err)
	}
	pubkey := resolveOperatorServicePubkey(cmd)
	if pubkey == "" {
		return nil, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for Nostr reads")
	}
	storePath, err := nostrServiceStorePath(pubkey)
	if err != nil {
		return nil, err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return nil, err
	}
	pool := nostrpool.NewRelayPool(relays, zap.NewNop())
	pool.Connect(cmd.Context())
	defer pool.Close()
	nc, err := client.NewNostrClient(client.NostrClientConfig{
		StorePath: storePath, ServicePubkey: pubkey, Pool: client.WrapRelayPool(pool), EOSETimeout: timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("create Nostr client: %w", err)
	}
	defer nc.Close()
	events, result, err := nc.SyncAndQuery(cmd.Context(), domain)
	if err != nil {
		return nil, fmt.Errorf("sync %s state: %w", domain, err)
	}
	if !result.Fresh {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: relay data may be stale (no EOSE within timeout)")
	}
	return events, nil
}

func listCLIStates(cmd *cobra.Command, driftedOnly bool) ([]domain.EnvironmentServiceState, error) {
	if useHTTPReadFallback(cmd) {
		if driftedOnly {
			return apiClient.ListDriftedStates(cmd.Context())
		}
		return apiClient.ListStates(cmd.Context())
	}
	events, err := readCLIStateEvents(cmd, "state")
	if err != nil {
		return nil, err
	}
	return decodeCLIStates(events, driftedOnly)
}

func decodeCLIStates(events []nostr.Event, driftedOnly bool) ([]domain.EnvironmentServiceState, error) {
	states := make([]domain.EnvironmentServiceState, 0, len(events))
	for _, ev := range events {
		state, err := client.DecodeState(ev)
		if err != nil {
			return nil, fmt.Errorf("decode state event %s: %w", ev.ID.Hex(), err)
		}
		if state != nil && (!driftedOnly || state.DriftStatus == domain.DriftStatusDrifted) {
			states = append(states, *state)
		}
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].ServiceID != states[j].ServiceID {
			return states[i].ServiceID.String() < states[j].ServiceID.String()
		}
		return states[i].EnvironmentID.String() < states[j].EnvironmentID.String()
	})
	return states, nil
}

func listCLIPolicies(cmd *cobra.Command) ([]domain.DeploymentPolicy, error) {
	if useHTTPReadFallback(cmd) {
		return apiClient.ListPolicies(cmd.Context())
	}
	events, err := readCLIStateEvents(cmd, "policy")
	if err != nil {
		return nil, err
	}
	return decodeCLIPolicies(events)
}

func decodeCLIPolicies(events []nostr.Event) ([]domain.DeploymentPolicy, error) {
	policies := make([]domain.DeploymentPolicy, 0, len(events))
	for _, ev := range events {
		policy, err := client.DecodePolicy(ev)
		if err != nil {
			return nil, fmt.Errorf("decode policy event %s: %w", ev.ID.Hex(), err)
		}
		if policy != nil {
			policies = append(policies, *policy)
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID.String() < policies[j].ID.String() })
	return policies, nil
}

func getCLIPolicy(cmd *cobra.Command, id string) (*domain.DeploymentPolicy, error) {
	if useHTTPReadFallback(cmd) {
		return apiClient.GetPolicy(cmd.Context(), id)
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid policy ID %q: %w", id, err)
	}
	policies, err := listCLIPolicies(cmd)
	if err != nil {
		return nil, err
	}
	for i := range policies {
		if policies[i].ID == parsed {
			return &policies[i], nil
		}
	}
	return nil, fmt.Errorf("policy %s not found", id)
}
