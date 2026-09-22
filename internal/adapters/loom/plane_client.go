package loom

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"fiatjaf.com/nostr"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// PlaneRelayPool preserves relay acceptance, EOSE, CLOSED and AUTH semantics.
// The shared RelayPool owns NIP-11 discovery, NIP-65-selected relay policy,
// connection backoff and signer-backed NIP-42 authentication.
type PlaneRelayPool interface {
	PublishWithResults(context.Context, nostr.Event) ([]nostrAdapter.PublishResult, error)
	SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*nostrAdapter.MergedSubscription, error)
	AuthenticateRelay(context.Context, string) error
}

type PlaneClient struct {
	pool      PlaneRelayPool
	signer    CanonicalSigner
	endpoints map[uuid.UUID]domain.ExecutionPlaneEndpoint
	now       func() time.Time
	backoff   time.Duration
}

var _ domain.ExecutionPlaneClient = (*PlaneClient)(nil)

// NewPlaneClient uses only explicitly configured endpoint identities. It never
// creates a relay pool, selects another host, or invokes the job/runtime clients.
func NewPlaneClient(pool PlaneRelayPool, signer CanonicalSigner, endpoints []domain.ExecutionPlaneEndpoint) (*PlaneClient, error) {
	if pool == nil || signer == nil {
		return nil, planeError(domain.VMErrorUnavailable, nil)
	}
	c := &PlaneClient{pool: pool, signer: signer, endpoints: map[uuid.UUID]domain.ExecutionPlaneEndpoint{}, now: time.Now, backoff: defaultJobSubscriptionBackoff}
	for _, endpoint := range endpoints {
		if !planeEndpointValid(endpoint) {
			return nil, planeError(domain.VMErrorInvalid, nil)
		}
		if _, exists := c.endpoints[endpoint.EndpointRef]; exists {
			return nil, planeError(domain.VMErrorInvalid, nil)
		}
		c.endpoints[endpoint.EndpointRef] = endpoint
	}
	return c, nil
}

func (c *PlaneClient) authorized(e domain.ExecutionPlaneEndpoint) error {
	if c == nil || c.pool == nil || c.signer == nil || !planeEndpointValid(e) || c.endpoints[e.EndpointRef] != e {
		return planeError(domain.VMErrorUnavailable, nil)
	}
	return nil
}

func planeFilter(e domain.ExecutionPlaneEndpoint, kind int, coordinate string) nostr.Filter {
	author, _ := nostr.PubKeyFromHex(e.Author)
	// Kind + authenticated author + exact coordinate is sufficient routing.
	// Longer metadata tags are validated locally, not assumed relay-indexable.
	return nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kind)}, Authors: []nostr.PubKey{author}, Limit: 1, Tags: nostr.TagMap{"d": {coordinate}}}
}

func (c *PlaneClient) Discover(ctx context.Context, e domain.ExecutionPlaneEndpoint) (domain.ExecutionPlaneSupport, error) {
	var out domain.ExecutionPlaneSupport
	if err := c.authorized(e); err != nil {
		return out, err
	}
	var latest *nostr.Event
	err := c.stream(ctx, e, []nostr.Filter{planeFilter(e, kinds.ContextVMServerAnnouncement, e.EndpointRef.String())}, nil,
		func(ev *nostr.Event) (bool, error) {
			if latest == nil || ev.CreatedAt > latest.CreatedAt || (ev.CreatedAt == latest.CreatedAt && ev.ID.Hex() < latest.ID.Hex()) {
				copy := *ev
				latest = &copy
			}
			return false, nil
		}, func() (bool, error) {
			if latest == nil {
				return false, planeError(domain.VMErrorUnavailable, nil)
			}
			var err error
			out, err = decodePlaneSupport(latest, e, c.now())
			return true, err
		}, nil)
	return out, err
}

func planeTags(e domain.ExecutionPlaneEndpoint) nostr.Tags {
	return nostr.Tags{{"domain", PlaneDomain}, {"schema", PlaneSchema}, {"host", e.HostID.String()}, {"endpoint", e.EndpointRef.String()}}
}

// exchange subscribes before EVENT publication. Application completion is an
// authenticated correlated event, never EOSE or elapsed time. Caller cancellation
// is unavailable/unconfirmed, not evidence that a command failed remotely.
func (c *PlaneClient) exchange(ctx context.Context, e domain.ExecutionPlaneEndpoint, tool string, operation, planeID uuid.UUID, params any, wantObservation bool, accept func(*domain.ExecutionPlaneObservation) bool) (*domain.ExecutionPlaneAcknowledgment, *domain.ExecutionPlaneObservation, error) {
	if err := c.authorized(e); err != nil {
		return nil, nil, err
	}
	id, _ := json.Marshal(operation.String())
	rpc, err := cascontextvm.NewRequest(id, tool, params)
	if err != nil {
		return nil, nil, planeError(domain.VMErrorInvalid, err)
	}
	content, err := json.Marshal(rpc)
	if err != nil {
		return nil, nil, planeError(domain.VMErrorInvalid, err)
	}
	request := &nostr.Event{Kind: nostr.Kind(kinds.ContextVMMessage), CreatedAt: nostr.Timestamp(c.now().Unix()), Tags: append(planeTags(e), nostr.Tag{"p", e.Author}), Content: string(content)}
	if err := c.signer.Sign(ctx, request); err != nil {
		return nil, nil, planeError(domain.VMErrorUnavailable, err)
	}
	if err := nostrAdapter.ValidateInboundEvent(request, c.now(), nostrAdapter.InboundEventMaxFutureSkew); err != nil {
		return nil, nil, planeError(domain.VMErrorIntegrity, err)
	}
	author, _ := nostr.PubKeyFromHex(e.Author)
	filters := []nostr.Filter{{Kinds: []nostr.Kind{nostr.Kind(kinds.ContextVMMessage)}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"e": {request.ID.Hex()}, "p": {request.PubKey.Hex()}}, Limit: 1}}
	if wantObservation {
		filter := planeFilter(e, kinds.CASControlState, PlaneStateCoordinate(planeID))
		filter.Tags["e"] = []string{request.ID.Hex()}
		filters = append(filters, filter)
	}
	var ack *domain.ExecutionPlaneAcknowledgment
	var observation *domain.ExecutionPlaneObservation
	published := false
	err = c.stream(ctx, e, filters, func() error {
		if published {
			return nil
		}
		results, err := c.pool.PublishWithResults(ctx, *request)
		accepted := false
		for _, result := range results {
			if result.Error == nil && result.Accepted {
				accepted = true
			}
		}
		if !accepted {
			return planeError(domain.VMErrorUnavailable, err)
		}
		published = true
		return nil
	}, func(ev *nostr.Event) (bool, error) {
		if ev.Kind == nostr.Kind(kinds.ContextVMMessage) {
			var err error
			ack, err = decodePlaneAck(ev, e, request, operation, c.now())
			if err != nil {
				return false, err
			}
		} else {
			if !wantObservation || !planeTag(ev, "e", request.ID.Hex()) || ev.CreatedAt < request.CreatedAt {
				return false, planeError(domain.VMErrorIntegrity, nil)
			}
			candidate, err := DecodePlaneObservation(ev, e, planeID, c.now())
			if err != nil {
				return false, err
			}
			if candidate.ObservedAt.Before(request.CreatedAt.Time()) || (accept != nil && !accept(candidate)) {
				return false, planeError(domain.VMErrorIntegrity, nil)
			}
			observation = candidate
		}
		return ack != nil && (!wantObservation || observation != nil), nil
	}, nil, nil)
	return ack, observation, err
}

func (c *PlaneClient) Inspect(ctx context.Context, e domain.ExecutionPlaneEndpoint, id uuid.UUID) (*domain.ExecutionPlaneObservation, error) {
	if id == uuid.Nil {
		return nil, planeError(domain.VMErrorInvalid, nil)
	}
	op := uuid.New()
	_, observation, err := c.exchange(ctx, e, domain.ExecutionPlaneInspectTool, op, id, struct {
		PlaneID     uuid.UUID `json:"plane_id"`
		OperationID uuid.UUID `json:"operation_id"`
	}{id, op}, true, nil)
	return observation, err
}

func (c *PlaneClient) Apply(ctx context.Context, e domain.ExecutionPlaneEndpoint, req domain.ExecutionPlaneApplyRequest) (*domain.ExecutionPlaneAcknowledgment, error) {
	p := &domain.ExecutionPlaneDeployment{VirtualizationResourceMeta: domain.VirtualizationResourceMeta{SchemaVersion: req.SchemaVersion, ID: req.PlaneID, OrgID: req.PlaneID, Generation: req.Generation, CreatedBy: e.Author}, HostID: req.HostID, ManagementEndpointRef: e.EndpointRef, ManagementAuthor: e.Author, WorkerPubKey: e.Author, Desired: req.Desired}
	if req.HostID != e.HostID || req.OperationID == uuid.Nil || domain.ValidateExecutionPlaneDeployment(p) != nil {
		return nil, planeError(domain.VMErrorInvalid, nil)
	}
	ack, _, err := c.exchange(ctx, e, domain.ExecutionPlaneApplyTool, req.OperationID, req.PlaneID, req, false, nil)
	return ack, err
}

func (c *PlaneClient) Probe(ctx context.Context, e domain.ExecutionPlaneEndpoint, req domain.ExecutionPlaneProbeRequest) (*domain.ExecutionPlaneProbeEvidence, error) {
	if req.PlaneID == uuid.Nil || req.Generation < 1 || req.SessionID == uuid.Nil || req.Sequence < 1 || !planeClassesValid(req.LifecycleClasses) {
		return nil, planeError(domain.VMErrorInvalid, nil)
	}
	started := c.now()
	_, observation, err := c.exchange(ctx, e, domain.ExecutionPlaneProbeTool, uuid.New(), req.PlaneID, req, true, func(o *domain.ExecutionPlaneObservation) bool {
		q := o.Probe
		return q != nil && !q.ObservedAt.Before(started) && q.ObservedGeneration == req.Generation && q.SessionID == req.SessionID && q.Sequence == req.Sequence && reflect.DeepEqual(q.LifecycleClasses, req.LifecycleClasses)
	})
	if err != nil {
		return nil, err
	}
	return observation.Probe, nil
}

func (c *PlaneClient) Observe(ctx context.Context, e domain.ExecutionPlaneEndpoint, id uuid.UUID, observer domain.ExecutionPlaneObserver) error {
	if observer == nil || id == uuid.Nil {
		return planeError(domain.VMErrorInvalid, nil)
	}
	if err := c.authorized(e); err != nil {
		return err
	}
	var last *domain.ExecutionPlaneObservation
	return c.stream(ctx, e, []nostr.Filter{planeFilter(e, kinds.CASControlState, PlaneStateCoordinate(id))}, nil, func(ev *nostr.Event) (bool, error) {
		o, err := DecodePlaneObservation(ev, e, id, c.now())
		if err != nil {
			return false, errors.Join(err, observer.OnUnavailable(ctx, domain.VMDiagnostic{Code: domain.VMErrorIntegrity, EvidenceDigest: "sha256:" + ev.ID.Hex()}))
		}
		if last != nil && last.SessionID == o.SessionID && last.ObservedGeneration == o.ObservedGeneration {
			if o.Sequence < last.Sequence {
				return false, nil
			}
			if o.Sequence == last.Sequence {
				if !reflect.DeepEqual(o, last) {
					return false, planeError(domain.VMErrorIntegrity, nil)
				}
				return false, nil
			}
		}
		if err := observer.OnObservation(ctx, *o); err != nil {
			return false, err
		}
		last = o
		return false, nil
	}, func() (bool, error) { return false, observer.OnEOSE(ctx) }, func() error {
		return observer.OnUnavailable(ctx, domain.VMDiagnostic{Code: domain.VMErrorUnavailable})
	})
}

// stream reconnects only after explicit transport loss/CLOSED, not to peek for
// events. The bounded dedupe ring avoids an ever-growing live subscription cache.
func (c *PlaneClient) stream(ctx context.Context, endpoint domain.ExecutionPlaneEndpoint, filters []nostr.Filter, ready func() error, event func(*nostr.Event) (bool, error), eose func() (bool, error), disconnected func() error) error {
	seen := make(map[nostr.ID]bool)
	var ring [1024]nostr.ID
	cursor := 0
	authenticated := map[string]bool{}
	backoff := c.backoff
	for {
		if err := ctx.Err(); err != nil {
			return planeError(domain.VMErrorUnavailable, err)
		}
		subCtx, cancel := context.WithCancel(ctx)
		sub, err := c.pool.SubscribeAllWithEOSE(subCtx, filters)
		if err != nil {
			cancel()
			if disconnected == nil {
				return planeError(domain.VMErrorUnavailable, err)
			}
			if err = disconnected(); err != nil {
				return err
			}
			if err = waitForJobResubscribe(ctx, backoff); err != nil {
				return planeError(domain.VMErrorUnavailable, err)
			}
			backoff = nextJobSubscriptionBackoff(backoff)
			continue
		}
		if ready != nil {
			if err = ready(); err != nil {
				sub.Close()
				cancel()
				return err
			}
		}
		retracted := false
		done, retry, err := func() (bool, bool, error) {
			defer sub.Close()
			defer cancel()
			events, closed, history := sub.Events, sub.Closed, sub.EndOfStoredEvents
			handle := func(ev *nostr.Event) (bool, error) {
				if err := validatePlaneEvent(ev, endpoint, c.now()); err != nil {
					return false, err
				}
				if seen[ev.ID] {
					return false, nil
				}
				delete(seen, ring[cursor])
				ring[cursor] = ev.ID
				cursor = (cursor + 1) % len(ring)
				seen[ev.ID] = true
				return event(ev)
			}
			for {
				select {
				case <-ctx.Done():
					return false, false, planeError(domain.VMErrorUnavailable, ctx.Err())
				case ev, ok := <-events:
					if !ok {
						return false, disconnected != nil, planeError(domain.VMErrorUnavailable, nil)
					}
					done, err := handle(ev)
					if done || err != nil {
						return done, false, err
					}
				case reason, ok := <-closed:
					if !ok {
						closed = nil
						continue
					}
					// CLOSED invalidates eligibility before AUTH can block on the relay.
					if disconnected != nil {
						retracted = true
						if err := disconnected(); err != nil {
							return false, false, err
						}
					}
					if nostrAdapter.IsAuthRequiredReason(reason.Reason) && !authenticated[reason.RelayURL] {
						authenticated[reason.RelayURL] = true
						if err := c.pool.AuthenticateRelay(ctx, reason.RelayURL); err != nil {
							return false, false, planeError(domain.VMErrorUnavailable, err)
						}
						return false, true, nil
					}
					return false, disconnected != nil && !nostrAdapter.IsAuthRequiredReason(reason.Reason), planeError(domain.VMErrorUnavailable, nil)
				case <-history:
					history = nil
					if !sub.HasRealEOSE() {
						return false, disconnected != nil, planeError(domain.VMErrorUnavailable, nil)
					}
					// Buffered EVENTs precede EOSE at the relay, even if select chose EOSE first.
					for len(events) > 0 {
						ev := <-events
						done, err := handle(ev)
						if done || err != nil {
							return done, false, err
						}
					}
					backoff = c.backoff
					if eose != nil {
						done, err := eose()
						if done || err != nil {
							return done, false, err
						}
					}
				}
			}
		}()
		if done {
			return err
		}
		if disconnected != nil && !retracted {
			if lostErr := disconnected(); lostErr != nil {
				return errors.Join(err, lostErr)
			}
		}
		if !retry {
			return err
		}
		if err := waitForJobResubscribe(ctx, backoff); err != nil {
			return planeError(domain.VMErrorUnavailable, err)
		}
		backoff = nextJobSubscriptionBackoff(backoff)
	}
}

// SupportsPlaneClasses makes missing endpoint support a hard admission boundary.
func SupportsPlaneClasses(support domain.ExecutionPlaneSupport, classes []domain.VMLifecycleClass) bool {
	if support.ProtocolVersion != domain.ExecutionPlaneProtocolVersion || !planeClassesValid(classes) {
		return false
	}
	for _, class := range classes {
		if !slices.Contains(support.LifecycleClasses, class) {
			return false
		}
	}
	for _, tool := range []string{domain.ExecutionPlaneInspectTool, domain.ExecutionPlaneApplyTool, domain.ExecutionPlaneProbeTool} {
		if !slices.Contains(support.Tools, tool) {
			return false
		}
	}
	return true
}
