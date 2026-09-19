package loom

import (
	"fmt"
	"strings"
	"testing"
)

func TestHiveCIJobArgsRendersSpecShapedLoomCIInvocation(t *testing.T) {
	run := strings.Repeat("12", 32)
	args, err := HiveCIJobArgs(HiveCIJobSpec{
		Repository: "https://git.fleet.internal/fleet/repository.git",
		Ref:        strings.Repeat("c", 40),
		Workflow:   ".github/workflows/build.yml",
		Event:      "push",
		Actor:      strings.Repeat("e", 64),
		RunEventID: run,
		Dependencies: []BuildDependency{
			{Name: "drydock", CloneURL: "https://git.sharegap.net/cascadia/drydock.git", CommitSHA: strings.Repeat("b", 40)},
			{Name: "cascadia-go", CloneURL: "https://git.sharegap.net/cascadia/cascadia-go.git", CommitSHA: strings.Repeat("a", 40)},
		},
	})
	if err != nil {
		t.Fatalf("HiveCIJobArgs() error = %v", err)
	}
	want := []string{
		"run",
		"--repo", "https://git.fleet.internal/fleet/repository.git",
		"--ref", strings.Repeat("c", 40),
		"--workflow", ".github/workflows/build.yml",
		"--event", "push",
		"--actor", strings.Repeat("e", 64),
		"--run", run,
		"--dep", "cascadia-go=https://git.sharegap.net/cascadia/cascadia-go.git@" + strings.Repeat("a", 40),
		"--dep", "drydock=https://git.sharegap.net/cascadia/drydock.git@" + strings.Repeat("b", 40),
	}
	if fmt.Sprint(args) != fmt.Sprint(want) {
		t.Fatalf("args = %q, want %q", args, want)
	}
}

func TestHiveCIJobArgsFailsClosedOnUnsafeInputs(t *testing.T) {
	cases := map[string]HiveCIJobSpec{
		"credentialed repository": {Repository: "https://user:secret@git.example/org/repo.git"},
		"non-https repository":    {Repository: "git@git.example:org/repo.git"},
		"whitespace ref":          {Repository: "https://git.example/org/repo.git", Ref: "main --evil"},
		"short run id":            {Repository: "https://git.example/org/repo.git", RunEventID: "5401-event-id"},
		"mutable dependency": {Repository: "https://git.example/org/repo.git", Dependencies: []BuildDependency{
			{Name: "dep", CloneURL: "https://git.example/org/dep.git", CommitSHA: "main"},
		}},
		"credentialed dependency": {Repository: "https://git.example/org/repo.git", Dependencies: []BuildDependency{
			{Name: "dep", CloneURL: "https://u:p@git.example/org/dep.git", CommitSHA: strings.Repeat("a", 40)},
		}},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if args, err := HiveCIJobArgs(spec); err == nil {
				t.Fatalf("expected error, got args %q", args)
			}
		})
	}
}
