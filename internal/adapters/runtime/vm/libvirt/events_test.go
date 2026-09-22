package libvirt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This fixture server is the actual libvirt RPC/socket boundary, not a fake
// DomainEvents. It withholds RegisterAny's reply to prove synchronous readiness.
func TestNativeRegistrationAcknowledgmentAndDisconnect(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "lv-events-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registration := make(chan struct{})
	ack := make(chan struct{})
	disconnect := make(chan struct{})
	emit := make(chan struct{})
	serverDone := make(chan error, 1)
	id := conformanceID("5")
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		for {
			var length uint32
			if err = binary.Read(conn, binary.BigEndian, &length); err != nil {
				serverDone <- err
				return
			}
			if length < 28 || length > 65536 {
				serverDone <- errors.New("invalid fixture packet")
				return
			}
			packet := make([]byte, length-4)
			if _, err = io.ReadFull(conn, packet); err != nil {
				serverDone <- err
				return
			}
			procedure := binary.BigEndian.Uint32(packet[8:12])
			var payload bytes.Buffer
			switch procedure {
			case 66:
				_ = binary.Write(&payload, binary.BigEndian, []uint32{1, 0}) // auth-list: none
			case 1: // connect-open acknowledgment
			case 24:
				if !bytes.Equal(packet[24:], id[:]) {
					serverDone <- errors.New("domain lookup was not exact UUID")
					return
				}
				_ = binary.Write(&payload, binary.BigEndian, uint32(4))
				payload.WriteString("test")
				payload.Write(id[:])
				_ = binary.Write(&payload, binary.BigEndian, int32(1))
			case 316:
				close(registration)
				select {
				case <-ack:
				case <-ctx.Done():
					serverDone <- ctx.Err()
					return
				}
				_ = binary.Write(&payload, binary.BigEndian, int32(7))
			default:
				serverDone <- errors.New("native client attempted non-observation operation")
				return
			}
			header := append([]byte(nil), packet[:24]...)
			binary.BigEndian.PutUint32(header[12:16], 1)
			binary.BigEndian.PutUint32(header[20:24], 0)
			if err = binary.Write(conn, binary.BigEndian, uint32(28+payload.Len())); err == nil {
				_, err = conn.Write(append(header, payload.Bytes()...))
			}
			if err != nil {
				serverDone <- err
				return
			}
			if procedure == 316 {
				select {
				case <-emit:
				case <-ctx.Done():
					serverDone <- ctx.Err()
					return
				}
				var event bytes.Buffer
				_ = binary.Write(&event, binary.BigEndian, []uint32{7, 4})
				event.WriteString("test")
				event.Write(id[:])
				_ = binary.Write(&event, binary.BigEndian, []int32{1, 5, 0})
				eventHeader := append([]byte(nil), header...)
				binary.BigEndian.PutUint32(eventHeader[8:12], 318)
				binary.BigEndian.PutUint32(eventHeader[12:16], 2)
				binary.BigEndian.PutUint32(eventHeader[16:20], 0)
				if err = binary.Write(conn, binary.BigEndian, uint32(28+event.Len())); err == nil {
					_, err = conn.Write(append(eventHeader, event.Bytes()...))
				}
				if err != nil {
					serverDone <- err
					return
				}
				select {
				case <-disconnect:
					serverDone <- nil
					return
				case <-ctx.Done():
					serverDone <- ctx.Err()
					return
				}
			}
		}
	}()
	type subscriptionResult struct {
		sub DomainSubscription
		err error
	}
	returned := make(chan subscriptionResult, 1)
	go func() {
		sub, err := NewDomainEvents(socket, DefaultURI).Subscribe(ctx, id, false)
		returned <- subscriptionResult{sub, err}
	}()
	select {
	case <-registration:
	case err := <-serverDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case got := <-returned:
		if got.sub != nil {
			got.sub.Close()
		}
		t.Fatalf("returned before registration acknowledgment: %v", got.err)
	default:
	}
	close(ack)
	var got subscriptionResult
	select {
	case got = <-returned:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.sub.Close()
	close(emit)
	observed, err := got.sub.Next(ctx)
	if err != nil || observed.ID != id || observed.State != "stopped" {
		t.Fatalf("native lifecycle frame transformation: %+v %v", observed, err)
	}
	close(disconnect)
	if err = <-serverDone; err != nil {
		t.Fatal(err)
	}
	if _, err = got.sub.Next(ctx); err == nil {
		t.Fatal("lost native stream reported an event")
	}
}
