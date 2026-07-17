package packagebackend

import (
	"strings"
	"testing"
)

func TestValidateEndpointRequiresTLSForRemoteBackends(t *testing.T) {
	if _, err := ValidateEndpoint("http://nexus.example.com", "backend url"); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("expected remote plaintext rejection, got %v", err)
	}
	if got, err := ValidateEndpoint("http://127.0.0.1:8081/", "backend url"); err != nil || got != "http://127.0.0.1:8081" {
		t.Fatalf("loopback endpoint = %q, %v", got, err)
	}
	if _, err := ValidateEndpoint("https://user:secret@nexus.example.com?debug=true", "backend url"); err == nil {
		t.Fatal("expected embedded credentials/query rejection")
	}
}

func TestAuthConfigValidateRejectsPartialOrAmbiguousCredentials(t *testing.T) {
	cases := []AuthConfig{
		{Username: "admin"},
		{Password: "secret"},
		{Username: "admin", Password: "secret", BearerToken: "token"},
	}
	for _, auth := range cases {
		if err := auth.Validate(); err == nil {
			t.Fatalf("Validate(%#v) unexpectedly succeeded", auth)
		}
	}
	if err := (AuthConfig{Username: "admin", Password: "secret"}).Validate(); err != nil {
		t.Fatalf("valid basic auth rejected: %v", err)
	}
	if err := (AuthConfig{BearerToken: "token"}).Validate(); err != nil {
		t.Fatalf("valid bearer auth rejected: %v", err)
	}
}
