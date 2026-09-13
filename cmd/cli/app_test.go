package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestAppOnboardCommandRegistered(t *testing.T) {
	root := newRootCommand()
	appCmd, _, err := root.Find([]string{"app"})
	if err != nil {
		t.Fatalf("find app command: %v", err)
	}
	if appCmd == nil {
		t.Fatal("app command not found")
	}
	onboardCmd, _, err := root.Find([]string{"app", "onboard"})
	if err != nil {
		t.Fatalf("find app onboard command: %v", err)
	}
	if onboardCmd == nil {
		t.Fatal("app onboard command not found")
	}
	if onboardCmd.Use != "onboard" {
		t.Fatalf("expected Use='onboard', got %q", onboardCmd.Use)
	}
}

func TestAppOnboardRequiredFlags(t *testing.T) {
	onboardCmd := appCommands()
	cmd := &cobra.Command{}
	cmd.AddCommand(onboardCmd)
	cmd.SetArgs([]string{"app", "onboard"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error with missing required flags")
	}
	if !strings.Contains(err.Error(), "required") && !strings.Contains(err.Error(), "name") && !strings.Contains(err.Error(), "artifact") && !strings.Contains(err.Error(), "environment") {
		t.Fatalf("error does not mention required flags: %v", err)
	}
}

func TestAppOnboardFlagDefaults(t *testing.T) {
	cmd := appCommands()
	onboardCmd := cmd.Commands()[0]
	onboardSub, _, _ := onboardCmd.Find([]string{"onboard"})
	if onboardSub == nil {
		t.Fatal("onboard subcommand not found")
	}

	defaultBranch := onboardSub.Flags().Lookup("default-branch")
	if defaultBranch == nil {
		t.Fatal("default-branch flag not found")
	}
	if defaultBranch.DefValue != "main" {
		t.Fatalf("expected default-branch default 'main', got %q", defaultBranch.DefValue)
	}

	runtimeType := onboardSub.Flags().Lookup("runtime-type")
	if runtimeType == nil {
		t.Fatal("runtime-type flag not found")
	}
	if runtimeType.DefValue != "compose" {
		t.Fatalf("expected runtime-type default 'compose', got %q", runtimeType.DefValue)
	}

	strategy := onboardSub.Flags().Lookup("strategy")
	if strategy == nil {
		t.Fatal("strategy flag not found")
	}
	if strategy.DefValue != "replace" {
		t.Fatalf("expected strategy default 'replace', got %q", strategy.DefValue)
	}
}

func TestAppOnboardOptionalPolicyFlag(t *testing.T) {
	cmd := appCommands()
	onboardCmd := cmd.Commands()[0]
	onboardSub, _, _ := onboardCmd.Find([]string{"onboard"})
	if onboardSub == nil {
		t.Fatal("onboard subcommand not found")
	}
	policyFlag := onboardSub.Flags().Lookup("policy")
	if policyFlag == nil {
		t.Fatal("policy flag not found")
	}
	if policyFlag.DefValue != "" {
		t.Fatalf("expected policy default empty, got %q", policyFlag.DefValue)
	}
}

func TestAppOnboardIdempotencyKeyFlag(t *testing.T) {
	cmd := appCommands()
	onboardCmd := cmd.Commands()[0]
	onboardSub, _, _ := onboardCmd.Find([]string{"onboard"})
	if onboardSub == nil {
		t.Fatal("onboard subcommand not found")
	}
	keyFlag := onboardSub.Flags().Lookup("idempotency-key")
	if keyFlag == nil {
		t.Fatal("idempotency-key flag not found")
	}
	if keyFlag.DefValue != "" {
		t.Fatalf("expected idempotency-key default empty, got %q", keyFlag.DefValue)
	}
}
