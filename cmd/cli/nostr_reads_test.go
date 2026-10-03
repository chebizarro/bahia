package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
)

type cliReadSubscription struct {
	events chan *nostr.Event
	eose   chan client.RelayEOSEInfo
	done   chan struct{}
}

func (s *cliReadSubscription) Events() <-chan *nostr.Event            { return s.events }
func (s *cliReadSubscription) EndOfStoredEvents() <-chan struct{}     { return s.done }
func (s *cliReadSubscription) RelayEOSE() <-chan client.RelayEOSEInfo { return s.eose }
func (s *cliReadSubscription) Close()                                 {}

type cliReadPool struct {
	events  []nostr.Event
	stale   bool
	filters []nostr.Filter
}

func (p *cliReadPool) SubscribeAllWithEOSE(_ context.Context, filters []nostr.Filter) (client.Subscription, error) {
	p.filters = append(p.filters, filters[0])
	sub := &cliReadSubscription{
		events: make(chan *nostr.Event, len(p.events)),
		eose:   make(chan client.RelayEOSEInfo, 1),
		done:   make(chan struct{}),
	}
	for i := range p.events {
		ev := &p.events[i]
		if filterHasTopic(filters[0], ev) && (filters[0].Since == 0 || ev.CreatedAt >= filters[0].Since) {
			sub.events <- ev
		}
	}
	if !p.stale {
		sub.eose <- client.RelayEOSEInfo{RelayURL: "wss://fixture.invalid"}
	}
	return sub, nil
}

func filterHasTopic(filter nostr.Filter, ev *nostr.Event) bool {
	for _, tag := range ev.Tags {
		if len(tag) < 2 || tag[0] != "t" {
			continue
		}
		for _, topic := range filter.Tags["t"] {
			if tag[1] == topic {
				return true
			}
		}
	}
	return false
}

func installCLIReadPool(t *testing.T, pool *cliReadPool) {
	t.Helper()
	prior := newCLIReadPool
	newCLIReadPool = func(context.Context, []string) (client.SubscriptionPool, func(), error) {
		return pool, func() {}, nil
	}
	t.Cleanup(func() { newCLIReadPool = prior })
}

func makeCLIReadEvent(t *testing.T, sk nostr.SecretKey, kind int, id uuid.UUID, content any, timestamp nostr.Timestamp) nostr.Event {
	t.Helper()
	_, tags := nostrpool.ControlStateEnvelope(kind, id.String(), false)
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: 30900, CreatedAt: timestamp, Tags: tags, Content: string(encoded)}
	if err := ev.Sign(sk); err != nil {
		t.Fatal(err)
	}
	return ev
}

func runReadCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prior := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = prior }()
	root := newRootCommand()
	root.SilenceUsage = true
	root.SilenceErrors = true
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs(args)
	runErr := root.Execute()
	_ = write.Close()
	out, err := io.ReadAll(read)
	_ = read.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), stderr.String(), runErr
}

func TestCLIReadRESTNostrGolden(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	sk := nostr.Generate()
	pub := nostr.GetPublicKey(sk).Hex()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	service := domain.Service{ID: uuid.New(), Name: "api", ArtifactRepo: "ghcr.io/acme/api", RuntimeType: domain.RuntimeTypeCompose, RuntimeConfig: &domain.ServiceRuntimeConfig{Adopted: &domain.AdoptedRuntimeConfig{TargetName: "api", SourceRuntime: "compose", HostAlias: "node-1"}}, CreatedAt: now, UpdatedAt: now}
	secondService := domain.Service{ID: uuid.New(), Name: "worker", ArtifactRepo: "ghcr.io/acme/worker", RuntimeType: domain.RuntimeTypeCompose, CreatedAt: now, UpdatedAt: now}
	env := domain.Environment{ID: uuid.New(), Name: "production", LoomWorkerSelector: map[string]any{"region": "west"}, RuntimeConfig: map[string]any{"type": "compose"}, DeployStrategy: domain.DeployStrategyCanary, Protected: true, CreatedAt: now, UpdatedAt: now}
	unit := domain.DeploymentUnit{ID: uuid.New(), EnvironmentID: env.ID, Key: "api", RuntimeType: domain.RuntimeTypeCompose, EndpointRef: "node-1", Implicit: false, CreatedAt: now, UpdatedAt: now}
	details := client.EnvironmentDetails{Environment: env, DeploymentUnits: []domain.DeploymentUnit{unit}}
	pool := &cliReadPool{events: []nostr.Event{
		makeCLIReadEvent(t, sk, nostrpool.KindServiceRegistry, secondService.ID, secondService, nostr.Timestamp(time.Now().Unix())),
		makeCLIReadEvent(t, sk, nostrpool.KindServiceRegistry, service.ID, service, nostr.Timestamp(time.Now().Unix())),
		makeCLIReadEvent(t, sk, nostrpool.KindEnvironmentRegistry, env.ID, details, nostr.Timestamp(time.Now().Unix())),
	}}
	installCLIReadPool(t, pool)
	httpCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpCalls++
		var data any
		switch r.URL.Path {
		case "/api/v1/services":
			data = []domain.Service{service, secondService}
		case "/api/v1/services/" + service.ID.String():
			data = service
		case "/api/v1/environments":
			data = []domain.Environment{env}
		case "/api/v1/environments/" + env.ID.String():
			data = details
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	for _, command := range [][]string{{"services", "list"}, {"services", "get", service.ID.String()}, {"environments", "list"}, {"environments", "get", env.ID.String()}} {
		for _, format := range []string{"table", "json"} {
			t.Run(strings.Join(command, "-")+"-"+format, func(t *testing.T) {
				base := []string{"--server", server.URL, "--service-pubkey", pub, "--relay", "wss://fixture.invalid", "--output", format}
				rest, _, err := runReadCLI(t, append(append([]string{}, base...), append([]string{"--http-fallback"}, command...)...)...)
				if err != nil {
					t.Fatalf("REST command: %v", err)
				}
				nostrOutput, stderr, err := runReadCLI(t, append(append([]string{}, base...), command...)...)
				if err != nil {
					t.Fatalf("Nostr command: %v", err)
				}
				if rest != nostrOutput {
					t.Fatalf("REST/Nostr output diff:\nREST:\n%s\nNostr:\n%s", rest, nostrOutput)
				}
				if stderr != "" {
					t.Fatalf("fresh read stderr = %q", stderr)
				}
			})
		}
	}
	if len(pool.filters) != 8 {
		t.Fatalf("Nostr subscriptions = %d, want 8; HTTP fallback must not subscribe", len(pool.filters))
	}
	if httpCalls != 8 {
		t.Fatalf("HTTP requests = %d, want 8 fallback-only reads", httpCalls)
	}
	for _, filter := range pool.filters {
		if len(filter.Tags["t"]) != 1 {
			t.Fatalf("read filter must select one state family: %v", filter)
		}
	}
	if findDirectChild(servicesCommands(), "list").Flags().Lookup("nostr") != nil {
		t.Fatal("services list still exposes --nostr")
	}
}

func TestCLIReadCursorReuseAndStaleExit(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	sk := nostr.Generate()
	pub := nostr.GetPublicKey(sk).Hex()
	service := domain.Service{ID: uuid.New(), Name: "cached", ArtifactRepo: "repo", RuntimeType: domain.RuntimeTypeCompose}
	stamp := nostr.Timestamp(time.Now().Unix())
	pool := &cliReadPool{events: []nostr.Event{makeCLIReadEvent(t, sk, nostrpool.KindServiceRegistry, service.ID, service, stamp)}}
	installCLIReadPool(t, pool)
	args := []string{"--service-pubkey", pub, "--relay", "wss://fixture.invalid", "--output", "json", "services", "list"}
	first, stderr, err := runReadCLI(t, args...)
	if err != nil || stderr != "" {
		t.Fatalf("initial read: err=%v stderr=%q", err, stderr)
	}
	pool.events = nil
	second, stderr, err := runReadCLI(t, args...)
	if err != nil || stderr != "" {
		t.Fatalf("cursor read: err=%v stderr=%q", err, stderr)
	}
	if first != second {
		t.Fatalf("cached output changed: %s != %s", first, second)
	}
	if len(pool.filters) != 2 || pool.filters[1].Since != stamp {
		t.Fatalf("cursor reuse: filters=%v", pool.filters)
	}
	pool.stale = true
	staleArgs := append([]string{"--eose-timeout", "1ms"}, args...)
	stale, stderr, err := runReadCLI(t, staleArgs...)
	if err != nil {
		t.Fatalf("stale read must exit 0: %v", err)
	}
	if stale != first {
		t.Fatalf("stale cache output changed: %s != %s", stale, first)
	}
	if stderr != "warning: relay data may be stale (no EOSE within timeout)\n" {
		t.Fatalf("stale warning = %q", stderr)
	}
	path, err := nostrServiceStorePath(pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("store path: %v", err)
	}
}

func TestNostrServiceStorePathRejectsInvalidPubkey(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	if _, err := nostrServiceStorePath("../../outside"); err == nil {
		t.Fatal("invalid service pubkey was accepted as a store path component")
	}
}

func TestCLIEmptyServiceListMatchesREST(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_OPERATOR_HTTP_FALLBACK", "")
	sk := nostr.Generate()
	pub := nostr.GetPublicKey(sk).Hex()
	installCLIReadPool(t, &cliReadPool{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	defer server.Close()
	base := []string{"--server", server.URL, "--service-pubkey", pub, "--relay", "wss://fixture.invalid", "--output", "json"}
	rest, _, err := runReadCLI(t, append(append([]string{}, base...), "--http-fallback", "services", "list")...)
	if err != nil {
		t.Fatal(err)
	}
	nostrOutput, _, err := runReadCLI(t, append(append([]string{}, base...), "services", "list")...)
	if err != nil {
		t.Fatal(err)
	}
	if rest != nostrOutput {
		t.Fatalf("empty REST/Nostr output diff: REST=%q Nostr=%q", rest, nostrOutput)
	}
}
