package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"golang.org/x/crypto/bcrypt"

	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/config"
)

const testNIP98Key = "9a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b"

func makeNIP98Token(t *testing.T, method, url string) string {
	t.Helper()
	ev := nostr.Event{
		Kind:      27235,
		Content:   "",
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{{"u", url}, {"method", method}},
	}
	if err := ev.Sign(testNIP98Key); err != nil {
		t.Fatalf("sign: %v", err)
	}
	b, _ := json.Marshal(ev)
	return base64.StdEncoding.EncodeToString(b)
}

func TestResolveRegistryPrincipal_BasicServiceAccount(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	cfg := config.OCIServerConfig{ServiceAccounts: []config.OCIServiceAccountConfig{{
		Username:     "hive-ci",
		PasswordHash: string(hash),
		Permissions:  []string{"pull", "push"},
		RepoPrefixes: []string{"cascadia/"},
	}}}
	req := httptest.NewRequest(http.MethodGet, "http://example/v2/cascadia/app/manifests/latest", nil)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("hive-ci:secret")))

	p := ResolveRegistryPrincipal(req, nil, cfg)
	if p.AuthType != "basic" || p.ServiceAccount != "hive-ci" {
		t.Fatalf("unexpected principal: %+v", p)
	}
	if len(p.Scopes) != 1 || p.Scopes[0] != "repository:cascadia/*:pull,push" {
		t.Fatalf("unexpected scopes: %+v", p.Scopes)
	}
}

func TestResolveRegistryPrincipal_NIP98Push(t *testing.T) {
	url := "http://example/v2/cascadia/app/blobs/uploads/"
	req := httptest.NewRequest(http.MethodPost, url, nil)
	token := makeNIP98Token(t, http.MethodPost, url)
	req.Header.Set("Authorization", "Nostr "+token)
	validator := auth.NewNIP98Validator(auth.DefaultNIP98Config())

	p := ResolveRegistryPrincipal(req, validator, config.OCIServerConfig{})
	if p.AuthType != "nip98" || len(p.Scopes) == 0 {
		t.Fatalf("unexpected principal: %+v", p)
	}
}

func TestResolveRegistryPrincipal_AnonymousPullCIDRAndTrustedProxy(t *testing.T) {
	cfg := config.OCIServerConfig{
		AllowAnonymousPullCIDRs: []string{"192.168.40.0/24"},
		TrustedProxyCIDRs:       []string{"10.0.0.0/8"},
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/v2/cascadia/app/manifests/latest", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	req.Header.Set("X-Forwarded-For", "192.168.40.10")

	p := ResolveRegistryPrincipal(req, nil, cfg)
	if len(p.Scopes) == 0 || p.Scopes[0] != "repository:*:pull" {
		t.Fatalf("expected anonymous pull scope, got %+v", p)
	}
}

func TestResolveRegistryPrincipal_UntrustedProxyIgnoresXFF(t *testing.T) {
	cfg := config.OCIServerConfig{
		AllowAnonymousPullCIDRs: []string{"192.168.40.0/24"},
		TrustedProxyCIDRs:       []string{"10.0.0.0/8"},
	}
	req := httptest.NewRequest(http.MethodGet, "http://example/v2/cascadia/app/manifests/latest", nil)
	req.RemoteAddr = "172.16.1.2:1234"
	req.Header.Set("X-Forwarded-For", "192.168.40.10")

	p := ResolveRegistryPrincipal(req, nil, cfg)
	if len(p.Scopes) != 0 {
		t.Fatalf("expected no anonymous scope, got %+v", p)
	}
}
