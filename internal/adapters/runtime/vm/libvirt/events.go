package libvirt

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"

	native "github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// nativeEvents uses the pure-Go client solely for registration and domain lookup.
// This pinned client's SubscribeEvents ignores its OptDomain argument; registration
// is host-scoped and Next rejects every event whose provider UUID is not exact.
// No foreign event is ever used as mutation-completion evidence.
type nativeEvents struct{ socket, uri string }
type nativeSubscription struct {
	conn   net.Conn
	client *native.Libvirt
	events <-chan interface{}
	cancel context.CancelFunc
	stop   func() bool
	once   sync.Once
	id     uuid.UUID
	wire   *eventConn
	epoch  uint64
}

func NewDomainEvents(socket, uri string) DomainEvents { return nativeEvents{socket: socket, uri: uri} }
func (n nativeEvents) Subscribe(ctx context.Context, id uuid.UUID, reboot bool) (DomainSubscription, error) {
	if !filepath.IsAbs(n.socket) || (n.uri != DefaultURI && n.uri != "qemu:///session") || id == uuid.Nil {
		return nil, vm.ProviderError(domain.VMErrorInvalid, nil)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", n.socket)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err = conn.SetDeadline(deadline); err != nil {
			return nil, errors.Join(err, conn.Close())
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	wire := &eventConn{Conn: conn}
	client := native.NewWithDialer(dialers.NewAlreadyConnected(wire))
	fail := func(err error) (DomainSubscription, error) {
		stop()
		return nil, errors.Join(err, conn.Close())
	}
	if err = client.ConnectToURI(native.ConnectURI(n.uri)); err != nil {
		return fail(err)
	}
	dom, err := client.DomainLookupByUUID(native.UUID(id))
	if err != nil {
		return fail(err)
	}
	subctx, cancel := context.WithCancel(ctx)
	eventID := native.DomainEventIDLifecycle
	if reboot {
		eventID = native.DomainEventIDReboot
	}
	// SubscribeEvents synchronously waits for RegisterAny's successful RPC reply
	// and installs its stream before returning. There is no spawn/sleep readiness.
	events, err := client.SubscribeEvents(subctx, eventID, native.OptDomain{dom})
	if err != nil {
		cancel()
		return fail(err)
	}
	return &nativeSubscription{conn: conn, client: client, events: events, cancel: cancel, stop: stop, id: id, wire: wire, epoch: wire.epoch.Load()}, nil
}
func (s *nativeSubscription) Close() error {
	s.once.Do(func() {
		s.cancel()
		s.stop()
		_ = s.conn.Close()
		for range s.events {
		}
	})
	return nil
}
func (s *nativeSubscription) Next(ctx context.Context) (DomainEvent, error) {
	for {
		select {
		case <-s.client.Disconnected():
			return DomainEvent{}, errors.New("libvirt event stream lost")
		default:
		}
		select {
		case <-ctx.Done():
			return DomainEvent{}, ctx.Err()
		case <-s.client.Disconnected():
			return DomainEvent{}, errors.New("libvirt event stream lost")
		case raw, ok := <-s.events:
			if !ok {
				return DomainEvent{}, errors.New("libvirt event stream closed")
			}
			select {
			case <-s.client.Disconnected():
				return DomainEvent{}, errors.New("libvirt event stream lost")
			default:
			}
			event, ok := nativeDomainEvent(raw)
			if ok && event.ID == s.id {
				return event, nil
			}
		}
	}
}
func nativeDomainEvent(raw interface{}) (DomainEvent, bool) {
	switch e := raw.(type) {
	case *native.DomainEventCallbackRebootMsg:
		return DomainEvent{ID: uuid.UUID(e.Msg.Dom.UUID), Reboot: true}, true
	case *native.DomainEventCallbackLifecycleMsg:
		out := DomainEvent{ID: uuid.UUID(e.Msg.Dom.UUID)}
		switch native.DomainEventType(e.Msg.Event) {
		case native.DomainEventStarted, native.DomainEventResumed:
			out.State = domain.VMRuntimeRunning
		case native.DomainEventStopped:
			out.State = domain.VMRuntimeStopped
		case native.DomainEventSuspended:
			out.State = domain.VMRuntimePaused
		case native.DomainEventCrashed:
			out.State = domain.VMRuntimeFailed
		default:
			return DomainEvent{}, false
		}
		return out, true
	}
	return DomainEvent{}, false
}

// DomainEvents returns only after the hypervisor acknowledges registration.
// Closing a subscription must release its transport and unblock Next. A virsh
// child having started is NOT proof of registration and cannot implement this.
type DomainEvents interface {
	Subscribe(context.Context, uuid.UUID, bool) (DomainSubscription, error)
}
type DomainSubscription interface {
	Next(context.Context) (DomainEvent, error)
	Close() error
}
type DomainEvent struct {
	ID     uuid.UUID
	State  domain.VMRuntimeState
	Reboot bool
}

func (d *Driver) eventTransition(ctx context.Context, r *vm.PersistentResource, command string, want domain.VMRuntimeState) (retErr error) {
	if d.cfg.Events == nil {
		return vm.ProviderError(domain.VMErrorUnsupported, nil)
	}
	sub, err := d.cfg.Events.Subscribe(ctx, r.ID, command == "reboot")
	if err != nil {
		return vm.ProviderError(domain.VMErrorUnavailable, err)
	}
	defer func() { retErr = vm.JoinCleanupError(retErr, sub.Close()) }()
	// Registration precedes this re-inspection and actuation, closing both the
	// lost-event window and the inspect/subscribe ownership race.
	if err = d.recheck(ctx, r); err != nil {
		return err
	}
	if _, err = d.virsh(ctx, command, r.ID.String()); err != nil {
		return vm.ProviderError(domain.VMErrorUnconfirmed, err)
	}
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		if event.ID != r.ID || (command == "reboot" && !event.Reboot) || (command != "reboot" && event.State != want) {
			continue
		}
		after, err := d.InspectPersistent(ctx, r.ID)
		if err != nil {
			return vm.ProviderError(domain.VMErrorUnconfirmed, err)
		}
		if after.State != want || after.Marker == nil || !vm.SameIdentity(after.Marker.VMResourceIdentity, r.Marker.VMResourceIdentity) {
			return vm.ProviderError(domain.VMErrorUnconfirmed, nil)
		}
		return nil
	}
}
