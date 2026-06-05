package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	userdocs "github.com/openagentsinc/bahia/internal/docs"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestAssistantContextBuilderResolvesSelectedDocumentationRefs(t *testing.T) {
	docsService := assistantDocsFixture(t, map[string]string{
		"index.md":             "# Bahia User Guide\n\nUse the guide to operate Bahia.\n",
		"features/services.md": "# Services\n\nServices are deployable application components.\n",
	})
	builder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, &docsService, AssistantContextBuilderConfig{})

	got, err := builder.BuildContext(context.Background(), nil, []string{"docs:index", "bahia://docs/features-services"}, "")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}

	assertContains(t, got, "## Documentation References")
	assertContains(t, got, "- ref=docs:index topic=index title=\"Bahia User Guide\" source=index.md href=/docs/index")
	assertContains(t, got, "# Bahia User Guide")
	assertContains(t, got, "- ref=bahia://docs/features-services topic=features-services title=\"Services\" source=features/services.md href=/docs/features-services")
	assertContains(t, got, "# Services")
	assertContains(t, got, "## Selected References")
	assertContains(t, got, "- docs:index")
	assertContains(t, got, "- bahia://docs/features-services")
}

func TestAssistantContextBuilderMissingDocumentationRefsAreNonFatal(t *testing.T) {
	docsService := assistantDocsFixture(t, map[string]string{"index.md": "# Bahia User Guide\n"})
	builder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, &docsService, AssistantContextBuilderConfig{})

	got, err := builder.BuildContext(context.Background(), nil, []string{"docs:missing-topic", "docs:"}, "")
	if err != nil {
		t.Fatalf("BuildContext returned error for unresolved docs refs: %v", err)
	}

	assertContains(t, got, "## Documentation References")
	assertContains(t, got, "- unresolved documentation ref=docs:missing-topic topic=missing-topic reason=documentation topic not found")
	assertContains(t, got, "- unresolved documentation ref=docs: reason=invalid documentation topic")
}

func TestAssistantContextBuilderBoundsDocumentationExcerptsAndFinalContext(t *testing.T) {
	longBody := "# Long Guide\n\n" + strings.Repeat("Keep this bounded.\n", 80) + "END-OF-DOC-SHOULD-NOT-APPEAR\n"
	docsService := assistantDocsFixture(t, map[string]string{"index.md": longBody})
	builder := NewAssistantContextBuilder(nil, nil, nil, nil, nil, &docsService, AssistantContextBuilderConfig{MaxChars: 1200})

	got, err := builder.BuildContext(context.Background(), nil, []string{"docs:index"}, "")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}
	if len(got) > 1200 {
		t.Fatalf("context length = %d, want <= 1200", len(got))
	}
	assertContains(t, got, "# Long Guide")
	if strings.Contains(got, "END-OF-DOC-SHOULD-NOT-APPEAR") {
		t.Fatalf("documentation excerpt was not bounded:\n%s", got)
	}
}

func TestAssistantContextBuilderKeepsOperationalSelectedRefResolutionWithDocsRefs(t *testing.T) {
	docsService := assistantDocsFixture(t, map[string]string{"index.md": "# Bahia User Guide\n"})
	serviceID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	registry := assistantContextServiceRegistry{
		services: []domain.Service{{
			ID:          serviceID,
			Name:        "api",
			RuntimeType: domain.RuntimeTypeDocker,
			UpdatedAt:   time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
		}},
	}
	builder := NewAssistantContextBuilder(registry, nil, nil, nil, nil, &docsService, AssistantContextBuilderConfig{})

	got, err := builder.BuildContext(context.Background(), nil, []string{"docs:index", "service:" + serviceID.String()}, "")
	if err != nil {
		t.Fatalf("BuildContext returned error: %v", err)
	}

	assertContains(t, got, "## Documentation References")
	assertContains(t, got, "topic=index")
	assertContains(t, got, "## Resolved Resources")
	assertContains(t, got, "- service id=33333333-3333-4333-8333-333333333333 name=api runtime=docker target=api updated_at=2026-06-05T12:00:00Z")
}

func assistantDocsFixture(t *testing.T, files map[string]string) userdocs.Service {
	t.Helper()
	dir := t.TempDir()
	for relPath, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create docs fixture directory: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write docs fixture: %v", err)
		}
	}
	return userdocs.New(dir)
}

type assistantContextServiceRegistry struct {
	services []domain.Service
	states   []domain.EnvironmentServiceState
}

func (r assistantContextServiceRegistry) ListServices(context.Context) ([]domain.Service, error) {
	return append([]domain.Service(nil), r.services...), nil
}

func (r assistantContextServiceRegistry) ListAllStates(context.Context) ([]domain.EnvironmentServiceState, error) {
	return append([]domain.EnvironmentServiceState(nil), r.states...), nil
}

func (r assistantContextServiceRegistry) GetService(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	for _, svc := range r.services {
		if svc.ID == id {
			copy := svc
			return &copy, nil
		}
	}
	return nil, nil
}
