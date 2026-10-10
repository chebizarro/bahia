package signet

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// Match Signet's exact statement-byte cap before base64 transport encoding.
const maxSBOMDSSEStatementBytes = 64 << 10

// KeyID identifies the existing Bahia service pubkey pinned at construction.
func (s *EpochSigner) KeyID() string {
	if s == nil {
		return ""
	}
	return s.expected.Hex()
}

// SignStatement requests a Signet-produced BIP-340 signature over Bahia's
// DSSE PAE digest of these exact statement bytes. It has no digest-sign,
// raw-key, or no-epoch fallback.
func (s *EpochSigner) SignStatement(ctx context.Context, statement []byte) ([]byte, error) {
	if s == nil {
		return nil, ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(statement) == 0 || len(statement) > maxSBOMDSSEStatementBytes {
		return nil, errors.New("SBOM DSSE statement is empty or oversized")
	}
	session, err := s.checkedSession(ctx)
	if err != nil {
		return nil, err
	}
	lease, err := s.lease(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Signet writer lease: %w", err)
	}
	if err := s.checkLease(lease); err != nil {
		return nil, err
	}
	if !session.alive() {
		return nil, ErrNotConnected
	}
	result, err := session.rpc(ctx, "sign_bahia_sbom_dsse", []string{
		base64.StdEncoding.EncodeToString(statement), strconv.FormatUint(lease.Epoch, 10),
	})
	if err != nil {
		return nil, fmt.Errorf("Signet epoch sign_bahia_sbom_dsse: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !session.alive() {
		return nil, ErrNotConnected
	}
	if result == "" {
		return nil, errors.New("Signet epoch sign_bahia_sbom_dsse returned no result")
	}
	if len(result) != base64.StdEncoding.EncodedLen(64) {
		return nil, errors.New("Signet returned malformed SBOM DSSE signature")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(result)
	if err != nil || len(signature) != 64 {
		return nil, errors.New("Signet returned malformed SBOM DSSE signature")
	}
	current, err := s.lease(ctx)
	if err != nil {
		return nil, fmt.Errorf("recheck Signet writer lease: %w", err)
	}
	if current.Epoch != lease.Epoch || current.OwnerPubkey != lease.OwnerPubkey {
		return nil, errors.New("Signet writer lease changed during SBOM DSSE signing")
	}
	if err := s.checkLease(current); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease.Epoch < s.highestEpoch || !s.now().Before(current.ExpiresAt) {
		return nil, errors.New("Signet writer lease became stale before SBOM DSSE signature acceptance")
	}
	if !session.alive() {
		return nil, ErrNotConnected
	}
	return signature, nil
}
