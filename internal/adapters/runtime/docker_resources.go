package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// Network ensure
// ---------------------------------------------------------------------------

// dockerNetworkInspect is the subset of Docker network inspect response we need.
type dockerNetworkInspect struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
}

// EnsureNetworks inspects each required network and creates it if missing.
// If an existing network has an incompatible driver, an error is returned.
// Bahia labels are added to newly created networks.
func EnsureNetworks(ctx context.Context, observer *DockerObserver, specs []domain.NetworkSpec) error {
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			return fmt.Errorf("network spec has empty name")
		}
		if err := ensureNetwork(ctx, observer, spec); err != nil {
			return fmt.Errorf("ensuring network %q: %w", spec.Name, err)
		}
	}
	return nil
}

func ensureNetwork(ctx context.Context, observer *DockerObserver, spec domain.NetworkSpec) error {
	// Inspect existing network.
	existing, found, err := inspectNetwork(ctx, observer, spec.Name)
	if err != nil {
		return err
	}

	if found {
		// Check compatibility: driver must match if specified.
		return checkNetworkCompatibility(existing, spec)
	}

	// Network does not exist — create it.
	return createNetwork(ctx, observer, spec)
}

func inspectNetwork(ctx context.Context, observer *DockerObserver, name string) (*dockerNetworkInspect, bool, error) {
	requestURL := fmt.Sprintf("%s/v1.44/networks/%s", observer.host, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("creating network inspect request: %w", err)
	}

	resp, err := observer.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("inspecting network: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("docker network inspect returned %d", resp.StatusCode)
	}

	var info dockerNetworkInspect
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, false, fmt.Errorf("decoding network inspect: %w", err)
	}
	return &info, true, nil
}

func checkNetworkCompatibility(existing *dockerNetworkInspect, spec domain.NetworkSpec) error {
	wantDriver := normalizeNetworkDriver(spec.Driver)
	existingDriver := normalizeNetworkDriver(existing.Driver)

	if wantDriver != existingDriver {
		return fmt.Errorf(
			"incompatible network %q: want driver %q, existing driver %q",
			spec.Name, wantDriver, existingDriver,
		)
	}
	return nil
}

// normalizeNetworkDriver returns the canonical driver name. Docker defaults
// to "bridge" when the driver is empty.
func normalizeNetworkDriver(driver string) string {
	d := strings.TrimSpace(strings.ToLower(driver))
	if d == "" {
		return "bridge"
	}
	return d
}

func createNetwork(ctx context.Context, observer *DockerObserver, spec domain.NetworkSpec) error {
	labels := makeBahiaResourceLabels(spec.Labels)

	body := map[string]any{
		"Name":   spec.Name,
		"Labels": labels,
	}
	if spec.Driver != "" {
		body["Driver"] = spec.Driver
	}
	if len(spec.Options) > 0 {
		body["Options"] = spec.Options
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling network create body: %w", err)
	}

	requestURL := fmt.Sprintf("%s/v1.44/networks/create", observer.host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return fmt.Errorf("creating network create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := observer.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating network: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("docker network create returned %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Volume ensure
// ---------------------------------------------------------------------------

// dockerVolumeInspect is the subset of Docker volume inspect response we need.
type dockerVolumeInspect struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
}

// EnsureVolumes inspects each required named volume and creates it if missing.
// If an existing volume has an incompatible driver, an error is returned.
// Bahia labels are added to newly created volumes.
func EnsureVolumes(ctx context.Context, observer *DockerObserver, specs []domain.VolumeSpec) error {
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			return fmt.Errorf("volume spec has empty name")
		}
		if err := ensureVolume(ctx, observer, spec); err != nil {
			return fmt.Errorf("ensuring volume %q: %w", spec.Name, err)
		}
	}
	return nil
}

func ensureVolume(ctx context.Context, observer *DockerObserver, spec domain.VolumeSpec) error {
	existing, found, err := inspectVolume(ctx, observer, spec.Name)
	if err != nil {
		return err
	}

	if found {
		return checkVolumeCompatibility(existing, spec)
	}

	return createVolume(ctx, observer, spec)
}

func inspectVolume(ctx context.Context, observer *DockerObserver, name string) (*dockerVolumeInspect, bool, error) {
	requestURL := fmt.Sprintf("%s/v1.44/volumes/%s", observer.host, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("creating volume inspect request: %w", err)
	}

	resp, err := observer.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("inspecting volume: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("docker volume inspect returned %d", resp.StatusCode)
	}

	var info dockerVolumeInspect
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, false, fmt.Errorf("decoding volume inspect: %w", err)
	}
	return &info, true, nil
}

func checkVolumeCompatibility(existing *dockerVolumeInspect, spec domain.VolumeSpec) error {
	wantDriver := normalizeVolumeDriver(spec.Driver)
	existingDriver := normalizeVolumeDriver(existing.Driver)

	if wantDriver != existingDriver {
		return fmt.Errorf(
			"incompatible volume %q: want driver %q, existing driver %q",
			spec.Name, wantDriver, existingDriver,
		)
	}
	return nil
}

// normalizeVolumeDriver returns the canonical driver name. Docker defaults
// to "local" when the driver is empty.
func normalizeVolumeDriver(driver string) string {
	d := strings.TrimSpace(strings.ToLower(driver))
	if d == "" {
		return "local"
	}
	return d
}

func createVolume(ctx context.Context, observer *DockerObserver, spec domain.VolumeSpec) error {
	labels := makeBahiaResourceLabels(spec.Labels)

	body := map[string]any{
		"Name":   spec.Name,
		"Labels": labels,
	}
	if spec.Driver != "" {
		body["Driver"] = spec.Driver
	}
	if len(spec.DriverOpts) > 0 {
		body["DriverOpts"] = spec.DriverOpts
	}

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling volume create body: %w", err)
	}

	requestURL := fmt.Sprintf("%s/v1.44/volumes/create", observer.host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return fmt.Errorf("creating volume create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := observer.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("docker volume create returned %d", resp.StatusCode)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// makeBahiaResourceLabels merges user-provided labels with the canonical
// bahia.managed label. User labels are preserved; bahia.managed is always set.
func makeBahiaResourceLabels(userLabels map[string]string) map[string]string {
	labels := make(map[string]string, len(userLabels)+1)
	for k, v := range userLabels {
		labels[k] = v
	}
	labels["bahia.managed"] = "true"
	return labels
}
