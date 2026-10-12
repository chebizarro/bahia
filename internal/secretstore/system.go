package secretstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// SystemLabelNamespace is the first label segment of daemon-internal
// ("system") credentials: "bahia-system/<purpose>/<key-id>". Tenant labels
// start with an org UUID, so the two never collide.
const SystemLabelNamespace = "bahia-system"

// System key purposes. Each names one daemon-internal use of a data key.
const (
	// PurposeAssistantTranscript encrypts assistant transcripts (30316) and
	// execution checkpoints (4903).
	PurposeAssistantTranscript = "assistant-transcript"
	// PurposeConfidentialDedupe keys the state_hash HMAC of confidential
	// cp-state records.
	PurposeConfidentialDedupe = "confidential-dedupe"
)

// SystemKeySize is the size of every system data key: 32 random bytes.
const SystemKeySize = 32

var systemLabelSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// IsSystemLabel reports whether label is in the system namespace.
func IsSystemLabel(label string) bool {
	return strings.HasPrefix(label, SystemLabelNamespace+"/")
}

// SystemScope is the scope of exactly one system credential, labelled
// "bahia-system/<purpose>/<key-id>". It admits nothing else, in particular no
// tenant credential.
func SystemScope(purpose, keyID string) (Scope, error) {
	if !systemLabelSegment.MatchString(purpose) || !systemLabelSegment.MatchString(keyID) {
		return Scope{}, fmt.Errorf("%w: system purpose and key id must match %s", ErrInvalidRef, systemLabelSegment)
	}
	return Scope{system: SystemLabelNamespace + "/" + purpose + "/" + keyID}, nil
}

// SystemKeyRef names one system data key: the credential that holds it and
// the key id that, with the purpose, forms its label. Ciphertext records the
// label and credential version so a reader finds the key it needs.
type SystemKeyRef struct {
	Purpose    string
	KeyID      string
	Credential domain.SecretCredentialRef
}

// Label is the credential's label in the store.
func (r SystemKeyRef) Label() string {
	return SystemLabelNamespace + "/" + r.Purpose + "/" + r.KeyID
}

// Validate checks the reference's shape.
func (r SystemKeyRef) Validate() error {
	if _, err := SystemScope(r.Purpose, r.KeyID); err != nil {
		return err
	}
	return ValidateRef(r.Credential)
}

// Keyring holds the daemon's system data keys for the life of the process.
// Load resolves every configured key once (all or nothing); Use lends a key
// to a callback, waiting for Load when the purpose is configured; Zeroize
// overwrites every key at shutdown. Keys never leave the keyring except
// through Use, and a keyring that failed to load serves nothing.
type Keyring struct {
	refs  []SystemKeyRef
	ready chan struct{}

	mu       sync.RWMutex
	keys     map[string][]byte // by label, once every key is resolved
	partial  map[string][]byte // keys resolved so far, never served
	loaded   bool
	zeroized bool
	lastErr  error
}

// NewKeyring validates refs: in config order, and for each purpose the first
// ref is the active key. Labels and credential ids must be unique.
func NewKeyring(refs []SystemKeyRef) (*Keyring, error) {
	labels := map[string]struct{}{}
	credentials := map[string]struct{}{}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("system key %s: %w", ref.Label(), err)
		}
		if _, dup := labels[ref.Label()]; dup {
			return nil, fmt.Errorf("%w: system key %s is configured twice", ErrInvalidRef, ref.Label())
		}
		if _, dup := credentials[ref.Credential.CredentialID]; dup {
			return nil, fmt.Errorf("%w: system key %s reuses a credential of another system key", ErrInvalidRef, ref.Label())
		}
		labels[ref.Label()] = struct{}{}
		credentials[ref.Credential.CredentialID] = struct{}{}
	}
	return &Keyring{refs: append([]SystemKeyRef(nil), refs...), ready: make(chan struct{}), partial: map[string][]byte{}}, nil
}

// Refs returns the configured refs of purpose, active first.
func (k *Keyring) Refs(purpose string) []SystemKeyRef {
	var out []SystemKeyRef
	for _, ref := range k.refs {
		if ref.Purpose == purpose {
			out = append(out, ref)
		}
	}
	return out
}

// Empty reports whether no system key is configured.
func (k *Keyring) Empty() bool { return len(k.refs) == 0 }

// Load resolves the configured keys through store. Keys resolved by an
// earlier, failed Load are kept (in memory, never served), so a retry asks
// the store only for the missing ones: each request costs outbound budget.
// The keyring serves nothing until every key is resolved, is exactly
// SystemKeySize bytes and the keyring is not zeroized; the last error is
// retained for Status. Loading a loaded keyring is a no-op.
func (k *Keyring) Load(ctx context.Context, store SecretStore) error {
	k.mu.Lock()
	if k.loaded {
		k.mu.Unlock()
		return nil
	}
	var missing []SystemKeyRef
	for _, ref := range k.refs {
		if _, ok := k.partial[ref.Label()]; !ok {
			missing = append(missing, ref)
		}
	}
	k.mu.Unlock()

	err := k.resolve(ctx, store, missing)

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.zeroized {
		err = errors.New("system keyring is shut down")
	}
	if err == nil && !k.loaded {
		for _, ref := range k.refs {
			if _, ok := k.partial[ref.Label()]; !ok {
				err = fmt.Errorf("%w: system key %s was not resolved", ErrUnavailable, ref.Label())
				break
			}
		}
	}
	if err != nil {
		k.lastErr = err
		return err
	}
	if !k.loaded {
		k.keys, k.partial, k.loaded, k.lastErr = k.partial, nil, true, nil
		close(k.ready)
	}
	return nil
}

// resolve fetches refs and records each key as it arrives.
func (k *Keyring) resolve(ctx context.Context, store SecretStore, refs []SystemKeyRef) error {
	if store == nil && len(refs) > 0 {
		return ErrNotConfigured
	}
	for _, ref := range refs {
		scope, err := SystemScope(ref.Purpose, ref.KeyID)
		if err != nil {
			return err
		}
		value, err := store.Resolve(ctx, ref.Credential, scope)
		if err != nil {
			return fmt.Errorf("system key %s: %w", ref.Label(), err)
		}
		raw := value.Bytes()
		if len(raw) != SystemKeySize {
			value.Zeroize()
			return fmt.Errorf("%w: system key %s is %d bytes, want %d random bytes", ErrInvalidRef, ref.Label(), len(raw), SystemKeySize)
		}
		key := append(make([]byte, 0, SystemKeySize), raw...)
		value.Zeroize()
		k.mu.Lock()
		if k.zeroized || k.loaded {
			clear(key)
		} else {
			if old, ok := k.partial[ref.Label()]; ok {
				clear(old)
			}
			k.partial[ref.Label()] = key
		}
		k.mu.Unlock()
	}
	return nil
}

// Status reports whether the keys are loaded and the last load error.
func (k *Keyring) Status() (loaded bool, lastErr error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.loaded && !k.zeroized, k.lastErr
}

// UnresolvedWait bounds how long Use waits for the first Load when the
// caller's context has no deadline.
const UnresolvedWait = 30 * time.Second

// Use lends fn the key of purpose with keyID ("" for the purpose's active
// key) and returns fn's error. Before the first Load completes it waits,
// within ctx and at most UnresolvedWait; once a Load has failed it fails at
// once with ErrUnavailable (and the last error) until a later Load succeeds.
// It fails with ErrNotConfigured when no such key is configured. fn must not
// retain the slice.
func (k *Keyring) Use(ctx context.Context, purpose, keyID string, fn func(ref SystemKeyRef, key []byte) error) error {
	ref, ok := k.lookup(purpose, keyID)
	if !ok {
		if keyID == "" {
			return fmt.Errorf("%w: no %s system key is configured (secret_store.system_keys)", ErrNotConfigured, purpose)
		}
		return fmt.Errorf("%w: system key %s/%s/%s is not configured (secret_store.system_keys)", ErrNotConfigured, SystemLabelNamespace, purpose, keyID)
	}
	select {
	case <-k.ready:
	default:
		if _, lastErr := k.Status(); lastErr != nil {
			return fmt.Errorf("%w: system key %s: %w", ErrUnavailable, ref.Label(), lastErr)
		}
		wait := ctx
		if _, bounded := ctx.Deadline(); !bounded {
			var cancel context.CancelFunc
			wait, cancel = context.WithTimeout(ctx, UnresolvedWait)
			defer cancel()
		}
		select {
		case <-k.ready:
		case <-wait.Done():
			lastErr := errors.New("not resolved yet")
			if _, err := k.Status(); err != nil {
				lastErr = err
			}
			return fmt.Errorf("%w: system key %s: %w (%w)", ErrUnavailable, ref.Label(), lastErr, wait.Err())
		}
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.zeroized {
		return fmt.Errorf("%w: system keyring is shut down", ErrUnavailable)
	}
	return fn(ref, k.keys[ref.Label()])
}

func (k *Keyring) lookup(purpose, keyID string) (SystemKeyRef, bool) {
	for _, ref := range k.refs {
		if ref.Purpose == purpose && (keyID == "" || ref.KeyID == keyID) {
			return ref, true
		}
	}
	return SystemKeyRef{}, false
}

// Zeroize overwrites every key; the keyring serves nothing afterwards. It is
// idempotent.
func (k *Keyring) Zeroize() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, key := range k.keys {
		clear(key)
	}
	for _, key := range k.partial {
		clear(key)
	}
	k.keys, k.partial, k.zeroized = nil, nil, true
}
