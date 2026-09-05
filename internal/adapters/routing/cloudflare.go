package routing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

type CloudflareConfig struct {
	APIBaseURL         string
	APIToken           string
	AccountID          string
	TunnelID           string
	ZoneIDs            map[string]string
	Timeout            time.Duration
	VerifyTimeout      time.Duration
	VerifyResolverAddr string
}

type CloudflareBackend struct {
	cfg            CloudflareConfig
	client         *http.Client
	verifyClient   *http.Client
	verifyResolver *net.Resolver
	verifyBackoff  time.Duration
	applyMu        sync.Mutex
}

func NewCloudflareBackend(cfg CloudflareConfig, client *http.Client) (*CloudflareBackend, error) {
	cfg.APIBaseURL = strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = "https://api.cloudflare.com/client/v4"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.VerifyTimeout <= 0 {
		cfg.VerifyTimeout = 30 * time.Second
	}
	if strings.TrimSpace(cfg.APIToken) == "" || strings.TrimSpace(cfg.AccountID) == "" || strings.TrimSpace(cfg.TunnelID) == "" {
		return nil, fmt.Errorf("Cloudflare API token, account ID, and tunnel ID are required")
	}
	if len(cfg.ZoneIDs) == 0 {
		return nil, fmt.Errorf("at least one Cloudflare zone ID is required")
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.VerifyResolverAddr = strings.TrimSpace(cfg.VerifyResolverAddr)
	verifyTransport, verifyResolver := newVerifyTransport(cfg.VerifyResolverAddr)
	return &CloudflareBackend{cfg: cfg, client: client, verifyResolver: verifyResolver, verifyBackoff: 2 * time.Second, verifyClient: &http.Client{
		Transport: verifyTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && !strings.EqualFold(req.URL.Hostname(), via[0].URL.Hostname()) {
				return http.ErrUseLastResponse
			}
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}}, nil
}

func newVerifyTransport(resolverAddr string) (*http.Transport, *net.Resolver) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(resolverAddr) == "" || strings.EqualFold(strings.TrimSpace(resolverAddr), "system") {
		return transport, nil
	}
	resolverAddress := strings.TrimSpace(resolverAddr)
	resolverDialer := &net.Dialer{}
	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return resolverDialer.DialContext(ctx, network, resolverAddress)
		},
	}
	transportDialer := &net.Dialer{Resolver: resolver}
	transport.DialContext = transportDialer.DialContext
	return transport, resolver
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}
type cfTunnelResult struct {
	Config map[string]any `json:"config"`
}
type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Comment string `json:"comment"`
}

func cloudflareOwnershipMarker(sourceCoordinate string) string {
	sum := sha256.Sum256([]byte(sourceCoordinate))
	return "bahia:" + hex.EncodeToString(sum[:])
}

func (b *CloudflareBackend) Check(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	if err := b.validatePlan(plan); err != nil {
		return err
	}
	records, err := b.listDNS(ctx, plan)
	if err != nil {
		return err
	}
	ownership := cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)
	// Do not match the raw coordinate as a legacy marker: Cloudflare rejected every
	// pre-fix apply because those 123-character comments exceeded its 100-character limit.
	owned := false
	for _, record := range records {
		if record.Comment == ownership {
			owned = true
			continue
		}
		return fmt.Errorf("hostname %s collides with an unmanaged DNS record", plan.Hostname)
	}
	tunnelConfig, err := b.getTunnelConfig(ctx)
	if err != nil {
		return err
	}
	ingress, err := tunnelIngress(tunnelConfig)
	if err != nil {
		return err
	}
	for _, rule := range ingress {
		host, _ := rule["hostname"].(string)
		service, _ := rule["service"].(string)
		if strings.EqualFold(strings.TrimSpace(host), plan.Hostname) && strings.TrimSpace(service) != plan.Tunnel.OriginURL && !owned {
			return fmt.Errorf("hostname %s already belongs to another tunnel route", plan.Hostname)
		}
	}
	return nil
}

func (b *CloudflareBackend) Apply(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	b.applyMu.Lock()
	defer b.applyMu.Unlock()
	if err := b.Check(ctx, plan); err != nil {
		return err
	}
	previousConfig, err := b.getTunnelConfig(ctx)
	if err != nil {
		return err
	}
	previousIngress, err := tunnelIngress(previousConfig)
	if err != nil {
		return err
	}
	previousDNS, err := b.listDNS(ctx, plan)
	if err != nil {
		return err
	}
	desiredIngress := upsertIngress(previousIngress, plan.Hostname, plan.Tunnel.OriginURL)
	desiredConfig, err := cloneTunnelConfig(previousConfig)
	if err != nil {
		return err
	}
	desiredConfig["ingress"] = desiredIngress
	if err := b.putTunnelConfig(ctx, desiredConfig); err != nil {
		return fmt.Errorf("apply tunnel ingress: %w", err)
	}
	dnsChanged := false
	rollback := func(cause error) error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), b.cfg.Timeout)
		defer cancel()
		if dnsChanged {
			if err := b.restoreDNS(cleanupCtx, plan, previousDNS); err != nil {
				return fmt.Errorf("%w; DNS compensation failed and tunnel ingress was retained for retry: %v", cause, err)
			}
		}
		if err := b.putTunnelConfig(cleanupCtx, previousConfig); err != nil {
			return fmt.Errorf("%w; compensation failed restoring tunnel after DNS withdrawal: %v", cause, err)
		}
		return fmt.Errorf("%w; previous public route restored", cause)
	}
	if err := b.upsertDNS(ctx, plan, previousDNS); err != nil {
		return rollback(fmt.Errorf("publish DNS: %w", err))
	}
	dnsChanged = true
	if err := b.verifyHTTPS(ctx, plan); err != nil {
		return rollback(fmt.Errorf("verify managed HTTPS: %w", err))
	}
	return nil
}

func (b *CloudflareBackend) validatePlan(plan *domain.DesiredPublicRoutePlan) error {
	if err := domain.ValidateDesiredPublicRoute(plan); err != nil {
		return err
	}
	if plan.Provider != "cloudflare_tunnel" || plan.Tunnel.TunnelRef != b.cfg.TunnelID {
		return fmt.Errorf("route plan does not target this Cloudflare tunnel")
	}
	if _, ok := b.cfg.ZoneIDs[plan.Zone]; !ok {
		return fmt.Errorf("Cloudflare zone %s is not configured", plan.Zone)
	}
	return nil
}

func (b *CloudflareBackend) getTunnelConfig(ctx context.Context) (map[string]any, error) {
	var result cfTunnelResult
	if err := b.do(ctx, http.MethodGet, fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", b.cfg.AccountID, b.cfg.TunnelID), nil, &result); err != nil {
		return nil, fmt.Errorf("read tunnel configuration: %w", err)
	}
	if result.Config == nil {
		result.Config = map[string]any{}
	}
	return result.Config, nil
}

func tunnelIngress(config map[string]any) ([]map[string]any, error) {
	raw, ok := config["ingress"]
	if !ok || raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode tunnel ingress: %w", err)
	}
	var ingress []map[string]any
	if err := json.Unmarshal(data, &ingress); err != nil {
		return nil, fmt.Errorf("decode tunnel ingress: %w", err)
	}
	return ingress, nil
}

func cloneTunnelConfig(config map[string]any) (map[string]any, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode tunnel configuration: %w", err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, fmt.Errorf("decode tunnel configuration: %w", err)
	}
	if cloned == nil {
		cloned = map[string]any{}
	}
	return cloned, nil
}

func (b *CloudflareBackend) putTunnelConfig(ctx context.Context, config map[string]any) error {
	body := map[string]any{"config": config}
	return b.do(ctx, http.MethodPut, fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", b.cfg.AccountID, b.cfg.TunnelID), body, nil)
}

func upsertIngress(current []map[string]any, hostname, origin string) []map[string]any {
	out := make([]map[string]any, 0, len(current)+2)
	var catchAll map[string]any
	for _, rule := range current {
		host, hasHost := rule["hostname"].(string)
		if !hasHost || strings.TrimSpace(host) == "" {
			if catchAll == nil {
				catchAll = rule
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(host), hostname) {
			continue
		}
		out = append(out, rule)
	}
	out = append(out, map[string]any{"hostname": hostname, "service": origin})
	if catchAll == nil {
		catchAll = map[string]any{"service": "http_status:404"}
	}
	return append(out, catchAll)
}

func (b *CloudflareBackend) listDNS(ctx context.Context, plan *domain.DesiredPublicRoutePlan) ([]cfDNSRecord, error) {
	zoneID := b.cfg.ZoneIDs[plan.Zone]
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s", zoneID, url.QueryEscape(plan.Hostname))
	var records []cfDNSRecord
	if err := b.do(ctx, http.MethodGet, path, nil, &records); err != nil {
		return nil, fmt.Errorf("read DNS records: %w", err)
	}
	return records, nil
}

func (b *CloudflareBackend) upsertDNS(ctx context.Context, plan *domain.DesiredPublicRoutePlan, current []cfDNSRecord) error {
	ownership := cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)
	payload := cfDNSRecord{Type: plan.DNS.Type, Name: plan.Hostname, Content: plan.DNS.Value, TTL: plan.DNS.TTL, Proxied: plan.DNS.Proxied, Comment: ownership}
	zoneID := b.cfg.ZoneIDs[plan.Zone]
	for _, record := range current {
		if record.Comment == ownership {
			return b.do(ctx, http.MethodPut, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, record.ID), payload, nil)
		}
	}
	return b.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID), payload, nil)
}

func (b *CloudflareBackend) restoreDNS(ctx context.Context, plan *domain.DesiredPublicRoutePlan, previous []cfDNSRecord) error {
	current, err := b.listDNS(ctx, plan)
	if err != nil {
		return err
	}
	zoneID := b.cfg.ZoneIDs[plan.Zone]
	ownership := cloudflareOwnershipMarker(plan.DNS.SourceCoordinate)
	for _, record := range current {
		if record.Comment == ownership {
			if err := b.do(ctx, http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, record.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	for _, record := range previous {
		if record.Comment == ownership {
			record.ID = ""
			if err := b.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID), record, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *CloudflareBackend) verifyHTTPS(ctx context.Context, plan *domain.DesiredPublicRoutePlan) error {
	verifyCtx, cancel := context.WithTimeout(ctx, b.cfg.VerifyTimeout)
	defer cancel()

	var lastDNSError, lastHTTPError error
	for {
		if b.verifyResolver != nil {
			addresses, err := b.verifyResolver.LookupIPAddr(verifyCtx, plan.Hostname)
			if err != nil {
				var dnsErr *net.DNSError
				if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
					lastDNSError = fmt.Errorf("public DNS has no record for %s (resolver %s)", plan.Hostname, b.cfg.VerifyResolverAddr)
				} else {
					lastDNSError = fmt.Errorf("public DNS resolution failed for %s (resolver %s): %w", plan.Hostname, b.cfg.VerifyResolverAddr, err)
				}
			} else if len(addresses) == 0 {
				lastDNSError = fmt.Errorf("public DNS has no record for %s (resolver %s)", plan.Hostname, b.cfg.VerifyResolverAddr)
			} else {
				lastDNSError = nil
				req, err := http.NewRequestWithContext(verifyCtx, http.MethodGet, "https://"+plan.Hostname+plan.Proxy.HealthPath, nil)
				if err != nil {
					return err
				}
				lastHTTPError = b.doVerifyRequest(req)
				if lastHTTPError == nil {
					return nil
				}
			}
		} else {
			req, err := http.NewRequestWithContext(verifyCtx, http.MethodGet, "https://"+plan.Hostname+plan.Proxy.HealthPath, nil)
			if err != nil {
				return err
			}
			lastHTTPError = b.doVerifyRequest(req)
			if lastHTTPError == nil {
				return nil
			}
		}

		timer := time.NewTimer(b.verifyBackoff)
		select {
		case <-verifyCtx.Done():
			timer.Stop()
			return verifyFailure(lastDNSError, lastHTTPError)
		case <-timer.C:
		}
	}
}

func (b *CloudflareBackend) doVerifyRequest(req *http.Request) error {
	resp, err := b.verifyClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("close health response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func verifyFailure(lastDNSError, lastHTTPError error) error {
	dnsDetail := "none"
	if lastDNSError != nil {
		dnsDetail = lastDNSError.Error()
	}
	httpDetail := "none"
	if lastHTTPError != nil {
		httpDetail = lastHTTPError.Error()
	} else if lastDNSError != nil {
		httpDetail = "not attempted because public DNS resolution failed"
	}
	return fmt.Errorf("HTTPS verification failed: last DNS failure: %s; last HTTP failure: %s", dnsDetail, httpDetail)
}

func (b *CloudflareBackend) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.cfg.APIBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("Cloudflare API request failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("read Cloudflare API response: %w", err)
	}
	var envelope cfEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode Cloudflare API response (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		message := "request rejected"
		if len(envelope.Errors) > 0 && strings.TrimSpace(envelope.Errors[0].Message) != "" {
			message = envelope.Errors[0].Message
		}
		return fmt.Errorf("Cloudflare API HTTP %d: %s", resp.StatusCode, message)
	}
	if out != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decode Cloudflare API result: %w", err)
		}
	}
	return nil
}
