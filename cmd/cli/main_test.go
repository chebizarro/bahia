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
			want: []string{"list", "get", "create"},
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

func findDirectChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}
