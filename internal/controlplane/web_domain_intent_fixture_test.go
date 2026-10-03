package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type webIntentFixtureRequest struct {
	Domain     string         `json:"domain"`
	Op         string         `json:"op"`
	Coordinate string         `json:"coordinate"`
	OrgID      string         `json:"orgId"`
	IntentID   string         `json:"intentId"`
	CreatedAt  int64          `json:"createdAt"`
	Pubkey     string         `json:"pubkey"`
	Content    map[string]any `json:"content"`
}

type webIntentFixtureCase struct {
	Name    string                  `json:"name"`
	Request webIntentFixtureRequest `json:"request"`
	Event   map[string]any          `json:"event"`
}

func TestWebDomainIntentFixtures(t *testing.T) {
	const orgID = "3b45458b-2724-4dda-9fc6-66f12249660d"
	const fleetOrgID = "f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7"
	const routeID = "018f1fae-7b91-7bea-81d6-0669758de941"
	const releaseID = "018f1fae-7b91-7bea-81d6-0669758de942"
	const repoID = "018f1fae-7b91-7bea-81d6-0669758de943"
	const recipeID = "018f1fae-7b91-7bea-81d6-0669758de944"
	const targetRepoID = "018f1fae-7b91-7bea-81d6-0669758de945"
	pubkey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fixtures := map[string][]webIntentFixtureCase{
		"llm": {
			{Name: "route-create", Request: webIntentFixtureRequest{Domain: "llm", Op: "create", Coordinate: routeID, OrgID: orgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de950", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"id": routeID, "name": "chat", "gateway_config": map[string]any{"public_model": "bahia/chat"}}}},
			{Name: "release-register", Request: webIntentFixtureRequest{Domain: "llm", Op: "release-register", Coordinate: "llm-release:" + releaseID, OrgID: orgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de951", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"id": releaseID, "route_id": routeID, "version": "v1", "model_ref": "hf://example/chat", "model_source": "huggingface", "backend_preferences": []string{"external_api"}, "external_backend": map[string]any{"base_url": "https://llm.example"}}}}},
		"backup": {
			{Name: "repository-register", Request: webIntentFixtureRequest{Domain: "backup", Op: "repository-register", Coordinate: "backup-repository:" + repoID, OrgID: fleetOrgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de952", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"id": repoID, "name": "archive", "backend": "kopia", "repository_uri": "kopia://archive", "metadata": map[string]any{"source": "web.backup.repositories.register"}}}},
			{Name: "run", Request: webIntentFixtureRequest{Domain: "backup", Op: "run", Coordinate: "backup-run:018f1fae-7b91-7bea-81d6-0669758de946", OrgID: fleetOrgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de953", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"id": "018f1fae-7b91-7bea-81d6-0669758de946", "recipe_id": recipeID, "repository_id": repoID, "backend": "kopia", "target_ref": "fs:/srv/app", "verification_mode": "none", "metadata": map[string]any{"source": "web.backup.run"}}}}},
		"package": {
			{Name: "promote", Request: webIntentFixtureRequest{Domain: "package", Op: "promote", Coordinate: "package:" + targetRepoID + ":acme:api:v1:api.tgz", OrgID: fleetOrgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de954", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"source_repository_id": repoID, "target_repository_id": targetRepoID, "namespace": "acme", "package_name": "api", "version": "v1", "filename": "api.tgz"}}},
			{Name: "yank", Request: webIntentFixtureRequest{Domain: "package", Op: "yank", Coordinate: "package:" + repoID + ":acme:api:v1:api.tgz", OrgID: fleetOrgID, IntentID: "018f1fae-7b91-7bea-81d6-0669758de955", CreatedAt: 1720000000, Pubkey: pubkey,
				Content: map[string]any{"repository_id": repoID, "namespace": "acme", "package_name": "api", "version": "v1", "filename": "api.tgz", "reason": "superseded", "deprecated": true}}}},
	}

	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "web", "tests", "fixtures")
	for family, cases := range fixtures {
		for i := range cases {
			request := cases[i].Request
			content, err := json.Marshal(request.Content)
			if err != nil {
				t.Fatal(err)
			}
			tags := nostr.Tags{{"d", request.Coordinate}, {"domain", request.Domain}, {"schema", "bahia.intent." + request.Domain + ".v1"},
				{"t", "bahia-intent"}, {"t", request.Domain}, {"op", request.Op}, {"org", request.OrgID}, {"intent_id", request.IntentID}}
			event := &nostr.Event{Kind: 30900, CreatedAt: nostr.Timestamp(request.CreatedAt), Tags: tags, Content: string(content)}
			intent, err := ParseIntent(event)
			if err != nil {
				t.Fatalf("%s: ParseIntent: %v", cases[i].Name, err)
			}
			if intent.Schema != "bahia.intent."+family+".v1" {
				t.Fatalf("%s: schema %q", cases[i].Name, intent.Schema)
			}
			switch cases[i].Name {
			case "route-create":
				route, err := llmRouteFromIntentContent(intent)
				if err != nil {
					t.Fatal(err)
				}
				if err := domain.ValidateLLMRouteName(route.Name); err != nil {
					t.Fatal(err)
				}
			case "release-register":
				release, err := llmReleaseFromIntentContent(intent)
				if err != nil {
					t.Fatal(err)
				}
				if release.ID.String() != releaseID || release.RouteID.String() != routeID {
					t.Fatalf("release or route ID lost")
				}
				if err := domain.ValidateLLMReleaseConfig(release); err != nil {
					t.Fatal(err)
				}
			case "repository-register":
				repo, err := backupRepositoryFromIntentContent(intent)
				if err != nil {
					t.Fatal(err)
				}
				if err := domain.ValidateBackupRepository(repo); err != nil {
					t.Fatal(err)
				}
			case "run":
				run, err := backupRunFromIntentContent(intent)
				if err != nil {
					t.Fatal(err)
				}
				run.RequestedBy, run.RequestEventID, run.RequestKind, run.RequestDTag = pubkey, pubkey, 30900, intent.Coordinate
				if err := domain.ValidateBackupRun(run); err != nil {
					t.Fatal(err)
				}
			case "promote":
				cmd, err := packagePromoteCmdFromContent(intent.Content)
				if err != nil {
					t.Fatal(err)
				}
				if cmd.SourceRepositoryID.String() != repoID || cmd.TargetRepositoryID.String() != targetRepoID {
					t.Fatalf("promote repository IDs lost")
				}
				if intent.Coordinate != "package:"+cmd.TargetRepositoryID.String()+":"+cmd.Namespace+":"+cmd.PackageName+":"+cmd.Version+":"+cmd.Filename {
					t.Fatalf("promote d-tag must address the target artifact: %q", intent.Coordinate)
				}
			case "yank":
				cmd, err := packageYankCmdFromContent(intent.Content)
				if err != nil {
					t.Fatal(err)
				}
				if cmd.RepositoryID.String() != repoID || !cmd.Deprecated {
					t.Fatalf("yank repository or deprecation lost")
				}
				if intent.Coordinate != "package:"+cmd.RepositoryID.String()+":"+cmd.Namespace+":"+cmd.PackageName+":"+cmd.Version+":"+cmd.Filename {
					t.Fatalf("yank d-tag must address the artifact: %q", intent.Coordinate)
				}
			}
			cases[i].Event = map[string]any{"kind": 30900, "created_at": request.CreatedAt, "pubkey": pubkey, "tags": tags, "content": string(content)}
		}
		fixtureJSON, err := json.MarshalIndent(struct {
			Cases []webIntentFixtureCase `json:"cases"`
		}{cases}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, family+"-intents.json")
		if os.Getenv("BAHIA_REGEN_WEB_INTENT_FIXTURES") == "1" {
			if err := os.WriteFile(path, append(fixtureJSON, '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(committed) != string(append(fixtureJSON, '\n')) {
			t.Fatalf("%s fixture drift: regenerate with BAHIA_REGEN_WEB_INTENT_FIXTURES=1 go test -run TestWebDomainIntentFixtures ./internal/controlplane/", family)
		}
	}
}
