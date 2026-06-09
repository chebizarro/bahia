package runtime

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ---------------------------------------------------------------------------
// FragmentLayout path derivation tests
// ---------------------------------------------------------------------------

func TestFragmentLayout_PathDerivation(t *testing.T) {
	composeDir := filepath.Join("/", "projects", "myapp")
	layout := NewFragmentLayout(composeDir, "api-server")

	wantDir := filepath.Join(composeDir, bahiaMarkerDir, bahiaFragmentsDir)
	wantFile := filepath.Join(wantDir, "api-server.yml")

	if layout.ServiceKey != "api-server" {
		t.Errorf("ServiceKey: want %q, got %q", "api-server", layout.ServiceKey)
	}
	if layout.FragmentDir != wantDir {
		t.Errorf("FragmentDir: want %q, got %q", wantDir, layout.FragmentDir)
	}
	if layout.FragmentFile != wantFile {
		t.Errorf("FragmentFile: want %q, got %q", wantFile, layout.FragmentFile)
	}
	// NewFragmentLayout does not populate FragmentYAML.
	if len(layout.FragmentYAML) != 0 {
		t.Errorf("FragmentYAML: expected empty from NewFragmentLayout, got %d bytes", len(layout.FragmentYAML))
	}
}

func TestFragmentLayout_PathDerivation_DifferentServices(t *testing.T) {
	composeDir := "/srv/compose"
	cases := []struct {
		serviceKey string
		wantFile   string
	}{
		{"api", filepath.Join(composeDir, bahiaMarkerDir, bahiaFragmentsDir, "api.yml")},
		{"web-frontend", filepath.Join(composeDir, bahiaMarkerDir, bahiaFragmentsDir, "web-frontend.yml")},
		{"worker-v2", filepath.Join(composeDir, bahiaMarkerDir, bahiaFragmentsDir, "worker-v2.yml")},
	}
	for _, tc := range cases {
		layout := NewFragmentLayout(composeDir, tc.serviceKey)
		if layout.FragmentFile != tc.wantFile {
			t.Errorf("service %q: FragmentFile want %q, got %q",
				tc.serviceKey, tc.wantFile, layout.FragmentFile)
		}
	}
}

// ---------------------------------------------------------------------------
// ComposeFragmentRenderer — RenderServiceFragment tests
// ---------------------------------------------------------------------------

func TestRenderServiceFragment_Basic(t *testing.T) {
	r := NewComposeFragmentRenderer()

	svc := domain.DesiredServiceSpec{
		StableServiceKey: "api",
		ImageRef:         "myapi:v1",
		Ports:            []string{"8080:8080"},
		RestartPolicy:    "unless-stopped",
	}

	got, err := r.RenderServiceFragment("myproject", svc)
	if err != nil {
		t.Fatalf("RenderServiceFragment: %v", err)
	}
	if got.ServiceKey != "api" {
		t.Errorf("ServiceKey: want %q, got %q", "api", got.ServiceKey)
	}

	content := string(got.FragmentYAML)

	// Must contain the project name.
	if !strings.Contains(content, "name: myproject") {
		t.Errorf("expected 'name: myproject' in fragment:\n%s", content)
	}
	// Must contain the service key.
	if !strings.Contains(content, "api:") {
		t.Errorf("expected service key 'api:' in fragment:\n%s", content)
	}
	// Must contain the image.
	if !strings.Contains(content, "myapi:v1") {
		t.Errorf("expected image 'myapi:v1' in fragment:\n%s", content)
	}
	// Must contain the port.
	if !strings.Contains(content, "8080:8080") {
		t.Errorf("expected port '8080:8080' in fragment:\n%s", content)
	}
}

func TestRenderServiceFragment_PreservesProjectName(t *testing.T) {
	r := NewComposeFragmentRenderer()
	svc := domain.DesiredServiceSpec{
		StableServiceKey: "worker",
		ImageRef:         "myworker:latest",
	}

	cases := []string{"bahia-production", "my-app", "bahia-00000000"}
	for _, projectName := range cases {
		got, err := r.RenderServiceFragment(projectName, svc)
		if err != nil {
			t.Fatalf("RenderServiceFragment(%q): %v", projectName, err)
		}
		want := "name: " + projectName
		if !strings.Contains(string(got.FragmentYAML), want) {
			t.Errorf("project name %q not preserved in fragment:\n%s", projectName, got.FragmentYAML)
		}
	}
}

func TestRenderServiceFragment_NoNetworkOrVolumeDeclarations(t *testing.T) {
	r := NewComposeFragmentRenderer()

	// Service references both networks and volume declarations via extension.
	// These are project-wide declarations and must NOT appear at the top level.
	svc := domain.DesiredServiceSpec{
		StableServiceKey: "api",
		ImageRef:         "api:v1",
		ComposeExtension: &domain.ComposeExtension{
			Networks:           []string{"backend", "frontend"},
			VolumeDeclarations: []string{"api-data"},
		},
	}

	got, err := r.RenderServiceFragment("myproject", svc)
	if err != nil {
		t.Fatalf("RenderServiceFragment: %v", err)
	}

	content := string(got.FragmentYAML)

	// Top-level network declarations must not appear (service-level networks are OK).
	if strings.Contains(content, "\nnetworks:\n") {
		t.Errorf("fragment must not contain top-level network declarations:\n%s", content)
	}

	// Top-level volume declarations must not appear.
	if strings.Contains(content, "\nvolumes:\n") {
		t.Errorf("fragment must not contain top-level volume declarations:\n%s", content)
	}
}

func TestRenderServiceFragment_Deterministic(t *testing.T) {
	r := NewComposeFragmentRenderer()

	svc := domain.DesiredServiceSpec{
		StableServiceKey: "api",
		ImageRef:         "api:v1",
		Ports:            []string{"8080:8080", "9090:9090"},
		Env: map[string]string{
			"FOO": "bar",
			"BAZ": "qux",
		},
		Labels: map[string]string{
			"bahia.managed": "true",
			"app.version":   "v1",
		},
		RestartPolicy: "always",
	}

	first, err := r.RenderServiceFragment("myproject", svc)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}

	// Render multiple times and confirm identical output.
	for i := 0; i < 5; i++ {
		again, err := r.RenderServiceFragment("myproject", svc)
		if err != nil {
			t.Fatalf("render %d: %v", i+2, err)
		}
		if string(first.FragmentYAML) != string(again.FragmentYAML) {
			t.Errorf("fragment render is not deterministic on iteration %d:\n"+
				"--- first ---\n%s\n--- iteration %d ---\n%s",
				i+2, first.FragmentYAML, i+2, again.FragmentYAML)
		}
	}
}

func TestRenderServiceFragment_ErrorOnEmptyProjectName(t *testing.T) {
	r := NewComposeFragmentRenderer()
	svc := domain.DesiredServiceSpec{
		StableServiceKey: "api",
		ImageRef:         "api:v1",
	}

	_, err := r.RenderServiceFragment("", svc)
	if err == nil {
		t.Error("expected error for empty project name, got nil")
	}
}

func TestRenderServiceFragment_ErrorOnEmptyServiceKey(t *testing.T) {
	r := NewComposeFragmentRenderer()
	svc := domain.DesiredServiceSpec{
		StableServiceKey: "",
		ImageRef:         "api:v1",
	}

	_, err := r.RenderServiceFragment("", svc)
	if err == nil {
		t.Error("expected error for empty service key, got nil")
	}
}
