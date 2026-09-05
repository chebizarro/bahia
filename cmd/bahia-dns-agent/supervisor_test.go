package main

import (
	"testing"
	"time"
)

func TestRestartPolicyBackoffCapsAndResetsAfterHealthyRun(t *testing.T) {
	policy := restartPolicy{Initial: time.Second, Maximum: 4 * time.Second, HealthyPeriod: 10 * time.Second, Jitter: 0}
	wait, next := policy.next(time.Second, time.Second, 0.5)
	if wait != time.Second || next != 2*time.Second {
		t.Fatalf("first decision = (%s, %s)", wait, next)
	}
	wait, next = policy.next(next, time.Second, 0.5)
	if wait != 2*time.Second || next != 4*time.Second {
		t.Fatalf("second decision = (%s, %s)", wait, next)
	}
	wait, next = policy.next(next, time.Second, 0.5)
	if wait != 4*time.Second || next != 4*time.Second {
		t.Fatalf("capped decision = (%s, %s)", wait, next)
	}
	wait, next = policy.next(next, policy.HealthyPeriod, 0.5)
	if wait != time.Second || next != 2*time.Second {
		t.Fatalf("healthy reset decision = (%s, %s)", wait, next)
	}
}

func TestRestartPolicyJitterBounds(t *testing.T) {
	policy := restartPolicy{Initial: 10 * time.Second, Maximum: time.Minute, HealthyPeriod: time.Minute, Jitter: 0.2}
	low, _ := policy.next(10*time.Second, time.Second, 0)
	high, _ := policy.next(10*time.Second, time.Second, 1)
	if low != 8*time.Second || high != 12*time.Second {
		t.Fatalf("jitter bounds = (%s, %s)", low, high)
	}
}
