package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
)

type confidentialFixture struct {
	serviceKey nostr.SecretKey
	memberKey  nostr.SecretKey
	org        domain.Organization
	member     domain.OrgMember
	secret     client.SecretRef
	channel    domain.NotificationChannel
	fleet      domain.NotificationChannel
	events     []nostr.Event
	timestamp  nostr.Timestamp
}

func fixedTestKey(t *testing.T, lastByte byte) nostr.SecretKey {
	t.Helper()
	key, err := nostr.SecretKeyFromHex(fmt.Sprintf("%063x%x", 0, lastByte))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func fixtureConfidentialEvent(t *testing.T, serviceKey nostr.SecretKey, ock controlplane.OrgContentKey, kind int, d string, payload any, timestamp nostr.Timestamp) nostr.Event {
	t.Helper()
	plaintext, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	_, tags := nostrpool.ControlStateEnvelope(kind, d, false)
	topic := ""
	for _, tag := range tags {
		if len(tag) > 1 && tag[0] == "t" {
			topic = tag[1]
		}
	}
	content, err := controlplane.EncryptConfidentialContent(context.Background(), ock, plaintext, controlplane.ConfidentialRecordContext{
		LegacyKind: kind, DTag: d, Topic: topic,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: timestamp, Tags: tags, Content: content}
	if err := ev.Sign(serviceKey); err != nil {
		t.Fatal(err)
	}
	return ev
}

func fixtureOCKEnvelope(t *testing.T, serviceKey nostr.SecretKey, recipient nostr.SecretKey, ock controlplane.OrgContentKey, handle string, timestamp nostr.Timestamp) nostr.Event {
	t.Helper()
	recipientPub := nostr.GetPublicKey(recipient)
	wrap, err := controlplane.MarshalOCKWrap(ock, recipientPub.Hex())
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := keyer.NewPlainKeySigner(serviceKey).Encrypt(context.Background(), string(wrap), recipientPub)
	if err != nil {
		t.Fatal(err)
	}
	d := fmt.Sprintf("org-key:%s:v%d:%s", ock.OrgID, ock.Version, handle)
	_, tags := nostrpool.ControlStateEnvelope(kinds.OrgKeyEnvelope, d, false)
	ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: timestamp, Tags: tags, Content: ciphertext}
	if err := ev.Sign(serviceKey); err != nil {
		t.Fatal(err)
	}
	return ev
}

func newConfidentialFixture(t *testing.T) confidentialFixture {
	t.Helper()
	serviceKey := fixedTestKey(t, 1)
	memberKey := fixedTestKey(t, 2)
	pubkey := nostr.GetPublicKey(memberKey).Hex()
	orgID := uuid.MustParse("00000000-0000-4000-8000-000000000101")
	serviceID := uuid.MustParse("00000000-0000-4000-8000-000000000102")
	secretID := uuid.MustParse("00000000-0000-4000-8000-000000000103")
	channelID := uuid.MustParse("00000000-0000-4000-8000-000000000104")
	fleetID := uuid.MustParse("00000000-0000-4000-8000-000000000105")
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	stamp := nostr.Timestamp(now.Unix())
	org := domain.Organization{ID: orgID, Name: "acme", DisplayName: "Acme", OwnerPubkey: pubkey, CreatedAt: now, UpdatedAt: now}
	member := domain.OrgMember{OrgID: orgID, Pubkey: pubkey, Role: domain.RoleOwner, NIP05: "owner@acme.test", JoinedAt: now, UpdatedAt: now}
	secret := client.SecretRef{ID: secretID.String(), ServiceID: serviceID.String(), Name: "API_KEY", EncryptionMethod: "nip44", Version: 2}
	channel := domain.NotificationChannel{ID: channelID, OrgID: orgID, Name: "operations", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"url": "https://private.invalid/hook", "secret": "do-not-reveal", "label": "ops"}, EventFilter: map[string]any{"type": "deploy"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	fleet := domain.NotificationChannel{ID: fleetID, Name: "fleet-alerts", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"url": "https://private.invalid/fleet"}, Enabled: true}
	var orgKey [32]byte
	var fleetKey [32]byte
	for i := range orgKey {
		orgKey[i] = 0xaa
		fleetKey[i] = 0xbb
	}
	ock := controlplane.OrgContentKey{OrgID: orgID.String(), Version: 1, Key: orgKey}
	fock := controlplane.OrgContentKey{OrgID: "fleet", Version: 1, Key: fleetKey}
	_, channelContent := nostrpool.NotificationChannelRegistryRecord(&channel, false, false)
	var channelMetadata map[string]any
	if err := json.Unmarshal([]byte(channelContent), &channelMetadata); err != nil {
		t.Fatal(err)
	}
	fleetMetadata := map[string]any{"id": fleet.ID.String(), "name": fleet.Name, "channel_type": string(fleet.ChannelType), "enabled": true, "fleet_scoped": true}
	events := []nostr.Event{
		fixtureOCKEnvelope(t, serviceKey, memberKey, ock, strings.Repeat("a", 32), stamp),
		fixtureOCKEnvelope(t, serviceKey, memberKey, fock, strings.Repeat("b", 32), stamp),
		fixtureConfidentialEvent(t, serviceKey, ock, kinds.OrgRegistry, orgID.String(), org, stamp),
		fixtureConfidentialEvent(t, serviceKey, ock, kinds.OrgMemberRegistry, "org:member:"+orgID.String()+":"+pubkey, member, stamp),
		fixtureConfidentialEvent(t, serviceKey, ock, kinds.SecretRegistry, secretID.String(), secret, stamp),
		fixtureConfidentialEvent(t, serviceKey, ock, kinds.NotificationChannelRegistry, channelID.String(), channelMetadata, stamp),
		fixtureConfidentialEvent(t, serviceKey, fock, kinds.NotificationChannelRegistry, fleetID.String(), fleetMetadata, stamp),
	}
	events[4].Tags = append(events[4].Tags, nostr.Tag{"service_id", serviceID.String()}, nostr.Tag{"name", secret.Name})
	if err := events[4].Sign(serviceKey); err != nil {
		t.Fatal(err)
	}
	return confidentialFixture{serviceKey, memberKey, org, member, secret, channel, fleet, events, stamp}
}

func installConfidentialPool(t *testing.T, pool *cliReadPool) {
	t.Helper()
	prior := newCLIConfidentialReadPool
	newCLIConfidentialReadPool = func(_ context.Context, _ []string, _ nostr.Keyer) (client.SubscriptionPool, func(), error) {
		return pool, func() {}, nil
	}
	t.Cleanup(func() { newCLIConfidentialReadPool = prior })
}

func confidentialArgs(f confidentialFixture) []string {
	return []string{"--service-pubkey", nostr.GetPublicKey(f.serviceKey).Hex(), "--relay", "wss://fixture.invalid", "--output", "json"}
}

func TestCLIConfidentialRESTNostrGolden(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	f := newConfidentialFixture(t)
	pool := &cliReadPool{events: f.events}
	installConfidentialPool(t, pool)
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls++
		var data any
		switch r.URL.Path {
		case "/api/v1/orgs":
			data = []domain.Organization{f.org}
		case "/api/v1/orgs/" + f.org.ID.String(), "/api/v1/orgs/" + f.org.Name:
			data = f.org
		case "/api/v1/orgs/" + f.org.ID.String() + "/members":
			data = []domain.OrgMember{f.member}
		case "/api/v1/services/" + f.secret.ServiceID + "/secrets":
			data = []client.SecretRef{f.secret}
		case "/api/v1/notifications/channels":
			data = []domain.NotificationChannel{f.channel, f.fleet}
		case "/api/v1/notifications/channels/" + f.channel.ID.String():
			data = f.channel
		case "/api/v1/notifications/channels/" + f.fleet.ID.String():
			data = f.fleet
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	commands := [][]string{
		{"orgs", "list"}, {"orgs", "get", f.org.ID.String()}, {"orgs", "get", f.org.Name},
		{"orgs", "members", "list", f.org.ID.String()}, {"secrets", "list", f.secret.ServiceID},
		{"notifications", "channels", "list"}, {"notifications", "channels", "get", f.channel.ID.String()},
		{"notifications", "channels", "get", f.fleet.ID.String()},
	}
	for _, command := range commands {
		for _, format := range []string{"json", "table"} {
			t.Run(strings.Join(command, "-")+"-"+format, func(t *testing.T) {
				keyFile := t.TempDir() + "/member.key"
				if err := os.WriteFile(keyFile, []byte(f.memberKey.Hex()), 0o600); err != nil {
					t.Fatal(err)
				}
				base := []string{"--server", server.URL, "--nostr-key-file", keyFile}
				base = append(base, confidentialArgs(f)...)
				base = append(base, "--output", format)
				rest, _, err := runReadCLI(t, append(append([]string{}, base...), append([]string{"--http-fallback"}, command...)...)...)
				if err != nil {
					t.Fatalf("REST: %v", err)
				}
				nostrOut, stderr, err := runReadCLI(t, append(append([]string{}, base...), command...)...)
				if err != nil {
					t.Fatalf("Nostr: %v", err)
				}
				if rest != nostrOut {
					t.Fatalf("REST/Nostr mismatch:\nREST %s\nNostr %s", rest, nostrOut)
				}
				if strings.Contains(nostrOut, "do-not-reveal") || strings.Contains(nostrOut, "private.invalid") {
					t.Fatalf("confidential channel credential leaked in output: %s", nostrOut)
				}
				if stderr != "" {
					t.Fatalf("fresh read stderr = %q", stderr)
				}
			})
		}
	}
	if serverCalls != 2*len(commands) {
		t.Fatalf("HTTP calls = %d, want %d", serverCalls, 2*len(commands))
	}
	if len(pool.filters) != 4*len(commands) {
		t.Fatalf("Nostr subscriptions = %d", len(pool.filters))
	}
	for _, filter := range pool.filters {
		if len(filter.Tags["t"]) != 1 || len(filter.Authors) != 1 {
			t.Fatalf("unscoped filter: %+v", filter)
		}
	}
}

func TestCLIConfidentialNIP46Signer(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	t.Setenv("BAHIA_NOSTR_NSEC", "")
	t.Setenv("BAHIA_NOSTR_PRIVATE_KEY", "")
	t.Setenv("BAHIA_NOSTR_BUNKER_URI", "bunker://fixture")
	t.Setenv("BAHIA_NOSTR_BUNKER_RELAYS", "wss://fixture.invalid")
	t.Setenv("BAHIA_NOSTR_CLIENT_PRIVATE_KEY", strings.Repeat("1", 64))
	f := newConfidentialFixture(t)
	installConfidentialPool(t, &cliReadPool{events: f.events})
	prior := newCLINIP46Signer
	closed := false
	newCLINIP46Signer = func(_ context.Context, _, _ string) (nostr.Signer, string, func() error, error) {
		return keyer.NewPlainKeySigner(f.memberKey), nostr.GetPublicKey(f.memberKey).Hex(), func() error { closed = true; return nil }, nil
	}
	t.Cleanup(func() { newCLINIP46Signer = prior })
	out, stderr, err := runReadCLI(t, append(confidentialArgs(f), "orgs", "get", f.org.ID.String())...)
	if err != nil || stderr != "" || !strings.Contains(out, f.org.Name) || !closed {
		t.Fatalf("NIP-46 decrypt: err=%v stderr=%q output=%q closed=%v", err, stderr, out, closed)
	}
}

func TestCLIConfidentialRejectsSignedADTamper(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	f := newConfidentialFixture(t)
	keyFile := t.TempDir() + "/member.key"
	if err := os.WriteFile(keyFile, []byte(f.memberKey.Hex()), 0o600); err != nil {
		t.Fatal(err)
	}
	forged := f.events[2]
	_, forged.Tags = nostrpool.ControlStateEnvelope(kinds.OrgRegistry, uuid.New().String(), false)
	if err := forged.Sign(f.serviceKey); err != nil {
		t.Fatal(err)
	}
	installConfidentialPool(t, &cliReadPool{events: []nostr.Event{f.events[0], forged}})
	_, _, err := runReadCLI(t, append(append([]string{"--nostr-key-file", keyFile}, confidentialArgs(f)...), "orgs", "list")...)
	if err == nil || !strings.Contains(err.Error(), "confidential AD mismatch") {
		t.Fatalf("signed coordinate replay must fail: %v", err)
	}
}

func TestCLIConfidentialCursorStaleAndNonMember(t *testing.T) {
	t.Setenv("BAHIA_DATA_DIR", t.TempDir())
	f := newConfidentialFixture(t)
	pool := &cliReadPool{events: f.events}
	installConfidentialPool(t, pool)
	keyFile := t.TempDir() + "/member.key"
	if err := os.WriteFile(keyFile, []byte(f.memberKey.Hex()), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"--nostr-key-file", keyFile}, confidentialArgs(f)...), "orgs", "list")
	first, _, err := runReadCLI(t, args...)
	if err != nil {
		t.Fatal(err)
	}
	pool.events = nil
	second, _, err := runReadCLI(t, args...)
	if err != nil || second != first {
		t.Fatalf("cursor read: err=%v output=%q", err, second)
	}
	if len(pool.filters) != 4 || pool.filters[2].Since != f.timestamp || pool.filters[3].Since != f.timestamp {
		t.Fatalf("cursor reuse: %+v", pool.filters)
	}
	pool.stale = true
	stale, stderr, err := runReadCLI(t, append([]string{"--eose-timeout", "1ms"}, args...)...)
	if err != nil || stale != first || stderr != "warning: relay data may be stale (no EOSE within timeout)\n" {
		t.Fatalf("stale: err=%v out=%q stderr=%q", err, stale, stderr)
	}
	outsider := fixedTestKey(t, 3)
	if err := os.WriteFile(keyFile, []byte(outsider.Hex()), 0o600); err != nil {
		t.Fatal(err)
	}
	pool.stale = false
	pool.events = f.events
	for _, command := range [][]string{{"orgs", "list"}, {"orgs", "get", f.org.ID.String()}, {"orgs", "members", "list", f.org.ID.String()}, {"secrets", "list", f.secret.ServiceID}, {"notifications", "channels", "list"}} {
		out, _, err := runReadCLI(t, append(append([]string{"--nostr-key-file", keyFile}, confidentialArgs(f)...), command...)...)
		if err != nil || out != "not readable with this key\n" {
			t.Fatalf("nonmember %v: err=%v out=%q", command, err, out)
		}
	}
}
