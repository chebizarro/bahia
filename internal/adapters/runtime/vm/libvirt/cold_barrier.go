package libvirt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"

	native "github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket"
	"github.com/openagentsinc/bahia/internal/adapters/runtime/vm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// SubscribeEvents has an asynchronous internal queue. Count notifications at
// socket ingress, before that queue, so a state RPC reply on the SAME connection
// is a barrier for already received transitions, including a fast start/stop.
// The subscription is host-scoped: unrelated notifications conservatively
// invalidate a copy rather than being mistaken for exact-resource evidence.
type eventConn struct {
	net.Conn
	pending []byte
	epoch   atomic.Uint64
}

func (c *eventConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.epoch.Add(1)
	}
	c.pending = append(c.pending, p[:n]...)
	for len(c.pending) >= 4 {
		size := int(binary.BigEndian.Uint32(c.pending[:4]))
		if size < 28 || size > 1<<20 {
			c.epoch.Add(1)
			return 0, errors.New("invalid libvirt event packet")
		}
		if len(c.pending) < size {
			break
		}
		var header socket.Header
		if decodeErr := binary.Read(bytes.NewReader(c.pending[4:28]), binary.BigEndian, &header); decodeErr != nil {
			c.epoch.Add(1)
			return 0, decodeErr
		}
		if header.Type == socket.Message {
			c.epoch.Add(1)
		}
		c.pending = c.pending[size:]
	}
	return n, err
}

func (s *nativeSubscription) CheckCold(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dom, err := s.client.DomainLookupByUUID(native.UUID(s.id))
	if err != nil {
		return vm.ProviderError(domain.VMErrorUnconfirmed, err)
	}
	state, _, err := s.client.DomainGetState(dom, 0)
	if err != nil {
		return vm.ProviderError(domain.VMErrorUnconfirmed, err)
	}
	if state != int32(native.DomainShutoff) || s.wire.epoch.Load() != s.epoch {
		return vm.ProviderError(domain.VMErrorConflict, nil)
	}
	select {
	case <-s.client.Disconnected():
		return vm.ProviderError(domain.VMErrorUnconfirmed, nil)
	default:
	}
	return nil
}
