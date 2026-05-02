package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandGroupsExposeExpectedSubcommands(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cobra.Command
		want []string
	}{
		{
			name: "deployments",
			cmd:  deployCommands(),
			want: []string{"deploy", "rollback"},
		},
		{
			name: "services",
			cmd:  servicesCommands(),
			want: []string{"list", "get", "create", "actions"},
		},
		{
			name: "service actions",
			cmd:  serviceActionsCommands(),
			want: []string{"deploy", "restart", "stop"},
		},
		{
			name: "adopt",
			cmd:  adoptCommands(),
			want: []string{"scan", "import"},
		},
		{
			name: "environments",
			cmd:  environmentsCommands(),
			want: []string{"list", "get", "create"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range tt.want {
				if findDirectChild(tt.cmd, name) == nil {
					t.Fatalf("%s command missing child %q", tt.cmd.Use, name)
				}
			}
		})
	}
}

func TestParseAdoptionTargets(t *testing.T) {
	targets, err := parseAdoptionTargets([]string{"local", "prod=prod-docker"}, nil, []string{"local=dev", "prod=prod"})
	if err != nil {
		t.Fatalf("parseAdoptionTargets returned error: %v", err)
	}
	if len(targets) != 2 || targets[0].Name != "local" || targets[0].EndpointRef != "local" || targets[0].EnvironmentName != "dev" {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if targets[1].EndpointRef != "prod-docker" {
		t.Fatalf("endpoint_ref not parsed: %#v", targets[1])
	}

	if _, err := parseAdoptionTargets([]string{"local", "local=other"}, nil, nil); err == nil {
		t.Fatal("expected duplicate alias error")
	}
	if _, err := parseAdoptionTargets([]string{"Local", "local=other"}, nil, nil); err == nil {
		t.Fatal("expected normalized duplicate alias error")
	}
	if _, err := parseAdoptionTargets([]string{"local"}, nil, []string{"missing=prod"}); err == nil {
		t.Fatal("expected unmatched environment alias error")
	}
	raw, err := parseAdoptionTargets(nil, []string{"local=unix:///docker.sock"}, nil)
	if err != nil {
		t.Fatalf("parse raw targets returned error: %v", err)
	}
	if len(raw) != 1 || raw[0].DockerHost != "unix:///docker.sock" || raw[0].EndpointRef != "" {
		t.Fatalf("unexpected raw targets: %#v", raw)
	}
}

func TestParseAdoptionSelections(t *testing.T) {
	selections, err := parseAdoptionSelections([]string{"local/abc123", "prod/def456=api-prod"})
	if err != nil {
		t.Fatalf("parseAdoptionSelections returned error: %v", err)
	}
	if len(selections) != 2 || selections[1].TargetName != "prod" || selections[1].ContainerID != "def456" || selections[1].ServiceNameOverride != "api-prod" {
		t.Fatalf("unexpected selections: %#v", selections)
	}
	if _, err := parseAdoptionSelections([]string{"local/abc123", "local/abc123"}); err == nil {
		t.Fatal("expected duplicate selection error")
	}
	if _, err := parseAdoptionSelections([]string{"bad"}); err == nil {
		t.Fatal("expected malformed selection error")
	}
	if _, err := parseAdoptionSelections([]string{"local/abc123=___"}); err == nil {
		t.Fatal("expected invalid service override error")
	}
}

func findDirectChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
