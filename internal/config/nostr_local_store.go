package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NostrLocalStoreConfig configures the daemon's local Nostr event store
// (bahia-irsry.10.1): a bbolt file caching the events its inbound
// subscriptions received, with a resume cursor per (relay, filter). It is a
// rebuildable cache: deleting the file is safe, and the daemon resyncs it from
// its relays (replaceable/addressable state in full, regular kinds from
// RegularLookback).
type NostrLocalStoreConfig struct {
	// Path is the bbolt file. One process per file: bbolt holds an exclusive
	// lock, so it must not be the relay sidecar's store or another daemon's.
	Path string `koanf:"path" yaml:"path" secret:"false"`
	// ResumeOverlap is how far before a relay's cursor a resumed REQ starts,
	// so late-propagated and modestly backdated events are still delivered.
	ResumeOverlap time.Duration `koanf:"resume_overlap" yaml:"resume_overlap" secret:"false"`
	// RegularLookback is how far back a regular-kind subscription with no
	// cursor (a fresh node or a deleted store) starts. 0 replays the relays'
	// whole history.
	RegularLookback time.Duration `koanf:"regular_lookback" yaml:"regular_lookback" secret:"false"`
	// NegentropyUpload makes NIP-77 sync also publish to a relay the
	// replaceable/addressable events it lacks and the local store holds
	// (repairing a relay from its peers). Off by default: the daemon is a
	// consumer of these subscriptions, not their publisher.
	NegentropyUpload bool `koanf:"negentropy_upload" yaml:"negentropy_upload" secret:"false"`
	// OutboxPath is the bbolt file of the daemon's publish outbox: signed
	// events waiting for relay acceptance, with each relay's delivery state
	// (bahia-irsry.10.4). Unlike Path it is not a cache, so it is a separate
	// file: deleting it drops events that no relay may hold yet. Empty means
	// "outbox.bolt" next to Path.
	OutboxPath string `koanf:"outbox_path" yaml:"outbox_path" secret:"false"`
}

// ResolvedOutboxPath returns OutboxPath, or "outbox.bolt" in Path's directory
// when it is unset.
func (c NostrLocalStoreConfig) ResolvedOutboxPath() string {
	if path := strings.TrimSpace(c.OutboxPath); path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(strings.TrimSpace(c.Path)), defaultNostrOutboxFile)
}

const (
	// DefaultNostrLocalStorePath is relative to the daemon's working
	// directory, next to the relay sidecar's default ./data/relay-sidecar.
	DefaultNostrLocalStorePath            = "./data/nostr-cache/daemon.bolt"
	DefaultNostrLocalStoreResumeOverlap   = 10 * time.Minute
	DefaultNostrLocalStoreRegularLookback = 24 * time.Hour
	MaxNostrLocalStoreResumeOverlap       = 24 * time.Hour
	// relaySidecarEventStoreFile is internal/relaysidecar's store file name
	// under nostr.sidecar.data_dir.
	relaySidecarEventStoreFile = "events.bolt"
	// defaultNostrOutboxFile is the outbox file name next to the event store.
	defaultNostrOutboxFile = "outbox.bolt"
)

// DefaultNostrLocalStoreConfig returns the local event store defaults.
func DefaultNostrLocalStoreConfig() NostrLocalStoreConfig {
	return NostrLocalStoreConfig{
		Path:            DefaultNostrLocalStorePath,
		ResumeOverlap:   DefaultNostrLocalStoreResumeOverlap,
		RegularLookback: DefaultNostrLocalStoreRegularLookback,
	}
}

func (c *Config) validateNostrLocalStore() error {
	store := &c.Nostr.LocalStore
	store.Path = strings.TrimSpace(store.Path)
	if store.Path == "" {
		return fmt.Errorf("config validation failed: nostr.local_store.path is required")
	}
	if info, err := os.Stat(store.Path); err == nil && info.IsDir() {
		return fmt.Errorf("config validation failed: nostr.local_store.path %q is a directory; it must name the store file", store.Path)
	}
	store.OutboxPath = strings.TrimSpace(store.OutboxPath)
	outboxPath := store.ResolvedOutboxPath()
	if info, err := os.Stat(outboxPath); err == nil && info.IsDir() {
		return fmt.Errorf("config validation failed: nostr.local_store.outbox_path %q is a directory; it must name the outbox file", outboxPath)
	}
	storeAbs, storeErr := filepath.Abs(store.Path)
	outboxAbs, outboxErr := filepath.Abs(outboxPath)
	if storeErr == nil && outboxErr == nil && storeAbs == outboxAbs {
		return fmt.Errorf("config validation failed: nostr.local_store.outbox_path must not be the event store file %q", storeAbs)
	}
	if c.Nostr.Sidecar.Enabled && strings.TrimSpace(c.Nostr.Sidecar.DataDir) != "" {
		sidecarPath, sidecarErr := filepath.Abs(filepath.Join(c.Nostr.Sidecar.DataDir, relaySidecarEventStoreFile))
		if sidecarErr == nil {
			for name, path := range map[string]string{"path": storeAbs, "outbox_path": outboxAbs} {
				if path == sidecarPath {
					return fmt.Errorf("config validation failed: nostr.local_store.%s must not be the relay sidecar's event store %q", name, sidecarPath)
				}
			}
		}
	}
	if store.ResumeOverlap < time.Second || store.ResumeOverlap > MaxNostrLocalStoreResumeOverlap {
		return fmt.Errorf("config validation failed: nostr.local_store.resume_overlap must be between 1s and %s (a bare number is nanoseconds)", MaxNostrLocalStoreResumeOverlap)
	}
	if store.RegularLookback < 0 || (store.RegularLookback > 0 && store.RegularLookback < time.Second) {
		return fmt.Errorf("config validation failed: nostr.local_store.regular_lookback must be 0 (whole history) or at least 1s (a bare number is nanoseconds)")
	}
	return nil
}
