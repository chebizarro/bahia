// Package atomicfile provides snapshot-backed atomic file replacement helpers.
package atomicfile

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Operation identifies the filesystem stage that failed.
type Operation string

const (
	OperationRead       Operation = "read"
	OperationStat       Operation = "stat"
	OperationCreateTemp Operation = "create_temp"
	OperationWrite      Operation = "write"
	OperationChmod      Operation = "chmod"
	OperationSync       Operation = "sync"
	OperationClose      Operation = "close"
	OperationRename     Operation = "rename"
	OperationRemove     Operation = "remove"
)

// Error preserves the failing operation while exposing the underlying error.
type Error struct {
	Operation Operation
	Err       error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Snapshot captures the exact file bytes, permission bits, and prior existence.
type Snapshot struct {
	Data   []byte
	Mode   fs.FileMode
	Exists bool
}

// Capture snapshots path. missingMode is retained for callers that later restore
// a path which did not exist, matching their previous default-mode behavior.
func Capture(path string, missingMode fs.FileMode) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{Mode: missingMode}, nil
	}
	if err != nil {
		return Snapshot{}, &Error{Operation: OperationRead, Err: err}
	}
	info, err := os.Stat(path)
	if err != nil {
		return Snapshot{}, &Error{Operation: OperationStat, Err: err}
	}
	return Snapshot{Data: data, Mode: info.Mode().Perm(), Exists: true}, nil
}

// WriteFile atomically replaces path using a temporary file in the same
// directory. The context is checked after the durable temp-file write and before
// rename, preserving the existing adapter cancellation boundary.
func WriteFile(ctx context.Context, path, tempPattern string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return &Error{Operation: OperationCreateTemp, Err: err}
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return &Error{Operation: OperationWrite, Err: err}
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return &Error{Operation: OperationChmod, Err: err}
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return &Error{Operation: OperationSync, Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &Error{Operation: OperationClose, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return &Error{Operation: OperationRename, Err: err}
	}
	committed = true
	return nil
}

// Restore converges path to snapshot. A previously absent path is removed;
// existing content is restored through the same atomic replacement machinery.
func Restore(path, tempPattern string, snapshot Snapshot) error {
	if !snapshot.Exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return &Error{Operation: OperationRemove, Err: err}
		}
		return nil
	}
	return WriteFile(context.Background(), path, tempPattern, snapshot.Data, snapshot.Mode)
}
