package version

import "testing"

func TestSemanticDefaultsToInitialVersionWithDevCommit(t *testing.T) {
	withVersionVars(t, "0.1.0", "dev", "")

	if got, want := Semantic(), "0.1.0-dev"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestSemanticUsesCommitHashPreRelease(t *testing.T) {
	withVersionVars(t, "0.1.0", "abcdef1234567890", "")

	if got, want := Semantic(), "0.1.0-abcdef1234567890"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestSemanticAllowsFullBuildOverride(t *testing.T) {
	withVersionVars(t, "0.1.0", "abcdef", "0.1.0-release.1")

	if got, want := Semantic(), "0.1.0-release.1"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestSemanticFallsBackForEmptyCommit(t *testing.T) {
	withVersionVars(t, "0.1.0", "", "")

	if got, want := Semantic(), "0.1.0-dev"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestSemanticPrefixesNumericCommitIdentifierWithLeadingZero(t *testing.T) {
	withVersionVars(t, "0.1.0", "012345", "")

	if got, want := Semantic(), "0.1.0-g012345"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestSemanticSanitizesCommitIdentifier(t *testing.T) {
	withVersionVars(t, "0.1.0", "sha256:abc/def", "")

	if got, want := Semantic(), "0.1.0-abc.def"; got != want {
		t.Fatalf("Semantic() = %q, want %q", got, want)
	}
}

func TestComponentsIncludeSeparatelyPackagedArtifacts(t *testing.T) {
	withVersionVars(t, "0.1.0", "abcdef1234567890", "")

	components := Components()
	wantIDs := map[string]string{
		"backend":                      "cmd/server",
		"cli":                          "cmd/cli",
		"relay":                        "cmd/relay",
		"fips-bahia-bridge":            "cmd/fips-bahia-bridge",
		"openclaw-soulfactory-sidecar": "cmd/openclaw-soulfactory-sidecar",
	}
	seen := make(map[string]Component, len(components))
	for _, component := range components {
		seen[component.ID] = component
	}
	for id, packagedAs := range wantIDs {
		component, ok := seen[id]
		if !ok {
			t.Fatalf("component %q missing from %#v", id, components)
		}
		if component.PackagedAs != packagedAs {
			t.Fatalf("component %q packaged_as = %q, want %q", id, component.PackagedAs, packagedAs)
		}
		if component.Version != "0.1.0-abcdef1234567890" || component.Commit != "abcdef1234567890" {
			t.Fatalf("component %q has version metadata %#v", id, component)
		}
	}
}

func withVersionVars(t *testing.T, base, commit, full string) {
	t.Helper()
	oldBase, oldCommit, oldFull := Base, Commit, Full
	Base, Commit, Full = base, commit, full
	t.Cleanup(func() { Base, Commit, Full = oldBase, oldCommit, oldFull })
}
