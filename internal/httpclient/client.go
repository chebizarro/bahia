package httpclient

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const (
	DefaultTimeout        = 30 * time.Second
	defaultDialTimeout    = 10 * time.Second
	defaultKeepAlive      = 30 * time.Second
	defaultHeaderTimeout  = 15 * time.Second
	defaultTLSHandshake   = 10 * time.Second
	defaultIdleTimeout    = 90 * time.Second
	defaultExpectContinue = time.Second
)

// Harden returns an independent HTTP client with bounded network operations and
// certificate-verified TLS 1.2 or newer. Opaque custom RoundTrippers are
// preserved, but the overall client timeout is still enforced.
func Harden(client *http.Client, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if client == nil {
		client = &http.Client{}
	}
	clone := *client
	clone.Timeout = timeout
	clone.Transport = hardenTransport(client.Transport)
	return &clone
}

// New returns a bounded, TLS-safe HTTP client.
func New(timeout time.Duration) *http.Client {
	return Harden(nil, timeout)
}

func hardenTransport(roundTripper http.RoundTripper) http.RoundTripper {
	var transport *http.Transport
	switch typed := roundTripper.(type) {
	case nil:
		transport = http.DefaultTransport.(*http.Transport).Clone()
	case *http.Transport:
		transport = typed.Clone()
	default:
		return roundTripper
	}

	if transport.DialContext == nil {
		transport.DialContext = (&net.Dialer{
			Timeout:   defaultDialTimeout,
			KeepAlive: defaultKeepAlive,
		}).DialContext
	}
	if transport.TLSHandshakeTimeout <= 0 {
		transport.TLSHandshakeTimeout = defaultTLSHandshake
	}
	if transport.ResponseHeaderTimeout <= 0 {
		transport.ResponseHeaderTimeout = defaultHeaderTimeout
	}
	if transport.ExpectContinueTimeout <= 0 {
		transport.ExpectContinueTimeout = defaultExpectContinue
	}
	if transport.IdleConnTimeout <= 0 {
		transport.IdleConnTimeout = defaultIdleTimeout
	}

	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.InsecureSkipVerify = false
	if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return transport
}
