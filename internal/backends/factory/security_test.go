package factory

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
)

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) ResolveSecret(ctx context.Context, ref string) (string, error) {
	return f(ctx, ref)
}

func TestSecretResolutionFailsClosedWithoutLeakingResolverErrors(t *testing.T) {
	for _, field := range []string{"auth", "tls", "generic"} {
		for _, failure := range []string{"missing", "unauthorized", "empty"} {
			t.Run(field+"/"+failure, func(t *testing.T) {
				const secret = "private-material-must-not-leak"
				cfg := config.PackageBackendConfig{Type: "nexus", BaseURL: "https://packages.example"}
				switch field {
				case "auth":
					cfg.AuthSecretRef = "opaque-ref"
				case "tls":
					cfg.TLSSecretRef = "opaque-ref"
				case "generic":
					cfg.SecretRefs = map[string]string{"extra": "opaque-ref"}
				}
				backend, err := BuildBackendWithSecrets(context.Background(), cfg, resolverFunc(func(context.Context, string) (string, error) {
					if failure == "empty" {
						return "", nil
					}
					return secret, fmt.Errorf("%s: %s", failure, secret)
				}))
				if backend != nil || err == nil {
					t.Fatalf("backend=%T error=%v", backend, err)
				}
				if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), secret) || (failure != "empty" && errors.Unwrap(err) != nil) {
					t.Fatalf("resolver error was not opaque: %#v", err)
				}
			})
		}
	}
}

func TestAuthPayloadValidation(t *testing.T) {
	for _, payload := range []string{
		"", " ", `{"username":"user"}`, `{"password":"password"}`, `{"token":"token","username":"user","password":"password"}`,
		`{"token":"token"`, `{"token":"token","unknown":"secret"}`, `{"token":"token"} {}`, "bearer\r\nInjected: true",
		`{"username":"user:other","password":"password"}`, `{"token":"bad token"}`, `[]`, `"quoted-token"`,
	} {
		if _, err := parseAuthSecret(payload); err == nil {
			t.Errorf("invalid auth payload accepted: %q", payload)
		}
	}
	for _, payload := range []string{"plain-token", `{"token":"bearer-token"}`, `{"username":"user","password":"password"}`} {
		if _, err := parseAuthSecret(payload); err != nil {
			t.Errorf("valid auth payload: %v", err)
		}
	}
}

func TestAuthenticatedBackendRequestsAndRejections(t *testing.T) {
	for _, typ := range []string{"nexus", "pulp"} {
		for _, basic := range []bool{false, true} {
			for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden} {
				t.Run(fmt.Sprintf("%s/basic=%v/status=%d", typ, basic, status), func(t *testing.T) {
					const user, password, token = "registry-user", "p@ss+word/value", "private-token"
					payload := `{"token":"` + token + `"}`
					if basic {
						payload = `{"username":"` + user + `","password":"` + password + `"}`
					}
					calls := 0
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls++
						if basic {
							u, p, ok := r.BasicAuth()
							if !ok || u != user || p != password {
								t.Error("incorrect basic auth")
							}
						} else if r.Header.Get("Authorization") != "Bearer "+token {
							t.Error("incorrect bearer auth")
						}
						if r.URL.User != nil || strings.Contains(r.URL.String(), token) || strings.Contains(r.URL.String(), password) {
							t.Error("credentials in request URL")
						}
						w.WriteHeader(status)
						if status == http.StatusOK {
							_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
							return
						}
						_, _ = fmt.Fprintf(w, "%s %s %s %s %s", user, password, token, url.QueryEscape(password), base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
					}))
					defer server.Close()
					backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{Type: typ, BaseURL: server.URL, AuthSecretRef: "opaque"}, mapResolver{"opaque": payload})
					if err != nil {
						t.Fatal(err)
					}
					obs, err := backend.ObserveRepository(context.Background(), testRepo("repo"))
					if (err != nil) != (status != http.StatusOK) || calls != 1 {
						t.Fatalf("calls=%d error=%v", calls, err)
					}
					if status != http.StatusOK && obs.Exists {
						t.Fatal("rejection reported success")
					}
					for _, value := range []string{user, password, token, url.QueryEscape(password), base64.StdEncoding.EncodeToString([]byte(user + ":" + password))} {
						if strings.Contains(fmt.Sprintf("%+v %#v", obs, err), value) {
							t.Fatal("credential escaped through observation/error")
						}
					}
				})
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRequesterScrubsTransportErrorsAndRejectsSecretURLs(t *testing.T) {
	const password = "sensitive-password"
	const generic = "sensitive-generic"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport %s %s", base64.StdEncoding.EncodeToString([]byte("user:"+password)), generic)
	})}
	r := packagebackend.NewRequester("https://packages.example", client, packagebackend.AuthConfig{Username: "user", Password: password}, map[string]string{"extra": generic})
	_, err := r.Do(context.Background(), http.MethodGet, "/test", nil, "")
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(fmt.Sprintf("%#v", err), generic) || errors.Unwrap(err) != nil {
		t.Fatalf("unsafe transport error: %#v", err)
	}
	_, err = r.Do(context.Background(), http.MethodGet, "/test?token="+generic, nil, "")
	if err == nil || strings.Contains(err.Error(), generic) {
		t.Fatalf("secret URL accepted: %v", err)
	}
}

func TestAuthenticatedRedirectsAreNotFollowed(t *testing.T) {
	for _, typ := range []string{"nexus", "pulp"} {
		t.Run(typ, func(t *testing.T) {
			destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("redirect leaked a request to another origin") }))
			defer destination.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
			}))
			defer source.Close()
			backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{Type: typ, BaseURL: source.URL, AuthSecretRef: "opaque"}, mapResolver{"opaque": `{"token":"credential"}`})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := backend.ObserveRepository(context.Background(), testRepo("repo")); err == nil {
				t.Fatal("redirect accepted as successful observation")
			}
		})
	}
}

func TestServerSecretsCannotReachStructuredObservationsOrTaskErrors(t *testing.T) {
	const secret = "opaque-private-value"
	ca := makeTLSFixture(t, "task-ca", nil, false, false, false)
	client := makeTLSFixture(t, "task-client", &ca, false, false, false)
	for _, typ := range []string{"nexus", "pulp"} {
		t.Run(typ+"/projection", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if typ == "nexus" {
					_, _ = fmt.Fprintf(w, `{"items":[{"repository":"repo","path":%q}],"continuationToken":null}`, secret)
				} else {
					_, _ = fmt.Fprintf(w, `{"results":[{"relative_path":%q}],"next":null}`, secret)
				}
			}))
			defer server.Close()
			backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{Type: typ, BaseURL: server.URL, PulpEnableCustomMutationAPI: true, SecretRefs: map[string]string{"extra": "opaque"}}, mapResolver{"opaque": secret})
			if err != nil {
				t.Fatal(err)
			}
			items, err := backend.ListArtifacts(context.Background(), testRepo("repo"))
			if err == nil || len(items) != 0 || strings.Contains(fmt.Sprintf("%+v %#v", items, err), secret) {
				t.Fatalf("unsafe projection: %+v %v", items, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"task":"/pulp/api/v3/tasks/11111111-1111-4111-8111-111111111111/"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"state":"failed","error":%q}`, base64.StdEncoding.EncodeToString([]byte(secret))+" "+client.certPEM+" "+client.keyPEM)
	}))
	defer server.Close()
	backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{Type: "pulp", BaseURL: server.URL, PulpEnableCustomMutationAPI: true, AuthSecretRef: "opaque", TLSSecretRef: "tls"}, mapResolver{"opaque": secret, "tls": tlsPayload(t, ca.certPEM, client.certPEM, client.keyPEM)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.StoreArtifact(context.Background(), domain.PackageRepository{Name: "repo"}, packagebackend.StoreArtifactRequest{PackageName: "pkg", Version: "1", Filename: "pkg.tgz", Reader: strings.NewReader("artifact")})
	if err == nil || !strings.Contains(err.Error(), "[REDACTED]") || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(secret))) || strings.Contains(err.Error(), "-----BEGIN") {
		t.Fatalf("unsafe task error: %v", err)
	}
}
