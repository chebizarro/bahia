package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	dir, name, err := openTrustedAssistantManifestDir(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal assistant key manifest: %w", err)
	}
	if len(encoded) > maxAssistantManifestBytes {
		return errors.New("assistant key manifest exceeds size bound")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("generate assistant manifest temp name: %w", err)
	}
	tmpName := ".assistant-key-manifest-" + hex.EncodeToString(random)
	fd, err := unix.Openat(int(dir.Fd()), tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create assistant key temp file: %w", err)
	}
	tmp := os.NewFile(uintptr(fd), tmpName)
	defer unix.Unlinkat(int(dir.Fd()), tmpName, 0)
	if err := unix.Fchmod(fd, 0600); err != nil {
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
	if err := unix.Linkat(int(dir.Fd()), tmpName, int(dir.Fd()), name, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("assistant key manifest already exists; load it instead of generating another key")
		}
		return fmt.Errorf("create assistant key manifest without overwrite: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("assistant key manifest linked but directory sync failed; inspect existing path before retry: %w", err)
	}
	if err := unix.Unlinkat(int(dir.Fd()), tmpName, 0); err != nil {
		return fmt.Errorf("assistant key manifest committed but temp cleanup failed: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("assistant key manifest committed but cleanup sync failed; inspect existing path: %w", err)
	}
	if err := verifyAssistantManifestDirPinned(path, dir); err != nil {
		return err
	}
	return nil
}

// loadAssistantWrappedKeyManifest performs a bounded, strict read of a private
// local manifest. The caller must still open it with the fenced Signet signer
// to authenticate and decrypt each key before using it for historical reads.
func loadAssistantWrappedKeyManifest(path string) (AssistantWrappedKeyManifest, error) {
	dir, name, err := openTrustedAssistantManifestDir(path)
	if err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("open assistant key manifest: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		return AssistantWrappedKeyManifest{}, fmt.Errorf("stat assistant key manifest: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG || info.Mode&0777 != 0600 || info.Uid != uint32(os.Geteuid()) || info.Size <= 0 || info.Size > maxAssistantManifestBytes {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest is not a bounded private regular file owned by this user")
	}
	if info.Nlink == 2 {
		if err := recoverAssistantManifestTempLink(dir, fd, &info); err != nil {
			return AssistantWrappedKeyManifest{}, err
		}
	} else if info.Nlink != 1 {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest has unrecognized hard links")
	}
	encoded, err := io.ReadAll(io.LimitReader(f, maxAssistantManifestBytes+1))
	if err != nil || len(encoded) > maxAssistantManifestBytes {
		return AssistantWrappedKeyManifest{}, errors.New("assistant key manifest read failed or exceeded size bound")
	}
	if err := verifyAssistantManifestDirPinned(path, dir); err != nil {
		return AssistantWrappedKeyManifest{}, err
	}
	if err := verifyAssistantManifestFinalName(dir, name, &info); err != nil {
		return AssistantWrappedKeyManifest{}, err
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

// recoverAssistantManifestTempLink accepts only the exact crash window after
// the final hard link was synced but before the generated temp name was
// removed. A second link outside this private directory, or a different temp
// inode, is not recoverable evidence and remains a hard failure.
func recoverAssistantManifestTempLink(dir *os.File, finalFD int, final *unix.Stat_t) error {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("scan assistant key temp aliases: %w", err)
	}
	matching := ""
	for _, entry := range entries {
		name := entry.Name()
		const prefix = ".assistant-key-manifest-"
		if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(name, prefix)); err != nil {
			continue
		}
		candidateFD, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open assistant key recovery alias: %w", err)
		}
		var candidate unix.Stat_t
		statErr := unix.Fstat(candidateFD, &candidate)
		unix.Close(candidateFD)
		if statErr != nil {
			return fmt.Errorf("stat assistant key recovery alias: %w", statErr)
		}
		if candidate.Dev != final.Dev || candidate.Ino != final.Ino {
			continue
		}
		if candidate.Mode&unix.S_IFMT != unix.S_IFREG || candidate.Mode&0777 != 0600 || candidate.Uid != uint32(os.Geteuid()) || candidate.Nlink != 2 || matching != "" {
			return errors.New("assistant key recovery alias is not the unique private temp link")
		}
		matching = name
	}
	if matching == "" {
		return errors.New("assistant key manifest has an unrecognized second hard link")
	}
	if err := unix.Unlinkat(int(dir.Fd()), matching, 0); err != nil {
		return fmt.Errorf("remove assistant key crash-window temp alias: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync assistant key recovery cleanup: %w", err)
	}
	var current unix.Stat_t
	if err := unix.Fstat(finalFD, &current); err != nil || current.Dev != final.Dev || current.Ino != final.Ino || current.Nlink != 1 {
		return errors.New("assistant key manifest did not become singly linked after recovery")
	}
	return nil
}

func verifyAssistantManifestFinalName(dir *os.File, name string, opened *unix.Stat_t) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("reopen assistant key final name: %w", err)
	}
	defer unix.Close(fd)
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil || current.Dev != opened.Dev || current.Ino != opened.Ino || current.Nlink != 1 {
		return errors.New("assistant key final name changed during read")
	}
	return nil
}

func openTrustedAssistantManifestDir(path string) (*os.File, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", errors.New("assistant key manifest path must be absolute and clean")
	}
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) {
		return nil, "", errors.New("assistant key manifest filename is required")
	}
	directory := filepath.Dir(path)
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open assistant key root: %w", err)
	}
	for _, part := range strings.Split(strings.TrimPrefix(directory, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if openErr != nil {
			return nil, "", fmt.Errorf("open assistant key directory without symlinks: %w", openErr)
		}
		fd = next
	}
	var info unix.Stat_t
	if err := unix.Fstat(fd, &info); err != nil {
		unix.Close(fd)
		return nil, "", fmt.Errorf("stat assistant key directory: %w", err)
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&0077 != 0 || info.Uid != uint32(os.Geteuid()) {
		unix.Close(fd)
		return nil, "", errors.New("assistant key directory must be owner-private and owned by this user")
	}
	return os.NewFile(uintptr(fd), directory), name, nil
}

func verifyAssistantManifestDirPinned(path string, pinned *os.File) error {
	reopened, _, err := openTrustedAssistantManifestDir(path)
	if err != nil {
		return err
	}
	defer reopened.Close()
	oldInfo, err := pinned.Stat()
	if err != nil {
		return err
	}
	newInfo, err := reopened.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(oldInfo, newInfo) {
		return errors.New("assistant key directory changed during operation")
	}
	return nil
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
