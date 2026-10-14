package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// CaddyConfig addresses an already-provisioned HTTPS server and TLS app.
// Credential is resolved from SecretStore, never from inline configuration.
type CaddyConfig struct {
	AdminURL   string
	ServerID   string
	Credential func(context.Context) (string, error)
	Timeout    time.Duration
}

type CaddyBackend struct {
	cfg    CaddyConfig
	client *http.Client
	mu     sync.Mutex
}

func NewCaddyBackend(cfg CaddyConfig, client *http.Client) (*CaddyBackend, error) {
	cfg.AdminURL = strings.TrimRight(strings.TrimSpace(cfg.AdminURL), "/")
	if cfg.AdminURL == "" || cfg.ServerID == "" {
		return nil, fmt.Errorf("caddy admin URL and server ID are required")
	}
	parsed, err := url.Parse(cfg.AdminURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, fmt.Errorf("caddy admin URL is invalid")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	guardedClient := *client
	guardedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &CaddyBackend{cfg: cfg, client: &guardedClient}, nil
}

type caddyHTTPApp struct {
	Servers map[string]caddyServer `json:"servers"`
}
type caddyServer struct {
	Listen         []string `json:"listen"`
	AutomaticHTTPS struct {
		Disable      bool `json:"disable"`
		DisableCerts bool `json:"disable_certificates"`
	} `json:"automatic_https"`
	Routes []json.RawMessage `json:"routes"`
}
type caddyTLSApp struct {
	Automation struct {
		Policies []json.RawMessage `json:"policies"`
	} `json:"automation"`
}
type caddyState struct {
	http              caddyHTTPApp
	tls               caddyTLSApp
	httpETag, tlsETag string
	route, policy     json.RawMessage
}

func (b *CaddyBackend) validatePlan(plan *domain.DesiredPublicRoutePlan) error {
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	if plan.Provider != "caddy" || plan.Caddy == nil || plan.Caddy.ServerID != b.cfg.ServerID {
		return fmt.Errorf("route plan does not target this Caddy server")
	}
	return nil
}

func caddyPolicyID(plan *domain.DesiredPublicRoutePlan) string { return plan.Caddy.RouteID + ":tls" }

func routeObject(plan *domain.DesiredPublicRoutePlan) json.RawMessage {
	dial := net.JoinHostPort(plan.Proxy.UpstreamHost, fmt.Sprintf("%d", plan.Proxy.UpstreamPort))
	object, _ := json.Marshal(map[string]any{
		"@id":      plan.Caddy.RouteID,
		"match":    []any{map[string]any{"host": []string{plan.Hostname}}},
		"handle":   []any{map[string]any{"@id": plan.Caddy.RouteID + ":proxy:" + plan.ProviderConfigHash, "handler": "reverse_proxy", "upstreams": []any{map[string]string{"dial": dial}}}},
		"terminal": true,
	})
	return object
}
func policyObject(plan *domain.DesiredPublicRoutePlan) json.RawMessage {
	object, _ := json.Marshal(map[string]any{
		"@id": caddyPolicyID(plan), "subjects": []string{plan.Hostname},
		"issuers": []any{map[string]string{"module": "acme"}},
	})
	return object
}

func caddyObjectID(raw json.RawMessage) (string, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", fmt.Errorf("invalid Caddy object")
	}
	var id string
	if value, ok := object["@id"]; ok {
		if err := json.Unmarshal(value, &id); err != nil {
			return "", fmt.Errorf("invalid Caddy object ID")
		}
	}
	return id, nil
}

func caddyRouteHosts(raw json.RawMessage) ([]string, error) {
	var object struct {
		Match  []json.RawMessage `json:"match"`
		Handle []json.RawMessage `json:"handle"`
		Routes []json.RawMessage `json:"routes"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("invalid Caddy route matcher")
	}
	var hosts []string
	for _, matcher := range object.Match {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(matcher, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("invalid Caddy route matcher")
		}
		for key := range fields {
			if key == "expression" || key == "host_regexp" {
				return nil, fmt.Errorf("caddy hostname matcher cannot be safely classified")
			}
		}
		if rawHosts, ok := fields["host"]; ok {
			var selected []string
			if err := json.Unmarshal(rawHosts, &selected); err != nil {
				return nil, fmt.Errorf("invalid Caddy host matcher")
			}
			hosts = append(hosts, selected...)
		}
	}
	for _, child := range append(object.Handle, object.Routes...) {
		nested, err := caddyRouteHosts(child)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, nested...)
	}
	return hosts, nil
}

func caddyRouteUnconstrained(raw json.RawMessage) (bool, error) {
	var object struct {
		Match []map[string]json.RawMessage `json:"match"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return false, fmt.Errorf("invalid Caddy route matcher")
	}
	if len(object.Match) == 0 {
		return true, nil
	}
	for _, matcher := range object.Match {
		if _, ok := matcher["host"]; !ok {
			return true, nil
		}
	}
	return false, nil
}

func hostnameMatches(pattern, hostname string) bool {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	if pattern == hostname {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		return domain.HostnameWithinZone(hostname, pattern[2:]) && hostname != pattern[2:]
	}
	if strings.ContainsAny(pattern, "*{}") {
		return true
	}
	return false
}

func sameJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == 0 && len(b) == 0
	}
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func (b *CaddyBackend) read(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (caddyState, error) {
	var state caddyState
	body, etag, err := b.request(ctx, http.MethodGet, "/config/apps/http", nil, "")
	if err != nil {
		return state, fmt.Errorf("read Caddy HTTP configuration: %w", err)
	}
	if err := json.Unmarshal(body, &state.http); err != nil || state.http.Servers == nil {
		return state, fmt.Errorf("invalid Caddy HTTP configuration")
	}
	body, state.tlsETag, err = b.request(ctx, http.MethodGet, "/config/apps/tls", nil, "")
	if err != nil {
		return state, fmt.Errorf("read Caddy TLS configuration: %w", err)
	}
	if err := json.Unmarshal(body, &state.tls); err != nil {
		return state, fmt.Errorf("invalid Caddy TLS configuration")
	}
	var tlsRoot struct {
		Automation map[string]json.RawMessage `json:"automation"`
	}
	if err := json.Unmarshal(body, &tlsRoot); err != nil || tlsRoot.Automation == nil || tlsRoot.Automation["policies"] == nil || state.tls.Automation.Policies == nil {
		return state, fmt.Errorf("caddy TLS automation policies must be provisioned")
	}
	state.httpETag = etag
	if state.httpETag == "" || state.tlsETag == "" {
		return state, fmt.Errorf("caddy admin API did not provide concurrency ETags")
	}
	server, ok := state.http.Servers[b.cfg.ServerID]
	if !ok {
		return state, fmt.Errorf("configured Caddy HTTPS server is absent")
	}
	if server.Routes == nil {
		return state, fmt.Errorf("configured Caddy server routes array is absent")
	}
	https := false
	for _, listen := range server.Listen {
		if listen == ":443" || strings.HasSuffix(listen, ":443") {
			https = true
		}
	}
	if !https || server.AutomaticHTTPS.Disable || server.AutomaticHTTPS.DisableCerts {
		return state, fmt.Errorf("configured Caddy server does not enable managed HTTPS")
	}
	for serverID, named := range state.http.Servers {
		for routeIndex, route := range named.Routes {
			id, err := caddyObjectID(route)
			if err != nil {
				return state, err
			}
			hosts, err := caddyRouteHosts(route)
			if err != nil {
				return state, err
			}
			unconstrained, err := caddyRouteUnconstrained(route)
			if err != nil {
				return state, err
			}
			if unconstrained && id != plan.Caddy.RouteID {
				return state, fmt.Errorf("hostname %s collides with another Caddy route", plan.Hostname)
			}
			for _, host := range hosts {
				if hostnameMatches(host, plan.Hostname) && id != plan.Caddy.RouteID {
					return state, fmt.Errorf("hostname %s collides with another Caddy route", plan.Hostname)
				}
			}
			if id == plan.Caddy.RouteID {
				if state.route != nil {
					return state, fmt.Errorf("duplicate owned Caddy route")
				}
				if serverID != b.cfg.ServerID {
					return state, fmt.Errorf("owned Caddy route is on another server")
				}
				if routeIndex != 0 {
					return state, fmt.Errorf("owned Caddy route is not first in its server")
				}
				state.route = append(json.RawMessage(nil), route...)
			}
		}
	}
	for policyIndex, policy := range state.tls.Automation.Policies {
		id, err := caddyObjectID(policy)
		if err != nil {
			return state, err
		}
		var object struct {
			Subjects []string `json:"subjects"`
		}
		if err := json.Unmarshal(policy, &object); err != nil {
			return state, fmt.Errorf("invalid Caddy TLS policy")
		}
		for _, subject := range object.Subjects {
			if hostnameMatches(subject, plan.Hostname) && id != caddyPolicyID(plan) {
				return state, fmt.Errorf("hostname %s collides with another Caddy TLS policy", plan.Hostname)
			}
		}
		if id == caddyPolicyID(plan) {
			if state.policy != nil {
				return state, fmt.Errorf("duplicate owned Caddy TLS policy")
			}
			if policyIndex != 0 {
				return state, fmt.Errorf("owned Caddy TLS policy is not first")
			}
			state.policy = append(json.RawMessage(nil), policy...)
		}
	}
	return state, nil
}

func (b *CaddyBackend) Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if err := b.validatePlan(plan); err != nil {
		return err
	}
	_, err := b.read(ctx, plan)
	return err
}

func (b *CaddyBackend) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	_, err := b.ApplyWithCompensation(ctx, plan)
	return err
}

func (b *CaddyBackend) ApplyWithCompensation(ctx context.Context, plan *domain.DesiredPublicRoutePlan) (Compensation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validatePlan(plan); err != nil {
		return nil, err
	}
	before, err := b.read(ctx, plan)
	if err != nil {
		return nil, err
	}
	route, policy := routeObject(plan), policyObject(plan)
	routeChanged, policyChanged := !sameJSON(before.route, route), !sameJSON(before.policy, policy)
	if !routeChanged && !policyChanged {
		return noopCompensation, nil
	}
	if policyChanged {
		method, path := http.MethodPatch, "/id/"+url.PathEscape(caddyPolicyID(plan))
		if before.policy == nil {
			method, path = http.MethodPut, "/config/apps/tls/automation/policies/0"
		}
		if _, _, err = b.request(ctx, method, path, policy, before.tlsETag); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), b.cfg.Timeout)
			defer cancel()
			return nil, errors.Join(fmt.Errorf("apply Caddy TLS policy: %w", err), b.restorePolicy(cleanupCtx, plan, before.policy, policy))
		}
	}
	if routeChanged {
		method, path := http.MethodPatch, "/id/"+url.PathEscape(plan.Caddy.RouteID)
		if before.route == nil {
			method, path = http.MethodPut, "/config/apps/http/servers/"+url.PathEscape(b.cfg.ServerID)+"/routes/0"
		}
		if _, _, err = b.request(ctx, method, path, route, before.httpETag); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), b.cfg.Timeout)
			defer cancel()
			return nil, errors.Join(fmt.Errorf("apply Caddy route: %w", err), b.restore(cleanupCtx, plan, before, route, policy, routeChanged, policyChanged))
		}
	}
	after, err := b.read(ctx, plan)
	if err == nil && (!sameJSON(after.route, route) || !sameJSON(after.policy, policy)) {
		err = fmt.Errorf("caddy managed TLS or route read-back mismatch")
	}
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), b.cfg.Timeout)
		defer cancel()
		restoreErr := b.restore(cleanupCtx, plan, before, route, policy, routeChanged, policyChanged)
		return nil, errors.Join(err, restoreErr)
	}
	return func(compensationCtx context.Context) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.restore(compensationCtx, plan, before, route, policy, routeChanged, policyChanged)
	}, nil
}

func (b *CaddyBackend) restore(ctx context.Context, plan *domain.DesiredPublicRoutePlan, before caddyState, route, policy json.RawMessage, routeChanged, policyChanged bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if routeChanged {
		if err := b.restoreRoute(ctx, plan, before.route, route); err != nil {
			return fmt.Errorf("restore Caddy route: %w", err)
		}
	}
	if policyChanged {
		if err := b.restorePolicy(ctx, plan, before.policy, policy); err != nil {
			return fmt.Errorf("restore Caddy TLS policy: %w", err)
		}
	}
	return nil
}

func (b *CaddyBackend) restoreRoute(ctx context.Context, plan *domain.DesiredPublicRoutePlan, previous, applied json.RawMessage) error {
	state, err := b.read(ctx, plan)
	if err != nil {
		return err
	}
	if sameJSON(state.route, previous) {
		return nil
	}
	if !sameJSON(state.route, applied) {
		return fmt.Errorf("owned Caddy route changed since apply")
	}
	method, body := http.MethodPatch, previous
	if previous == nil {
		method, body = http.MethodDelete, nil
	}
	_, _, err = b.request(ctx, method, "/id/"+url.PathEscape(plan.Caddy.RouteID), body, state.httpETag)
	return err
}
func (b *CaddyBackend) restorePolicy(ctx context.Context, plan *domain.DesiredPublicRoutePlan, previous, applied json.RawMessage) error {
	state, err := b.read(ctx, plan)
	if err != nil {
		return err
	}
	if sameJSON(state.policy, previous) {
		return nil
	}
	if !sameJSON(state.policy, applied) {
		return fmt.Errorf("owned Caddy TLS policy changed since apply")
	}
	method, body := http.MethodPatch, previous
	if previous == nil {
		method, body = http.MethodDelete, nil
	}
	_, _, err = b.request(ctx, method, "/id/"+url.PathEscape(caddyPolicyID(plan)), body, state.tlsETag)
	return err
}

func (b *CaddyBackend) Withdraw(ctx context.Context, plan *domain.DesiredPublicRoutePlan, phase func(context.Context, string) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.validatePlan(plan); err != nil {
		return err
	}
	state, err := b.read(ctx, plan)
	if err != nil {
		return err
	}
	if state.route != nil {
		if !sameJSON(state.route, routeObject(plan)) {
			return fmt.Errorf("owned Caddy route changed; refusing withdrawal")
		}
		if _, _, err := b.request(ctx, http.MethodDelete, "/id/"+url.PathEscape(plan.Caddy.RouteID), nil, state.httpETag); err != nil {
			return fmt.Errorf("withdraw Caddy route: %w", err)
		}
	}
	if phase != nil {
		if err := phase(ctx, "route_withdrawn"); err != nil {
			return err
		}
	}
	state, err = b.read(ctx, plan)
	if err != nil {
		return err
	}
	if state.policy != nil {
		if !sameJSON(state.policy, policyObject(plan)) {
			return fmt.Errorf("owned Caddy TLS policy changed; refusing withdrawal")
		}
		if _, _, err := b.request(ctx, http.MethodDelete, "/id/"+url.PathEscape(caddyPolicyID(plan)), nil, state.tlsETag); err != nil {
			return fmt.Errorf("withdraw Caddy TLS policy: %w", err)
		}
	}
	if phase != nil {
		return phase(ctx, "tls_withdrawn")
	}
	return nil
}

func (b *CaddyBackend) request(ctx context.Context, method, path string, body []byte, etag string) (data []byte, responseETag string, err error) {
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.AdminURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("build Caddy admin request")
	}
	req.Header.Set("Content-Type", "application/json")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	if b.cfg.Credential != nil {
		credential, err := b.cfg.Credential(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("resolve Caddy admin credential")
		}
		if credential == "" {
			return nil, "", fmt.Errorf("empty Caddy admin credential")
		}
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := b.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("caddy admin API request failed")
	}
	defer func() {
		if closeErr := response.Body.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close Caddy admin response")
		}
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", fmt.Errorf("caddy admin API HTTP %d", response.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return nil, "", fmt.Errorf("read Caddy admin response")
	}
	if len(data) > 2<<20 {
		return nil, "", fmt.Errorf("caddy admin response is too large")
	}
	return data, response.Header.Get("ETag"), nil
}
