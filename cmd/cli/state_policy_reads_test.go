package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

type statePolicyRelay struct {
	mu      sync.Mutex
	events  []nostr.Event
	filters []nostr.Filter
	noEOSE  bool
}

func (relay *statePolicyRelay) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Accept") == "application/nostr+json" {
		w.Header().Set("Content-Type", "application/nostr+json")
		_, _ = w.Write([]byte(`{"name":"state-policy-test"}`))
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	for {
		_, message, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if json.Unmarshal(message, &frame) != nil || len(frame) < 3 {
			continue
		}
		var verb, subID string
		_ = json.Unmarshal(frame[0], &verb)
		_ = json.Unmarshal(frame[1], &subID)
		if verb != "REQ" {
			continue
		}
		var filter nostr.Filter
		if json.Unmarshal(frame[2], &filter) != nil {
			continue
		}
		relay.mu.Lock()
		relay.filters = append(relay.filters, filter)
		events := append([]nostr.Event(nil), relay.events...)
		noEOSE := relay.noEOSE
		relay.mu.Unlock()
		for _, ev := range events {
			if filter.Matches(ev) {
				payload, _ := json.Marshal([]any{"EVENT", subID, ev})
				if conn.Write(r.Context(), websocket.MessageText, payload) != nil {
					return
				}
			}
		}
		if !noEOSE {
			payload, _ := json.Marshal([]any{"EOSE", subID})
			if conn.Write(r.Context(), websocket.MessageText, payload) != nil {
				return
			}
		}
	}
}

func makeCLIStatePolicyEvent(t *testing.T, sk nostr.SecretKey, kind int, d string, tags nostr.Tags, content string, createdAt nostr.Timestamp) nostr.Event {
	t.Helper()
	_, envelope := nostradapter.ControlStateEnvelope(kind, d, false)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Tags: append(envelope, tags...), Content: content}
	require.NoError(t, ev.Sign(sk))
	return ev
}

func runStatePolicyCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()
	var stderr bytes.Buffer
	root := newRootCommand()
	root.SetArgs(args)
	root.SetErr(&stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true
	execErr := root.Execute()
	require.NoError(t, writer.Close())
	output, readErr := io.ReadAll(reader)
	require.NoError(t, readErr)
	require.NoError(t, reader.Close())
	return string(output), stderr.String(), execErr
}

func TestStatePolicyReadsNostrGolden(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Setenv("BAHIA_NOSTR_KEY_FILE", "")
	sk := nostr.Generate()
	pubkey := nostr.GetPublicKey(sk).Hex()
	now := time.Date(2026, 10, 3, 10, 0, 0, 123456000, time.UTC)
	base := nostr.Timestamp(now.Unix())
	state := domain.EnvironmentServiceState{ServiceID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), EnvironmentID: uuid.MustParse("00000000-0000-0000-0000-000000000002"), DriftStatus: domain.DriftStatusDrifted, DesiredHash: "abc", ReconcileBackoffUntil: &now, ReconcileConsecutiveFailures: 2, UpdatedAt: now}
	state.DesiredRuntimeState = &domain.DesiredServiceSpec{SchemaVersion: "v1", ServiceID: state.ServiceID, EnvironmentID: state.EnvironmentID, ImageRef: "registry.example/image@sha256:abc", DesiredHash: "abc"}
	stable := domain.EnvironmentServiceState{ServiceID: uuid.MustParse("00000000-0000-0000-0000-000000000003"), EnvironmentID: state.EnvironmentID, DriftStatus: domain.DriftStatusInSync, UpdatedAt: now}
	policy := domain.DeploymentPolicy{ID: uuid.MustParse("00000000-0000-0000-0000-000000000004"), Name: "release", Rules: []domain.PolicyRule{{Type: domain.RuleRequireSBOM}}, Enforcement: domain.PolicyEnforcementBlock, Enabled: true, CreatedAt: now, UpdatedAt: now}
	relay := &statePolicyRelay{}
	for i, item := range []domain.EnvironmentServiceState{state, stable} {
		tags, content := nostradapter.RuntimeStateRecord(&item, nil)
		dTag := "service:" + item.ServiceID.String() + ":environment:" + item.EnvironmentID.String()
		relay.events = append(relay.events, makeCLIStatePolicyEvent(t, sk, kinds.ServiceState, dTag, tags, content, base+nostr.Timestamp(i)))
	}
	policyTags, policyContent := controlplane.PolicyRegistryRecord(&policy, false)
	relay.events = append(relay.events, makeCLIStatePolicyEvent(t, sk, kinds.PolicyRegistry, policy.ID.String(), policyTags, policyContent, base+2))
	relayServer := httptest.NewServer(http.HandlerFunc(relay.serve))
	defer relayServer.Close()
	relayURL := "ws" + strings.TrimPrefix(relayServer.URL, "http")
	common := []string{"--relay", relayURL, "--service-pubkey", pubkey, "--eose-timeout", "2s"}
	cases := []struct {
		name    string
		command []string
	}{
		{"state list", []string{"state", "list"}},
		{"state drifted", []string{"state", "drifted"}},
		{"policies list", []string{"policies", "list"}},
		{"policies get", []string{"policies", "get", policy.ID.String()}},
	}
	for _, tc := range cases {
		for _, format := range []string{"table", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				args := append(append([]string(nil), common...), "--output", format)
				output, stderr, err := runStatePolicyCLI(t, append(args, tc.command...)...)
				require.NoError(t, err)
				require.Empty(t, stderr)
				require.NotEmpty(t, output)
				if strings.HasPrefix(tc.name, "policies") {
					require.Contains(t, output, policy.ID.String())
				} else {
					require.Contains(t, output, state.ServiceID.String())
				}
				if format == "json" {
					require.True(t, json.Valid([]byte(output)), "invalid JSON: %s", output)
				}

			})
		}
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	require.NotEmpty(t, relay.filters)
	require.NotZero(t, relay.filters[len(relay.filters)-1].Since, "later invocation must reuse the persisted cursor")
}

func TestStateReadStaleStoreWarnsAndSucceeds(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	sk := nostr.Generate()
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	state := domain.EnvironmentServiceState{ServiceID: uuid.New(), EnvironmentID: uuid.New(), DriftStatus: domain.DriftStatusDrifted, UpdatedAt: now}
	tags, content := nostradapter.RuntimeStateRecord(&state, nil)
	dTag := "service:" + state.ServiceID.String() + ":environment:" + state.EnvironmentID.String()
	relay := &statePolicyRelay{events: []nostr.Event{makeCLIStatePolicyEvent(t, sk, kinds.ServiceState, dTag, tags, content, nostr.Timestamp(now.Unix()))}}
	server := httptest.NewServer(http.HandlerFunc(relay.serve))
	defer server.Close()
	args := []string{"--relay", "ws" + strings.TrimPrefix(server.URL, "http"), "--service-pubkey", nostr.GetPublicKey(sk).Hex(), "--output", "json", "state", "list"}
	fresh, stderr, err := runStatePolicyCLI(t, args...)
	require.NoError(t, err)
	require.Empty(t, stderr)
	relay.mu.Lock()
	relay.noEOSE = true
	relay.mu.Unlock()
	staleArgs := append([]string{"--eose-timeout", "30ms"}, args...)
	stale, stderr, err := runStatePolicyCLI(t, staleArgs...)
	require.NoError(t, err, "stale read must have exit code 0")
	require.Equal(t, fresh, stale)
	require.Contains(t, stderr, "warning: relay data may be stale")
}

func TestPolicyGetMissingReturnsError(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_NOSTR_NSEC", "not-a-key")
	sk := nostr.Generate()
	relay := &statePolicyRelay{}
	server := httptest.NewServer(http.HandlerFunc(relay.serve))
	defer server.Close()
	_, _, err := runStatePolicyCLI(t, "--relay", "ws"+strings.TrimPrefix(server.URL, "http"), "--service-pubkey", nostr.GetPublicKey(sk).Hex(), "policies", "get", uuid.New().String())
	require.ErrorContains(t, err, "not found")
}

func TestStateReadRejectsInvalidEOSETimeout(t *testing.T) {
	_, _, err := runStatePolicyCLI(t, "--relay", "ws://127.0.0.1:1", "--service-pubkey", nostr.GetPublicKey(nostr.Generate()).Hex(), "--eose-timeout", "0s", "state", "list")
	require.ErrorContains(t, err, "must be positive")
}
