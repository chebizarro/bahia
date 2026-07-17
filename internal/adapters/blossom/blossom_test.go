package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// testLogger returns a logger that discards all output
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestComputeSHA256(t *testing.T) {
	data := []byte("hello world")
	expected := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	result := ComputeSHA256(data)
	if result != expected {
		t.Errorf("ComputeSHA256() = %s, want %s", result, expected)
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("hello world")
	validHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	invalidHash := "0000000000000000000000000000000000000000000000000000000000000000"

	if !VerifySHA256(data, validHash) {
		t.Error("VerifySHA256() should return true for valid hash")
	}
	if VerifySHA256(data, invalidHash) {
		t.Error("VerifySHA256() should return false for invalid hash")
	}
	// Case insensitive
	if !VerifySHA256(data, "B94D27B9934D3E08A52E52D7DA7DABFAC484EFE37A5380EE9088F7ACE2EFCDE9") {
		t.Error("VerifySHA256() should be case insensitive")
	}
}

func TestParseBlossomURL(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantServer string
		wantHash   string
		wantErr    bool
	}{
		{
			name:       "valid URL",
			url:        "https://blossom.example.com/b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			wantServer: "https://blossom.example.com",
			wantHash:   "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			wantErr:    false,
		},
		{
			name:       "URL with extension",
			url:        "https://blossom.example.com/b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9.txt",
			wantServer: "https://blossom.example.com",
			wantHash:   "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
			wantErr:    false,
		},
		{
			name:    "invalid - no slash",
			url:     "invalidurl",
			wantErr: true,
		},
		{
			name:    "invalid - short hash",
			url:     "https://example.com/abc123",
			wantErr: true,
		},
		{
			name:    "invalid - non-hex hash",
			url:     "https://example.com/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, hash, err := ParseBlossomURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBlossomURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if server != tt.wantServer {
					t.Errorf("server = %s, want %s", server, tt.wantServer)
				}
				if hash != tt.wantHash {
					t.Errorf("hash = %s, want %s", hash, tt.wantHash)
				}
			}
		})
	}
}

func TestClient_Upload(t *testing.T) {
	data := []byte("test content")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	// Create test server - declare first to allow closure reference
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/upload" {
			t.Errorf("expected /upload, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BlobDescriptor{
			URL:      server.URL + "/" + hashStr,
			SHA256:   hashStr,
			Size:     int64(len(data)),
			Uploaded: time.Now(),
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	bd, err := client.Upload(context.Background(), data, "text/plain")
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if bd.SHA256 != hashStr {
		t.Errorf("SHA256 = %s, want %s", bd.SHA256, hashStr)
	}
}

func TestClient_Upload_Fallback(t *testing.T) {
	data := []byte("test content")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	// First server fails
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failServer.Close()

	// Second server succeeds - declare first to allow closure reference
	var goodServer *httptest.Server
	goodServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BlobDescriptor{
			URL:    goodServer.URL + "/" + hashStr,
			SHA256: hashStr,
			Size:   int64(len(data)),
		})
	}))
	defer goodServer.Close()

	client := NewClient(Config{
		Servers:    []string{failServer.URL, goodServer.URL},
		MaxRetries: 1,
	}, testLogger())

	bd, err := client.Upload(context.Background(), data, "")
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if bd.URL != goodServer.URL+"/"+hashStr {
		t.Errorf("URL = %s, want from good server", bd.URL)
	}
}

func TestClient_UploadRejectsMalformedOrUnconfirmedResponse(t *testing.T) {
	data := []byte("test content")
	hash := ComputeSHA256(data)
	for _, tc := range []struct {
		name   string
		status int
		body   func(serverURL string) string
		want   string
	}{
		{name: "malformed", status: http.StatusOK, body: func(string) string { return "not-json" }, want: "decoding upload descriptor"},
		{name: "wrong hash", status: http.StatusOK, body: func(serverURL string) string {
			encoded, _ := json.Marshal(BlobDescriptor{URL: serverURL + "/" + strings.Repeat("0", 64), SHA256: strings.Repeat("0", 64), Size: int64(len(data))})
			return string(encoded)
		}, want: "does not match uploaded hash"},
		{name: "redirect", status: http.StatusTemporaryRedirect, body: func(string) string { return "" }, want: "server returned 307"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", server.URL+"/elsewhere")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body(server.URL)))
			}))
			defer server.Close()

			client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
			client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			descriptor, err := client.Upload(context.Background(), data, "text/plain")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Upload() = (%#v, %v), want error containing %q for hash %s", descriptor, err, tc.want, hash)
			}
			if descriptor != nil {
				t.Fatalf("Upload() descriptor = %#v, want nil", descriptor)
			}
		})
	}
}

func TestClient_Download(t *testing.T) {
	data := []byte("downloaded content")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Write(data)
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	url := server.URL + "/" + hashStr
	result, err := client.Download(context.Background(), url)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(result) != string(data) {
		t.Errorf("Download() = %s, want %s", string(result), string(data))
	}
}

func TestClient_Download_HashMismatch(t *testing.T) {
	data := []byte("actual content")
	wrongHash := "0000000000000000000000000000000000000000000000000000000000000000"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	url := server.URL + "/" + wrongHash
	_, err := client.Download(context.Background(), url)
	if err == nil {
		t.Error("Download() should fail on hash mismatch")
	}
}

func TestClient_DownloadByHash(t *testing.T) {
	data := []byte("content by hash")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+hashStr {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	result, err := client.DownloadByHash(context.Background(), hashStr)
	if err != nil {
		t.Fatalf("DownloadByHash() error = %v", err)
	}
	if string(result) != string(data) {
		t.Errorf("DownloadByHash() = %s, want %s", string(result), string(data))
	}
}

func TestClient_Exists(t *testing.T) {
	hash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		if r.URL.Path == "/"+hash {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	exists, foundServer, err := client.Exists(context.Background(), hash)
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if !exists {
		t.Error("Exists() = false, want true")
	}
	if foundServer != server.URL {
		t.Errorf("foundServer = %s, want %s", foundServer, server.URL)
	}

	// Test non-existent
	exists, _, err = client.Exists(context.Background(), "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if exists {
		t.Error("Exists() = true for non-existent blob")
	}
}

func TestClient_ExistsReturnsIndeterminateOnServerFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())

	exists, foundServer, err := client.Exists(context.Background(), strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(), "indeterminate") {
		t.Fatalf("Exists() = (%v, %q, %v), want indeterminate error", exists, foundServer, err)
	}
	if exists || foundServer != "" {
		t.Fatalf("Exists() = (%v, %q), want false and empty server", exists, foundServer)
	}
}

func TestClient_GetStats(t *testing.T) {
	data := []byte("stats test")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			json.NewEncoder(w).Encode(BlobDescriptor{URL: server.URL + "/" + hashStr, SHA256: hashStr, Size: int64(len(data))})
		} else {
			w.Write(data)
		}
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:    []string{server.URL},
		MaxRetries: 1,
	}, testLogger())

	// Do some operations
	client.Upload(context.Background(), data, "")
	client.Download(context.Background(), server.URL+"/"+hashStr)

	stats := client.GetStats()
	if stats[server.URL]["uploads"] != 1 {
		t.Errorf("uploads = %d, want 1", stats[server.URL]["uploads"])
	}
	if stats[server.URL]["downloads"] != 1 {
		t.Errorf("downloads = %d, want 1", stats[server.URL]["downloads"])
	}
}

func TestClient_NoServers(t *testing.T) {
	client := NewClient(Config{
		Servers: []string{},
	}, testLogger())

	_, err := client.Upload(context.Background(), []byte("test"), "")
	if err == nil {
		t.Error("Upload() should fail with no servers")
	}

	_, err = client.DownloadByHash(context.Background(), "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9")
	if err == nil {
		t.Error("DownloadByHash() should fail with no servers")
	}
}

func TestClient_CreateAuthHeader_NoPrivateKey(t *testing.T) {
	client := NewClient(Config{
		Servers: []string{"https://example.com"},
	}, testLogger())

	header, err := client.createAuthHeader(context.Background(), "https://example.com/upload", "PUT", "abc123")
	if err != nil {
		t.Fatalf("createAuthHeader() error = %v", err)
	}
	if header != "" {
		t.Errorf("createAuthHeader() = %q, want empty string when no private key", header)
	}
}

func TestClient_CreateAuthHeader_WithPrivateKey(t *testing.T) {
	// Generate a test private key
	privateKey := nostrutil.GeneratePrivateKeyHex()
	pubkey, _ := nostrutil.PublicKeyHexFromPrivateKeyHex(privateKey)

	client := NewClient(Config{
		Servers:       []string{"https://example.com"},
		PrivateKeyHex: privateKey,
	}, testLogger())

	url := "https://example.com/upload"
	method := "PUT"
	contentHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	header, err := client.createAuthHeader(context.Background(), url, method, contentHash)
	if err != nil {
		t.Fatalf("createAuthHeader() error = %v", err)
	}

	// Should start with "Nostr "
	if !strings.HasPrefix(header, "Nostr ") {
		t.Fatalf("header should start with 'Nostr ', got %q", header)
	}

	// Decode and validate the event
	encodedEvent := strings.TrimPrefix(header, "Nostr ")
	eventJSON, err := base64.RawURLEncoding.DecodeString(encodedEvent)
	if err != nil {
		t.Fatalf("failed to decode base64url: %v", err)
	}

	var event nostr.Event
	if err := json.Unmarshal(eventJSON, &event); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	// Verify event properties — BUD-11 requires kind 24242
	if event.Kind != 24242 {
		t.Errorf("event.Kind = %d, want 24242", event.Kind)
	}
	if event.PubKey.Hex() != pubkey {
		t.Errorf("event.PubKey = %s, want %s", event.PubKey.Hex(), pubkey)
	}

	// Verify tags per BUD-11
	foundT := false
	foundExpiration := false
	foundX := false
	for _, tag := range event.Tags {
		if len(tag) >= 2 {
			switch tag[0] {
			case "t":
				if tag[1] != "upload" {
					t.Errorf("t tag = %s, want upload", tag[1])
				}
				foundT = true
			case "expiration":
				foundExpiration = true
			case "x":
				if tag[1] != contentHash {
					t.Errorf("x tag = %s, want %s", tag[1], contentHash)
				}
				foundX = true
			}
		}
	}
	if !foundT {
		t.Error("missing 't' tag")
	}
	if !foundExpiration {
		t.Error("missing 'expiration' tag")
	}
	if !foundX {
		t.Error("missing 'x' tag")
	}

	// Verify signature
	if !event.VerifySignature() {
		t.Error("event signature is invalid")
	}
}

func TestClient_CreateAuthHeader_NoPayload(t *testing.T) {
	privateKey := nostrutil.GeneratePrivateKeyHex()

	client := NewClient(Config{
		Servers:       []string{"https://example.com"},
		PrivateKeyHex: privateKey,
	}, testLogger())

	header, err := client.createAuthHeader(context.Background(), "https://example.com/upload", "GET", "")
	if err != nil {
		t.Fatalf("createAuthHeader() error = %v", err)
	}

	// Decode and check no x tag when hash is empty
	encodedEvent := strings.TrimPrefix(header, "Nostr ")
	eventJSON, _ := base64.RawURLEncoding.DecodeString(encodedEvent)

	var event nostr.Event
	json.Unmarshal(eventJSON, &event)

	for _, tag := range event.Tags {
		if len(tag) >= 1 && tag[0] == "x" {
			t.Error("should not have 'x' tag when contentHash is empty")
		}
	}
}

func TestClient_Upload_WithAuth(t *testing.T) {
	privateKey := nostrutil.GeneratePrivateKeyHex()
	data := []byte("authenticated upload")
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	var receivedAuth string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BlobDescriptor{
			URL:    server.URL + "/" + hashStr,
			SHA256: hashStr,
			Size:   int64(len(data)),
		})
	}))
	defer server.Close()

	client := NewClient(Config{
		Servers:       []string{server.URL},
		PrivateKeyHex: privateKey,
		MaxRetries:    1,
	}, testLogger())

	_, err := client.Upload(context.Background(), data, "text/plain")
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if receivedAuth == "" {
		t.Error("server did not receive Authorization header")
	}
	if !strings.HasPrefix(receivedAuth, "Nostr ") {
		t.Errorf("Authorization header should start with 'Nostr ', got %q", receivedAuth)
	}
}

func TestClient_UploadFile(t *testing.T) {
	data := []byte("file upload content")
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "blob.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var uploadedLen int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/upload" {
			t.Fatalf("expected /upload path, got %s", r.URL.Path)
		}
		uploadedLen = r.ContentLength
		if r.Header.Get("X-SHA-256") != hash {
			t.Fatalf("unexpected hash header: %s", r.Header.Get("X-SHA-256"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(BlobDescriptor{URL: server.URL + "/" + hash, SHA256: hash, Size: int64(len(data))})
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
	bd, err := client.UploadFile(context.Background(), path, "application/octet-stream", "")
	if err != nil {
		t.Fatalf("UploadFile() error = %v", err)
	}
	if bd.SHA256 != hash {
		t.Fatalf("got hash %s want %s", bd.SHA256, hash)
	}
	if uploadedLen != int64(len(data)) {
		t.Fatalf("uploaded length %d want %d", uploadedLen, len(data))
	}
}

func TestClient_DownloadAuthHeaderFailureDoesNotFallbackUnauthenticated(t *testing.T) {
	data := []byte("private content")
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])
	called := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("server should not receive unauthenticated fallback request")
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, PrivateKeyHex: "not-a-private-key", MaxRetries: 1}, testLogger())
	_, err := client.Download(context.Background(), server.URL+"/"+hash)
	if !errors.Is(err, ErrAuthHeader) {
		t.Fatalf("Download() error = %v, want ErrAuthHeader", err)
	}
	if called {
		t.Fatal("download attempted HTTP request after auth preparation failed")
	}
}

func TestClient_ProxyAuthHeaderFailureDoesNotFallbackUnauthenticated(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("server should not receive unauthenticated fallback request")
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, PrivateKeyHex: "not-a-private-key", MaxRetries: 1}, testLogger())
	url := server.URL + "/" + strings.Repeat("a", 64)

	if _, err := client.HeadByURL(context.Background(), url); !errors.Is(err, ErrAuthHeader) {
		t.Fatalf("HeadByURL() error = %v, want ErrAuthHeader", err)
	}
	if _, err := client.OpenStreamByURL(context.Background(), url); !errors.Is(err, ErrAuthHeader) {
		t.Fatalf("OpenStreamByURL() error = %v, want ErrAuthHeader", err)
	}
	if called {
		t.Fatal("proxy attempted HTTP request after auth preparation failed")
	}
}

func TestClient_HeadByURLAndOpenStreamByURL(t *testing.T) {
	data := []byte("stream-me")
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", "application/vnd.oci.image.layer.v1.tar")
			w.Header().Set("Content-Length", "9")
			w.Header().Set("Etag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.layer.v1.tar")
		w.Header().Set("Etag", `"etag-1"`)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
	url := server.URL + "/" + hash

	head, err := client.HeadByURL(context.Background(), url)
	if err != nil {
		t.Fatalf("HeadByURL() error = %v", err)
	}
	if !head.Exists {
		t.Fatalf("expected blob to exist")
	}
	if head.ContentLength != int64(len(data)) {
		t.Fatalf("ContentLength=%d want %d", head.ContentLength, len(data))
	}
	if got := head.Header.Get("Etag"); got == "" {
		t.Fatalf("expected Etag header")
	}

	stream, err := client.OpenStreamByURL(context.Background(), url)
	if err != nil {
		t.Fatalf("OpenStreamByURL() error = %v", err)
	}
	defer stream.Close()

	if stream.ContentType != "application/vnd.oci.image.layer.v1.tar" {
		t.Fatalf("unexpected content type: %s", stream.ContentType)
	}
	body, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatalf("ReadAll(stream.Body) error = %v", err)
	}
	if string(body) != string(data) {
		t.Fatalf("stream body = %q want %q", string(body), string(data))
	}
}
