package loom

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

const (
	CanonicalLoomJobSchema = "cascadia.loom.v1"
	CanonicalLoomDomain    = "loom"
	CanonicalLoomEntity    = "job"
)

// CanonicalPublisher is satisfied by Bahia relay pools that publish signed events.
type CanonicalPublisher interface {
	Publish(context.Context, nostr.Event) (int, error)
}

// CanonicalSigner is satisfied by Signet/NIP-46 clients and by the explicit
// raw-key compatibility adapter below.
type CanonicalSigner interface {
	Sign(context.Context, *nostr.Event) error
}

// HexKeyCanonicalSigner adapts a configured hex private key to CanonicalSigner.
// It is intended for development and migration compatibility; production callers
// should pass a Signet/NIP-46 signer instead.
type HexKeyCanonicalSigner struct {
	PrivateKey string
}

func (s HexKeyCanonicalSigner) Sign(_ context.Context, event *nostr.Event) error {
	if strings.TrimSpace(s.PrivateKey) == "" {
		return fmt.Errorf("canonical Loom projection signer is not configured")
	}
	return nostrutil.SignEventWithHexKey(event, s.PrivateKey)
}

// ProjectCanonicalStatus maps a native Loom kind 30100 status event to Bahia's
// canonical 30900 loom-job:<id> state plus a 4903 audit fact.
func ProjectCanonicalStatus(ctx context.Context, publisher CanonicalPublisher, privateKey string, ev *nostr.Event) error {
	return ProjectCanonicalStatusWithSigner(ctx, publisher, HexKeyCanonicalSigner{PrivateKey: privateKey}, ev)
}

// ProjectCanonicalStatusWithSigner maps a native Loom kind 30100 status event to
// canonical state/audit events signed by the supplied Signet-compatible signer.
func ProjectCanonicalStatusWithSigner(ctx context.Context, publisher CanonicalPublisher, signer CanonicalSigner, ev *nostr.Event) error {
	if ev == nil {
		return fmt.Errorf("loom status event is nil")
	}
	jobID := firstTag(ev.Tags, tagJobDedup)
	if jobID == "" {
		jobID = firstTag(ev.Tags, tagJobEvent)
	}
	if jobID == "" {
		return fmt.Errorf("loom status event missing job id")
	}
	status := &JobStatus{
		JobID:        jobID,
		Status:       firstTag(ev.Tags, "status"),
		WorkerPubkey: nostrutil.EventPubKeyHex(ev),
		LogOutput:    ev.Content,
	}
	if elapsed := firstTag(ev.Tags, "elapsed_sec"); elapsed != "" {
		if seconds, err := strconv.Atoi(elapsed); err == nil {
			status.Duration = &seconds
		}
	}
	return ProjectCanonicalJobStateWithSigner(ctx, publisher, signer, status, "loom.status")
}

// ProjectCanonicalResult maps a native Loom kind 5101 result event to Bahia's
// canonical 30900 loom-job:<id> state plus a 4903 audit fact.
func ProjectCanonicalResult(ctx context.Context, publisher CanonicalPublisher, privateKey string, ev *nostr.Event) error {
	return ProjectCanonicalResultWithSigner(ctx, publisher, HexKeyCanonicalSigner{PrivateKey: privateKey}, ev)
}

// ProjectCanonicalResultWithSigner maps a native Loom kind 5101 result event to
// canonical state/audit events signed by the supplied Signet-compatible signer.
func ProjectCanonicalResultWithSigner(ctx context.Context, publisher CanonicalPublisher, signer CanonicalSigner, ev *nostr.Event) error {
	if ev == nil {
		return fmt.Errorf("loom result event is nil")
	}
	jobID := firstTag(ev.Tags, tagJobEvent)
	if jobID == "" {
		return fmt.Errorf("loom result event missing job id")
	}
	return ProjectCanonicalJobStateWithSigner(ctx, publisher, signer, parseJobResult(ev, jobID), "loom.result")
}

// ProjectCanonicalJobState publishes a replaceable 30900 state event with d-tag
// loom-job:<id> and an append-only 4903 audit event using the raw-key
// compatibility adapter.
func ProjectCanonicalJobState(ctx context.Context, publisher CanonicalPublisher, privateKey string, status *JobStatus, auditType string) error {
	return ProjectCanonicalJobStateWithSigner(ctx, publisher, HexKeyCanonicalSigner{PrivateKey: privateKey}, status, auditType)
}

// ProjectCanonicalJobStateWithSigner publishes canonical Loom state/audit events
// using a Signet-compatible signer.
func ProjectCanonicalJobStateWithSigner(ctx context.Context, publisher CanonicalPublisher, signer CanonicalSigner, status *JobStatus, auditType string) error {
	if publisher == nil {
		return fmt.Errorf("canonical Loom publisher is not configured")
	}
	if signer == nil {
		return fmt.Errorf("canonical Loom projection signer is not configured")
	}
	if status == nil || strings.TrimSpace(status.JobID) == "" {
		return fmt.Errorf("canonical Loom job id is required")
	}
	if auditType == "" {
		auditType = "loom.job"
	}
	now := time.Now().UTC()
	dTag := "loom-job:" + status.JobID
	state := map[string]any{
		"schema":        CanonicalLoomJobSchema,
		"deleted":       false,
		"job_id":        status.JobID,
		"status":        status.Status,
		"worker_pubkey": status.WorkerPubkey,
		"stdout_url":    status.StdoutURL,
		"stderr_url":    status.StderrURL,
		"error":         status.Error,
		"log_output":    status.LogOutput,
		"updated_at":    now.Format(time.RFC3339Nano),
	}
	if status.Success != nil {
		state["success"] = *status.Success
	}
	if status.ExitCode != nil {
		state["exit_code"] = *status.ExitCode
	}
	if status.Duration != nil {
		state["duration_sec"] = *status.Duration
	}
	stateJSON, _ := json.Marshal(state)
	stateTags := nostr.Tags{{"d", dTag}, {"domain", CanonicalLoomDomain}, {"entity", CanonicalLoomEntity}, {"schema", CanonicalLoomJobSchema}, {"job", status.JobID}, {"status", status.Status}, {"deleted", "false"}}
	if status.WorkerPubkey != "" {
		stateTags = append(stateTags, nostr.Tag{"worker", status.WorkerPubkey})
	}
	stateEvent := nostr.Event{Kind: nostr.Kind(cascadia.CAS_CP_STATE), CreatedAt: nostr.Now(), Tags: stateTags, Content: string(stateJSON)}
	if err := signer.Sign(ctx, &stateEvent); err != nil {
		return fmt.Errorf("sign Loom canonical state: %w", err)
	}
	if n, err := publisher.Publish(ctx, stateEvent); err != nil {
		return fmt.Errorf("publish Loom canonical state: %w", err)
	} else if n == 0 {
		return fmt.Errorf("publish Loom canonical state: no relay accepted event")
	}

	audit := map[string]any{"schema": "bahia.audit.v1", "type": auditType, "domain": CanonicalLoomDomain, "entity": CanonicalLoomEntity, "job_id": status.JobID, "status": status.Status, "state_d_tag": dTag, "recorded_at": now.Format(time.RFC3339Nano)}
	auditJSON, _ := json.Marshal(audit)
	auditEvent := nostr.Event{Kind: nostr.Kind(cascadia.CAS_INTENT), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"domain", CanonicalLoomDomain}, {"entity", CanonicalLoomEntity}, {"type", auditType}, {"job", status.JobID}, {"state", dTag}, {"schema", "bahia.audit.v1"}}, Content: string(auditJSON)}
	if err := signer.Sign(ctx, &auditEvent); err != nil {
		return fmt.Errorf("sign Loom canonical audit: %w", err)
	}
	if n, err := publisher.Publish(ctx, auditEvent); err != nil {
		return fmt.Errorf("publish Loom canonical audit: %w", err)
	} else if n == 0 {
		return fmt.Errorf("publish Loom canonical audit: no relay accepted event")
	}
	return nil
}

func firstTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
