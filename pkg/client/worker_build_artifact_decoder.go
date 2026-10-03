package client

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func decodeRegistry[T any](ev nostr.Event, family int, label string) (*T, error) {
	decoded, err := DecodeControlStateEvent(ev)
	if err != nil {
		return nil, err
	}
	if decoded.LegacyKind != family {
		return nil, fmt.Errorf("event legacy_kind %d is not %s (%d)", decoded.LegacyKind, label, family)
	}
	if decoded.Deleted {
		return nil, nil
	}
	var result T
	if err := json.Unmarshal(decoded.Content, &result); err != nil {
		return nil, fmt.Errorf("decode %s content: %w", label, err)
	}
	return &result, nil
}

// DecodeBuild decodes a canonical build-registry record.
func DecodeBuild(ev nostr.Event) (*domain.Build, error) {
	return decodeRegistry[domain.Build](ev, kinds.BuildRegistry, "build-registry")
}

// DecodeArtifact decodes a canonical artifact-registry record.
func DecodeArtifact(ev nostr.Event) (*domain.Artifact, error) {
	return decodeRegistry[domain.Artifact](ev, kinds.ArtifactRegistry, "artifact-registry")
}

// DecodeWorkerState decodes a canonical worker-state record.
func DecodeWorkerState(ev nostr.Event) (*domain.Worker, error) {
	return decodeRegistry[domain.Worker](ev, kinds.CPStateFamilyWorkerState.LegacyKind(), "worker-state")
}

// DecodeWorkerAssignment decodes a canonical worker-assignment record.
func DecodeWorkerAssignment(ev nostr.Event) (*domain.WorkerAssignmentState, error) {
	return decodeRegistry[domain.WorkerAssignmentState](ev, kinds.CPStateFamilyWorkerAssignment.LegacyKind(), "worker-assignment")
}

// DecodeWorkerDrain decodes a canonical worker-drain record.
func DecodeWorkerDrain(ev nostr.Event) (*domain.WorkerDrainStatus, error) {
	return decodeRegistry[domain.WorkerDrainStatus](ev, kinds.CPStateFamilyWorkerDrain.LegacyKind(), "worker-drain")
}

// DecodeWorkerEligibility decodes a canonical worker-eligibility record.
func DecodeWorkerEligibility(ev nostr.Event) (*domain.WorkerEligibilityPreview, error) {
	return decodeRegistry[domain.WorkerEligibilityPreview](ev, kinds.CPStateFamilyWorkerEligibility.LegacyKind(), "worker-eligibility")
}

// DecodeWorkerAdvertisement validates and decodes a worker-authored advertisement.
func DecodeWorkerAdvertisement(ev nostr.Event) (*domain.Worker, error) {
	if int(ev.Kind) != kinds.LoomWorkerAdvertisement {
		return nil, fmt.Errorf("event kind %d is not worker advertisement", ev.Kind)
	}
	if !ev.CheckID() || !ev.VerifySignature() {
		return nil, fmt.Errorf("invalid worker advertisement signature")
	}
	var content struct {
		Name              string                      `json:"name"`
		Description       string                      `json:"description"`
		MaxConcurrentJobs int                         `json:"max_concurrent_jobs"`
		CurrentQueueDepth int                         `json:"current_queue_depth"`
		Resources         *domain.WorkerResources     `json:"resources,omitempty"`
		Accelerators      []domain.WorkerAccelerator  `json:"accelerators,omitempty"`
		RuntimeTarget     *domain.WorkerRuntimeTarget `json:"runtime_target,omitempty"`
		MLCapabilities    domain.WorkerMLCapabilities `json:"ml_capabilities,omitempty"`
		Capabilities      domain.WorkerCapabilities   `json:"capabilities,omitempty"`
		Telemetry         *domain.WorkerTelemetry     `json:"telemetry,omitempty"`
	}
	if strings.TrimSpace(ev.Content) != "" {
		if err := json.Unmarshal([]byte(ev.Content), &content); err != nil {
			return nil, fmt.Errorf("decode worker advertisement: %w", err)
		}
	}
	at := ev.CreatedAt.Time().UTC()
	worker := domain.Worker{PubKey: ev.PubKey.Hex(), Name: content.Name, Description: content.Description,
		MaxConcurrentJobs: content.MaxConcurrentJobs, CurrentQueueDepth: content.CurrentQueueDepth,
		Resources: content.Resources, Accelerators: content.Accelerators, RuntimeTarget: content.RuntimeTarget,
		MLCapabilities: content.MLCapabilities, Capabilities: content.Capabilities, Telemetry: content.Telemetry,
		LastAdvertisementAt: at, Status: domain.WorkerStatusOnline, CreatedAt: at, UpdatedAt: at}
	for _, tag := range ev.Tags {
		if len(tag) < 2 {
			continue
		}
		switch tag[0] {
		case "S":
			sw := domain.WorkerSoftware{Name: tag[1]}
			if len(tag) > 2 {
				sw.Version = tag[2]
			}
			if len(tag) > 3 {
				sw.Path = tag[3]
			}
			worker.Software = append(worker.Software, sw)
		case "A":
			worker.Architecture = tag[1]
		case "price":
			if len(tag) >= 4 {
				price, err := strconv.Atoi(tag[2])
				if err != nil {
					return nil, fmt.Errorf("invalid worker price %q: %w", tag[2], err)
				}
				worker.Pricing = append(worker.Pricing, domain.WorkerPricing{MintURL: tag[1], PricePerSecond: price, Unit: tag[3]})
			}
		case "min_duration":
			worker.MinDurationSecs, _ = strconv.Atoi(tag[1])
		case "max_duration":
			worker.MaxDurationSecs, _ = strconv.Atoi(tag[1])
		case "g":
			worker.Geohash = tag[1]
		case "relay":
			worker.PreferredRelays = append(worker.PreferredRelays, tag[1])
		case "runtime":
			worker.MLCapabilities.Runtimes = append(worker.MLCapabilities.Runtimes, domain.MLRuntimeKind(tag[1]))
		case "artifact_format", "format":
			worker.MLCapabilities.ArtifactFormats = append(worker.MLCapabilities.ArtifactFormats, domain.MLArtifactFormat(tag[1]))
		case "task":
			worker.MLCapabilities.Tasks = append(worker.MLCapabilities.Tasks, domain.MLTaskKind(tag[1]))
		case "accelerator":
			worker.MLCapabilities.Accelerators = append(worker.MLCapabilities.Accelerators, tag[1])
		case "toolchain":
			worker.MLCapabilities.Toolchains = append(worker.MLCapabilities.Toolchains, tag[1])
		case "cached_artifact", "artifact":
			worker.MLCapabilities.CachedArtifacts = append(worker.MLCapabilities.CachedArtifacts, tag[1])
		}
	}
	worker.MLCapabilities = domain.NormalizeWorkerMLCapabilities(worker)
	return &worker, nil
}
