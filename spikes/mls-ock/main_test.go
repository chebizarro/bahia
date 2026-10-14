package main

import (
	"context"
	"testing"
	"time"
)

func TestLocalMLSContentKeyLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m, err := run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.KeyPackageBytes == 0 || m.WelcomeBytes == 0 || m.JoinCommitBytes == 0 || m.LeaveCommitBytes == 0 {
		t.Fatalf("missing MLS wire measurements: %+v", m)
	}
	if m.JoinEvents != 3 || m.LeaveEvents != 1 || m.EpochAfterLeave <= m.EpochAfterJoin {
		t.Fatalf("unexpected lifecycle measurements: %+v", m)
	}
}
