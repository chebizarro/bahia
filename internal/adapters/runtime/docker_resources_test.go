package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Test helpers — mock Docker API server
// ---------------------------------------------------------------------------

// mockDockerResource holds state for a mock network or volume.
type mockDockerResource struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
}

// mockDockerServer simulates the Docker Engine API for network and volume
// operations. It tracks inspected and created resources for assertions.
type mockDockerServer struct {
	mu       sync.Mutex
	networks map[string]mockDockerResource
	volumes  map[string]mockDockerResource

	// Track API calls for verification.
	inspectedNetworks []string
	createdNetworks   []string
	inspectedVolumes  []string
	createdVolumes    []string
}

func newMockDockerServer() *mockDockerServer {
	return &mockDockerServer{
		networks: make(map[string]mockDockerResource),
		volumes:  make(map[string]mockDockerResource),
	}
}

func (m *mockDockerServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Network inspect: GET /v1.44/networks/{name}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.44/networks/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1.44/networks/")
			m.inspectedNetworks = append(m.inspectedNetworks, name)
			if net, ok := m.networks[name]; ok {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(net)
			} else {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "network not found"})
			}

		// Network create: POST /v1.44/networks/create
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/networks/create":
			var body struct {
				Name    string            `json:"Name"`
				Driver  string            `json:"Driver"`
				Labels  map[string]string `json:"Labels"`
				Options map[string]string `json:"Options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.createdNetworks = append(m.createdNetworks, body.Name)
			driver := body.Driver
			if driver == "" {
				driver = "bridge"
			}
			m.networks[body.Name] = mockDockerResource{
				Name:   body.Name,
				Driver: driver,
				Labels: body.Labels,
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": "net-" + body.Name})

		// Volume inspect: GET /v1.44/volumes/{name}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.44/volumes/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1.44/volumes/")
			m.inspectedVolumes = append(m.inspectedVolumes, name)
			if vol, ok := m.volumes[name]; ok {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(vol)
			} else {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"message": "volume not found"})
			}

		// Volume create: POST /v1.44/volumes/create
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/volumes/create":
			var body struct {
				Name       string            `json:"Name"`
				Driver     string            `json:"Driver"`
				Labels     map[string]string `json:"Labels"`
				DriverOpts map[string]string `json:"DriverOpts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			m.createdVolumes = append(m.createdVolumes, body.Name)
			driver := body.Driver
			if driver == "" {
				driver = "local"
			}
			m.volumes[body.Name] = mockDockerResource{
				Name:   body.Name,
				Driver: driver,
				Labels: body.Labels,
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Name": body.Name})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func setupTestServer(mock *mockDockerServer) (*httptest.Server, *DockerObserver) {
	server := httptest.NewServer(mock.handler())
	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}
	return server, observer
}

// ===========================================================================
// EnsureNetworks tests
// ===========================================================================

func TestEnsureNetworks_CreatesNew(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "app-net", Driver: "bridge", Labels: map[string]string{"env": "prod"}},
		{Name: "db-net"},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both should have been inspected and then created.
	if len(mock.inspectedNetworks) != 2 {
		t.Errorf("inspected %d networks, want 2", len(mock.inspectedNetworks))
	}
	if len(mock.createdNetworks) != 2 {
		t.Errorf("created %d networks, want 2", len(mock.createdNetworks))
	}

	// Verify created network has Bahia labels.
	net := mock.networks["app-net"]
	if net.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label on created network")
	}
	if net.Labels["env"] != "prod" {
		t.Error("missing user label on created network")
	}
	if net.Driver != "bridge" {
		t.Errorf("network driver = %q, want bridge", net.Driver)
	}

	// Default driver for db-net.
	dbNet := mock.networks["db-net"]
	if dbNet.Driver != "bridge" {
		t.Errorf("db-net driver = %q, want bridge (default)", dbNet.Driver)
	}
	if dbNet.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label on db-net")
	}
}

func TestEnsureNetworks_ExistingCompatible(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	// Pre-populate with a compatible existing network.
	mock.networks["app-net"] = mockDockerResource{
		Name:   "app-net",
		Driver: "bridge",
		Labels: map[string]string{"bahia.managed": "true"},
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "app-net", Driver: "bridge"},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be inspected but NOT created.
	if len(mock.inspectedNetworks) != 1 {
		t.Errorf("inspected %d networks, want 1", len(mock.inspectedNetworks))
	}
	if len(mock.createdNetworks) != 0 {
		t.Errorf("created %d networks, want 0 (already exists)", len(mock.createdNetworks))
	}
}

func TestEnsureNetworks_ExistingCompatible_DefaultDriver(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	// Existing network has explicit "bridge", spec has empty driver (default).
	mock.networks["app-net"] = mockDockerResource{
		Name:   "app-net",
		Driver: "bridge",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "app-net"}, // empty driver → defaults to bridge
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createdNetworks) != 0 {
		t.Errorf("should not create network when compatible existing, created %d", len(mock.createdNetworks))
	}
}

func TestEnsureNetworks_ExistingIncompatible(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.networks["app-net"] = mockDockerResource{
		Name:   "app-net",
		Driver: "overlay",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "app-net", Driver: "bridge"},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err == nil {
		t.Fatal("expected error for incompatible network driver")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error should mention 'incompatible', got: %v", err)
	}
	if !strings.Contains(err.Error(), "overlay") {
		t.Errorf("error should mention existing driver 'overlay', got: %v", err)
	}
	if !strings.Contains(err.Error(), "bridge") {
		t.Errorf("error should mention wanted driver 'bridge', got: %v", err)
	}
}

func TestEnsureNetworks_EmptyName(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	err := EnsureNetworks(context.Background(), observer, []domain.NetworkSpec{{Name: ""}})
	if err == nil {
		t.Fatal("expected error for empty network name")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("error should mention empty name, got: %v", err)
	}
}

func TestEnsureNetworks_EmptySpecs(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	// No specs = no-op, no error.
	err := EnsureNetworks(context.Background(), observer, nil)
	if err != nil {
		t.Fatalf("unexpected error for nil specs: %v", err)
	}
	err = EnsureNetworks(context.Background(), observer, []domain.NetworkSpec{})
	if err != nil {
		t.Fatalf("unexpected error for empty specs: %v", err)
	}
}

func TestEnsureNetworks_WithOptions(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{
			Name:    "custom-net",
			Driver:  "bridge",
			Options: map[string]string{"com.docker.network.bridge.name": "br-custom"},
			Labels:  map[string]string{"tier": "frontend"},
		},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := mock.networks["custom-net"]
	if created.Labels["tier"] != "frontend" {
		t.Error("missing user label 'tier'")
	}
	if created.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label")
	}
}

// ===========================================================================
// EnsureVolumes tests
// ===========================================================================

func TestEnsureVolumes_CreatesNew(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{Name: "app-data", Driver: "local", Labels: map[string]string{"backup": "daily"}},
		{Name: "db-data"},
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.inspectedVolumes) != 2 {
		t.Errorf("inspected %d volumes, want 2", len(mock.inspectedVolumes))
	}
	if len(mock.createdVolumes) != 2 {
		t.Errorf("created %d volumes, want 2", len(mock.createdVolumes))
	}

	vol := mock.volumes["app-data"]
	if vol.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label on created volume")
	}
	if vol.Labels["backup"] != "daily" {
		t.Error("missing user label on created volume")
	}
	if vol.Driver != "local" {
		t.Errorf("volume driver = %q, want local", vol.Driver)
	}

	dbVol := mock.volumes["db-data"]
	if dbVol.Driver != "local" {
		t.Errorf("db-data driver = %q, want local (default)", dbVol.Driver)
	}
}

func TestEnsureVolumes_ExistingCompatible(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.volumes["app-data"] = mockDockerResource{
		Name:   "app-data",
		Driver: "local",
		Labels: map[string]string{"bahia.managed": "true"},
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{Name: "app-data", Driver: "local"},
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.inspectedVolumes) != 1 {
		t.Errorf("inspected %d volumes, want 1", len(mock.inspectedVolumes))
	}
	if len(mock.createdVolumes) != 0 {
		t.Errorf("created %d volumes, want 0 (already exists)", len(mock.createdVolumes))
	}
}

func TestEnsureVolumes_ExistingCompatible_DefaultDriver(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.volumes["app-data"] = mockDockerResource{
		Name:   "app-data",
		Driver: "local",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{Name: "app-data"}, // empty driver → defaults to local
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createdVolumes) != 0 {
		t.Errorf("should not create volume when compatible existing, created %d", len(mock.createdVolumes))
	}
}

func TestEnsureVolumes_ExistingIncompatible(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.volumes["app-data"] = mockDockerResource{
		Name:   "app-data",
		Driver: "nfs",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{Name: "app-data", Driver: "local"},
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err == nil {
		t.Fatal("expected error for incompatible volume driver")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error should mention 'incompatible', got: %v", err)
	}
	if !strings.Contains(err.Error(), "nfs") {
		t.Errorf("error should mention existing driver 'nfs', got: %v", err)
	}
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("error should mention wanted driver 'local', got: %v", err)
	}
}

func TestEnsureVolumes_EmptyName(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	err := EnsureVolumes(context.Background(), observer, []domain.VolumeSpec{{Name: ""}})
	if err == nil {
		t.Fatal("expected error for empty volume name")
	}
	if !strings.Contains(err.Error(), "empty name") {
		t.Errorf("error should mention empty name, got: %v", err)
	}
}

func TestEnsureVolumes_EmptySpecs(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	err := EnsureVolumes(context.Background(), observer, nil)
	if err != nil {
		t.Fatalf("unexpected error for nil specs: %v", err)
	}
	err = EnsureVolumes(context.Background(), observer, []domain.VolumeSpec{})
	if err != nil {
		t.Fatalf("unexpected error for empty specs: %v", err)
	}
}

func TestEnsureVolumes_WithDriverOpts(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{
			Name:       "nfs-data",
			Driver:     "local",
			DriverOpts: map[string]string{"type": "nfs", "o": "addr=192.168.1.1,rw"},
			Labels:     map[string]string{"storage": "nfs"},
		},
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	created := mock.volumes["nfs-data"]
	if created.Labels["storage"] != "nfs" {
		t.Error("missing user label 'storage'")
	}
	if created.Labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed label")
	}
}

// ===========================================================================
// Mixed scenarios
// ===========================================================================

func TestEnsureNetworks_MixedExistingAndNew(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	// Pre-populate one network.
	mock.networks["existing-net"] = mockDockerResource{
		Name:   "existing-net",
		Driver: "bridge",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "existing-net", Driver: "bridge"},
		{Name: "new-net", Driver: "bridge"},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only new-net should be created.
	if len(mock.createdNetworks) != 1 {
		t.Errorf("created %d networks, want 1", len(mock.createdNetworks))
	}
	if len(mock.createdNetworks) > 0 && mock.createdNetworks[0] != "new-net" {
		t.Errorf("created network = %q, want new-net", mock.createdNetworks[0])
	}
}

func TestEnsureVolumes_MixedExistingAndNew(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.volumes["existing-vol"] = mockDockerResource{
		Name:   "existing-vol",
		Driver: "local",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.VolumeSpec{
		{Name: "existing-vol"},
		{Name: "new-vol"},
	}

	err := EnsureVolumes(context.Background(), observer, specs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createdVolumes) != 1 {
		t.Errorf("created %d volumes, want 1", len(mock.createdVolumes))
	}
	if len(mock.createdVolumes) > 0 && mock.createdVolumes[0] != "new-vol" {
		t.Errorf("created volume = %q, want new-vol", mock.createdVolumes[0])
	}
}

func TestEnsureNetworks_StopsOnFirstIncompatible(t *testing.T) {
	t.Parallel()
	mock := newMockDockerServer()
	mock.networks["bad-net"] = mockDockerResource{
		Name:   "bad-net",
		Driver: "overlay",
	}
	server, observer := setupTestServer(mock)
	defer server.Close()

	specs := []domain.NetworkSpec{
		{Name: "bad-net", Driver: "bridge"},
		{Name: "good-net", Driver: "bridge"},
	}

	err := EnsureNetworks(context.Background(), observer, specs)
	if err == nil {
		t.Fatal("expected error")
	}

	// Second network should not have been inspected or created.
	if len(mock.inspectedNetworks) != 1 {
		t.Errorf("inspected %d networks, want 1 (should stop on first error)", len(mock.inspectedNetworks))
	}
	if len(mock.createdNetworks) != 0 {
		t.Errorf("created %d networks, want 0", len(mock.createdNetworks))
	}
}

// ===========================================================================
// makeBahiaResourceLabels
// ===========================================================================

func TestMakeBahiaResourceLabels(t *testing.T) {
	t.Parallel()

	// With user labels.
	labels := makeBahiaResourceLabels(map[string]string{"env": "prod", "team": "infra"})
	if labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed")
	}
	if labels["env"] != "prod" {
		t.Error("missing user label 'env'")
	}
	if labels["team"] != "infra" {
		t.Error("missing user label 'team'")
	}

	// With nil labels.
	labels = makeBahiaResourceLabels(nil)
	if labels["bahia.managed"] != "true" {
		t.Error("missing bahia.managed with nil input")
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label, got %d", len(labels))
	}
}

// ===========================================================================
// Driver normalization
// ===========================================================================

func TestNormalizeNetworkDriver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"", "bridge"},
		{"bridge", "bridge"},
		{"Bridge", "bridge"},
		{"  BRIDGE  ", "bridge"},
		{"overlay", "overlay"},
	}
	for _, tt := range tests {
		got := normalizeNetworkDriver(tt.input)
		if got != tt.want {
			t.Errorf("normalizeNetworkDriver(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeVolumeDriver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"", "local"},
		{"local", "local"},
		{"Local", "local"},
		{"  LOCAL  ", "local"},
		{"nfs", "nfs"},
	}
	for _, tt := range tests {
		got := normalizeVolumeDriver(tt.input)
		if got != tt.want {
			t.Errorf("normalizeVolumeDriver(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
