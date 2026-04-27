package loom

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestParseWorkerAdvertisement(t *testing.T) {
	tests := []struct {
		name    string
		event   *nostr.Event
		want    *domain.Worker
		wantErr bool
	}{
		{
			name: "full advertisement",
			event: &nostr.Event{
				PubKey:    "abc123",
				Kind:      KindWorkerAd,
				CreatedAt: nostr.Timestamp(time.Now().Unix()),
				Content:   `{"name":"Test Worker","description":"A test worker","max_concurrent_jobs":3,"current_queue_depth":1}`,
				Tags: nostr.Tags{
					{"A", "linux/amd64"},
					{"S", "docker", "24.0.0", "/usr/bin/docker"},
					{"S", "bash", "5.2.0", "/bin/bash"},
					{"price", "https://mint.example.com", "10", "sat"},
					{"g", "c23nbjmn9"},
					{"min_duration", "10"},
					{"max_duration", "3600"},
					{"relay", "wss://relay.example.com"},
				},
			},
			want: &domain.Worker{
				PubKey:            "abc123",
				Name:              "Test Worker",
				Description:       "A test worker",
				Architecture:      "linux/amd64",
				MaxConcurrentJobs: 3,
				CurrentQueueDepth: 1,
				Software: []domain.WorkerSoftware{
					{Name: "docker", Version: "24.0.0", Path: "/usr/bin/docker"},
					{Name: "bash", Version: "5.2.0", Path: "/bin/bash"},
				},
				Pricing: []domain.WorkerPricing{
					{MintURL: "https://mint.example.com", PricePerSecond: 10, Unit: "sat"},
				},
				Geohash:         "c23nbjmn9",
				MinDurationSecs: 10,
				MaxDurationSecs: 3600,
				PreferredRelays: []string{"wss://relay.example.com"},
				Status:          domain.WorkerStatusOnline,
			},
			wantErr: false,
		},
		{
			name: "minimal advertisement",
			event: &nostr.Event{
				PubKey:    "def456",
				Kind:      KindWorkerAd,
				CreatedAt: nostr.Timestamp(time.Now().Unix()),
				Content:   `{"name":"Minimal Worker","description":"","max_concurrent_jobs":1,"current_queue_depth":0}`,
				Tags:      nostr.Tags{},
			},
			want: &domain.Worker{
				PubKey:            "def456",
				Name:              "Minimal Worker",
				MaxConcurrentJobs: 1,
				CurrentQueueDepth: 0,
				Status:            domain.WorkerStatusOnline,
			},
			wantErr: false,
		},
		{
			name: "invalid JSON content",
			event: &nostr.Event{
				PubKey:    "bad123",
				Kind:      KindWorkerAd,
				CreatedAt: nostr.Timestamp(time.Now().Unix()),
				Content:   `invalid json`,
				Tags:      nostr.Tags{},
			},
			wantErr: true,
		},
		{
			name: "multiple mints",
			event: &nostr.Event{
				PubKey:    "multi123",
				Kind:      KindWorkerAd,
				CreatedAt: nostr.Timestamp(time.Now().Unix()),
				Content:   `{"name":"Multi-Mint Worker","description":"","max_concurrent_jobs":2,"current_queue_depth":0}`,
				Tags: nostr.Tags{
					{"price", "https://mint1.example.com", "10", "sat"},
					{"price", "https://mint2.example.com", "15", "sat"},
					{"price", "https://testnut.cashu.space", "5", "sat"},
				},
			},
			want: &domain.Worker{
				PubKey:            "multi123",
				Name:              "Multi-Mint Worker",
				MaxConcurrentJobs: 2,
				Pricing: []domain.WorkerPricing{
					{MintURL: "https://mint1.example.com", PricePerSecond: 10, Unit: "sat"},
					{MintURL: "https://mint2.example.com", PricePerSecond: 15, Unit: "sat"},
					{MintURL: "https://testnut.cashu.space", PricePerSecond: 5, Unit: "sat"},
				},
				Status: domain.WorkerStatusOnline,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWorkerAdvertisement(tt.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseWorkerAdvertisement() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			// Check key fields
			if got.PubKey != tt.want.PubKey {
				t.Errorf("PubKey = %v, want %v", got.PubKey, tt.want.PubKey)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Name = %v, want %v", got.Name, tt.want.Name)
			}
			if got.Architecture != tt.want.Architecture {
				t.Errorf("Architecture = %v, want %v", got.Architecture, tt.want.Architecture)
			}
			if got.MaxConcurrentJobs != tt.want.MaxConcurrentJobs {
				t.Errorf("MaxConcurrentJobs = %v, want %v", got.MaxConcurrentJobs, tt.want.MaxConcurrentJobs)
			}
			if len(got.Software) != len(tt.want.Software) {
				t.Errorf("Software count = %v, want %v", len(got.Software), len(tt.want.Software))
			}
			if len(got.Pricing) != len(tt.want.Pricing) {
				t.Errorf("Pricing count = %v, want %v", len(got.Pricing), len(tt.want.Pricing))
			}
			if got.Geohash != tt.want.Geohash {
				t.Errorf("Geohash = %v, want %v", got.Geohash, tt.want.Geohash)
			}
			if got.Status != tt.want.Status {
				t.Errorf("Status = %v, want %v", got.Status, tt.want.Status)
			}
		})
	}
}

func TestParseWorkerAdvertisement_SoftwareParsing(t *testing.T) {
	event := &nostr.Event{
		PubKey:    "sw123",
		Kind:      KindWorkerAd,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Content:   `{"name":"SW Worker","description":"","max_concurrent_jobs":1,"current_queue_depth":0}`,
		Tags: nostr.Tags{
			{"S", "python", "3.11.0", "/usr/bin/python3"},
			{"S", "node", "20.10.0"}, // no path
			{"S"},                    // invalid - too short
		},
	}

	worker, err := parseWorkerAdvertisement(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(worker.Software) != 2 {
		t.Errorf("expected 2 software entries, got %d", len(worker.Software))
	}

	// Check first software entry
	if len(worker.Software) > 0 {
		sw := worker.Software[0]
		if sw.Name != "python" || sw.Version != "3.11.0" || sw.Path != "/usr/bin/python3" {
			t.Errorf("first software = %+v, expected python 3.11.0", sw)
		}
	}

	// Check second software entry (no path)
	if len(worker.Software) > 1 {
		sw := worker.Software[1]
		if sw.Name != "node" || sw.Version != "20.10.0" || sw.Path != "" {
			t.Errorf("second software = %+v, expected node 20.10.0 with empty path", sw)
		}
	}
}

func TestParseWorkerAdvertisement_StaleStatus(t *testing.T) {
	// Create an event from 10 minutes ago (should be stale)
	event := &nostr.Event{
		PubKey:    "stale123",
		Kind:      KindWorkerAd,
		CreatedAt: nostr.Timestamp(time.Now().Add(-10 * time.Minute).Unix()),
		Content:   `{"name":"Stale Worker","description":"","max_concurrent_jobs":1,"current_queue_depth":0}`,
		Tags:      nostr.Tags{},
	}

	worker, err := parseWorkerAdvertisement(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if worker.Status != domain.WorkerStatusStale {
		t.Errorf("expected status %s, got %s", domain.WorkerStatusStale, worker.Status)
	}
}

func TestParseWorkerAdvertisement_OfflineStatus(t *testing.T) {
	// Create an event from 1 hour ago (should be offline)
	event := &nostr.Event{
		PubKey:    "offline123",
		Kind:      KindWorkerAd,
		CreatedAt: nostr.Timestamp(time.Now().Add(-1 * time.Hour).Unix()),
		Content:   `{"name":"Offline Worker","description":"","max_concurrent_jobs":1,"current_queue_depth":0}`,
		Tags:      nostr.Tags{},
	}

	worker, err := parseWorkerAdvertisement(event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if worker.Status != domain.WorkerStatusOffline {
		t.Errorf("expected status %s, got %s", domain.WorkerStatusOffline, worker.Status)
	}
}
