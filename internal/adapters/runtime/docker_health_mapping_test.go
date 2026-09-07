package runtime

import (
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

// TestMapDockerStateRestartingIsUnhealthy is the root-cause regression for the
// live Astillero incident. "restarting" was mapped to Starting, which the
// reconciler accepted as convergeable, so a container crash-looping on a missing
// secret mount reported in_sync.
func TestMapDockerStateRestartingIsUnhealthy(t *testing.T) {
	if got := mapDockerState("restarting"); got != domain.HealthStatusUnhealthy {
		t.Fatalf("mapDockerState(restarting) = %q, want unhealthy", got)
	}
}

// TestDockerHealthFromStatus covers the healthcheck verdict Docker embeds in the
// list-API status string. A container with no healthcheck must report "not
// stated" so such services still converge on container state alone.
func TestDockerHealthFromStatus(t *testing.T) {
	for _, test := range []struct {
		status string
		want   domain.HealthStatus
		stated bool
	}{
		{"Up 2 minutes (healthy)", domain.HealthStatusHealthy, true},
		{"Up 2 minutes (unhealthy)", domain.HealthStatusUnhealthy, true},
		{"Up 3 seconds (health: starting)", domain.HealthStatusStarting, true},
		{"Up 5 days", "", false},
		{"Exited (1) 4 seconds ago", "", false},
		{"", "", false},
	} {
		got, stated := dockerHealthFromStatus(test.status)
		if stated != test.stated {
			t.Errorf("dockerHealthFromStatus(%q) stated = %v, want %v", test.status, stated, test.stated)
			continue
		}
		if stated && got != test.want {
			t.Errorf("dockerHealthFromStatus(%q) = %q, want %q", test.status, got, test.want)
		}
	}
}
