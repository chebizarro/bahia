package servicesigner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/promenade/frost"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"golang.org/x/crypto/hkdf"
)

// The round coordinator owns message authentication, replay rejection and
// delivery. It must bind every reply to the session, peer and requested phase.
// A missing coordinator is a fatal configuration error, never a local-key fallback.
type FrostrRoundTransport interface {
	Commit(context.Context, FrostrCommitRequest) (frost.Commitment, error)
	Sign(context.Context, FrostrSignRequest) (frost.PartialSignature, error)
	ECDH(context.Context, FrostrECDHRequest) (*btcec.JacobianPoint, error)
}

// FrostrFence serializes service-key authorship across writers. A production
// implementation must use a durable, cross-process lease; a process mutex is
// insufficient for a replicated daemon.
type FrostrFence interface {
	Acquire(context.Context, nostr.PubKey) (release func(), err error)
}

type FrostrCommitRequest struct {
	PeerID       int
	Session      [32]byte
	Message      [32]byte
	Participants [2]int
}

type FrostrSignRequest struct {
	FrostrCommitRequest
	Commitments []frost.Commitment
}

type FrostrECDHRequest struct {
	PeerID       int
	Session      [32]byte
	Peer         nostr.PubKey
	Participants [2]int
}

type frostrKeyer struct {
	mu        sync.Mutex // serializes signing, ECDH, and session use within this writer
	share     frost.KeyShard
	peers     map[int]frost.PublicKeyShard
	pubkey    nostr.PubKey
	transport FrostrRoundTransport
	fence     FrostrFence
}

func (k *frostrKeyer) Close() error {
	if closer, ok := k.transport.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func openNostrFrostrTransport(ctx context.Context, cfg config.NostrSignerFrostrConfig, relay FrostrEventRelay, timeout time.Duration) (FrostrRoundTransport, error) {
	keyHex, err := readMountedShare(cfg.TransportKeyFile)
	if err != nil {
		return nil, errors.New("FROSTR transport key file unavailable")
	}
	local, err := nostrutil.NewLocalKeyer(keyHex)
	if err != nil {
		return nil, errors.New("FROSTR transport key invalid")
	}
	localPub, err := local.GetPublicKey(ctx)
	if err != nil {
		return nil, errors.New("FROSTR transport key unavailable")
	}
	peers := make(map[int]nostr.PubKey, 2)
	for _, peer := range cfg.Peers {
		pub, err := nostr.PubKeyFromHex(peer.Pubkey)
		if err != nil || pub == localPub {
			return nil, errors.New("FROSTR transport peer identity invalid")
		}
		peers[peer.ID] = pub
	}
	return NewNostrFrostrTransport(ctx, local, peers, relay, timeout)
}

var _ nostr.Keyer = (*frostrKeyer)(nil)
var _ BinaryCipher = (*frostrKeyer)(nil)

func openFrostr(cfg config.NostrConfig, expected nostr.PubKey, transport FrostrRoundTransport, fence FrostrFence) (*frostrKeyer, error) {
	if transport == nil {
		return nil, errors.New("FROSTR round transport is not configured")
	}
	if fence == nil {
		return nil, errors.New("FROSTR single-writer fence is not configured")
	}
	if expected == nostr.ZeroPK {
		return nil, errors.New("FROSTR requires a pinned service pubkey")
	}
	shareHex, err := readMountedShare(cfg.Signer.Frostr.ShareFile)
	if err != nil {
		return nil, err
	}
	var share frost.KeyShard
	if err := decodeShare(&share, shareHex); err != nil {
		return nil, err
	}
	if share.ID < 1 || share.ID > 3 || share.Secret == nil || share.Secret.IsZero() || share.PublicKey == nil {
		return nil, errors.New("FROSTR share is invalid")
	}
	share.PublicKey.ToAffine()
	if share.PublicKey.Y.IsOdd() {
		return nil, errors.New("FROSTR group key must have even Y")
	}
	var actual nostr.PubKey
	copy(actual[:], share.PublicKey.X.Bytes()[:])
	if actual != expected {
		return nil, errors.New("FROSTR share differs from pinned service pubkey")
	}
	// ValidateKeyShard checks that the mounted secret actually matches its
	// advertised public share. Its upstream error includes the secret bytes,
	// so never return it to callers or logs.
	check := &frost.Configuration{PublicKey: share.PublicKey, Threshold: 2, MaxSigners: 3}
	if err := check.ValidateKeyShard(share); err != nil {
		return nil, errors.New("FROSTR mounted share is invalid")
	}
	peers := make(map[int]frost.PublicKeyShard, 2)
	if len(cfg.Signer.Frostr.Peers) != 2 {
		return nil, errors.New("FROSTR requires two configured peers")
	}
	for _, p := range cfg.Signer.Frostr.Peers {
		if p.ID < 1 || p.ID > 3 || p.ID == share.ID || peers[p.ID].ID != 0 {
			return nil, errors.New("FROSTR peer identifiers must be distinct from the local share")
		}
		var pub frost.PublicKeyShard
		if err := decodePublicShare(&pub, p.PublicShare); err != nil || pub.ID != p.ID {
			return nil, errors.New("FROSTR peer public share is invalid")
		}
		combined, err := frost.AggregatePublicKeyShards([]frost.PublicKeyShard{share.PublicKeyShard, pub})
		if err != nil || !samePoint(combined, share.PublicKey) {
			return nil, errors.New("FROSTR peer public share does not recover group key")
		}
		peers[p.ID] = pub
	}
	if len(peers) != 2 {
		return nil, errors.New("FROSTR requires two distinct peers")
	}
	return &frostrKeyer{share: share, peers: peers, pubkey: actual, transport: transport, fence: fence}, nil
}

func samePoint(a, b *btcec.JacobianPoint) bool {
	if a == nil || b == nil {
		return false
	}
	a.ToAffine()
	b.ToAffine()
	return a.X.Equals(&b.X) && a.Y.Equals(&b.Y)
}

func readMountedShare(path string) (string, error) {
	if path == "" {
		return "", errors.New("FROSTR share_file is required")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("cannot inspect FROSTR share_file")
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return "", errors.New("FROSTR share_file must be a regular owner-only file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open FROSTR share_file")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o077 != 0 || !os.SameFile(st, opened) {
		return "", errors.New("FROSTR share_file changed or is not owner-only")
	}
	b, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(b) > 1024 {
		return "", errors.New("cannot read bounded FROSTR share_file")
	}
	return strings.TrimSpace(string(b)), nil
}

func encodedPublicShareSize(b []byte) (int, bool) {
	if len(b) < 39 {
		return 0, false
	}
	n := binary.LittleEndian.Uint32(b[2:6])
	if n > 2 {
		return 0, false
	}
	return 39 + int(n)*33, true
}

func decodeShare(dst *frost.KeyShard, value string) error {
	b, err := hex.DecodeString(value)
	if err != nil {
		return errors.New("FROSTR share must be hex")
	}
	size, ok := encodedPublicShareSize(b)
	if !ok || len(b) != size+65 {
		return errors.New("FROSTR share has invalid length")
	}
	if err := dst.Decode(b); err != nil {
		return errors.New("FROSTR share decode failed")
	}
	return nil
}

func decodePublicShare(dst *frost.PublicKeyShard, value string) error {
	b, err := hex.DecodeString(value)
	if err != nil {
		return errors.New("FROSTR public share must be hex")
	}
	size, ok := encodedPublicShareSize(b)
	if !ok || len(b) != size {
		return errors.New("FROSTR public share has invalid length")
	}
	if _, err := dst.Decode(b); err != nil {
		return errors.New("FROSTR public share decode failed")
	}
	return nil
}

func (k *frostrKeyer) GetPublicKey(ctx context.Context) (nostr.PubKey, error) {
	if err := ctx.Err(); err != nil {
		return nostr.ZeroPK, err
	}
	return k.pubkey, nil
}

func (k *frostrKeyer) configFor(peerID int) (*frost.Configuration, [2]int) {
	ids := [2]int{k.share.ID, peerID}
	sort.Ints(ids[:])
	return &frost.Configuration{PublicKey: k.share.PublicKey, Threshold: 2, MaxSigners: 3, Participants: ids[:]}, ids
}

func (k *frostrKeyer) orderedPeers() []int {
	ids := make([]int, 0, len(k.peers))
	for id := range k.peers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func randomSession() ([32]byte, error) {
	var id [32]byte
	_, err := rand.Read(id[:])
	return id, err
}

func (k *frostrKeyer) SignEvent(ctx context.Context, event *nostr.Event) error {
	if event == nil {
		return errors.New("event is required")
	}
	if event.PubKey != nostr.ZeroPK && event.PubKey != k.pubkey {
		return errors.New("event author differs from FROSTR group key")
	}
	if event.ID != (nostr.ID{}) || event.Sig != ([64]byte{}) {
		return errors.New("event must be unsigned")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	release, err := k.fence.Acquire(ctx, k.pubkey)
	if err != nil || release == nil {
		return errors.New("FROSTR single-writer fence unavailable")
	}
	defer release()
	requested := *event
	requested.Tags = cloneTags(event.Tags)
	requested.PubKey = k.pubkey
	requested.SetID()
	var message [32]byte
	copy(message[:], requested.ID[:])
	for _, peerID := range k.orderedPeers() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := k.signWith(ctx, peerID, message, &requested); err == nil {
			*event = requested
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("FROSTR threshold signature unavailable or invalid")
}

func (k *frostrKeyer) signWith(ctx context.Context, peerID int, message [32]byte, event *nostr.Event) error {
	cfg, ids := k.configFor(peerID)
	local, err := cfg.Signer(k.share, make(frost.LambdaRegistry))
	if err != nil {
		return errors.New("FROSTR local share rejected")
	}
	session, err := randomSession()
	if err != nil {
		return errors.New("FROSTR session randomness unavailable")
	}
	req := FrostrCommitRequest{PeerID: peerID, Session: session, Message: message, Participants: ids}
	localCommit := local.Commit(hex.EncodeToString(session[:]))
	remoteCommit, err := k.transport.Commit(ctx, req)
	if err != nil {
		return errors.New("FROSTR commitment unavailable")
	}
	commits := []frost.Commitment{localCommit, remoteCommit}
	sort.Slice(commits, func(i, j int) bool { return commits[i].SignerID < commits[j].SignerID })
	if remoteCommit.SignerID != peerID || cfg.ValidateCommitmentList(commits) != nil {
		return errors.New("FROSTR commitment invalid")
	}
	wireCommits := make([]frost.Commitment, len(commits))
	for i, c := range commits {
		if err := wireCommits[i].Decode(c.Encode()); err != nil {
			return errors.New("FROSTR commitment encoding failed")
		}
	}
	remoteSig, err := k.transport.Sign(ctx, FrostrSignRequest{FrostrCommitRequest: req, Commitments: wireCommits})
	if err != nil || remoteSig.SignerIdentifier != peerID {
		return errors.New("FROSTR remote partial signature unavailable")
	}
	groupCommitment, binding, finalNonce := cfg.ComputeGroupCommitment(commits, message[:])
	localSig, err := local.Sign(message[:], groupCommitment)
	if err != nil {
		return errors.New("FROSTR local partial signature failed")
	}
	for i, part := range []frost.PartialSignature{localSig, remoteSig} {
		id := part.SignerIdentifier
		pub := k.share.PublicKeyShard
		if id == peerID {
			pub = k.peers[peerID]
		}
		var commit frost.Commitment
		for _, c := range commits {
			if c.SignerID == id {
				commit = c
			}
		}
		if commit.SignerID == 0 || cfg.VerifyPartialSignature(pub, commit.BinoncePublic, binding, finalNonce, part, message[:], make(frost.LambdaRegistry)) != nil {
			return fmt.Errorf("FROSTR partial signature %d invalid", i)
		}
	}
	sig, err := cfg.AggregateSignatures(finalNonce, []frost.PartialSignature{localSig, remoteSig})
	if err != nil {
		return errors.New("FROSTR signature aggregation failed")
	}
	copy(event.Sig[:], sig.Serialize())
	if !event.CheckID() || !event.VerifySignature() {
		return errors.New("FROSTR aggregate failed BIP-340 verification")
	}
	return nil
}

// ConversationKey computes NIP-44's key from two Lagrange-weighted ECDH
// contributions. No service secret scalar is reconstructed.
func (k *frostrKeyer) ConversationKey(ctx context.Context, peer nostr.PubKey) ([32]byte, error) {
	var zero [32]byte
	peerPoint, err := btcec.ParsePubKey(append([]byte{2}, peer[:]...))
	if err != nil || peer == nostr.ZeroPK {
		return zero, errors.New("invalid NIP-44 peer pubkey")
	}
	var point btcec.JacobianPoint
	peerPoint.AsJacobian(&point)
	k.mu.Lock()
	defer k.mu.Unlock()
	release, err := k.fence.Acquire(ctx, k.pubkey)
	if err != nil || release == nil {
		return zero, errors.New("FROSTR single-writer fence unavailable")
	}
	defer release()
	for _, peerID := range k.orderedPeers() {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		cfg, ids := k.configFor(peerID)
		session, err := randomSession()
		if err != nil {
			return zero, errors.New("FROSTR session randomness unavailable")
		}
		local := cfg.CreateECDHShare(k.share, &point, make(frost.LambdaRegistry))
		remote, err := k.transport.ECDH(ctx, FrostrECDHRequest{PeerID: peerID, Session: session, Peer: peer, Participants: ids})
		if err != nil {
			continue
		}
		shared, err := cfg.AggregateECDHShards([]*btcec.JacobianPoint{local, remote})
		if err != nil {
			continue
		}
		var x [32]byte
		shared.X.PutBytesUnchecked(x[:])
		derived := hkdf.Extract(sha256.New, x[:], []byte("nip44-v2"))
		copy(zero[:], derived)
		return zero, nil
	}
	return zero, errors.New("FROSTR threshold ECDH unavailable")
}

func (k *frostrKeyer) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	key, err := k.ConversationKey(ctx, recipient)
	if err != nil {
		return "", err
	}
	return nip44.Encrypt(plaintext, key)
}
func (k *frostrKeyer) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	key, err := k.ConversationKey(ctx, sender)
	if err != nil {
		return "", err
	}
	return nip44.Decrypt(ciphertext, key)
}
func (k *frostrKeyer) EncryptBytes(ctx context.Context, plaintext []byte, recipient nostr.PubKey) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("NIP-44 plaintext must not be empty")
	}
	return k.Encrypt(ctx, string(plaintext), recipient)
}
func (k *frostrKeyer) DecryptBytes(ctx context.Context, ciphertext string, sender nostr.PubKey) ([]byte, error) {
	value, err := k.Decrypt(ctx, ciphertext, sender)
	return []byte(value), err
}
func (k *frostrKeyer) Nip04Encrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", fmt.Errorf("NIP-04 for FROSTR: %w", errors.ErrUnsupported)
}
func (k *frostrKeyer) Nip04Decrypt(context.Context, string, nostr.PubKey) (string, error) {
	return "", fmt.Errorf("NIP-04 for FROSTR: %w", errors.ErrUnsupported)
}
