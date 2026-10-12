package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/service"
)

// maxKeyFileBytes bounds the retired key file, like config.LoadPrivateKey.
const maxKeyFileBytes = 4096

// retiredIdentity reads the retired private key from path through one file
// descriptor (no symlink; a regular file owned by this user, mode 0600 or
// stricter, checked on that descriptor) and derives its pubkey and the v1
// assistant transcript key the daemon derived from it: SHA-256 over
// "bahia assistant transcript key v1\x00" and the configured key text. The
// key text and the secret are cleared before it returns; the caller clears
// the derived key when done.
func retiredIdentity(path string) (nostr.PubKey, service.StaticAssistantTranscriptKeyProvider, error) {
	var none service.StaticAssistantTranscriptKeyProvider
	if os.Getenv(oldKeyEnvRefused) != "" {
		return nostr.PubKey{}, none, fmt.Errorf("%s is not accepted; use --old-key-file", oldKeyEnvRefused)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		return nostr.PubKey{}, none, errors.New("must be a regular file owned by this user and readable by its owner only")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxKeyFileBytes+1))
	defer clear(raw)
	if err != nil {
		return nostr.PubKey{}, none, err
	}
	if len(raw) > maxKeyFileBytes {
		return nostr.PubKey{}, none, fmt.Errorf("exceeds %d bytes", maxKeyFileBytes)
	}
	text := bytes.TrimSpace(raw)
	var secret nostr.SecretKey
	defer clear(secret[:])
	if n, err := hex.Decode(secret[:], text); err != nil || n != len(secret) || len(text) != 2*len(secret) {
		return nostr.PubKey{}, none, errors.New("must hold a 64-hex private key")
	}
	if secret == (nostr.SecretKey{}) {
		return nostr.PubKey{}, none, errors.New("the private key must not be zero")
	}
	derive := sha256.New()
	derive.Write([]byte("bahia assistant transcript key v1\x00"))
	derive.Write(text)
	return secret.Public(), service.StaticAssistantTranscriptKeyProvider{Key: service.AssistantTranscriptKey{
		Ref: "assistant-transcript/service-nostr-key", Version: "v1", Rotation: "service-nostr-key", Key: derive.Sum(nil),
	}}, nil
}
