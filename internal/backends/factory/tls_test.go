package factory

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/config"
)

type tlsFixture struct {
	cert            *x509.Certificate
	key             ed25519.PrivateKey
	certPEM, keyPEM string
	pair            tls.Certificate
}

func makeTLSFixture(t *testing.T, label string, parent *tlsFixture, server, expired, wrongHost bool) tlsFixture {
	t.Helper()
	seed := sha256.Sum256([]byte(label))
	key := ed25519.NewKeyFromSeed(seed[:])
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: label}, NotBefore: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
	if expired {
		template.NotBefore = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		template.NotAfter = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	issuer, signingKey := template, key
	if parent == nil {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	} else {
		issuer = parent.cert
		signingKey = parent.key
	}
	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		if wrongHost {
			template.DNSNames = []string{"wrong.example"}
		} else {
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
	} else if parent != nil {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, key.Public(), signingKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return tlsFixture{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM, pair: pair}
}

func tlsPayload(t *testing.T, ca, cert, key string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"ca_cert": ca, "client_cert": cert, "client_key": key})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestTLSSecretMaterialValidation(t *testing.T) {
	ca := makeTLSFixture(t, "test-ca", nil, false, false, false)
	client := makeTLSFixture(t, "test-client", &ca, false, false, false)
	other := makeTLSFixture(t, "other-client", &ca, false, false, false)
	for _, payload := range []string{
		"", `{}`, `{"ca_cert":"garbage"}`, `{"ca_cert":17}`, `{"client_key":"secret"}`, `{"ca_cert":"bad","unknown":"private"}`,
		tlsPayload(t, ca.certPEM+"trailing-garbage", "", ""), tlsPayload(t, "prefix-garbage"+ca.certPEM, "", ""),
		tlsPayload(t, "", client.certPEM, ""), tlsPayload(t, "", client.certPEM, other.keyPEM), tlsPayload(t, "", client.certPEM, "bad-key"),
		tlsPayload(t, "", client.certPEM+"junk", client.keyPEM), tlsPayload(t, "", client.certPEM, "junk"+client.keyPEM), tlsPayload(t, "", client.certPEM, client.keyPEM+"junk"),
	} {
		if _, _, err := parseTLSSecret(payload); err == nil {
			t.Fatal("invalid TLS payload accepted")
		}
	}
	parsed, _, err := parseTLSSecret(tlsPayload(t, ca.certPEM, client.certPEM, client.keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.InsecureSkipVerify || parsed.RootCAs == nil || len(parsed.Certificates) != 1 || parsed.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS verification configuration is unsafe")
	}
	for _, typ := range []string{"nexus", "pulp"} {
		if _, err := BuildBackend(config.PackageBackendConfig{Type: typ, BaseURL: "https://registry.example", InsecureSkipVerify: true}); err == nil {
			t.Fatal("insecure TLS option accepted")
		}
	}
}

func TestBackendVerifiedTLSAndMutualTLS(t *testing.T) {
	ca := makeTLSFixture(t, "trusted-ca", nil, false, false, false)
	otherCA := makeTLSFixture(t, "untrusted-ca", nil, false, false, false)
	client := makeTLSFixture(t, "client", &ca, false, false, false)
	for _, typ := range []string{"nexus", "pulp"} {
		for _, test := range []struct {
			name                                                     string
			wrongHost, expired, wrongCA, mutual, omitClient, noTrust bool
		}{
			{name: "custom CA"}, {name: "mutual TLS", mutual: true}, {name: "missing client", mutual: true, omitClient: true},
			{name: "untrusted CA", wrongCA: true}, {name: "wrong hostname", wrongHost: true}, {name: "expired certificate", expired: true}, {name: "no trust secret", noTrust: true},
		} {
			t.Run(typ+"/"+test.name, func(t *testing.T) {
				serverCert := makeTLSFixture(t, "server", &ca, true, test.expired, test.wrongHost)
				pool := x509.NewCertPool()
				pool.AddCert(ca.cert)
				calls := 0
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if test.mutual && len(r.TLS.PeerCertificates) != 1 {
						t.Error("client certificate not verified")
					}
					_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
				}))
				server.Config.ErrorLog = log.New(io.Discard, "", 0)
				server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert.pair}, ClientCAs: pool}
				if test.mutual {
					server.TLS.ClientAuth = tls.RequireAndVerifyClientCert
				}
				server.StartTLS()
				defer server.Close()
				caPEM := ca.certPEM
				if test.wrongCA {
					caPEM = otherCA.certPEM
				}
				certPEM, keyPEM := "", ""
				if test.mutual && !test.omitClient {
					certPEM = client.certPEM
					keyPEM = client.keyPEM
				}
				cfg := config.PackageBackendConfig{Type: typ, BaseURL: server.URL, TLSSecretRef: "tls-opaque"}
				if test.noTrust {
					cfg.TLSSecretRef = ""
				}
				backend, err := BuildBackendWithSecrets(context.Background(), cfg, mapResolver{"tls-opaque": tlsPayload(t, caPEM, certPEM, keyPEM)})
				if err != nil {
					t.Fatal(err)
				}
				_, err = backend.ObserveRepository(context.Background(), testRepo("repo"))
				wantError := test.wrongHost || test.expired || test.wrongCA || test.omitClient || test.noTrust
				if (err != nil) != wantError {
					t.Fatalf("request error=%v wantError=%v", err, wantError)
				}
				if wantError && calls != 0 {
					t.Fatal("failed TLS request reached HTTP handler")
				}
				if !wantError && calls != 1 {
					t.Fatal("verified request did not reach HTTP handler")
				}
				if err != nil && strings.Contains(err.Error(), client.keyPEM) {
					t.Fatal("TLS private key in error")
				}
			})
		}
	}
}
