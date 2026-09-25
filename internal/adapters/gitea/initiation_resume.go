package gitea

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	loomAdapter "github.com/openagentsinc/bahia/internal/adapters/loom"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// PublicationInspector supplies verified relay evidence, not a guess based on
// elapsed time. Absence is never permission to repeat an unconfirmed dispatch.
type PublicationInspector interface {
	FindPublishedEvent(context.Context, nostr.Filter) (*nostr.Event, error)
}

func WithPublicationInspector(inspector PublicationInspector) InitiatorOption {
	return func(i *Initiator) { i.inspection = inspector }
}

func (i *Initiator) advance(ctx context.Context, rec *InitiationRecord, to InitiationStage) error {
	from := rec.Stage
	rec.Stage = to
	if err := i.store.Advance(ctx, from, rec); err != nil {
		rec.Stage = from
		return err
	}
	return nil
}

func (i *Initiator) resumeInitiation(ctx context.Context, rec *InitiationRecord) (*controlplane.HiveCIBuildStartResult, error) {
	for {
		var err error
		switch rec.Stage {
		case StageRequestReady:
			err = i.publishStage(ctx, rec, rec.RunEvent, StageRequestUnconfirmed, StageRequestPublished)
		case StageRequestUnconfirmed:
			err = i.confirmEvent(ctx, rec.RunEvent)
			if err == nil {
				err = i.advance(ctx, rec, StageRequestPublished)
			}
		case StageRequestPublished:
			err = i.dispatchJob(ctx, rec)
		case StageJobUnconfirmed:
			err = i.confirmJob(ctx, rec)
			if err == nil {
				err = i.advance(ctx, rec, StageJobPublished)
			}
		case StageJobPublished:
			rec.EvidenceEvent, err = i.prepareQueuedEvidence(ctx, rec)
			if err == nil {
				err = i.advance(ctx, rec, StageEvidenceReady)
			}
		case StageEvidenceReady:
			err = i.publishStage(ctx, rec, rec.EvidenceEvent, StageEvidenceUnconfirmed, StageEvidencePublished)
		case StageEvidenceUnconfirmed:
			err = i.confirmEvent(ctx, rec.EvidenceEvent)
			if err == nil {
				err = i.advance(ctx, rec, StageEvidencePublished)
			}
		case StageEvidencePublished:
			result := rec.Result
			return &result, nil
		default:
			return nil, fmt.Errorf("invalid build initiation stage %q", rec.Stage)
		}
		if err != nil {
			return nil, err
		}
	}
}

func (i *Initiator) publishStage(ctx context.Context, rec *InitiationRecord, event *nostr.Event, pending, published InitiationStage) error {
	if event == nil {
		return fmt.Errorf("build initiation has no prepared event for %s", pending)
	}
	// Only the CAS winner may cross this boundary. An interrupted winner leaves
	// an unconfirmed stage, so another process cannot blindly repeat its send.
	if err := i.advance(ctx, rec, pending); err != nil {
		return err
	}
	accepted, err := i.publisher.Publish(ctx, *event)
	if err != nil {
		return fmt.Errorf("%w (%s): %w", ErrPublishUnconfirmed, pending, err)
	}
	if accepted == 0 {
		return fmt.Errorf("%w (%s): no relay acknowledged acceptance", ErrPublishUnconfirmed, pending)
	}
	return i.advance(ctx, rec, published)
}

func (i *Initiator) confirmEvent(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return fmt.Errorf("%w: missing prepared event", ErrPublishUnconfirmed)
	}
	_, err := i.inspect(ctx, nostr.Filter{IDs: []nostr.ID{event.ID}, Authors: []nostr.PubKey{event.PubKey}, Kinds: []nostr.Kind{event.Kind}})
	return err
}

func (i *Initiator) inspect(ctx context.Context, filter nostr.Filter) (*nostr.Event, error) {
	if i.inspection == nil {
		return nil, fmt.Errorf("%w: publication inspector unavailable", ErrPublishUnconfirmed)
	}
	event, err := i.inspection.FindPublishedEvent(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPublishUnconfirmed, err)
	}
	if event == nil {
		return nil, fmt.Errorf("%w: no matching relay evidence; dispatch not repeated", ErrPublishUnconfirmed)
	}
	if !filter.Matches(*event) || !event.CheckID() || !event.VerifySignature() {
		return nil, fmt.Errorf("%w: invalid relay evidence", ErrPublishUnconfirmed)
	}
	return event, nil
}

func (i *Initiator) dispatchJob(ctx context.Context, rec *InitiationRecord) error {
	if rec.LoomJob == nil {
		rec.PublisherNsec = ""
		return i.advance(ctx, rec, StageJobPublished)
	}
	if i.loom == nil {
		return fmt.Errorf("prepared build requires Loom dispatch, but Loom is unavailable")
	}
	password, manifest, err := i.secrets.ResolveSecretWithAudit(ctx, rec.MirrorReadCredentialRef, domain.SecretResolveOptions{
		Operation: domain.SecretAccessOperationResolve, Actor: rec.Request.RequesterPubkey,
		Reason: "resume fleet gitea private-mirror Loom dispatch", RequestID: rec.SourceEventID,
	})
	if err != nil {
		return scrubSecrets(fmt.Errorf("resolve mirror-read credential reference: %w", err), password)
	}
	secretID, err := uuid.Parse(rec.MirrorReadCredentialRef)
	if err != nil || secretID == uuid.Nil || manifest.SecretID != secretID || manifest.ServiceID != rec.Request.ServiceID || strings.TrimSpace(password) == "" {
		return fmt.Errorf("mirror-read credential reference must belong to the selected service and resolve to a credential")
	}
	job := *rec.LoomJob
	job.Secrets = map[string]string{
		hiveCIGitUsernameSecretKey:           rec.MirrorReadUsername,
		hiveCIGitPasswordSecretKey:           password,
		loomAdapter.HiveCIPublisherSecretKey: rec.PublisherNsec,
	}
	if err := i.advance(ctx, rec, StageJobUnconfirmed); err != nil {
		return err
	}
	jobID, err := i.loom.SubmitJob(ctx, job)
	if err != nil {
		return fmt.Errorf("%w: submit Hive-CI Loom job: %v", ErrPublishUnconfirmed, scrubSecrets(err, password, rec.PublisherNsec))
	}
	if _, err := nostr.IDFromHex(jobID); err != nil {
		return fmt.Errorf("%w: Loom returned no valid job event ID", ErrPublishUnconfirmed)
	}
	rec.LoomJobID = jobID
	rec.PublisherNsec = ""
	return i.advance(ctx, rec, StageJobPublished)
}

func (i *Initiator) confirmJob(ctx context.Context, rec *InitiationRecord) error {
	if rec.RunEvent == nil || rec.LoomJob == nil {
		return fmt.Errorf("%w: missing prepared Loom identity", ErrPublishUnconfirmed)
	}
	event, err := i.inspect(ctx, nostr.Filter{
		Authors: []nostr.PubKey{rec.RunEvent.PubKey}, Kinds: []nostr.Kind{nostr.Kind(kinds.LoomJobRequest)},
		Tags: nostr.TagMap{"e": []string{rec.Result.CIRunID}},
	})
	if err != nil {
		return err
	}
	args, worker := event.Tags.Find("args"), event.Tags.Find("p")
	if event.Tags.FindWithValue("cmd", rec.LoomJob.Cmd) == nil || len(args) < 2 || len(worker) != 2 ||
		!slices.Equal(args[1:], rec.LoomJob.Args) || !containsPubkey(rec.LoomJob.AllowedWorkerPubkeys, worker[1]) {
		return fmt.Errorf("%w: Loom evidence conflicts with the prepared job", ErrPublishUnconfirmed)
	}
	rec.LoomJobID = event.ID.Hex()
	rec.PublisherNsec = ""
	return nil
}
