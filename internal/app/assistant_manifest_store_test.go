package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func assistantManifestFixture(t *testing.T) (localAssistantWrapFixture, AssistantWrappedKeyManifest, string) {
	t.Helper()
	wrapper := assistantWrapFixture(t)
	cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
	manifest, err := createAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	dir = realDir
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return wrapper, manifest, filepath.Join(dir, "assistant-keys.json")
}

func TestAssistantManifestStoreRestartReadAndNoOverwrite(t *testing.T) {
	wrapper, manifest, path := assistantManifestFixture(t)
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, path, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAssistantWrappedKeyManifestPinned(path, manifest.Active.Version)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Active.Version != manifest.Active.Version || loaded.Legacy.Ciphertext != manifest.Legacy.Ciphertext {
		t.Fatal("restart read changed manifest")
	}
	provider, err := openAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ActiveTranscriptKey(t.Context()); err == nil {
		t.Fatal("durable file unexpectedly enabled v2 writer")
	}
	another, err := createAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, path, another); err == nil {
		t.Fatal("existing manifest overwritten")
	}
	if _, err := loadAssistantWrappedKeyManifestPinned(path, another.Active.Version); err == nil {
		t.Fatal("rollback/generation mismatch accepted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("manifest mode = %o", info.Mode().Perm())
	}
}

func TestAssistantManifestStoreConcurrentCreateOnlyOneWins(t *testing.T) {
	wrapper, _, path := assistantManifestFixture(t)
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := make([]string, 0, 1)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := &config.Config{Nostr: config.NostrConfig{PrivateKey: strings.Repeat("1", 64)}}
			manifest, err := createAssistantWrappedKeyManifest(context.Background(), wrapper, wrapper.pubkey, cfg)
			if err != nil {
				t.Error(err)
				return
			}
			err = persistAssistantWrappedKeyManifest(context.Background(), wrapper, wrapper.pubkey, path, manifest)
			if err == nil {
				mu.Lock()
				winners = append(winners, manifest.Active.Version)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(winners) != 1 {
		t.Fatalf("successful create count = %d", len(winners))
	}
	loaded, err := loadAssistantWrappedKeyManifestPinned(path, winners[0])
	if err != nil || loaded.Active.Version != winners[0] {
		t.Fatalf("winner not persisted: %v", err)
	}
}

func TestAssistantManifestStoreCrashMissingCorruptAndInsecure(t *testing.T) {
	wrapper, manifest, path := assistantManifestFixture(t)
	if _, err := loadAssistantWrappedKeyManifest(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing manifest = %v", err)
	}
	// A crash after syncing a temp file but before linking leaves no final path.
	orphan := filepath.Join(filepath.Dir(path), ".assistant-key-manifest-orphan")
	if err := os.WriteFile(orphan, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan temp became a manifest: %v", err)
	}
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, path, manifest); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// A crash after the final link's directory sync but before temp cleanup
	// leaves exactly one generated same-inode temp alias. Restart removes it.
	tempAlias := filepath.Join(filepath.Dir(path), ".assistant-key-manifest-0123456789abcdef0123456789abcdef")
	if err := os.Link(path, tempAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err != nil {
		t.Fatalf("committed crash-window manifest was not recovered: %v", err)
	}
	if _, err := os.Lstat(tempAlias); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered temp alias still exists: %v", err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err != nil {
		t.Fatalf("committed manifest unreadable after cleanup: %v", err)
	}
	unknownAlias := filepath.Join(filepath.Dir(path), "external-alias")
	if err := os.Link(path, unknownAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
		t.Fatal("unrecognized external hard link accepted")
	}
	if err := os.Remove(unknownAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
		t.Fatal("world-readable manifest accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(orphan, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
		t.Fatal("symlink manifest accepted")
	}
}

func TestAssistantManifestStoreRejectsJSONAliasesAndBadDirectory(t *testing.T) {
	wrapper, manifest, path := assistantManifestFixture(t)
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, path, manifest); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{
		[]byte(strings.Replace(string(encoded), `"version":`, `"Version":`, 1)),
		[]byte(strings.Replace(string(encoded), `"schema":`, `"schema":"wrong","schema":`, 1)),
		[]byte(strings.Replace(string(encoded), `"active":`, `"Active":`, 1)),
	} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
			t.Fatal("JSON alias/duplicate accepted")
		}
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAssistantWrappedKeyManifest(path); err == nil {
		t.Fatal("insecure directory accepted")
	}
}

func TestAssistantManifestStoreRejectsSymlinkAncestorAndMovedDirectory(t *testing.T) {
	wrapper, manifest, path := assistantManifestFixture(t)
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, path, manifest); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	alias := filepath.Join(filepath.Dir(dir), "assistant-dir-link-"+filepath.Base(dir))
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(alias) })
	if _, err := loadAssistantWrappedKeyManifest(filepath.Join(alias, filepath.Base(path))); err == nil {
		t.Fatal("symlinked ancestor accepted for read")
	}
	if err := persistAssistantWrappedKeyManifest(t.Context(), wrapper, wrapper.pubkey, filepath.Join(alias, "different.json"), manifest); err == nil {
		t.Fatal("symlinked ancestor accepted for create")
	}
	pinned, _, err := openTrustedAssistantManifestDir(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(moved, dir) })
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(dir) })
	if err := verifyAssistantManifestDirPinned(path, pinned); err == nil {
		t.Fatal("replaced directory accepted after pin")
	}
}
