package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	gonostr "github.com/nbd-wtf/go-nostr"
	"go.uber.org/zap"
)

const (
	kindBahiaIdentityDefinition = 31410
	kindBahiaReplayCheckpoint   = 31411
	kindBahiaReadinessStatus    = 30360
)

type BahiaIdentityPayload struct {
	Version        string `json:"version"`
	CatalogVersion string `json:"catalog_version"`
	Mode           string `json:"mode"`
	StartedAt      int64  `json:"started_at"`
}

type ReplayCheckpointPayload struct {
	CatalogVersion string           `json:"catalog_version"`
	Cursors        map[string]int64 `json:"cursors"`
	Phase          string           `json:"phase"`
}

type ReadinessStatusPayload struct {
	Phase         string            `json:"phase"`
	ActiveTier    int               `json:"active_tier"`
	RequestedTier int               `json:"requested_tier"`
	Ready         bool              `json:"ready"`
	Checks        map[string]string `json:"checks"`
}

// NostrEventPublisher signs and publishes Nostr events to relays.
type NostrEventPublisher interface {
	PublishSignedEvent(ctx context.Context, ev *gonostr.Event) error
}

// BahiaStatusProjector publishes Bahia self-describing status events.
type BahiaStatusProjector struct {
	publisher  NostrEventPublisher
	logger     *zap.Logger
	instanceID string
	mu         sync.Mutex
	lastHash   string
}

func NewBahiaStatusProjector(publisher NostrEventPublisher, logger *zap.Logger, instanceID string) *BahiaStatusProjector {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BahiaStatusProjector{
		publisher:  publisher,
		logger:     logger.Named("bahia-status-projector"),
		instanceID: instanceID,
	}
}

func (p *BahiaStatusProjector) PublishIdentity(ctx context.Context, payload BahiaIdentityPayload) error {
	if p == nil {
		return nil
	}
	ev, err := encodeBahiaStatusEvent(kindBahiaIdentityDefinition, p.instanceID, "bahia-identity", payload)
	if err != nil {
		return err
	}
	return p.publishIfChanged(ctx, ev)
}

func (p *BahiaStatusProjector) PublishCheckpoint(ctx context.Context, payload ReplayCheckpointPayload) error {
	if p == nil {
		return nil
	}
	ev, err := encodeBahiaStatusEvent(kindBahiaReplayCheckpoint, p.instanceID, "bahia-replay-checkpoint", payload)
	if err != nil {
		return err
	}
	return p.publishIfChanged(ctx, ev)
}

func (p *BahiaStatusProjector) PublishReadiness(ctx context.Context, payload ReadinessStatusPayload) error {
	if p == nil {
		return nil
	}
	ev, err := encodeBahiaStatusEvent(kindBahiaReadinessStatus, p.instanceID, "bahia-readiness", payload)
	if err != nil {
		return err
	}
	return p.publishIfChanged(ctx, ev)
}

func (p *BahiaStatusProjector) publishIfChanged(ctx context.Context, ev gonostr.Event) error {
	fingerprint, err := bahiaStatusFingerprint(ev)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if fingerprint == p.lastHash {
		return nil
	}
	if p.publisher != nil {
		if err := p.publisher.PublishSignedEvent(ctx, &ev); err != nil {
			return fmt.Errorf("publish Bahia status kind %d: %w", ev.Kind, err)
		}
	}
	p.lastHash = fingerprint
	return nil
}

func encodeBahiaStatusEvent(kind int, dTag, topic string, payload any) (gonostr.Event, error) {
	content, err := json.Marshal(payload)
	if err != nil {
		return gonostr.Event{}, fmt.Errorf("marshal Bahia status kind %d: %w", kind, err)
	}
	return gonostr.Event{
		Kind:      kind,
		CreatedAt: gonostr.Now(),
		Tags:      gonostr.Tags{{"d", strings.TrimSpace(dTag)}, {"t", "bahia"}, {"t", topic}},
		Content:   string(content),
	}, nil
}

func bahiaStatusFingerprint(ev gonostr.Event) (string, error) {
	encoded, err := json.Marshal(struct {
		Kind    int          `json:"kind"`
		Tags    gonostr.Tags `json:"tags"`
		Content string       `json:"content"`
	}{
		Kind:    ev.Kind,
		Tags:    ev.Tags,
		Content: ev.Content,
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint Bahia status event: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
