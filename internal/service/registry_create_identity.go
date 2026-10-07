package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// Create-path identity.
//
// A create intent may carry a client-minted id. The registry mints one only
// when none is supplied. Because the id is fixed before any write, a retried
// create is resolved by content:
// - id free -> create
// - id taken, same content -> idempotent replay: the stored entity is
// returned and nothing is written or published again
// - id taken, different content -> *domain.EntityIDConflictError
//
// "Content" is the desired state the create intent declares: every field
// except the id and the server-stamped timestamps, after the same
// normalization the write path applies. A different org is different content,
// so an id cannot be used to reach into another organization.

// replayServiceCreate resolves svc.ID against the stored services. It reports
// replay=true (and overwrites *svc with the stored entity) when the create is
// an idempotent retry, and returns a conflict when the id names a different
// service. svc must already carry its id and write-path defaults.
func (s *RegistryService) replayServiceCreate(ctx context.Context, svc *domain.Service) (bool, error) {
	if s.services == nil || svc.ID == uuid.Nil {
		return false, nil
	}
	existing, err := s.services.GetByID(ctx, svc.ID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && existing == nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking service id %s: %w", svc.ID, err)
	}
	if !bytes.Equal(serviceCreateFingerprint(existing), serviceCreateFingerprint(svc)) {
		return false, &domain.EntityIDConflictError{Entity: "service", ID: svc.ID}
	}
	normalizeServiceRepositoryForRead(existing)
	*svc = *existing
	return true, nil
}

// replayEnvironmentCreate is replayServiceCreate for environments. When units
// is non-nil the create declared an explicit deployment-unit set, which is
// part of its content: it must match the stored units (by key), and on replay
// each requested unit is overwritten with its stored copy so callers return
// the original unit ids.
func (s *RegistryService) replayEnvironmentCreate(ctx context.Context, env *domain.Environment, units []*domain.DeploymentUnit) (bool, error) {
	if env == nil || env.ID == uuid.Nil {
		return false, nil
	}
	existing, storedUnits, err := s.loadEnvironmentForCreate(ctx, env.ID, units != nil)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, nil
	}
	requested := environmentCreateFingerprint(env, derefDeploymentUnits(units))
	stored := environmentCreateFingerprint(existing, nil)
	if units != nil {
		stored = environmentCreateFingerprint(existing, storedUnits)
	}
	if !bytes.Equal(stored, requested) {
		return false, &domain.EntityIDConflictError{Entity: "environment", ID: env.ID}
	}
	*env = *existing
	if units != nil {
		byKey := make(map[string]domain.DeploymentUnit, len(storedUnits))
		for _, unit := range storedUnits {
			byKey[unit.Key] = unit
		}
		for _, unit := range units {
			if storedUnit, ok := byKey[unit.Key]; ok {
				*unit = storedUnit
			}
		}
	}
	return true, nil
}

func (s *RegistryService) loadEnvironmentForCreate(ctx context.Context, id uuid.UUID, withUnits bool) (*domain.Environment, []domain.DeploymentUnit, error) {
	if !withUnits && s.environments != nil {
		existing, err := s.environments.GetByID(ctx, id)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("checking environment id %s: %w", id, err)
		}
		return existing, nil, nil
	}
	if s.txExecutor == nil {
		if s.environments == nil {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("environment deployment-unit transaction handling is not configured")
	}
	var (
		existing *domain.Environment
		units    []domain.DeploymentUnit
	)
	err := s.txExecutor.WithinTx(ctx, func(repos repository.TxRepos) error {
		if repos.Environments == nil {
			return fmt.Errorf("environment transaction repositories are not configured")
		}
		found, err := repos.Environments.GetByID(ctx, id)
		if errors.Is(err, repository.ErrNotFound) || (err == nil && found == nil) {
			return nil
		}
		if err != nil {
			return err
		}
		existing = found
		if withUnits && repos.DeploymentUnits != nil {
			units, err = repos.DeploymentUnits.ListByEnvironment(ctx, id)
		}
		return err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("checking environment id %s: %w", id, err)
	}
	return existing, units, nil
}

// serviceCreateFingerprint is the canonical encoding of a service's declared
// desired state: id and timestamps are dropped and both write- and
// read-normalization are applied so a stored row and a fresh intent compare
// equal when they declare the same thing.
func serviceCreateFingerprint(svc *domain.Service) []byte {
	var c domain.Service
	if !cloneJSON(svc, &c) {
		return nil
	}
	c.ID, c.CreatedAt, c.UpdatedAt = uuid.Nil, time.Time{}, time.Time{}
	if c.RuntimeType == "" {
		c.RuntimeType = domain.RuntimeTypeDocker
	}
	if c.DefaultBranch == "" {
		c.DefaultBranch = "main"
	}
	normalizeServiceRepositoryForWrite(&c)
	normalizeServiceRepositoryForRead(&c)
	if c.RuntimeConfig != nil {
		if raw, err := json.Marshal(c.RuntimeConfig); err == nil && (string(raw) == "{}" || string(raw) == "null") {
			c.RuntimeConfig = nil
		}
	}
	out, _ := json.Marshal(c)
	return out
}

// environmentCreateFingerprint is serviceCreateFingerprint for environments,
// including the explicit deployment units (sorted by key, without ids) when
// the create declared them.
func environmentCreateFingerprint(env *domain.Environment, units []domain.DeploymentUnit) []byte {
	var c domain.Environment
	if !cloneJSON(env, &c) {
		return nil
	}
	c.ID, c.CreatedAt, c.UpdatedAt = uuid.Nil, time.Time{}, time.Time{}
	if c.DeployStrategy == "" {
		c.DeployStrategy = domain.DeployStrategyReplace
	}
	domain.NormalizeEnvironmentTargeting(&c)
	if len(c.LoomWorkerSelector) == 0 {
		c.LoomWorkerSelector = nil
	}
	if len(c.RuntimeConfig) == 0 {
		c.RuntimeConfig = nil
	}
	projected := make([]domain.DeploymentUnit, 0, len(units))
	for _, unit := range units {
		unit.ID, unit.EnvironmentID, unit.CreatedAt, unit.UpdatedAt = uuid.Nil, uuid.Nil, time.Time{}, time.Time{}
		unit.Implicit = false
		if len(unit.NetworkProfile) == 0 {
			unit.NetworkProfile = nil
		}
		if len(unit.RuntimeConfig) == 0 {
			unit.RuntimeConfig = nil
		}
		projected = append(projected, unit)
	}
	sort.Slice(projected, func(i, j int) bool { return projected[i].Key < projected[j].Key })
	out, _ := json.Marshal(struct {
		Environment domain.Environment      `json:"environment"`
		Units       []domain.DeploymentUnit `json:"units,omitempty"`
	}{c, projected})
	return out
}

func derefDeploymentUnits(units []*domain.DeploymentUnit) []domain.DeploymentUnit {
	if units == nil {
		return nil
	}
	out := make([]domain.DeploymentUnit, 0, len(units))
	for _, unit := range units {
		if unit != nil {
			out = append(out, *unit)
		}
	}
	return out
}

// cloneJSON deep-copies src into dst through its JSON form, which is also the
// form the relay and the stored row carry (numbers in free-form maps become
// float64 on both sides).
func cloneJSON(src, dst any) bool {
	raw, err := json.Marshal(src)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, dst) == nil
}
