package gitea

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMigrateMirrorPrivateGiteaUsesGitBasicAuth(t *testing.T) {
	const (
		cloneURL = "https://git.sharegap.net/fleet/astillero.git"
		username = "bahia-mirror"
		password = "private-gitea-token"
	)
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/repos/migrate" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode migration request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "fleet-admin-token", server.Client())
	err := client.MigrateMirror(context.Background(), MigrateMirrorRequest{
		Owner:        "fleet",
		Name:         "astillero",
		CloneAddr:    cloneURL,
		Service:      MigrationServiceGit,
		AuthUsername: username,
		AuthPassword: password,
	})
	if err != nil {
		t.Fatalf("MigrateMirror: %v", err)
	}
	if got["service"] != MigrationServiceGit || got["auth_username"] != username || got["auth_password"] != password {
		t.Fatalf("private Gitea migration auth = %#v", got)
	}
	if _, ok := got["auth_token"]; ok {
		t.Fatalf("private Gitea migration must not send auth_token: %#v", got)
	}
	if got["clone_addr"] != cloneURL {
		t.Fatalf("clone_addr = %q, want credential-free %q", got["clone_addr"], cloneURL)
	}
	if got["private"] != true || got["mirror"] != true {
		t.Fatalf("migration must create a private mirror: %#v", got)
	}
}

func TestMigrateMirrorGitHubUsesTokenAuth(t *testing.T) {
	const token = "github-private-token"
	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode migration request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, "fleet-admin-token", server.Client())
	err := client.MigrateMirror(context.Background(), MigrateMirrorRequest{
		Owner:     "fleet",
		Name:      "bahia",
		CloneAddr: "https://github.com/openagentsinc/bahia.git",
		Service:   MigrationServiceGitHub,
		AuthToken: token,
	})
	if err != nil {
		t.Fatalf("MigrateMirror: %v", err)
	}
	if got["service"] != MigrationServiceGitHub || got["auth_token"] != token {
		t.Fatalf("GitHub migration auth = %#v", got)
	}
	if _, ok := got["auth_username"]; ok {
		t.Fatalf("GitHub migration must not send auth_username: %#v", got)
	}
	if _, ok := got["auth_password"]; ok {
		t.Fatalf("GitHub migration must not send auth_password: %#v", got)
	}
}

func TestMigrateMirrorGitHubRejectsNonGitHubHost(t *testing.T) {
	client := NewAPIClient("https://fleet-gitea.example", "fleet-admin-token", nil)
	err := client.MigrateMirror(context.Background(), MigrateMirrorRequest{
		Owner:     "fleet",
		Name:      "private",
		CloneAddr: "https://attacker.example/openagentsinc/bahia.git",
		Service:   MigrationServiceGitHub,
		AuthToken: "github-private-token",
	})
	if err == nil || !strings.Contains(err.Error(), "must use github.com") {
		t.Fatalf("expected GitHub host binding error, got %v", err)
	}
}

func TestMigrateMirror422IncludesScrubbedBoundedResponse(t *testing.T) {
	const (
		password   = `private token/with "special" +&= characters`
		adminToken = "fleet-admin-token"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		userinfo := strings.TrimPrefix(url.UserPassword("_", password).String(), "_:")
		_, _ = fmt.Fprintf(w, `{"message":"migration failed for %s; query=%s; userinfo=%s; html=%s; base64=%s; admin=%s; detail=%s"}`,
			password, url.QueryEscape(password), userinfo, html.EscapeString(password),
			base64.StdEncoding.EncodeToString([]byte(password)), adminToken, strings.Repeat("x", 6000))
	}))
	defer server.Close()

	client := NewAPIClient(server.URL, adminToken, server.Client())
	err := client.MigrateMirror(context.Background(), MigrateMirrorRequest{
		Owner:        "fleet",
		Name:         "astillero",
		CloneAddr:    "https://git.sharegap.net/fleet/astillero.git",
		Service:      MigrationServiceGit,
		AuthUsername: "bahia-mirror",
		AuthPassword: password,
	})
	if err == nil {
		t.Fatal("expected 422 migration failure")
	}
	message := err.Error()
	if !strings.Contains(message, "status 422") || !strings.Contains(message, "migration failed") {
		t.Fatalf("422 error lacks diagnostic response excerpt: %v", err)
	}
	for _, secretVariant := range []string{
		password,
		url.QueryEscape(password),
		strings.TrimPrefix(url.UserPassword("_", password).String(), "_:"),
		html.EscapeString(password),
		base64.StdEncoding.EncodeToString([]byte(password)),
		adminToken,
	} {
		if strings.Contains(message, secretVariant) {
			t.Fatalf("credential survived error scrubbing: %q", message)
		}
	}
	if len([]rune(message)) > maxGiteaErrorExcerptRunes+200 {
		t.Fatalf("error response excerpt is not bounded: %d runes", len([]rune(message)))
	}
}

func TestMigrateMirrorRejectsUnsafeCloneURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	client := NewAPIClient(server.URL, "admin", server.Client())
	for _, cloneURL := range []string{
		"http://git.example/fleet/private.git",
		"https://mirror-user:private-token@git.example/fleet/private.git",
		"https://git.example/fleet/private.git?token=private-token",
		"https://git.example/fleet/private.git#private-token",
	} {
		t.Run(cloneURL, func(t *testing.T) {
			err := client.MigrateMirror(context.Background(), MigrateMirrorRequest{
				Owner:        "fleet",
				Name:         "private",
				CloneAddr:    cloneURL,
				Service:      MigrationServiceGit,
				AuthUsername: "mirror-user",
				AuthPassword: "private-token",
			})
			if err == nil {
				t.Fatal("expected unsafe clone URL rejection")
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatalf("unsafe clone URL leaked credential: %v", err)
			}
		})
	}
}
