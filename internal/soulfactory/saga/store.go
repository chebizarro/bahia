package saga

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var ErrNotFound = errors.New("provisioning saga not found")
var ErrConflict = errors.New("provisioning saga checkpoint conflict")

// Store uses optimistic versioning so an old failure cannot overwrite newer success.
type Store interface {
	Create(context.Context, *Run) error
	Load(context.Context, string) (*Run, error)
	Save(context.Context, *Run, uint64) error
	List(context.Context) ([]*Run, error)
	Delete(context.Context, string, uint64) error
}

// FileStore persists one atomically-renamed JSON checkpoint per request.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

func NewFileStore(dir string) (*FileStore, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("saga state directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create saga state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure saga state directory: %w", err)
	}
	return &FileStore{dir: dir}, nil
}

func (s *FileStore) path(requestID string) string {
	return filepath.Join(s.dir, DeriveKey(requestID, "file")+".json")
}

func (s *FileStore) Create(ctx context.Context, run *Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := run.validate(); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	path := s.path(run.RequestID)
	if _, err := os.Stat(path); err == nil {
		return ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeAtomic(path, run)
}

func (s *FileStore) Load(ctx context.Context, requestID string) (*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return readRun(s.path(requestID))
}

func (s *FileStore) Save(ctx context.Context, run *Run, expectedVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := run.validate(); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := readRun(s.path(run.RequestID))
	if err != nil {
		return err
	}
	if current.Version != expectedVersion || run.Version != expectedVersion+1 {
		return ErrConflict
	}
	if err := validateImmutableUpdate(current, run); err != nil {
		return err
	}
	return writeAtomic(s.path(run.RequestID), run)
}

func validateImmutableUpdate(current, next *Run) error {
	if current.RequestID != next.RequestID || current.RunID != next.RunID || current.RootKey != next.RootKey || current.AgentID != next.AgentID || current.SpecHash != next.SpecHash || !current.CreatedAt.Equal(next.CreatedAt) {
		return fmt.Errorf("%w: immutable saga identity changed", ErrConflict)
	}
	if len(next.Resources) < len(current.Resources) || len(next.Transitions) < len(current.Transitions) || len(next.Compensations) < len(current.Compensations) || len(next.Failures) < len(current.Failures) {
		return fmt.Errorf("%w: append-only saga history was truncated", ErrConflict)
	}
	for i := range current.Resources {
		if current.Resources[i] != next.Resources[i] {
			return fmt.Errorf("%w: resource lineage was rewritten", ErrConflict)
		}
	}
	for i := range current.Transitions {
		if current.Transitions[i] != next.Transitions[i] {
			return fmt.Errorf("%w: transition history was rewritten", ErrConflict)
		}
	}
	for i := range current.Compensations {
		if current.Compensations[i] != next.Compensations[i] {
			return fmt.Errorf("%w: compensation history was rewritten", ErrConflict)
		}
	}
	for i := range current.Failures {
		if current.Failures[i] != next.Failures[i] {
			return fmt.Errorf("%w: failure history was rewritten", ErrConflict)
		}
	}
	return nil
}

func (s *FileStore) List(ctx context.Context) ([]*Run, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	out := make([]*Run, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		run, err := readRun(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func (s *FileStore) Delete(ctx context.Context, requestID string, expectedVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	current, err := readRun(s.path(requestID))
	if err != nil {
		return err
	}
	if current.Version != expectedVersion {
		return ErrConflict
	}
	return os.Remove(s.path(requestID))
}

func (s *FileStore) lock() (func(), error) {
	s.mu.Lock()
	file, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		s.mu.Unlock()
		return nil, err
	}
	return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close(); s.mu.Unlock() }, nil
}

func readRun(path string) (*Run, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("decode saga checkpoint: %w", err)
	}
	if err := run.validate(); err != nil {
		return nil, fmt.Errorf("validate saga checkpoint: %w", err)
	}
	return run.clone(), nil
}

func writeAtomic(path string, run *Run) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".saga-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// RetentionPolicy deletes only expired failed or rolled-back runs; running history is retained.
type RetentionPolicy struct {
	FailedFor     time.Duration
	RolledBackFor time.Duration
}

func (p RetentionPolicy) Mark(run *Run, now time.Time) {
	var duration time.Duration
	switch run.Stage {
	case StageFailedTerminal:
		duration = p.FailedFor
	case StageRolledBack:
		duration = p.RolledBackFor
	}
	if duration > 0 {
		until := now.UTC().Add(duration)
		run.RetainUntil = &until
	}
}

func PurgeExpired(ctx context.Context, store Store, now time.Time) (int, error) {
	runs, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, run := range runs {
		// Recoverable runs retain intent and ownership lineage until an operator
		// reconciles or safely aborts them; time alone may never orphan resources.
		if run.Stage == StageFailedRecoverable {
			continue
		}
		if run.RetainUntil == nil || now.UTC().Before(*run.RetainUntil) {
			continue
		}
		if run.Stage != StageFailedTerminal && run.Stage != StageRolledBack {
			continue
		}
		if err := store.Delete(ctx, run.RequestID, run.Version); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
