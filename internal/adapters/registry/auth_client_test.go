package registry

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestRegistryAuthClientsUseBoundedTLSSafeHTTP(t *testing.T) {
	tests := []struct {
		name   string
		client *http.Client
	}{
		{name: "dockerhub", client: NewDockerHubAuth("", "").httpClient},
		{name: "ghcr", client: NewGHCRAuth("").httpClient},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.client.Timeout <= 0 {
				t.Fatalf("timeout = %s, want positive timeout", test.client.Timeout)
			}
			transport, ok := test.client.Transport.(*http.Transport)
			if !ok || transport.TLSClientConfig == nil {
				t.Fatalf("transport = %#v, want TLS-configured *http.Transport", test.client.Transport)
			}
			if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
				t.Fatalf("TLS minimum = %d, want TLS 1.2 or newer", transport.TLSClientConfig.MinVersion)
			}
			if transport.TLSClientConfig.InsecureSkipVerify {
				t.Fatal("TLS certificate verification must not be disabled")
			}
		})
	}
}

func TestRegistryAuthClientHardeningOverridesUnsafeTLS(t *testing.T) {
	unsafe := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec -- proves hardening removes the unsafe test input
	auth := newGHCRAuth("", unsafe)
	transport := auth.httpClient.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("TLS certificate verification remains disabled")
	}
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("TLS minimum = %d, want TLS 1.2 or newer", transport.TLSClientConfig.MinVersion)
	}
}
