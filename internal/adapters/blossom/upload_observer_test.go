package blossom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadObservationPublishesExactlyOnceAfterFallback(t *testing.T) {
	payload := []byte("f75 blob")
	hash := ComputeSHA256(payload)
	var first, second *httptest.Server
	first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/upload" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(BlobDescriptor{URL: second.URL + "/" + hash, SHA256: hash, Size: int64(len(payload)), Type: "text/plain"})
	}))
	defer second.Close()
	client := NewClient(Config{Servers: []string{first.URL, second.URL}, MaxRetries: 1}, testLogger())
	calls := 0
	var observed BlobDescriptor
	client.SetUploadObserver(func(_ context.Context, descriptor BlobDescriptor) error { calls++; observed = descriptor; return nil })
	descriptor, err := client.Upload(context.Background(), payload, "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if descriptor == nil || calls != 1 || observed.SHA256 != hash {
		t.Fatalf("descriptor=%v observation calls=%d observed hash=%s", descriptor, calls, observed.SHA256)
	}
}
