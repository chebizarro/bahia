package blossom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x00}

func TestClientStoreAvatarReturnsBlossomRef(t *testing.T) {
	h := sha256.Sum256(tinyPNG)
	hash := hex.EncodeToString(h[:])

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodPut + " /upload":
			if got := r.Header.Get("Content-Type"); got != "image/png" {
				t.Fatalf("Content-Type = %q, want image/png", got)
			}
			if got := r.Header.Get("X-SHA-256"); got != hash {
				t.Fatalf("X-SHA-256 = %q, want %q", got, hash)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(BlobDescriptor{URL: server.URL + "/" + hash, SHA256: hash, Size: int64(len(tinyPNG)), Type: "image/png"})
		case http.MethodGet + " /" + hash:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(tinyPNG)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
	stored, err := client.StoreAvatar(context.Background(), tinyPNG, "image/png", "")
	if err != nil {
		t.Fatalf("StoreAvatar() error = %v", err)
	}
	if stored.Ref != "blossom:"+hash {
		t.Fatalf("Ref = %q, want blossom:%s", stored.Ref, hash)
	}
	if stored.Hash != hash || stored.URL != server.URL+"/"+hash || stored.Fallback {
		t.Fatalf("unexpected store result: %#v", stored)
	}

	preview, err := client.ResolveAvatarRef(context.Background(), stored.Ref)
	if err != nil {
		t.Fatalf("ResolveAvatarRef() error = %v", err)
	}
	if preview.Hash != hash || preview.ContentType != "image/png" || string(preview.Data) != string(tinyPNG) {
		t.Fatalf("unexpected preview: %#v", preview)
	}
}

func TestClientStoreAvatarFallsBackToDirectURL(t *testing.T) {
	fallback := "https://cdn.example/avatar.png"
	stored, err := (*Client)(nil).StoreAvatar(context.Background(), tinyPNG, "image/png", fallback)
	if err != nil {
		t.Fatalf("StoreAvatar() error = %v", err)
	}
	if stored.Ref != fallback || stored.URL != fallback || !stored.Fallback {
		t.Fatalf("unexpected fallback result: %#v", stored)
	}
}

func TestClientStoreAvatarFallsBackWhenUploadFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
	fallback := "https://cdn.example/generated-avatar.png"
	stored, err := client.StoreAvatar(context.Background(), tinyPNG, "image/png", fallback)
	if err != nil {
		t.Fatalf("StoreAvatar() error = %v", err)
	}
	if stored.Ref != fallback || !stored.Fallback {
		t.Fatalf("expected direct URL fallback, got %#v", stored)
	}
}

func TestClientResolveAvatarRefDirectURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(tinyPNG)
	}))
	defer server.Close()

	if _, err := (*Client)(nil).ResolveAvatarRef(context.Background(), server.URL+"/avatar.png"); err == nil {
		t.Fatalf("expected direct URL preview to require opt-in")
	}

	preview, err := (*Client)(nil).ResolveAvatarRef(context.Background(), server.URL+"/avatar.png", AllowDirectAvatarPreviewURLs())
	if err != nil {
		t.Fatalf("ResolveAvatarRef() error = %v", err)
	}
	if preview.Ref != server.URL+"/avatar.png" || preview.URL != server.URL+"/avatar.png" || preview.ContentType != "image/png" || string(preview.Data) != string(tinyPNG) {
		t.Fatalf("unexpected direct URL preview: %#v", preview)
	}
}

func TestClientStoreAvatarFallsBackOnMalformedBlossomDescriptor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(BlobDescriptor{SHA256: "not-a-hash"})
	}))
	defer server.Close()

	client := NewClient(Config{Servers: []string{server.URL}, MaxRetries: 1}, testLogger())
	fallback := "https://cdn.example/avatar.png"
	stored, err := client.StoreAvatar(context.Background(), tinyPNG, "image/png", fallback)
	if err != nil {
		t.Fatalf("StoreAvatar() error = %v", err)
	}
	if stored.Ref != fallback || !stored.Fallback {
		t.Fatalf("expected fallback on malformed descriptor, got %#v", stored)
	}
}

func TestClientResolveAvatarRefDirectURLSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("too-large"))
	}))
	defer server.Close()

	_, err := (*Client)(nil).ResolveAvatarRef(context.Background(), server.URL+"/avatar.png", AllowDirectAvatarPreviewURLs(), WithMaxAvatarPreviewBytes(3))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}

func TestHashFromRefValidation(t *testing.T) {
	hash := strings.Repeat("a", 64)
	got, err := HashFromRef("blossom:" + hash)
	if err != nil {
		t.Fatalf("HashFromRef() error = %v", err)
	}
	if got != hash {
		t.Fatalf("HashFromRef() = %q, want %q", got, hash)
	}
	if _, err := HashFromRef("blossom:not-a-hash"); err == nil {
		t.Fatalf("expected invalid hash error")
	}
	if _, err := HashFromRef("https://example/avatar.png"); err == nil {
		t.Fatalf("expected non-Blossom ref error")
	}
}
