package domain

import (
	"testing"
	"time"
)

func TestWorker_ComputeStatus(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		lastAd  time.Time
		want    WorkerStatus
	}{
		{"just now", now, WorkerStatusOnline},
		{"1 min ago", now.Add(-time.Minute), WorkerStatusOnline},
		{"4 min ago", now.Add(-4 * time.Minute), WorkerStatusOnline},
		{"6 min ago", now.Add(-6 * time.Minute), WorkerStatusStale},
		{"20 min ago", now.Add(-20 * time.Minute), WorkerStatusStale},
		{"31 min ago", now.Add(-31 * time.Minute), WorkerStatusOffline},
		{"2 hours ago", now.Add(-2 * time.Hour), WorkerStatusOffline},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &Worker{LastAdvertisementAt: tc.lastAd}
			if got := w.ComputeStatus(now); got != tc.want {
				t.Errorf("ComputeStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorker_HasSoftware(t *testing.T) {
	w := &Worker{
		Software: []WorkerSoftware{
			{Name: "python", Version: "3.11"},
			{Name: "docker", Version: "24.0"},
		},
	}
	if !w.HasSoftware("python") {
		t.Error("expected HasSoftware('python') = true")
	}
	if !w.HasSoftware("docker") {
		t.Error("expected HasSoftware('docker') = true")
	}
	if w.HasSoftware("node") {
		t.Error("expected HasSoftware('node') = false")
	}
}
