package vm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestJoinCleanupErrorPreservesClassificationAndPrivateCauses(t *testing.T) {
	cause := errors.New("password=primary-sentinel")
	cleanup := errors.New("/private/cleanup-sentinel")
	primary := &domain.VMProviderError{Code: domain.VMErrorConflict, Retryable: true, Unconfirmed: true, Cause: cause}
	if got := JoinCleanupError(primary, nil); got != primary {
		t.Fatal("successful cleanup changed the primary error")
	}
	got := JoinCleanupError(primary, cleanup)
	var classified *domain.VMProviderError
	if !errors.As(got, &classified) || classified.Code != primary.Code || classified.Retryable != primary.Retryable || classified.Unconfirmed != primary.Unconfirmed {
		t.Fatalf("cleanup changed provider classification: %v", got)
	}
	if !errors.Is(got, cause) || !errors.Is(got, cleanup) {
		t.Fatal("cleanup lost an error cause")
	}
	if strings.Contains(got.Error(), "sentinel") || primary.Cause != cause {
		t.Fatal("cleanup leaked private diagnostics or mutated the primary error")
	}
	if !errors.Is(JoinCleanupError(nil, cleanup), cleanup) {
		t.Fatal("cleanup-only failure was swallowed")
	}
}

func TestCheckpointReportsColdGuardCleanupFailure(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "conflict"}[invalid], func(t *testing.T) {
			p, driver, q := checkpointRequest(t)
			cleanup := errors.New("/private/cleanup-sentinel")
			driver.coldCloseErr = cleanup
			driver.coldInvalid = invalid
			result, err := p.Execute(context.Background(), q)
			if !errors.Is(err, cleanup) {
				t.Fatalf("cold guard cleanup failure was swallowed: %v", err)
			}
			var classified *domain.VMProviderError
			if !errors.As(err, &classified) || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("cleanup crossed the provider error boundary: %v", err)
			}
			if invalid {
				if classified.Code != domain.VMErrorConflict || result.Confirmed || result.Checkpoint != nil {
					t.Fatal("cleanup downgraded a cold-state conflict")
				}
			} else {
				// Teardown failure is reported without erasing the committed evidence.
				if result.Checkpoint == nil {
					t.Fatal("committed checkpoint evidence was lost")
				}
				q.Checkpoint = result.Checkpoint
				if _, _, err := p.loadCheckpoint(context.Background(), q); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
