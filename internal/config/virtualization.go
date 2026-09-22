package config

import (
	"fmt"
	"path/filepath"
	"slices"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

// VirtualizationConfig is installation authority, not caller-supplied desired state.
// Zero configuration leaves both mutation surfaces unavailable.
type VirtualizationConfig struct {
	OperatorPubkeys []string                       `koanf:"operator_pubkeys"`
	ReconcilePubkey string                         `koanf:"reconcile_pubkey"`
	Hosts           []VirtualizationHostPolicy     `koanf:"hosts"`
	PersistentVM    PersistentVMConfig             `koanf:"persistent_vm"`
	PlaneEndpoints  []ExecutionPlaneEndpointConfig `koanf:"plane_endpoints"`
}
type VirtualizationHostPolicy struct {
	OrgID          uuid.UUID               `koanf:"org_id"`
	HostID         uuid.UUID               `koanf:"host_id"`
	TrustPolicyRef uuid.UUID               `koanf:"trust_policy_ref"`
	TrustedSigners []string                `koanf:"trusted_signers"`
	Networks       []VirtualizationNetwork `koanf:"networks"`
	// Secret UUIDs must also resolve to a service in this host's organization.
	PlaneSecretRefs []uuid.UUID `koanf:"plane_secret_refs"`
}
type VirtualizationNetwork struct {
	Ref  uuid.UUID            `koanf:"ref"`
	Mode domain.VMNetworkMode `koanf:"mode"`
	Name string               `koanf:"name"`
}
type PersistentVMConfig struct {
	Enabled           bool      `koanf:"enabled"`
	HostID            uuid.UUID `koanf:"host_id"`
	StoragePoolRef    uuid.UUID `koanf:"storage_pool_ref"`
	StateDir          string    `koanf:"state_dir"`
	ImageRoot         string    `koanf:"image_root"`
	LibvirtURI        string    `koanf:"libvirt_uri"`
	EventSocket       string    `koanf:"event_socket"`
	FirecrackerBinary string    `koanf:"firecracker_binary"`
}
type ExecutionPlaneEndpointConfig struct {
	HostID      uuid.UUID `koanf:"host_id"`
	EndpointRef uuid.UUID `koanf:"endpoint_ref"`
	Author      string    `koanf:"author"`
}

func (c VirtualizationConfig) Validate() error {
	invalid := func() error { return fmt.Errorf("invalid virtualization configuration") }
	keyOK := func(s string) bool {
		k, err := nostr.PubKeyFromHex(s)
		return err == nil && k != (nostr.PubKey{}) && k.Hex() == s
	}
	active := c.PersistentVM.Enabled || len(c.PlaneEndpoints) > 0
	if active && len(c.OperatorPubkeys) == 0 {
		return invalid()
	}
	for _, key := range c.OperatorPubkeys {
		if !keyOK(key) {
			return invalid()
		}
	}
	if c.ReconcilePubkey != "" && !slices.Contains(c.OperatorPubkeys, c.ReconcilePubkey) {
		return invalid()
	}
	hosts := map[uuid.UUID]bool{}
	for _, h := range c.Hosts {
		if h.OrgID == uuid.Nil || h.HostID == uuid.Nil || hosts[h.HostID] || h.TrustPolicyRef == uuid.Nil || len(h.TrustedSigners) == 0 {
			return invalid()
		}
		hosts[h.HostID] = true
		for _, key := range h.TrustedSigners {
			if !keyOK(key) {
				return invalid()
			}
		}
		refs := map[uuid.UUID]bool{}
		for _, n := range h.Networks {
			if n.Ref == uuid.Nil || refs[n.Ref] || n.Name == "" || (n.Mode != domain.VMNetworkNAT && n.Mode != domain.VMNetworkBridged) {
				return invalid()
			}
			refs[n.Ref] = true
		}
		for _, ref := range h.PlaneSecretRefs {
			if ref == uuid.Nil {
				return invalid()
			}
		}
	}
	p := c.PersistentVM
	if p.Enabled && (!hosts[p.HostID] || p.StoragePoolRef == uuid.Nil || !filepath.IsAbs(p.StateDir) || !filepath.IsAbs(p.ImageRoot) || p.StateDir == p.ImageRoot || c.ReconcilePubkey == "") {
		return invalid()
	}
	endpoints := map[uuid.UUID]bool{}
	for _, e := range c.PlaneEndpoints {
		if !hosts[e.HostID] || e.EndpointRef == uuid.Nil || endpoints[e.EndpointRef] || !keyOK(e.Author) {
			return invalid()
		}
		endpoints[e.EndpointRef] = true
	}
	return nil
}
