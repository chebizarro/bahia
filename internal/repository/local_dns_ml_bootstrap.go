package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
)

// BootstrapLocalDNS copies the old PostgreSQL DNS read model only once. A
// durable marker prevents a later restart from resurrecting local deletions.
func BootstrapLocalDNS(ctx context.Context, store *localstore.Outbox, zones DNSZoneRepository, policies DNSPolicyRepository, overrides DNSRecordOverrideRepository) error {
	marker, err := store.GetControlRecord("bootstrap", "dns")
	if err != nil || marker != nil {
		return err
	}
	put := func(family, key string, value any) error {
		current, err := store.GetControlRecord(family, key)
		if err != nil || current != nil {
			return err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return store.PutControlRecord(family, key, data)
	}
	if zones != nil {
		items, err := zones.List(ctx)
		if err != nil {
			return err
		}
		for _, zone := range items {
			if err := put("dns-zone", zone.Name, zone); err != nil {
				return err
			}
			if overrides != nil {
				records, err := overrides.ListByZone(ctx, zone.Name)
				if err != nil {
					return err
				}
				for _, record := range records {
					if err := put("dns-override", record.ID.String(), record); err != nil {
						return err
					}
				}
			}
		}
	}
	if policies != nil {
		items, err := policies.List(ctx)
		if err != nil {
			return err
		}
		for _, policy := range items {
			if err := put("dns-policy", policy.ID.String(), policy); err != nil {
				return err
			}
		}
	}
	return store.PutControlRecord("bootstrap", "dns", []byte("1"))
}

// BootstrapLocalML imports the legacy registry without making PostgreSQL a
// prerequisite for subsequent model, version, or endpoint mutations.
func BootstrapLocalML(ctx context.Context, store *localstore.Outbox, legacy MLRegistryRepository) error {
	marker, err := store.GetControlRecord("bootstrap", "ml")
	if err != nil || marker != nil {
		return err
	}
	if legacy != nil {
		put := func(family, key string, value any) error {
			current, err := store.GetControlRecord(family, key)
			if err != nil || current != nil {
				return err
			}
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return store.PutControlRecord(family, key, data)
		}
		const pageSize = 100
		for offset := 0; ; offset += pageSize {
			models, err := legacy.ListModels(ctx, "", pageSize, offset)
			if err != nil {
				return err
			}
			for _, model := range models {
				if err := put("ml-model", model.ID.String(), model); err != nil {
					return err
				}
				for versionOffset := 0; ; versionOffset += pageSize {
					versions, err := legacy.ListModelVersions(ctx, model.ID, pageSize, versionOffset)
					if err != nil {
						return err
					}
					for _, version := range versions {
						if err := put("ml-version", version.ID.String(), version); err != nil {
							return err
						}
					}
					if len(versions) < pageSize {
						break
					}
				}
			}
			if len(models) < pageSize {
				break
			}
		}
		for offset := 0; ; offset += pageSize {
			endpoints, err := legacy.ListInferenceEndpoints(ctx, uuid.Nil, pageSize, offset)
			if err != nil {
				return err
			}
			for _, endpoint := range endpoints {
				if err := put("ml-endpoint", endpoint.ID.String(), endpoint); err != nil {
					return err
				}
			}
			if len(endpoints) < pageSize {
				break
			}
		}
	}
	return store.PutControlRecord("bootstrap", "ml", []byte("1"))
}
