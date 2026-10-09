package app

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// f74aDirtyMarkerBridge converts the publisher's post-commit failure signal
// into the v2 atomic dirty generation. The v1 record remains readable but is
// never considered completion evidence for semantic package coordinates.
type f74aDirtyMarkerBridge struct {
	runner *service.F74aBackfillRunner
	skip   bool
}

func (b *f74aDirtyMarkerBridge) PutControlRecord(family, id string, _ []byte) error {
	if family != "bootstrap" || id != "f74a-canonical-v1" {
		return fmt.Errorf("unexpected F74a dirty marker %s/%s", family, id)
	}
	if b.skip {
		return nil
	}
	if b.runner == nil {
		return fmt.Errorf("F74a dirty marker runner is not configured")
	}
	return b.runner.MarkDirty(true)
}

// The live wrapper is deliberately distinct from the backfill publisher: a
// backfill replay must not dirty its own generation.
type f74aLiveBackfillPublisher struct {
	service.F74aLivePublisher
	tombstone service.F74aBackfillPublisher
}

func (p f74aLiveBackfillPublisher) PublishLegacySBOMPackageTombstone(ctx context.Context, pkg *domain.SBOMPackage) error {
	return p.tombstone.PublishLegacySBOMPackageTombstone(ctx, pkg)
}

func registerF74aBackfillHealthCheck(provider *HealthProvider, runner *service.F74aBackfillRunner) {
	provider.RegisterCheck("f74a_backfill", func() HealthCheck {
		s := runner.Snapshot()
		status := HealthStatusWarn
		message := "canonical families are being durably staged"
		if s.Paused {
			message = "canonical backfill paused by outbox admission"
		}
		if s.Completed {
			status = HealthStatusPass
			message = "canonical families staged locally"
		} else if s.LastError != "" {
			message = "canonical backfill retrying: " + s.LastError
		}
		details := map[string]string{
			"phase": s.Phase, "cursor": s.Cursor, "visited": strconv.FormatUint(s.Visited, 10),
			"staged": strconv.FormatUint(s.Staged, 10), "failures": strconv.FormatUint(s.Failures, 10),
			"retries": strconv.FormatUint(s.Retries, 10), "pending_outbox": strconv.FormatInt(s.Pending, 10), "paused": strconv.FormatBool(s.Paused),
			"dirty_generation": strconv.FormatUint(s.Generation, 10), "completed": strconv.FormatBool(s.Completed),
		}
		if !s.UpdatedAt.IsZero() {
			details["cursor_age_seconds"] = strconv.FormatInt(int64(time.Since(s.UpdatedAt).Seconds()), 10)
		}
		return HealthCheck{Name: "f74a_backfill", Status: status, Message: message, Details: details}
	})
}
