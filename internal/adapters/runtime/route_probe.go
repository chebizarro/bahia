package runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/httpclient"
)

// maxRouteProbeBodyBytes bounds how much of a response body a route probe reads.
// It matches the managed-instance probe bound so evidence size stays uniform.
const maxRouteProbeBodyBytes = 512

// RouteProber executes one bounded observation of a managed route from a single
// perspective. It is stateless and safe for concurrent use.
//
// The prober performs no retries and no waiting: a canary observation is a
// single sample, and hysteresis lives in the pure domain evaluator. Callers that
// need retry-until-deadline semantics wrap this in their own bounded loop.
type RouteProber struct{}

// ProbeRoute observes one target and returns a provider-neutral observation.
//
// It never returns an error for a failing route: an unreachable or untrusted
// route is a normal, classifiable outcome, and returning it as data keeps the
// classifier total. An error is returned only when the target itself is invalid
// or the transport cannot be constructed, which is a configuration fault.
func (RouteProber) ProbeRoute(ctx context.Context, target domain.RouteCanaryTarget) (domain.RouteCanaryObservation, error) {
	if err := target.Validate(); err != nil {
		return domain.RouteCanaryObservation{}, err
	}

	started := time.Now()
	observation := domain.RouteCanaryObservation{
		Target:     target,
		ObservedAt: started.UTC(),
	}

	transport, resolver, err := httpclient.ProbeTransport(httpclient.ProbeTransportOptions{
		ResolverAddr: target.ResolverAddr,
		ResolveTo:    target.ResolveTo,
	})
	if err != nil {
		return domain.RouteCanaryObservation{}, err
	}
	// Keep the TLS server name canonical even when the dial address is pinned,
	// so an internal_lan check still proves the certificate is valid for the
	// public hostname rather than for whatever the LAN address presents.
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = target.Hostname

	probeCtx, cancel := context.WithTimeout(ctx, target.Timeout)
	defer cancel()

	// A pinned dial address bypasses DNS entirely, so resolution is vacuously
	// satisfied. Otherwise resolve first so a missing record is reported as
	// dns_unresolved rather than being flattened into a connect failure.
	if strings.TrimSpace(target.ResolveTo) != "" {
		observation.Resolved = true
	} else {
		resolved, resolveErr := resolveTargetHost(probeCtx, resolver, target.Hostname)
		observation.Resolved = resolved
		if !resolved {
			observation.Error = domain.SanitizeEvidence(resolveErr.Error())
			observation.Duration = time.Since(started)
			return observation, nil
		}
	}

	request, err := http.NewRequestWithContext(probeCtx, target.Method, target.URL(), nil)
	if err != nil {
		return domain.RouteCanaryObservation{}, err
	}
	if header := strings.TrimSpace(target.HostHeader); header != "" {
		request.Host = header
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   target.Timeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("redirect limit exceeded")
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()

	response, err := client.Do(request)
	if err != nil {
		// Distinguish a certificate rejection from a plain connection failure so
		// the operator sees "TLS is untrusted" rather than "route is down".
		if tlsErr, ok := certificateError(err); ok {
			observation.TLS.HandshakeCompleted = true
			observation.TLS.ChainVerified = false
			observation.TLSError = domain.SanitizeEvidence(tlsErr)
		} else {
			observation.Error = domain.SanitizeEvidence(err.Error())
		}
		observation.Duration = time.Since(started)
		return observation, nil
	}
	defer response.Body.Close()

	observation.Connected = true
	observation.StatusCode = response.StatusCode
	if response.TLS != nil {
		observation.TLS.HandshakeCompleted = true
		// The handshake only completes when verification succeeded, because the
		// transport never disables verification.
		observation.TLS.ChainVerified = true
		if len(response.TLS.PeerCertificates) > 0 {
			leaf := response.TLS.PeerCertificates[0]
			observation.TLS.NotAfter = leaf.NotAfter
			observation.TLS.Issuer = leaf.Issuer.CommonName
		}
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxRouteProbeBodyBytes))
	if readErr != nil {
		observation.Error = domain.SanitizeEvidence(readErr.Error())
	}
	// Match against the raw bounded body, not the sanitized one, so redaction can
	// never change assertion semantics. Only the stored evidence is sanitized.
	if target.ExpectedBodyContains != "" {
		observation.BodyMatched = strings.Contains(string(body), target.ExpectedBodyContains)
	}
	observation.Body = domain.SanitizeEvidence(string(body))
	observation.Duration = time.Since(started)
	return observation, nil
}

// resolveTargetHost reports whether the hostname resolves, using the probe's
// resolver when one is configured.
func resolveTargetHost(ctx context.Context, resolver *net.Resolver, hostname string) (bool, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, hostname)
	if err != nil {
		return false, err
	}
	if len(addresses) == 0 {
		return false, errors.New("hostname resolved to no addresses")
	}
	return true, nil
}

// certificateError reports whether an HTTP client error was caused by
// certificate verification, and returns a bounded description.
func certificateError(err error) (string, bool) {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return "certificate signed by unknown authority", true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return "certificate is not valid for the requested hostname", true
	}
	var invalidError x509.CertificateInvalidError
	if errors.As(err, &invalidError) {
		if invalidError.Reason == x509.Expired {
			return "certificate has expired or is not yet valid", true
		}
		return "certificate is invalid", true
	}
	var recordError *tls.CertificateVerificationError
	if errors.As(err, &recordError) {
		return "certificate chain verification failed", true
	}
	return "", false
}
