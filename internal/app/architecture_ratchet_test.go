package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
)

// Architecture ratchet (audit Recommendation 2a): a daemon
// with no reachable Postgres must boot, gate nil-repository routes with
// RequireRepo middleware (503), and never expose a route whose handler would
// dereference a nil repository.

// unreachableDatabaseConfig points the daemon at a local port with nothing
// listening, so the real db.Connect path fails fast.
func unreachableDatabaseConfig(t *testing.T, mode string) *config.Config {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	cfg := startupTestConfig(mode)
	cfg.DB.Host = "127.0.0.1"
	cfg.DB.Port = port
	cfg.DB.User = "bahia"
	cfg.DB.Name = "bahia"
	cfg.DB.SSLMode = "disable"
	return cfg
}

func TestArchitectureDBLessDaemonBootGatesNilRepositoryRoutes(t *testing.T) {
	app, err := New(unreachableDatabaseConfig(t, "full"))
	require.NoError(t, err, "the daemon must boot without Postgres")
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	require.Nil(t, app.DB)

	routes, ok := app.HTTPServer.Handler.(chi.Routes)
	require.True(t, ok, "HTTP handler must stay a chi router so routes can be enumerated")

	type probe struct{ method, route string }
	var probes []probe
	require.NoError(t, chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		probes = append(probes, probe{method: method, route: route})
		return nil
	}))
	require.NotEmpty(t, probes)
	sort.Slice(probes, func(i, j int) bool {
		return probes[i].route+probes[i].method < probes[j].route+probes[j].method
	})

	var failures []string
	statuses := map[int]int{}
	for i, p := range probes {
		path := concreteRoutePath(p.route)
		code, finished := serveProbe(app.HTTPServer.Handler, p.method, path, i)
		statuses[code]++
		switch {
		case !finished:
			failures = append(failures, fmt.Sprintf("%s %s did not answer within 2s", p.method, p.route))
		case code == http.StatusInternalServerError:
			failures = append(failures, fmt.Sprintf("%s %s returned 500 (nil repository or panic) without a database", p.method, p.route))
		case code != http.StatusServiceUnavailable && !dbLessServedRoutes[p.route]:
			failures = append(failures, fmt.Sprintf("%s %s returned %d; without a database only %v may serve, everything else must return 503", p.method, p.route, code, sortedKeys(dbLessServedRoutes)))
		}
	}
	t.Logf("probed %d DB-less routes; status histogram %v", len(probes), statuses)
	require.Empty(t, failures, "DB-less routes must be gated (503) or work without Postgres")
}

// TestArchitectureServicesVisibleFromRelaysWithNoDB is the target the ratchet
// above falls short of (audit Recommendation 2a): with no Postgres, services
// (and environments and DNS) come from addressable relay state, so the
// daemon serves them.
func TestArchitectureServicesVisibleFromRelaysWithNoDB(t *testing.T) {
	t.Skip("pending DB-less daemon relay-state rebuild coverage")

	app, err := New(unreachableDatabaseConfig(t, "full"))
	require.NoError(t, err)
	defer syncTestLogger(t, app.Logger)
	defer closeRelayPools(app.relayPools...)

	recorder := httptest.NewRecorder()
	app.HTTPServer.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
}

// dbLessServedRoutes are the routes a DB-less daemon answers.
// Growing this set means a route works from relay state alone.
var dbLessServedRoutes = map[string]bool{
	"/health":  true,
	"/ready":   true,
	"/metrics": true,
	// Payment records are canonical cp-state in the local event store
	//.
	"/api/v1/payments/history":           true,
	"/api/v1/deployments/runs/{id}/cost": true,
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var routeParam = regexp.MustCompile(`\{[^}]+\}`)

func concreteRoutePath(route string) string {
	path := routeParam.ReplaceAllString(route, "00000000-0000-0000-0000-000000000001")
	path = strings.ReplaceAll(path, "/*/", "/x/")
	path = strings.TrimSuffix(path, "*")
	return path
}

// serveProbe sends one request with a distinct client address so per-IP rate
// limits do not mask handler behaviour.
func serveProbe(handler http.Handler, method, path string, n int) (int, bool) {
	var body *strings.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		body = strings.NewReader("{}")
	} else {
		body = strings.NewReader("")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:40000", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(recorder, req)
	}()
	select {
	case <-done:
		return recorder.Code, true
	case <-ctx.Done():
		return 0, false
	}
}
