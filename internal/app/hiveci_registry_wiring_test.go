package app

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

type atomicHiveCIReleaseRegistry interface {
	RegisterReleaseArtifactWithAudit(
		context.Context,
		*domain.Build,
		*domain.Artifact,
		service.ReleaseArtifactVerificationProof,
		service.ReleaseArtifactAuditPreparer,
	) error
}

func TestRelayFirstRegistryRetainsAtomicHiveCIRegistrationCapability(t *testing.T) {
	var configuredRegistry any = (*service.RelayFirstRegistry)(nil)
	if _, ok := configuredRegistry.(atomicHiveCIReleaseRegistry); !ok {
		t.Fatal("relay-first app registry does not expose atomic Hive-CI release registration")
	}

	// Keep the audit record type load-bearing in this app-wiring assertion:
	// Bridge's atomic capability must prepare a durable Nostr outbox row.
	var _ service.ReleaseArtifactAuditPreparer = func(*domain.Artifact) (*repository.NostrEventRecord, error) {
		return nil, nil
	}
}
