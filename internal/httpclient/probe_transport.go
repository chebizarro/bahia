package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// SystemResolverAddr is the sentinel that selects the host's default resolver.
const SystemResolverAddr = "system"

// ProbeTransportOptions selects how a probe reaches its target.
//
// The two overrides are deliberately distinct because they model different real
// deployments. ResolverAddr changes which DNS server answers, which is how a
// public-edge check avoids being satisfied by split-horizon LAN DNS. ResolveTo
// bypasses DNS entirely and pins the dial address, which is how a LAN check
// reaches the internal vhost the way split DNS would while keeping the canonical
// hostname for TLS server name and Host header.
type ProbeTransportOptions struct {
	// ResolverAddr is a host:port DNS server, "system", or empty for the system
	// resolver. Mutually exclusive with ResolveTo.
	ResolverAddr string
	// ResolveTo is a literal IP address that every dial is redirected to,
	// preserving the original port. Mutually exclusive with ResolverAddr.
	ResolveTo string
}

// Resolver returns the custom DNS resolver implied by the options, or nil when
// the system resolver applies. Callers that need to distinguish a DNS failure
// from a connection failure resolve through this before issuing the request.
func (o ProbeTransportOptions) Resolver() (*net.Resolver, error) {
	addr := strings.TrimSpace(o.ResolverAddr)
	if addr == "" || strings.EqualFold(addr, SystemResolverAddr) {
		return nil, nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("probe resolver address must be host:port: %w", err)
	}
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}, nil
}

// ProbeTransport builds a hardened transport honoring the options, together with
// the custom resolver it uses, if any.
//
// TLS verification is always enabled: the transport comes from hardenTransport,
// which unconditionally clears InsecureSkipVerify. There is no path through this
// constructor that can disable certificate checking, so a probe can never report
// a route as healthy over an untrusted chain.
func ProbeTransport(options ProbeTransportOptions) (*http.Transport, *net.Resolver, error) {
	resolveTo := strings.TrimSpace(options.ResolveTo)
	if resolveTo != "" && strings.TrimSpace(options.ResolverAddr) != "" {
		return nil, nil, fmt.Errorf("probe transport: resolver_addr and resolve_to are mutually exclusive")
	}

	transport, ok := hardenTransport(nil).(*http.Transport)
	if !ok {
		return nil, nil, fmt.Errorf("probe transport: hardened transport has unexpected type")
	}

	if resolveTo != "" {
		if net.ParseIP(resolveTo) == nil {
			return nil, nil, fmt.Errorf("probe transport: resolve_to must be an IP address, got %q", resolveTo)
		}
		dialer := &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultKeepAlive}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(resolveTo, port))
		}
		return transport, nil, nil
	}

	resolver, err := options.Resolver()
	if err != nil {
		return nil, nil, err
	}
	if resolver != nil {
		dialer := &net.Dialer{Timeout: defaultDialTimeout, KeepAlive: defaultKeepAlive, Resolver: resolver}
		transport.DialContext = dialer.DialContext
	}
	return transport, resolver, nil
}
