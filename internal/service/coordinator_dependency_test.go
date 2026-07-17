package service

import (
	"context"
	"strings"
	"testing"
)

func TestCoordinatorsFailClosedWhenMandatoryDependenciesAreMissing(t *testing.T) {
	tests := []struct {
		name        string
		dependency  string
		run         func(context.Context) error
		processOnce func(context.Context) error
	}{
		{
			name: "backup run", dependency: "registry",
			run: (&BackupRunCoordinator{}).Run, processOnce: (&BackupRunCoordinator{}).ProcessOnce,
		},
		{
			name: "backup restore", dependency: "registry",
			run: (&BackupRestoreCoordinator{}).Run, processOnce: (&BackupRestoreCoordinator{}).ProcessOnce,
		},
		{
			name: "ML recipe", dependency: "registry",
			run: (&MLRecipeCoordinator{}).Run, processOnce: (&MLRecipeCoordinator{}).ProcessOnce,
		},
		{
			name: "ML inference", dependency: "registry",
			run: (&MLInferenceProvisioningCoordinator{}).Run, processOnce: (&MLInferenceProvisioningCoordinator{}).ProcessOnce,
		},
		{
			name: "LLM provisioning", dependency: "registry",
			run: (&LLMProvisioningCoordinator{}).Run, processOnce: (&LLMProvisioningCoordinator{}).ProcessOnce,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for method, call := range map[string]func(context.Context) error{"Run": tc.run, "ProcessOnce": tc.processOnce} {
				err := call(context.Background())
				if err == nil || !strings.Contains(err.Error(), tc.dependency) {
					t.Fatalf("%s error = %v, want missing %s dependency", method, err, tc.dependency)
				}
			}
		})
	}
}
