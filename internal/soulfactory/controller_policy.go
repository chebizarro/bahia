package soulfactory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"fiatjaf.com/nostr"
)

const (
	OpenClawControllerGrantMethod  = "soulfactory.controller.grant"
	OpenClawControllerRevokeMethod = "soulfactory.controller.revoke"
)

type OpenClawControllerPolicy interface {
	Controllers() []string
	Reload() error
	Apply(method, pubkey, eventID string, createdAt int64) error
}

type openClawControllerPolicyState struct {
	Version       int      `json:"version"`
	Initialized   bool     `json:"initialized"`
	Controllers   []string `json:"controllers"`
	LastEventID   string   `json:"last_event_id,omitempty"`
	LastCreatedAt int64    `json:"last_created_at,omitempty"`
}

type memoryOpenClawControllerPolicy struct {
	mu          sync.RWMutex
	controllers []string
	lastEvent   string
	lastCreated int64
}

func newMemoryOpenClawControllerPolicy(seed []string) (OpenClawControllerPolicy, error) {
	controllers, err := normalizeControllerPubkeys(seed)
	if err != nil {
		return nil, err
	}
	return &memoryOpenClawControllerPolicy{controllers: controllers}, nil
}

func (p *memoryOpenClawControllerPolicy) Controllers() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.controllers...)
}

func (p *memoryOpenClawControllerPolicy) Reload() error { return nil }

func (p *memoryOpenClawControllerPolicy) Apply(method, pubkey, eventID string, createdAt int64) error {
	normalized, err := normalizeControllerPubkeys([]string{pubkey})
	if err != nil || len(normalized) != 1 {
		return fmt.Errorf("invalid controller pubkey")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if staleControllerPolicyEvent(eventID, createdAt, p.lastEvent, p.lastCreated) {
		return fmt.Errorf("stale controller policy event")
	}
	switch method {
	case OpenClawControllerGrantMethod:
		p.controllers = append(p.controllers, normalized[0])
	case OpenClawControllerRevokeMethod:
		filtered := p.controllers[:0]
		for _, existing := range p.controllers {
			if existing != normalized[0] {
				filtered = append(filtered, existing)
			}
		}
		p.controllers = filtered
	default:
		return fmt.Errorf("unsupported controller policy method")
	}
	p.controllers, err = normalizeControllerPubkeys(p.controllers)
	if err != nil {
		return err
	}
	p.lastEvent = eventID
	p.lastCreated = createdAt
	return nil
}

type fileOpenClawControllerPolicy struct {
	mu    sync.RWMutex
	path  string
	state openClawControllerPolicyState
}

func NewFileOpenClawControllerPolicy(path string, seed []string) (OpenClawControllerPolicy, bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false, fmt.Errorf("controller policy path is required")
	}
	store := &fileOpenClawControllerPolicy{path: path}
	if err := store.Reload(); err == nil {
		return store, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	controllers, err := normalizeControllerPubkeys(seed)
	if err != nil {
		return nil, false, err
	}
	if len(controllers) == 0 {
		return nil, false, fmt.Errorf("controller policy is absent and SOULFACTORY_CONTROLLER_PUBKEYS seed is empty")
	}
	state := openClawControllerPolicyState{
		Version: 1, Initialized: true, Controllers: controllers,
	}
	if err := store.persist(state); err != nil {
		return nil, false, err
	}
	store.state = state
	return store, true, nil
}

func (s *fileOpenClawControllerPolicy) Controllers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.state.Controllers...)
}

func (s *fileOpenClawControllerPolicy) Reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read controller policy %s: %w", s.path, err)
	}
	var state openClawControllerPolicyState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse controller policy %s: %w", s.path, err)
	}
	if state.Version != 1 || !state.Initialized {
		return fmt.Errorf("controller policy %s has unsupported or uninitialized state", s.path)
	}
	state.Controllers, err = normalizeControllerPubkeys(state.Controllers)
	if err != nil {
		return fmt.Errorf("validate controller policy %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	return nil
}

func (s *fileOpenClawControllerPolicy) Apply(method, pubkey, eventID string, createdAt int64) error {
	normalized, err := normalizeControllerPubkeys([]string{pubkey})
	if err != nil || len(normalized) != 1 {
		return fmt.Errorf("invalid controller pubkey")
	}
	pubkey = normalized[0]
	s.mu.Lock()
	defer s.mu.Unlock()
	if staleControllerPolicyEvent(eventID, createdAt, s.state.LastEventID, s.state.LastCreatedAt) {
		return fmt.Errorf("stale or uncorrelated controller policy event")
	}
	next := s.state
	next.Controllers = append([]string(nil), s.state.Controllers...)
	switch method {
	case OpenClawControllerGrantMethod:
		next.Controllers = append(next.Controllers, pubkey)
	case OpenClawControllerRevokeMethod:
		filtered := next.Controllers[:0]
		for _, existing := range next.Controllers {
			if existing != pubkey {
				filtered = append(filtered, existing)
			}
		}
		next.Controllers = filtered
	default:
		return fmt.Errorf("unsupported controller policy method")
	}
	next.Controllers, err = normalizeControllerPubkeys(next.Controllers)
	if err != nil {
		return err
	}
	next.LastEventID = eventID
	next.LastCreatedAt = createdAt
	if err := s.persist(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *fileOpenClawControllerPolicy) persist(state openClawControllerPolicyState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFileMode(s.path, data, 0o600)
}

func staleControllerPolicyEvent(eventID string, createdAt int64, lastEventID string, lastCreatedAt int64) bool {
	eventID = strings.TrimSpace(eventID)
	return eventID == "" || createdAt < lastCreatedAt ||
		(createdAt == lastCreatedAt && eventID <= lastEventID)
}

func normalizeControllerPubkeys(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, err := nostr.PubKeyFromHex(value); err != nil {
			return nil, fmt.Errorf("controller pubkey must be 64-hex: %w", err)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}
