package nip55l

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip44"
	"github.com/godbus/dbus/v5"
)

const busConfig = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:path=%s</listen>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

// startPrivateBus runs a dbus-daemon private to the test and returns its
// address. The test is skipped when dbus-daemon is not installed.
func startPrivateBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if _, statErr := os.Stat("/opt/homebrew/bin/dbus-daemon"); statErr != nil {
			t.Skip("dbus-daemon not found on PATH or at /opt/homebrew/bin/dbus-daemon; install dbus to run NIP-55L bus tests")
		}
		daemon = "/opt/homebrew/bin/dbus-daemon"
	}
	// Unix socket paths are length-limited; t.TempDir is too long on macOS.
	dir, err := os.MkdirTemp("/tmp", "nip55l-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfg := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(cfg, fmt.Appendf(nil, busConfig, filepath.Join(dir, "bus")), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(daemon, "--config-file="+cfg, "--print-address=1", "--nofork")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	addr := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		addr <- strings.TrimSpace(line)
	}()
	select {
	case a := <-addr:
		if a == "" {
			t.Fatalf("dbus-daemon printed no address; stderr: %s", stderr.String())
		}
		return a
	case <-time.After(10 * time.Second):
		t.Fatalf("dbus-daemon did not print its address; stderr: %s", stderr.String())
	}
	return ""
}

type fakeCall struct {
	Method   string
	Identity string
	AppID    string
	Input    string
}

// fakeSigner is an org.nostr.Signer backed by a real key. Fields set before
// export select the method surface and failure behaviour.
type fakeSigner struct {
	sk nostr.SecretKey
	pk nostr.PubKey

	// Method surface.
	noListIdentities bool
	noB64            bool

	// Behaviour.
	identities []string                   // ListIdentities reply; default the signer's npub
	active     nostr.PubKey               // GetPublicKeyForApp reply; default pk
	failWith   map[string]*dbus.Error     // method -> error reply
	signWith   nostr.SecretKey            // SignEvent key override
	tamper     func(evt *nostr.Event)     // applied to the signed event
	rawReply   func(signed string) string // replaces the SignEvent reply
	block      chan struct{}              // gated methods wait for this when set
	entered    chan string                // receives gated method names when set

	stop     chan struct{}
	mu       sync.Mutex
	calls    []fakeCall
	optIns   int
	received []string // SignEvent eventJson inputs
}

func newFakeSigner() *fakeSigner {
	sk := nostr.Generate()
	return &fakeSigner{sk: sk, pk: sk.Public(), stop: make(chan struct{})}
}

func (f *fakeSigner) npub() string { return nip19.EncodeNpub(f.pk) }

// export owns org.nostr.Signer on the bus at addr until the test ends.
func (f *fakeSigner) export(t *testing.T, addr string) {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("fake signer connect: %v", err)
	}
	t.Cleanup(func() {
		close(f.stop)
		_ = conn.Close()
	})
	if err := conn.ExportMethodTable(f.methods(), signerPath, signerInterface); err != nil {
		t.Fatal(err)
	}
	reply, err := conn.RequestName(signerBusName, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake signer RequestName: reply %v err %v", reply, err)
	}
}

func (f *fakeSigner) methods() map[string]any {
	m := map[string]any{
		"EnableTypedApprovalErrors": func() *dbus.Error {
			f.mu.Lock()
			f.optIns++
			f.mu.Unlock()
			return nil
		},
		"GetPublicKeyForApp": func(appID string) (string, *dbus.Error) {
			if err := f.gate("GetPublicKeyForApp", "", appID, ""); err != nil {
				return "", err
			}
			active := f.active
			if active == nostr.ZeroPK {
				active = f.pk
			}
			return nip19.EncodeNpub(active), nil
		},
		"SignEvent":          f.signEvent,
		"NIP44EncryptForApp": f.nip44Encrypt("NIP44EncryptForApp", false),
		"NIP44DecryptForApp": f.nip44Decrypt("NIP44DecryptForApp", false),
	}
	if !f.noListIdentities {
		m["ListIdentities"] = func() ([]string, *dbus.Error) {
			f.record(fakeCall{Method: "ListIdentities"})
			if err := f.failure("ListIdentities"); err != nil {
				return nil, err
			}
			if f.identities != nil {
				return f.identities, nil
			}
			return []string{f.npub()}, nil
		}
	}
	if !f.noB64 {
		m["NIP44EncryptB64ForApp"] = f.nip44Encrypt("NIP44EncryptB64ForApp", true)
		m["NIP44DecryptB64ForApp"] = f.nip44Decrypt("NIP44DecryptB64ForApp", true)
	}
	return m
}

func (f *fakeSigner) record(c fakeCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeSigner) failure(method string) *dbus.Error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failWith[method]
}

func (f *fakeSigner) snapshot() ([]fakeCall, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall(nil), f.calls...), f.optIns
}

// gate records a gated call, then applies the configured block and error.
func (f *fakeSigner) gate(method, identity, appID, input string) *dbus.Error {
	f.record(fakeCall{Method: method, Identity: identity, AppID: appID, Input: input})
	if f.entered != nil {
		f.entered <- method
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-f.stop:
		}
	}
	if err := f.failure(method); err != nil {
		return err
	}
	if identity != "" && identity != f.pk.Hex() && identity != f.npub() {
		return dbus.NewError(signerErrorPrefix+"NoKeyConfigured", []any{"no such identity"})
	}
	return nil
}

func (f *fakeSigner) signEvent(eventJSON, identity, appID string) (string, *dbus.Error) {
	if err := f.gate("SignEvent", identity, appID, ""); err != nil {
		return "", err
	}
	f.mu.Lock()
	f.received = append(f.received, eventJSON)
	f.mu.Unlock()
	var evt nostr.Event
	if err := json.Unmarshal([]byte(eventJSON), &evt); err != nil {
		return "", dbus.NewError(signerErrorPrefix+"InvalidInput", []any{"not an event"})
	}
	if evt.CreatedAt == 0 {
		evt.CreatedAt = nostr.Now()
	}
	key := f.sk
	if f.signWith != [32]byte{} {
		key = f.signWith
	}
	if err := evt.Sign(key); err != nil {
		return "", dbus.NewError(signerErrorPrefix+"Internal", []any{err.Error()})
	}
	if f.tamper != nil {
		f.tamper(&evt)
	}
	out, _ := json.Marshal(evt)
	if f.rawReply != nil {
		return f.rawReply(string(out)), nil
	}
	return string(out), nil
}

func (f *fakeSigner) conversationKey(peerHex string) ([32]byte, *dbus.Error) {
	peer, err := nostr.PubKeyFromHex(peerHex)
	if err != nil {
		return [32]byte{}, dbus.NewError(signerErrorPrefix+"InvalidInput", []any{"bad peer"})
	}
	ck, err := nip44.GenerateConversationKey(peer, f.sk)
	if err != nil {
		return [32]byte{}, dbus.NewError(signerErrorPrefix+"InvalidInput", []any{"bad peer"})
	}
	return ck, nil
}

func (f *fakeSigner) nip44Encrypt(method string, b64 bool) func(string, string, string, string) (string, *dbus.Error) {
	return func(plaintext, peer, identity, appID string) (string, *dbus.Error) {
		if err := f.gate(method, identity, appID, ""); err != nil {
			return "", err
		}
		if b64 {
			raw, err := base64.StdEncoding.DecodeString(plaintext)
			if err != nil {
				return "", dbus.NewError(signerErrorPrefix+"InvalidInput", []any{"bad base64"})
			}
			plaintext = string(raw)
		}
		ck, derr := f.conversationKey(peer)
		if derr != nil {
			return "", derr
		}
		ct, err := nip44.Encrypt(plaintext, ck)
		if err != nil {
			return "", dbus.NewError(signerErrorPrefix+"Internal", []any{err.Error()})
		}
		return ct, nil
	}
}

func (f *fakeSigner) nip44Decrypt(method string, b64 bool) func(string, string, string, string) (string, *dbus.Error) {
	return func(ciphertext, peer, identity, appID string) (string, *dbus.Error) {
		if err := f.gate(method, identity, appID, ""); err != nil {
			return "", err
		}
		ck, derr := f.conversationKey(peer)
		if derr != nil {
			return "", derr
		}
		pt, err := nip44.Decrypt(ciphertext, ck)
		if err != nil {
			return "", dbus.NewError(signerErrorPrefix+"InvalidInput", []any{"cannot decrypt"})
		}
		if b64 {
			return base64.StdEncoding.EncodeToString([]byte(pt)), nil
		}
		return pt, nil
	}
}
