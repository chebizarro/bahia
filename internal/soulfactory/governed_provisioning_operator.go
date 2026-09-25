package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
	"golang.org/x/sys/unix"
)

type provisioningRequestMethodKey struct{}

// lockRequest serializes the whole workflow, not just checkpoint writes. CAS
// alone cannot prevent two drivers from applying the same external effect.
func (s *productionStateStore) lockRequest(ctx context.Context, requestID string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(s.requestPath(requestID)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: provisioning request is busy", saga.ErrConflict)
		}
		return nil, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
}

func (p *ProductionGovernedProvisioner) prepareRequest(ctx context.Context, req *domain.ProvisioningRequest, run *domain.ProvisioningRun) (*productionProvisioningState, error) {
	if req.EventID != run.RequestID || req.Requester != run.RequesterPubkey {
		return nil, fmt.Errorf("%w: provisioning request correlation differs", saga.ErrConflict)
	}
	state, err := p.states.load(ctx, run.RequestID)
	if err != nil && !errors.Is(err, errProductionStateNotFound) {
		return nil, err
	}
	if state != nil && state.Request != nil && state.Resolved != nil {
		if *state.Request != *req {
			return nil, fmt.Errorf("%w: persisted provisioning input differs", saga.ErrConflict)
		}
	} else {
		resolved, err := p.full.resolveProvisioningSpec(ctx, req)
		if err != nil {
			return nil, err
		}
		if resolved.Runtime.Target != domain.RuntimeTargetOpenClaw && resolved.Runtime.Target != domain.RuntimeTargetMetiq {
			return nil, fmt.Errorf("governed provisioning requires openclaw or metiq runtime, got %q", resolved.Runtime.Target)
		}
		if _, err := uuid.Parse(strings.TrimSpace(resolved.Runtime.RuntimeReleaseID)); err != nil {
			return nil, fmt.Errorf("governed provisioning requires a valid verified runtime_release_id: %w", err)
		}
		if state == nil {
			state = &productionProvisioningState{RequestID: run.RequestID, RunID: run.ID.String(), AgentID: resolved.AgentID, SpecHash: resolved.SpecHash, Runtime: resolved.Runtime.Target}
		}
		if state.AgentID != resolved.AgentID || state.SpecHash != resolved.SpecHash || state.Runtime != resolved.Runtime.Target {
			return nil, fmt.Errorf("%w: durable production request differs from replay", saga.ErrConflict)
		}
		request := *req
		state.Request, state.Resolved = &request, resolved
		state.RequestMethod, _ = ctx.Value(provisioningRequestMethodKey{}).(string)
		if err := p.states.save(ctx, state); err != nil {
			return nil, err
		}
	}
	runID, err := uuid.Parse(state.RunID)
	if err != nil {
		return nil, fmt.Errorf("invalid durable governed run id: %w", err)
	}
	run.ID, run.AgentID, run.SpecHash = runID, state.AgentID, state.SpecHash
	run.DraftRef, run.DraftEventID = state.Resolved.DraftRef, state.Resolved.DraftEventID
	return state, nil
}

func (p *ProductionGovernedProvisioner) governedForState(state *productionProvisioningState) (*GovernedProvisioner, error) {
	if state.Request == nil || state.Resolved == nil {
		return nil, errors.New("provisioning inputs unavailable; replay the original provisioning request before operating this legacy run")
	}
	resolved := state.Resolved
	if state.Request.EventID != state.RequestID || resolved.AgentID != state.AgentID || resolved.SpecHash != state.SpecHash || resolved.Runtime.Target != state.Runtime {
		return nil, fmt.Errorf("%w: stored provisioning inputs differ from durable identity", saga.ErrConflict)
	}
	runID, err := uuid.Parse(state.RunID)
	if err != nil {
		return nil, err
	}
	run := &domain.ProvisioningRun{ID: runID, RequestID: state.RequestID, RequesterPubkey: state.Request.Requester, AgentID: state.AgentID, SpecHash: state.SpecHash, DraftRef: resolved.DraftRef, DraftEventID: resolved.DraftEventID}
	if _, err := productionRequestEvent(run, state.RequestMethod); err != nil {
		return nil, fmt.Errorf("invalid durable provisioning requester: %w", err)
	}
	port := &productionProvisioningPort{engine: p, request: state.Request, resolved: resolved, run: run}
	return NewGovernedProvisioner(p.store, GovernedProvisioningRequest{
		RequestID: state.RequestID, RunID: state.RunID, AgentID: state.AgentID, SpecHash: state.SpecHash, Runtime: state.Runtime,
	}, port, productionProjectionPort{steps: port})
}

// ExecuteProvisioningCommand reconstructs the production drivers from durable
// inputs. Callers must authorize the operator; they cannot supply a replacement
// spec, runtime, run identity, or credentials through this surface.
func (p *ProductionGovernedProvisioner) ExecuteProvisioningCommand(ctx context.Context, command saga.Command) (*saga.Report, error) {
	command.RequestID = strings.TrimSpace(command.RequestID)
	if command.RequestID == "" {
		return nil, errors.New("request id is required")
	}
	unlock, err := p.states.lockRequest(ctx, command.RequestID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := p.states.load(ctx, command.RequestID)
	if err != nil {
		return nil, err
	}
	governed, err := p.governedForState(state)
	if err != nil {
		return nil, err
	}
	if err := governed.validateDurableRequest(ctx); err != nil {
		return nil, err
	}
	operator, err := governed.Operator()
	if err != nil {
		return nil, err
	}
	report, err := operator.Execute(ctx, command)
	if err != nil || (command.Operation != saga.CommandRetry && command.Operation != saga.CommandReconcile) || command.DryRun || report.Stage != saga.StageRunning {
		return report, err
	}
	state, err = p.states.load(ctx, command.RequestID)
	if err != nil {
		return report, err
	}
	return report, p.deliverSuccess(ctx, state)
}

// deliverSuccess retains one signed result before publication. A crash or lost
// OK can replay the identical event without repeating provisioning. Delivery is
// complete only after the result and its canonical projections are accepted.
func (p *ProductionGovernedProvisioner) deliverSuccess(ctx context.Context, state *productionProvisioningState) error {
	if state.SuccessDelivered {
		return nil
	}
	if !state.ActiveSoulPublished || state.Soul.Status != domain.SoulStatusActive {
		return errors.New("cannot publish success before active Soul projection")
	}
	requestEvent, err := productionRequestEvent(&domain.ProvisioningRun{RequestID: state.RequestID, RequesterPubkey: state.Request.Requester}, state.RequestMethod)
	if err != nil {
		return err
	}
	reactor := p.full.reactor
	if state.SuccessResult == nil {
		// Adopt an already accepted result, including a pre-upgrade result,
		// rather than create a competing event after a response-loss window.
		existing, err := reactor.findExistingProvisioningResult(ctx, requestEvent)
		if err != nil {
			return err
		}
		if existing != nil && tagValue(existing.Tags, tagStatus) != "success" {
			return fmt.Errorf("%w: running saga has a conflicting terminal result", saga.ErrConflict)
		}
		if existing == nil {
			existing, err = BuildProvisioningSuccessResultEvent(requestEvent, &state.Soul, reactor.config.SoulFactoryPubkey)
			if err != nil {
				return err
			}
			if err := reactor.signer.Sign(ctx, existing); err != nil {
				return err
			}
		}
		state.SuccessResult = existing
		if err := p.states.save(ctx, state); err != nil {
			return err
		}
	}
	if err := reactor.publish(ctx, state.SuccessResult, reactor.provisioningPublicationRelays()); err != nil {
		return err
	}
	if err := reactor.publishCanonicalProvisioningObservable(ctx, requestEvent, state.SuccessResult); err != nil {
		return err
	}
	state.SuccessDelivered = true
	return p.states.save(ctx, state)
}
