package gitea

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type dependencyRepoClient struct {
	repos      map[string]*RepoInfo
	shas       map[string]string
	resolveErr map[string]error
	resolved   []string
}

func (f *dependencyRepoClient) GetRepo(_ context.Context, owner, name string) (*RepoInfo, error) {
	return f.repos[owner+"/"+name], nil
}

func (f *dependencyRepoClient) ResolveRef(_ context.Context, owner, name, ref string) (string, error) {
	key := owner + "/" + name
	f.resolved = append(f.resolved, key+"@"+ref)
	if err := f.resolveErr[key]; err != nil {
		return "", err
	}
	return f.shas[key], nil
}

func TestBuildDependencyResolverPinsAuthorizedDefaultBranchHeads(t *testing.T) {
	client := &dependencyRepoClient{
		repos: map[string]*RepoInfo{
			"cascadia/cascadia-go": {DefaultBranch: "main"},
			"cascadia/drydock":     {DefaultBranch: "trunk", Private: true},
		},
		shas: map[string]string{
			"cascadia/cascadia-go": strings.Repeat("A", 40),
			"cascadia/drydock":     strings.Repeat("b", 40),
		},
	}
	resolver, err := NewBuildDependencyResolver(client, "https://git.sharegap.net")
	if err != nil {
		t.Fatalf("NewBuildDependencyResolver() error = %v", err)
	}
	pins, err := resolver.Resolve(t.Context(), []BuildDependencySpec{
		{Name: "cascadia-go", CloneURL: "https://git.sharegap.net/cascadia/cascadia-go.git"},
		{Name: "drydock", CloneURL: "https://git.sharegap.net/cascadia/drydock.git"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(pins) != 2 || pins[0].CommitSHA != strings.Repeat("a", 40) || pins[1].CommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("pins = %#v", pins)
	}
	if got := strings.Join(client.resolved, ","); got != "cascadia/cascadia-go@main,cascadia/drydock@trunk" {
		t.Fatalf("resolved refs = %q", got)
	}
}

func TestBuildDependencyResolverFailsClosedWithoutPartialPins(t *testing.T) {
	client := &dependencyRepoClient{
		repos: map[string]*RepoInfo{
			"cascadia/cascadia-go": {DefaultBranch: "main"},
			"cascadia/drydock":     {DefaultBranch: "main"},
		},
		shas:       map[string]string{"cascadia/cascadia-go": strings.Repeat("a", 40)},
		resolveErr: map[string]error{"cascadia/drydock": fmt.Errorf("ref unavailable")},
	}
	resolver, _ := NewBuildDependencyResolver(client, "https://git.sharegap.net")
	pins, err := resolver.Resolve(t.Context(), []BuildDependencySpec{
		{Name: "cascadia-go", CloneURL: "https://git.sharegap.net/cascadia/cascadia-go.git"},
		{Name: "drydock", CloneURL: "https://git.sharegap.net/cascadia/drydock.git"},
	})
	if err == nil || pins != nil {
		t.Fatalf("Resolve() = (%#v, %v), want no partial pins", pins, err)
	}
}

func TestBuildDependencyResolverRejectsUnsafeCloneURLsWithoutEchoingSecrets(t *testing.T) {
	client := &dependencyRepoClient{}
	resolver, _ := NewBuildDependencyResolver(client, "https://git.sharegap.net")
	tests := []string{
		"http://git.sharegap.net/cascadia/drydock.git",
		"ssh://git.sharegap.net/cascadia/drydock.git",
		"https://other.example/cascadia/drydock.git",
		"https://user:super-secret@git.sharegap.net/cascadia/drydock.git",
		"https://git.sharegap.net/cascadia/drydock.git?token=super-secret",
		"https://git.sharegap.net/cascadia/drydock.git#super-secret",
		"https://git.sharegap.net/cascadia/too/many/segments.git",
		"https://git.sharegap.net/cascadia/%2e%2e.git",
	}
	for _, cloneURL := range tests {
		t.Run(cloneURL, func(t *testing.T) {
			pins, err := resolver.Resolve(t.Context(), []BuildDependencySpec{{Name: "drydock", CloneURL: cloneURL}})
			if err == nil || pins != nil {
				t.Fatalf("Resolve() = (%#v, %v), want rejection", pins, err)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("secret reached error: %q", err)
			}
		})
	}
}
