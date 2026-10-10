package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"fiatjaf.com/nostr"
)

const maxAssistantManifestBytes = 2 << 20

// persistAssistantWrappedKeyManifest writes a verified manifest once. The
// destination directory must already exist with private permissions and be on
// a filesystem supporting atomic hard-link creation. No existing manifest is
// ever overwritten; an uncertain fsync result must be resolved by loading the
// existing path, not by generating another key.
func persistAssistantWrappedKeyManifest(ctx context.Context, wrapper assistantKeyWrapper, servicePubkey nostr.PubKey, path string, manifest AssistantWrappedKeyManifest) error {
	if _, err := openAssistantWrappedKeyManifest(ctx, wrapper, servicePubkey, manifest); err != nil {
		return fmt.Errorf("verify assistant key manifest before persistence: %w", err)
	}
	if err := validateAssistantManifestPath(path); err != nil {
		return err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal assistant key manifest: %w", err)
	}
	if len(encoded) > maxAssistantManifestBytes {
		return errors.New("assistant key manifest exceeds size bound")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".assistant-key-manifest-*")
	if err != nil {
		return fmt.Errorf("create assistant key temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure assistant key temp file: %w", err)
	}
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("write assistant key temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync assistant key temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close assistant key temp file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("assistant key manifest already exists; load it instead of generating another key")
		}
		return fmt.Errorf("create assistant key manifest without overwrite: %w", err)
	}
	if err := syncAssistantManifestDir(dir); err != nil {
		return fmt.Errorf("assistant key manifest linked but directory sync failed; inspect existing path before retry: %w", err)
	}
	if err := os.Remove(tmpName); err != nil {
		return fmt.Errorf("assistant key manifest committed but temp cleanup failed: %w", err)
	}
	if err := syncAssistantManifestDir(dir); err != nil {
		return fmt.Errorf("assistant key manifest committed but cleanup sync failed; inspect existing path: %w", err)
	}
	return nil
}

// loadAssistantWrappedKeyManifest performs a bounded, strict read of a private
// local manifest. The caller must still open it with the fenced Signet signer
// to authenticate and decrypt each key before using it for historical reads.
func loadAssistantWrappedKeyManifest(path string) (AssistantWrappedKeyManifest, error) {
	if err := validateAssistantManifestPath(path); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("stat assistant key manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maxAssistantManifestBytes {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest is not a bounded private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("open assistant key manifest: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest changed during open")
	}
	encoded, err := io.ReadAll(io.LimitReader(f, maxAssistantManifestBytes+1))
	if err != nil || len(encoded) > maxAssistantManifestBytes {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest read failed or exceeded size bound")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("decode assistant key manifest: %w", err)
	}
	if len(fields) != 4 {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest field set mismatch")
	}
	for _, name := range []string{"schema", "service_pubkey", "active", "legacy"} {
		if _, ok := fields[name]; !ok {
			return AssistantWrappedKeyManifest{}, fmt.Errorf("assistant key manifest missing %s", name)
		}
	}
	if err := checkAssistantManifestRecordFields(fields["active"]); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	if err := checkAssistantManifestRecordFields(fields["legacy"]); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	if err := rejectDuplicateAssistantJSONKeys(encoded); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	var manifest AssistantWrappedKeyManifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("decode assistant key manifest: %w", err)
	}
	if err := validateAssistantManifestShape(manifest); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	return manifest, nil
}

func validateAssistantManifestPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("assistant key manifest path must be absolute and clean")
	}
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat assistant key manifest directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("assistant key manifest directory must be private and not a symlink")
	}
	return nil
}

func syncAssistantManifestDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Reject duplicate object keys at every level before decoding. Exact field-set
// checks above reject case-insensitive struct aliases.
func rejectDuplicateAssistantJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkAssistantJSON(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("assistant key manifest has trailing JSON")
	}
	return nil
}

func walkAssistantJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid assistant manifest JSON key")
			}
			if seen[key] {
				return errors.New("duplicate assistant manifest JSON key")
			}
			seen[key] = true
			if err := walkAssistantJSON(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		for dec.More() {
			if err := walkAssistantJSON(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	default:
		return errors.New("invalid assistant manifest JSON delimiter")
	}
}

func checkAssistantManifestRecordFields(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 4 {
		return errors.New("assistant key record field set mismatch")
	}
	for _, name := range []string{"ref", "version", "rotation", "ciphertext"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("assistant key record missing %s", name)
		}
	}
	return nil
}

// loadAssistantWrappedKeyManifestPinned rejects a different v2 generation.
// The expected version must come from independently retained deployment state;
// a local file alone cannot prove that it has not been rolled back by its owner.
func loadAssistantWrappedKeyManifestPinned(path, expectedVersion string) (AssistantWrappedKeyManifest, error) {
	if expectedVersion == "" {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest generation pin is required")
	}
	manifest, err := loadAssistantWrappedKeyManifest(path)
	if err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	if manifest.Active.Version != expectedVersion {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest generation differs from pinned version")
	}
	return manifest, nil
}
