package relaysidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip42"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// startDefaultReadAuthSidecar starts a sidecar on config.Defaults() without
// touching read_auth_mode, so the test pins the shipped default.
func startDefaultReadAuthSidecar(t *testing.T) (serviceKey, adminKey, allowedKey nostr.SecretKey, relayURL string, server *Server) {
	t.Helper()
	serviceKey, adminKey, allowedKey = nostr.Generate(), nostr.Generate(), nostr.Generate()
	cfg := config.Defaults().Nostr
	cfg.Sidecar.DataDir = t.TempDir()
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.PublicURL = "ws://localhost:3334"
	cfg.Sidecar.AdministratorPubkeys = []string{adminKey.Public().Hex()}
	cfg.Sidecar.AdminPolicyPath = cfg.Sidecar.DataDir + "/relay-admin-policy.json"
	cfg.Sidecar.ReadAuthAllowedPubkeys = []string{allowedKey.Public().Hex()}
	require.Equal(t, config.ReadAuthModeEnforce, cfg.Sidecar.NormalizedReadAuthMode(), "the shipped default must be enforce")

	server, err := New(t.Context(), cfg, localSigner(t, serviceKey), zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	runtimePolicy := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(),
		Tags:    nostr.Tags{{"d", "soul-factory:runtime-policy"}, {"t", kinds.CPStateTopicSoulRuntimePolicy}},
		Content: `{"controller_pubkeys":["aa"]}`}
	require.NoError(t, runtimePolicy.Sign(serviceKey))
	require.NoError(t, server.store.Save(context.Background(), runtimePolicy))
	serviceState := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", "service:web"}, {"t", kinds.CPStateTopicServiceState}}, Content: `{"ok":true}`}
	require.NoError(t, serviceState.Sign(serviceKey))
	require.NoError(t, server.store.Save(context.Background(), serviceState))
	return serviceKey, adminKey, allowedKey, httpServer.URL, server
}

func protectedFilter() nostr.Filter {
	return nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Tags: nostr.TagMap{"t": {kinds.CPStateTopicSoulRuntimePolicy}}}
}

// reqOutcome sends a REQ and returns the frames until EOSE or CLOSED for it,
// plus any AUTH challenge the relay issued on the way.
func reqOutcome(c *rawRelayClient, subID string, filter nostr.Filter) (events int, closed string, challenge string) {
	c.t.Helper()
	c.send("REQ", subID, filter)
	for {
		frame := c.next()
		switch {
		case frame.label == "AUTH":
			challenge = frame.reason
		case frame.label == "EVENT" && frame.subID == subID:
			events++
		case frame.label == "EOSE" && frame.subID == subID:
			c.send("CLOSE", subID)
			return events, "", challenge
		case frame.label == "CLOSED" && frame.subID == subID:
			return events, frame.reason, challenge
		}
	}
}

func countOutcome(c *rawRelayClient, subID string, filter nostr.Filter) (count uint32, closed string, challenge string) {
	c.t.Helper()
	c.send("COUNT", subID, filter)
	for {
		frame := c.next()
		switch {
		case frame.label == "AUTH":
			challenge = frame.reason
		case frame.label == "COUNT" && frame.subID == subID:
			return frame.count, "", challenge
		case frame.label == "CLOSED" && frame.subID == subID:
			return 0, frame.reason, challenge
		}
	}
}

// authenticate answers the relay's NIP-42 challenge with key and waits for OK.
func authenticate(c *rawRelayClient, challenge string, key nostr.SecretKey) {
	c.t.Helper()
	require.NotEmpty(c.t, challenge, "relay must issue an AUTH challenge before the client can authenticate")
	auth := nip42.CreateUnsignedAuthEvent(challenge, key.Public(), "ws://localhost:3334")
	require.NoError(c.t, auth.Sign(key))
	c.send("AUTH", auth)
	for {
		frame := c.next()
		if frame.label == "OK" && frame.subID == auth.ID.Hex() {
			require.Truef(c.t, frame.ok, "AUTH refused: %s", frame.reason)
			return
		}
	}
}

// TestReadAuthDefaultEnforcesOverWebsocket: on the
// shipped default config the sidecar refuses unauthenticated REQ and COUNT on
// a protected topic with CLOSED "auth-required:" and a NIP-42 challenge,
// serves them to an authenticated admitted pubkey, answers an authenticated
// but non-admitted pubkey with CLOSED "restricted:", leaves public topics
// open, and advertises auth_required in NIP-11.
func TestReadAuthDefaultEnforcesOverWebsocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	serviceKey, adminKey, allowedKey, relayURL, _ := startDefaultReadAuthSidecar(t)
	public := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Tags: nostr.TagMap{"t": {kinds.CPStateTopicServiceState}}}

	t.Run("unauthenticated REQ and COUNT are refused, public topics are served", func(t *testing.T) {
		c := dialRawRelay(t, ctx, relayURL)
		events, closed, challenge := reqOutcome(c, "anon-protected", protectedFilter())
		require.Zero(t, events)
		require.True(t, strings.HasPrefix(closed, "auth-required:"), closed)
		require.NotEmpty(t, challenge, "an auth-required CLOSED comes with an AUTH challenge")

		count, closed, _ := countOutcome(c, "anon-count", protectedFilter())
		require.Zero(t, count)
		require.True(t, strings.HasPrefix(closed, "auth-required:"), closed)

		events, closed, _ = reqOutcome(c, "anon-public", public)
		require.Equal(t, 1, events, "public cp-state topics stay readable without auth")
		require.Empty(t, closed)
		count, closed, _ = countOutcome(c, "anon-public-count", public)
		require.Equal(t, uint32(1), count)
		require.Empty(t, closed)
	})

	for name, key := range map[string]nostr.SecretKey{"service key": serviceKey, "relay administrator": adminKey, "read_auth_allowed_pubkeys": allowedKey} {
		t.Run("authenticated admitted "+name+" reads the protected topic", func(t *testing.T) {
			c := dialRawRelay(t, ctx, relayURL)
			_, closed, challenge := reqOutcome(c, "probe", protectedFilter())
			require.True(t, strings.HasPrefix(closed, "auth-required:"), closed)
			authenticate(c, challenge, key)
			events, closed, _ := reqOutcome(c, "admitted", protectedFilter())
			require.Empty(t, closed)
			require.Equal(t, 1, events)
			count, closed, _ := countOutcome(c, "admitted-count", protectedFilter())
			require.Empty(t, closed)
			require.Equal(t, uint32(1), count)
		})
	}

	t.Run("authenticated non-admitted pubkey is refused with restricted", func(t *testing.T) {
		c := dialRawRelay(t, ctx, relayURL)
		_, closed, challenge := reqOutcome(c, "probe", protectedFilter())
		require.True(t, strings.HasPrefix(closed, "auth-required:"), closed)
		authenticate(c, challenge, nostr.Generate())
		events, closed, _ := reqOutcome(c, "stranger", protectedFilter())
		require.Zero(t, events)
		require.True(t, strings.HasPrefix(closed, "restricted:"), closed)
		count, closed, _ := countOutcome(c, "stranger-count", protectedFilter())
		require.Zero(t, count)
		require.True(t, strings.HasPrefix(closed, "restricted:"), closed)
		events, closed, _ = reqOutcome(c, "stranger-public", public)
		require.Equal(t, 1, events)
		require.Empty(t, closed)
	})

	t.Run("NIP-11 advertises auth_required", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayURL, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "application/nostr+json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var info struct {
			Limitation struct {
				AuthRequired bool `json:"auth_required"`
			} `json:"limitation"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		require.True(t, info.Limitation.AuthRequired)
	})
}

// TestReadAuthModeNormalisationDefaultsToEnforce pins that an unset or
// mistyped read_auth_mode does not silently open protected topics.
func TestReadAuthModeNormalisationDefaultsToEnforce(t *testing.T) {
	for _, raw := range []string{"", "  ", "enforced", "WARN "} {
		got := config.RelaySidecarConfig{ReadAuthMode: raw}.NormalizedReadAuthMode()
		if raw == "WARN " {
			require.Equal(t, config.ReadAuthModeWarn, got)
			continue
		}
		require.Equalf(t, config.ReadAuthModeEnforce, got, "read_auth_mode %q", raw)
	}
	require.Equal(t, config.ReadAuthModeEnforce, config.Defaults().Nostr.Sidecar.ReadAuthMode)
}
