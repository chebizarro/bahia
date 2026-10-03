package main

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func runEnvironmentsListNostr(cmd *cobra.Command) error {
	events, err := readNostrEvents(cmd, "environment", kinds.EnvironmentRegistry)
	if err != nil {
		return err
	}
	envs := make([]domain.Environment, 0, len(events))
	for _, ev := range events {
		env, err := client.DecodeEnvironment(ev)
		if err != nil {
			return fmt.Errorf("decode environment event %s: %w", ev.GetID(), err)
		}
		if env != nil {
			envs = append(envs, *env)
		}
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	return renderEnvironments(envs)
}

func runEnvironmentGetNostr(cmd *cobra.Command, id string) error {
	want, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid environment ID %q: %w", id, err)
	}
	events, err := readNostrEvents(cmd, "environment", kinds.EnvironmentRegistry)
	if err != nil {
		return err
	}
	for _, ev := range events {
		env, err := client.DecodeEnvironmentDetails(ev)
		if err != nil {
			return fmt.Errorf("decode environment event %s: %w", ev.GetID(), err)
		}
		if env != nil && env.ID == want {
			return outputSingle(env)
		}
	}
	return fmt.Errorf("environment %s not found", id)
}

func renderEnvironments(envs []domain.Environment) error {
	return output(envs, []string{"ID", "NAME", "STRATEGY", "PROTECTED"}, func(e domain.Environment) []string {
		protected := ""
		if e.Protected {
			protected = "yes"
		}
		return []string{e.ID.String(), e.Name, string(e.DeployStrategy), protected}
	})
}
