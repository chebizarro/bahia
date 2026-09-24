package controlplane

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestBuildDesiredStateSummaryProxyUpstreamMatchesOriginURL(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		originURL string
		want      string
	}{
		{name: "hostname", host: "edge-01.internal", originURL: "http://edge-01.internal:8080", want: "http://edge-01.internal:8080"},
		{name: "IPv4", host: "192.0.2.10", originURL: "http://192.0.2.10:8080", want: "http://192.0.2.10:8080"},
		{name: "IPv6", host: "2001:db8::10", originURL: "http://[2001:db8::10]:8080", want: "http://[2001:db8::10]:8080"},
		{name: "bracketed IPv6", host: "[2001:db8::10]", originURL: "http://[2001:db8::10]:8080", want: "http://[2001:db8::10]:8080"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := &domain.DesiredServiceSpec{
				PublicRoute: &domain.DesiredPublicRoutePlan{
					Tunnel: domain.DesiredPublicRouteTunnel{OriginURL: test.originURL},
					Proxy: domain.DesiredPublicRouteProxy{
						UpstreamScheme: "http",
						UpstreamHost:   test.host,
						UpstreamPort:   8080,
					},
				},
			}
			summary := buildDesiredStateSummary(state)
			if summary.PublicRoute.ProxyUpstream != test.want {
				t.Fatalf("ProxyUpstream = %q, want %q", summary.PublicRoute.ProxyUpstream, test.want)
			}
			if summary.PublicRoute.ProxyUpstream != summary.PublicRoute.TunnelOriginURL {
				t.Fatalf("proxy upstream %q does not match tunnel origin %q", summary.PublicRoute.ProxyUpstream, summary.PublicRoute.TunnelOriginURL)
			}
		})
	}
}
