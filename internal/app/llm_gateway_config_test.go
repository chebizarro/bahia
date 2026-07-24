package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestLLMGatewayHTTPConfigReadsAuthTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-token")
	if err := os.WriteFile(path, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := llmGatewayHTTPConfig(config.LLMControlplaneConfig{
		Gateways: map[string]config.LLMGatewayEndpointConfig{
			"fleet": {Type: "http", BaseURL: "http://gateway:8790", AuthTokenFile: path},
		},
	})
	if err != nil {
		t.Fatalf("llmGatewayHTTPConfig() error = %v", err)
	}
	if got.Endpoints["fleet"].AuthToken != "secret-from-file" {
		t.Fatalf("resolved token = %q", got.Endpoints["fleet"].AuthToken)
	}
}

func TestLLMGatewayHTTPConfigRejectsUnreadableOrEmptyTokenFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(t *testing.T) string
		want string
	}{
		{name: "missing", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }, want: "read llm.gateways.fleet.auth_token_file"},
		{name: "empty", path: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "empty")
			if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: "is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := llmGatewayHTTPConfig(config.LLMControlplaneConfig{
				Gateways: map[string]config.LLMGatewayEndpointConfig{
					"fleet": {BaseURL: "http://gateway:8790", AuthTokenFile: tc.path(t)},
				},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("llmGatewayHTTPConfig() error = %v, want %q", err, tc.want)
			}
		})
	}
}
