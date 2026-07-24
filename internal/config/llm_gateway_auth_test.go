package config

import (
	"strings"
	"testing"
	"time"
)

func validLLMConfigWithGateway(gateway LLMGatewayEndpointConfig) *Config {
	cfg := Defaults()
	cfg.LLM.Enabled = true
	cfg.LLM.CoordinatorPollInterval = time.Second
	cfg.LLM.StaleRunTimeout = time.Minute
	cfg.LLM.ReconcileInterval = time.Second
	cfg.LLM.DefaultGatewayRef = "fleet"
	cfg.LLM.Gateways = map[string]LLMGatewayEndpointConfig{"fleet": gateway}
	return cfg
}

func TestValidateLLMGatewayAuthTokenFile(t *testing.T) {
	valid := validLLMConfigWithGateway(LLMGatewayEndpointConfig{
		BaseURL:       "http://bahia-litellm-adapter:8790",
		AuthTokenFile: "/run/secrets/bahia-litellm-adapter-token",
	})
	if err := valid.validateLLM(); err != nil {
		t.Fatalf("validateLLM() error = %v", err)
	}

	for _, tc := range []struct {
		name    string
		gateway LLMGatewayEndpointConfig
		want    string
	}{
		{
			name: "both token sources",
			gateway: LLMGatewayEndpointConfig{
				BaseURL: "http://gateway:8790", AuthToken: "inline", AuthTokenFile: "/run/secrets/token",
			},
			want: "only one of auth_token or auth_token_file",
		},
		{
			name:    "relative token file",
			gateway: LLMGatewayEndpointConfig{BaseURL: "http://gateway:8790", AuthTokenFile: "secrets/token"},
			want:    "auth_token_file must be an absolute path",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validLLMConfigWithGateway(tc.gateway).validateLLM()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateLLM() error = %v, want %q", err, tc.want)
			}
		})
	}
}
