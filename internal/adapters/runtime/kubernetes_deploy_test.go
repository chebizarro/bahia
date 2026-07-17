package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestKubernetesDeployFailsWhenRequiredApplyStepFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		failedStep  string
		wantError   string
		environment map[string]string
	}{
		{name: "label", failedStep: " label deployment ", wantError: "applying bahia label"},
		{name: "environment", failedStep: " set env ", wantError: "setting deployment environment", environment: map[string]string{"DATABASE_URL": "postgres://db"}},
		{name: "rollout", failedStep: " rollout restart ", wantError: "restarting deployment rollout"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runtime := NewKubernetesRuntime("", "default", "", zap.NewNop())
			runtime.execCmd = func(_ context.Context, args []string, _ []byte) (string, error) {
				command := " " + strings.Join(args, " ") + " "
				if strings.Contains(command, tt.failedStep) {
					return "", errors.New("injected kubectl failure")
				}
				if strings.Contains(command, " get deployment ") {
					return "", nil
				}
				return "ok", nil
			}

			err := runtime.Deploy(context.Background(), "api", "example.com/api:v1", DeployOptions{Environment: tt.environment})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Deploy error = %v, want error containing %q", err, tt.wantError)
			}
		})
	}
}
