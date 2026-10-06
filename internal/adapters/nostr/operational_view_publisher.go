package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// OperationalViewPublisher publishes bounded operator read models through the
// shared cp-state signer and durable outbox. Blossom metadata is fleet-OCK
// encrypted; the raw blob bytes remain on Blossom's HTTP protocol.
type OperationalViewPublisher struct {
	projector *Projector
	encryptor ConfidentialStateEncryptor
}

func NewOperationalViewPublisher(projector *Projector, encryptor ConfidentialStateEncryptor) *OperationalViewPublisher {
	return &OperationalViewPublisher{projector: projector, encryptor: encryptor}
}

// SoulRuntimePolicy is the SoulFactory policy the Bahia service vouches for.
// Browsers trust only the deployment-seeded service key, so this record is
// their trust root for the SoulFactory keys: which controller signs Souls and
// which runtime identities may advertise kind:30317 capabilities.
type SoulRuntimePolicy struct {
	// AgentRuntimes lists the administratively enabled runtime targets.
	AgentRuntimes []string
	// ControllerPubkeys are the SoulFactory controller identities, empty when
	// SoulFactory is disabled.
	ControllerPubkeys []string
	// RuntimePubkeys mirrors soul_factory.runtime_pubkeys, empty when unpinned.
	RuntimePubkeys map[string][]string
}

func (p *OperationalViewPublisher) PublishSoulRuntimePolicy(ctx context.Context, policy SoulRuntimePolicy) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	body := map[string]any{"agent_runtimes": append([]string{}, policy.AgentRuntimes...)}
	if len(policy.ControllerPubkeys) > 0 {
		body["controller_pubkeys"] = append([]string{}, policy.ControllerPubkeys...)
	}
	if len(policy.RuntimePubkeys) > 0 {
		body["runtime_pubkeys"] = policy.RuntimePubkeys
	}
	content, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(content) > 32768 {
		return fmt.Errorf("soul runtime policy exceeds 32768 bytes")
	}
	return p.projector.publishControlState(ctx, kinds.SoulRuntimePolicyRecord, "soul-factory:runtime-policy", false, nil, string(content), "soul_factory_runtime_policy.projection", nil)
}

func (p *OperationalViewPublisher) PublishBlossomAdmin(ctx context.Context, servers []string, health map[string]string) error {
	content, err := json.Marshal(map[string]any{"servers": servers, "health": health})
	if err != nil {
		return err
	}
	return p.publishBlossom(ctx, kinds.BlossomAdminRecord, "blossom:admin", content, "blossom_admin.projection")
}

func (p *OperationalViewPublisher) PublishBlossomBlob(ctx context.Context, owner string, descriptor blossom.BlobDescriptor) error {
	owner = strings.ToLower(strings.TrimSpace(owner))
	sha := strings.ToLower(strings.TrimSpace(descriptor.SHA256))
	if len(owner) != 64 || len(sha) != 64 || !hexOnly(owner) || !hexOnly(sha) {
		return fmt.Errorf("blossom blob publication requires owner pubkey and SHA-256 hash")
	}
	content, err := json.Marshal(map[string]any{
		"pubkey": owner, "url": descriptor.URL, "sha256": sha,
		"size": descriptor.Size, "type": descriptor.Type, "uploaded": descriptor.Uploaded,
	})
	if err != nil {
		return err
	}
	return p.publishBlossom(ctx, kinds.BlossomBlobRecord, "blossom:blob:"+owner+":"+sha, content, "blossom_blob.projection")
}

func (p *OperationalViewPublisher) publishBlossom(ctx context.Context, family int, coordinate string, content []byte, entityType string) error {
	if p.projector == nil || !p.projector.Enabled() {
		return nil
	}
	if p.encryptor == nil {
		return fmt.Errorf("confidential encryptor not configured; refusing plaintext Blossom publication")
	}
	if len(content) > 32768 {
		return fmt.Errorf("Blossom record exceeds 32768 bytes")
	}
	topic := cpStateFamilies[family].topic
	encrypted, err := p.encryptor.EncryptConfidential(ctx, kinds.FleetOCKScope, content, family, coordinate, topic, nil)
	if err != nil {
		return fmt.Errorf("encrypt Blossom record: %w", err)
	}
	return p.projector.publishControlState(ctx, family, coordinate, false, nil, encrypted, entityType, nil)
}

func hexOnly(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
