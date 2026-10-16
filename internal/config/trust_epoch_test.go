package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrustEpochConfigurationRequiresIndependentOperatorRoot(t *testing.T) {
	service := strings.Repeat("a", 64)
	root := strings.Repeat("b", 64)
	for _, test := range []struct {
		name  string
		cfg   NostrConfig
		valid bool
	}{
		{"legacy", NostrConfig{PublicKey: service}, true},
		{"configured", NostrConfig{PublicKey: service, ServiceID: "deployment-a", OperatorRootPubkey: root}, true},
		{"missing root", NostrConfig{PublicKey: service, ServiceID: "deployment-a"}, false},
		{"missing legacy identity", NostrConfig{ServiceID: "deployment-a", OperatorRootPubkey: root}, false},
		{"missing service id", NostrConfig{PublicKey: service, OperatorRootPubkey: root}, false},
		{"service root", NostrConfig{PublicKey: service, ServiceID: "deployment-a", OperatorRootPubkey: service}, false},
		{"pubkey as service id", NostrConfig{PublicKey: service, ServiceID: "key:" + service, OperatorRootPubkey: root}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.validateServiceSigner()
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
