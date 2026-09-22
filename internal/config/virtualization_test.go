package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestVirtualizationConfigurationLoadAndFailClosed(t *testing.T) {
	require.NoError(t, VirtualizationConfig{}.Validate())
	org, host, trust, pool := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	key := "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	file := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte(fmt.Sprintf(`dev_mode: true
virtualization:
  operator_pubkeys: ["%s"]
  reconcile_pubkey: "%s"
  hosts:
    - org_id: "%s"
      host_id: "%s"
      trust_policy_ref: "%s"
      trusted_signers: ["%s"]
  persistent_vm:
    enabled: true
    host_id: "%s"
    storage_pool_ref: "%s"
    state_dir: /var/lib/bahia/persistent
    image_root: /var/lib/bahia/images
    libvirt_uri: qemu:///system
    event_socket: /run/libvirt/libvirt-sock
`, key, key, org, host, trust, key, host, pool)), 0600))
	cfg, err := Load(file)
	require.NoError(t, err)
	require.Equal(t, host, cfg.Virtualization.PersistentVM.HostID)
	require.Equal(t, org, cfg.Virtualization.Hosts[0].OrgID)
	for _, change := range []func(*VirtualizationConfig){
		func(c *VirtualizationConfig) { c.OperatorPubkeys = nil },
		func(c *VirtualizationConfig) { c.Hosts = nil },
		func(c *VirtualizationConfig) { c.ReconcilePubkey = "" },
		func(c *VirtualizationConfig) { c.PersistentVM.ImageRoot = "relative" },
		func(c *VirtualizationConfig) {
			c.PlaneEndpoints = []ExecutionPlaneEndpointConfig{{HostID: uuid.New(), EndpointRef: uuid.New(), Author: key}}
		},
	} {
		c := cfg.Virtualization
		change(&c)
		require.Error(t, c.Validate())
	}
}
