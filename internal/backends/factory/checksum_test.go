package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/backends/packagebackend"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

const checksumRepoHref = "/pulp/api/v3/repositories/file/file/11111111-1111-4111-8111-111111111111/"
const checksumVersionHref = checksumRepoHref + "versions/3/"

func checksumPayload(typ, digest, scenario string) string {
	if scenario == "missing envelope" {
		return `{}`
	}
	if scenario == "null envelope" {
		return `null`
	}
	if scenario == "invalid JSON" {
		return `{`
	}
	if scenario == "missing pagination" {
		if typ == "nexus" {
			return `{"items":[]}`
		}
		return `{"count":0,"results":[]}`
	}
	var payload string
	if typ == "nexus" {
		item := `{"repository":"repo","path":"pkg.tgz","checksum":{"sha256":"` + digest + `"},"downloadUrl":"https://private-token@evil.example/?secret=private-token"}`
		items := item
		if scenario == "absent" {
			items = ""
		}
		if scenario == "ambiguous" {
			items = item + "," + item
		}
		if scenario == "wrong repository" {
			items = strings.ReplaceAll(item, `"repository":"repo"`, `"repository":"other"`)
		}
		payload = `{"items":[` + items + `],"continuationToken":null}`
	} else {
		item := `{"relative_path":"pkg.tgz","sha256":"` + digest + `"}`
		items, count := item, 1
		if scenario == "absent" {
			items = ""
			count = 0
		}
		if scenario == "ambiguous" {
			items = item + "," + item
			count = 2
		}
		if scenario == "wrong repository" {
			items = strings.ReplaceAll(item, `"pkg.tgz"`, `"other.tgz"`)
		}
		payload = fmt.Sprintf(`{"count":%d,"next":null,"results":[%s]}`, count, items)
	}
	if scenario == "trailing JSON" {
		payload += ` {}`
	}
	return payload
}

func TestChecksumEvidenceFlowsThroughPackageDriftComparison(t *testing.T) {
	expected := strings.Repeat("a", 64)
	for _, typ := range []string{"nexus", "pulp"} {
		for _, scenario := range []string{"matching", "mismatching", "absent", "missing checksum", "short checksum", "nonhex checksum", "whitespace checksum", "missing envelope", "null envelope", "invalid JSON", "trailing JSON", "missing pagination", "ambiguous", "wrong repository", "401", "403", "404", "500"} {
			t.Run(typ+"/"+scenario, func(t *testing.T) {
				digest := expected
				switch scenario {
				case "mismatching":
					digest = strings.Repeat("B", 64)
				case "missing checksum":
					digest = ""
				case "short checksum":
					digest = "ab"
				case "nonhex checksum":
					digest = strings.Repeat("z", 64)
				case "whitespace checksum":
					digest = " " + expected
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer private-token" {
						t.Error("checksum request was not authenticated")
					}
					if strings.Contains(r.URL.String(), expected) {
						t.Error("lookup filtered by expected hash and could hide drift")
					}
					if typ == "pulp" && r.URL.Path == "/pulp/api/v3/repositories/file/file/" {
						if r.URL.Query().Get("name") != "repo" {
							t.Error("unscoped repository query")
						}
						_, _ = fmt.Fprintf(w, `{"count":1,"next":null,"results":[{"name":"repo","pulp_href":%q,"latest_version_href":%q}]}`, checksumRepoHref, checksumVersionHref)
						return
					}
					if typ == "pulp" {
						if r.URL.Path != "/pulp/api/v3/content/file/files/" || r.URL.Query().Get("repository_version") != checksumVersionHref || r.URL.Query().Get("relative_path") != "pkg.tgz" {
							t.Error("checksum query not scoped to exact path and immutable repository version")
						}
					} else if r.URL.Path != "/service/rest/v1/search/assets" || r.URL.Query().Get("repository") != "repo" || r.URL.Query().Get("name") != "pkg.tgz" {
						t.Error("invalid Nexus checksum query")
					}
					switch scenario {
					case "401":
						w.WriteHeader(401)
					case "403":
						w.WriteHeader(403)
					case "404":
						w.WriteHeader(404)
					case "500":
						w.WriteHeader(500)
					}
					_, _ = w.Write([]byte(checksumPayload(typ, digest, scenario)))
				}))
				defer server.Close()
				backend, err := BuildBackendWithSecrets(context.Background(), config.PackageBackendConfig{Type: typ, BaseURL: server.URL, NexusAPIVersion: "v1", PulpAPIVersion: "v3", AuthSecretRef: "opaque"}, mapResolver{"opaque": "private-token"})
				if err != nil {
					t.Fatal(err)
				}
				if !backend.Capabilities().CanObserveDrift {
					t.Fatal("supported API did not advertise checksums")
				}
				svc, err := service.NewPackageRegistryService(config.PackageControlplaneConfig{}, packagebackend.Registry{"primary": backend}, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				repo := domain.PackageRepository{Name: "repo", BackendRef: "primary"}
				artifact := domain.PackageArtifact{BackendPath: "pkg.tgz", SHA256: expected, Status: domain.PackageArtifactStatusAvailable}
				obs, err := svc.ObserveArtifactDrift(context.Background(), &repo, &artifact)
				wantError := scenario != "matching" && scenario != "mismatching" && scenario != "absent"
				if wantError {
					if err == nil || obs != nil {
						t.Fatalf("malformed/unsupported response produced observation=%+v error=%v", obs, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if obs.Drifted != (scenario != "matching") {
					t.Fatalf("wrong drift result: %+v", obs)
				}
				if scenario == "mismatching" && (!strings.Contains(obs.Reason, "expected="+expected) || !strings.Contains(obs.Reason, "observed="+strings.ToLower(digest))) {
					t.Fatalf("missing verified digest evidence: %+v", obs)
				}
				backendObs, err := backend.ObserveArtifact(context.Background(), repo, artifact)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(backendObs)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "evil.example") {
					t.Fatal("credential-bearing backend URL reached a persistable observation")
				}
			})
		}
	}
}

func TestDriftAPIVersionGatesAndExpectedDigestValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid or unsupported verification must not make requests")
	}))
	defer server.Close()
	for _, typ := range []string{"nexus", "pulp"} {
		for _, version := range []string{"", "v0", "v2", "v99", "auto", "v1", "v3"} {
			t.Run(typ+"/"+version, func(t *testing.T) {
				backend, err := BuildBackend(config.PackageBackendConfig{Type: typ, BaseURL: server.URL, NexusAPIVersion: version, PulpAPIVersion: version})
				if err != nil {
					t.Fatal(err)
				}
				wantCapability := (typ == "nexus" && version == "v1") || (typ == "pulp" && version == "v3")
				if backend.Capabilities().CanObserveDrift != wantCapability {
					t.Fatal("incorrect checksum capability")
				}
				svc, err := service.NewPackageRegistryService(config.PackageControlplaneConfig{}, packagebackend.Registry{"primary": backend}, nil, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				artifact := domain.PackageArtifact{BackendPath: "pkg.tgz", SHA256: strings.Repeat("a", 64), Status: domain.PackageArtifactStatusAvailable}
				if wantCapability {
					artifact.SHA256 = ""
				}
				obs, err := svc.ObserveArtifactDrift(context.Background(), &domain.PackageRepository{Name: "repo", BackendRef: "primary"}, &artifact)
				if err == nil || obs != nil {
					t.Fatalf("unverifiable state reported no drift: %+v %v", obs, err)
				}
			})
		}
	}
}

func TestPulpChecksumRepositoryLookupFailsClosed(t *testing.T) {
	for _, payload := range []string{
		`{}`, `{"count":0}`, `{"count":1,"results":[]}`, `{"count":-1,"results":[]}`,
		`{"count":0,"results":[],"next":"/pulp/api/v3/repositories/file/file/?page=2"}`,
		fmt.Sprintf(`{"count":1,"results":[{"name":"other","pulp_href":%q,"latest_version_href":%q}]}`, checksumRepoHref, checksumVersionHref),
		fmt.Sprintf(`{"count":1,"results":[{"name":"repo","pulp_href":%q}]}`, checksumRepoHref),
		fmt.Sprintf(`{"count":1,"results":[{"name":"repo","pulp_href":%q,"latest_version_href":"https://evil.example/"}]}`, checksumRepoHref),
	} {
		t.Run(payload, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(payload)) }))
			defer server.Close()
			backend, err := BuildBackend(config.PackageBackendConfig{Type: "pulp", BaseURL: server.URL, PulpAPIVersion: "v3"})
			if err != nil {
				t.Fatal(err)
			}
			obs, err := backend.ObserveArtifact(context.Background(), testRepo("repo"), domain.PackageArtifact{BackendPath: "pkg.tgz", SHA256: strings.Repeat("a", 64)})
			if err == nil || obs.Exists || obs.SHA256 != "" {
				t.Fatalf("unsafe repository lookup accepted: %+v %v", obs, err)
			}
		})
	}
}
