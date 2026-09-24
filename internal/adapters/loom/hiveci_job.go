package loom

import (
	"fmt"
	"sort"
	"strings"

	"fiatjaf.com/nostr"
)

// LoomCICommand is the loom-protocol executable that Hive-CI-capable
// loom-workers advertise as kind-10100 `S` software. A Hive-CI job is a
// spec-shaped kind-5100 request: cmd=loom-ci plus args/env/secret tags. It
// replaces the retired non-spec method/repo/ref/run/workflow/event/actor/dep
// tag selector.
const LoomCICommand = "loom-ci"

// HiveCIPublisherSecretKey is the secret tag that delivers the per-run
// ephemeral kind-5401 publisher key so the worker signs the kind-5402 result
// with it (hive-ci-protocol). It never reaches the workflow environment.
const HiveCIPublisherSecretKey = "HIVE_CI_NSEC"

// HiveCIJobSpec is everything loom-ci needs to run one workflow attempt.
type HiveCIJobSpec struct {
	Repository   string // credential-free HTTPS clone URL
	Ref          string // immutable commit SHA or branch
	Workflow     string // workflow file path inside the repository
	Event        string // act event (push, workflow_dispatch, ...)
	Actor        string // pubkey recorded as the act actor
	RunEventID   string // kind-5401 workflow run event id
	Dependencies []BuildDependency
}

// HiveCIJobArgs renders the spec argv `run --repo ... [--dep name=url@sha]`
// consumed by loom-ci. Dependencies are validated and sorted exactly as the
// retired dep tags were, so provenance stays deterministic.
func HiveCIJobArgs(spec HiveCIJobSpec) ([]string, error) {
	repository := strings.TrimSpace(spec.Repository)
	if !isCredentialFreeHTTPSCloneURL(repository) {
		return nil, fmt.Errorf("hive-ci repository must be a credential-free absolute HTTPS clone URL")
	}
	args := []string{"run", "--repo", repository}
	if ref := strings.TrimSpace(spec.Ref); ref != "" {
		if strings.ContainsAny(ref, " \t\r\n") {
			return nil, fmt.Errorf("hive-ci ref must not contain whitespace")
		}
		args = append(args, "--ref", ref)
	}
	if workflow := strings.TrimSpace(spec.Workflow); workflow != "" {
		args = append(args, "--workflow", workflow)
	}
	if event := strings.TrimSpace(spec.Event); event != "" {
		args = append(args, "--event", event)
	}
	if actor := strings.TrimSpace(spec.Actor); actor != "" {
		args = append(args, "--actor", actor)
	}
	if run := strings.TrimSpace(spec.RunEventID); run != "" {
		if _, err := nostr.IDFromHex(run); err != nil {
			return nil, fmt.Errorf("hive-ci run event id must be a 64-hex event id: %w", err)
		}
		args = append(args, "--run", run)
	}
	dependencies, err := validatedBuildDependencies(spec.Dependencies)
	if err != nil {
		return nil, err
	}
	for _, dependency := range dependencies {
		args = append(args, "--dep", dependency.Name+"="+dependency.CloneURL+"@"+dependency.CommitSHA)
	}
	return args, nil
}

// validatedBuildDependencies returns the dependencies sorted by name with
// every field validated: safe name, credential-free HTTPS URL, immutable
// 40-hex commit SHA.
func validatedBuildDependencies(dependencies []BuildDependency) ([]BuildDependency, error) {
	ordered := append([]BuildDependency(nil), dependencies...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	seen := make(map[string]struct{}, len(ordered))
	for index := range ordered {
		name := strings.TrimSpace(ordered[index].Name)
		if !buildDependencyNamePattern.MatchString(name) {
			return nil, fmt.Errorf("loom job build dependency %d has an invalid name", index)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("loom job build dependency %d duplicates a name", index)
		}
		seen[name] = struct{}{}
		cloneURL := strings.TrimSpace(ordered[index].CloneURL)
		if !isCredentialFreeHTTPSCloneURL(cloneURL) {
			return nil, fmt.Errorf("loom job build dependency %d URL must be credential-free absolute HTTPS", index)
		}
		sha := strings.TrimSpace(ordered[index].CommitSHA)
		if !isLowerFullCommitSHA(sha) {
			return nil, fmt.Errorf("loom job build dependency %d must use an immutable 40-hex commit SHA", index)
		}
		ordered[index] = BuildDependency{Name: name, CloneURL: cloneURL, CommitSHA: sha}
	}
	return ordered, nil
}
