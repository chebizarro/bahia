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
	file, err := os.Open(u.Path)
	if err != nil {
		return 0, fmt.Errorf("F74a backup object cannot be opened")
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return 0, fmt.Errorf("F74a backup object is not a regular readable file")
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
