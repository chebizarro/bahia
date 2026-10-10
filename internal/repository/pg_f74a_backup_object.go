package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

// VerifyF74aLocalBackupObject checks actual bytes at a signed file:// object
// reference. This is a read-only point-in-time hash check, not evidence of
// independent custody, future retention, revocation, or recoverable credentials.
// It never authorizes deletion.
func VerifyF74aLocalBackupObject(ctx context.Context, proof F74aReceiptVerification) (int64, error) {
	if proof.ReceiptID == uuid.Nil || !validF74aDigest(proof.BackupObjectSHA256) || !time.Now().UTC().Before(proof.ExpiresAt) {
		return 0, fmt.Errorf("F74a receipt proof is incomplete or expired")
	}
	if !strings.HasPrefix(proof.BackupObjectRef, "file:///") {
		return 0, fmt.Errorf("F74a backup object is not a local file URI")
	}
	u, err := url.Parse(proof.BackupObjectRef)
	if err != nil || u.Scheme != "file" || u.Host != "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/") {
		return 0, fmt.Errorf("F74a backup object file URI is invalid")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// A blocking os.Open can hang forever on a FIFO (including a symlink to
	// one), before the context can be checked. Open nonblocking, reject a final
	// symlink, and classify the opened descriptor before any read.
	fd, err := unix.Open(u.Path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("F74a backup object cannot be opened")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return 0, fmt.Errorf("F74a backup object is not a regular readable file")
	}
	file := os.NewFile(uintptr(fd), "f74a-backup-object")
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("F74a backup object cannot be stated")
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, fmt.Errorf("F74a backup object read failed")
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || total != before.Size() {
		return 0, fmt.Errorf("F74a backup object changed during verification")
	}
	if !time.Now().UTC().Before(proof.ExpiresAt) {
		return 0, fmt.Errorf("F74a receipt proof expired during backup object verification")
	}
	if hex.EncodeToString(hash.Sum(nil)) != proof.BackupObjectSHA256 {
		return 0, fmt.Errorf("F74a backup object SHA-256 differs from signed receipt")
	}
	return total, nil
}
