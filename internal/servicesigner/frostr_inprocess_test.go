package servicesigner

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"

	"fiatjaf.com/promenade/frost"
	"github.com/btcsuite/btcd/btcec/v2"
)

// InProcessFrostrTransport runs peer shares in one process for deterministic
// crypto and latency tests. It is not a custody deployment transport.
type InProcessFrostrTransport struct {
	mu      sync.Mutex
	shares  map[int]frost.KeyShard
	pending map[[32]byte]inProcessRound
}

type inProcessRound struct {
	signer  *frost.Signer
	request FrostrCommitRequest
}

func NewInProcessFrostrTransport(shares ...frost.KeyShard) *InProcessFrostrTransport {
	t := &InProcessFrostrTransport{shares: make(map[int]frost.KeyShard), pending: make(map[[32]byte]inProcessRound)}
	for _, share := range shares {
		t.shares[share.ID] = share
	}
	return t
}

func (t *InProcessFrostrTransport) Commit(ctx context.Context, req FrostrCommitRequest) (frost.Commitment, error) {
	if err := ctx.Err(); err != nil {
		return frost.Commitment{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	share, ok := t.shares[req.PeerID]
	if !ok || req.Session == ([32]byte{}) {
		return frost.Commitment{}, errors.New("FROSTR peer or session unavailable")
	}
	if _, exists := t.pending[req.Session]; exists {
		return frost.Commitment{}, errors.New("FROSTR session replay")
	}
	cfg := &frost.Configuration{PublicKey: share.PublicKey, Threshold: 2, MaxSigners: 3, Participants: req.Participants[:]}
	signer, err := cfg.Signer(share, make(frost.LambdaRegistry))
	if err != nil {
		return frost.Commitment{}, errors.New("FROSTR peer share invalid")
	}
	commitment := signer.Commit(hexSession(req.Session))
	t.pending[req.Session] = inProcessRound{signer: signer, request: req}
	return commitment, nil
}

func hexSession(id [32]byte) string { return hex.EncodeToString(id[:]) }

func (t *InProcessFrostrTransport) Sign(ctx context.Context, req FrostrSignRequest) (frost.PartialSignature, error) {
	if err := ctx.Err(); err != nil {
		return frost.PartialSignature{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	round, ok := t.pending[req.Session]
	delete(t.pending, req.Session)
	if !ok || round.request != req.FrostrCommitRequest {
		return frost.PartialSignature{}, errors.New("FROSTR session mismatch")
	}
	if len(req.Commitments) != 2 || round.signer.Configuration.ValidateCommitmentList(req.Commitments) != nil {
		return frost.PartialSignature{}, errors.New("FROSTR commitments invalid")
	}
	group, _, _ := round.signer.Configuration.ComputeGroupCommitment(req.Commitments, req.Message[:])
	return round.signer.Sign(req.Message[:], group)
}

func (t *InProcessFrostrTransport) ECDH(ctx context.Context, req FrostrECDHRequest) (*btcec.JacobianPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	share, ok := t.shares[req.PeerID]
	if !ok || req.Session == ([32]byte{}) {
		return nil, errors.New("FROSTR ECDH peer unavailable")
	}
	pub, err := btcec.ParsePubKey(append([]byte{2}, req.Peer[:]...))
	if err != nil {
		return nil, errors.New("FROSTR ECDH pubkey invalid")
	}
	var point btcec.JacobianPoint
	pub.AsJacobian(&point)
	cfg := &frost.Configuration{PublicKey: share.PublicKey, Threshold: 2, MaxSigners: 3, Participants: req.Participants[:]}
	return cfg.CreateECDHShare(share, &point, make(frost.LambdaRegistry)), nil
}
