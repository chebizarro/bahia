package main

import (
	"strings"
	"testing"
)

func TestSoulFactoryCLIListUnavailable(t *testing.T) {
	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"list"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "does not yet have configured Nostr signing/publish/query support") {
		t.Fatalf("Execute() error = %v, want explicit unavailable error", err)
	}
}

func TestSoulFactoryCLIProvisionDoesNotSimulateSuccess(t *testing.T) {
	cmd := soulFactoryCommands()
	cmd.SetArgs([]string{"provision", "agent", "--brief", "do work", "--follow"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot provision soul") {
		t.Fatalf("Execute() error = %v, want explicit unavailable error", err)
	}
}
