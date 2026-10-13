package signet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/nostrout"
)

type managementReply struct {
	response signetJSONRPCResponse
	err      error
}

// managementReplyStream owns one reply REQ and its JSON-RPC waiters for one
// provisioner bunker connection. The relay pool handles per-relay reconnects;
// this supervisor reopens the REQ if every relay ends it.
type managementReplyStream struct {
	pool        *nostrpool.RelayPool
	bunker      *nostrout.Bunker
	signetPK    nostr.PubKey
	provisioner nostr.PubKey
	logger      *zap.Logger
	cancel      context.CancelFunc
	done        chan struct{}

	mu      sync.Mutex
	pending map[string]chan managementReply
	ready   chan struct{}
	subErr  error
	seen    map[string]struct{} // event-id dedupe across REQ reopens
}

func newManagementReplyStream(ctx context.Context, pool *nostrpool.RelayPool, bunker *nostrout.Bunker, signetPK, provisioner nostr.PubKey, logger *zap.Logger) *managementReplyStream {
	ctx, cancel := context.WithCancel(ctx)
	s := &managementReplyStream{
		pool: pool, bunker: bunker, signetPK: signetPK, provisioner: provisioner,
		logger: logger, cancel: cancel, done: make(chan struct{}),
		pending: make(map[string]chan managementReply),
		ready:   make(chan struct{}), subErr: errors.New("waiting for Signet reply EOSE"),
		seen: make(map[string]struct{}),
	}
	go s.supervise(ctx)
	return s
}

func (s *managementReplyStream) Close() {
	s.cancel()
	<-s.done
}

func (s *managementReplyStream) register(ctx context.Context, id string) (<-chan managementReply, func(), error) {
	for {
		s.mu.Lock()
		ready := s.ready
		if s.subErr == nil {
			ch := make(chan managementReply, 1)
			s.pending[id] = ch
			s.mu.Unlock()
			return ch, func() {
				s.mu.Lock()
				delete(s.pending, id)
				s.mu.Unlock()
			}, nil
		}
		s.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("await Signet reply subscription: %w", ctx.Err())
		case <-s.done:
			return nil, nil, ErrNotConnected
		}
	}
}

func (s *managementReplyStream) setUp() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil {
		s.subErr = nil
		close(s.ready)
	}
}

func (s *managementReplyStream) setDown(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr == nil {
		s.ready = make(chan struct{})
	}
	s.subErr = err
	for id, waiter := range s.pending {
		select {
		case waiter <- managementReply{err: fmt.Errorf("Signet reply subscription ended: %w", err)}:
		default:
		}
		delete(s.pending, id)
	}
}

func (s *managementReplyStream) supervise(ctx context.Context) {
	defer close(s.done)
	backoff := defaultReconnectMin
	for {
		caughtUp, err := s.readReplies(ctx)
		if ctx.Err() != nil {
			s.setDown(ctx.Err())
			return
		}
		s.setDown(err)
		s.logger.Warn("Signet management reply subscription ended; reconnecting", zap.Error(err), zap.Duration("backoff", backoff))

		if caughtUp {
			backoff = defaultReconnectMin
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			s.setDown(ctx.Err())
			return
		case <-timer.C:
		}
		backoff = min(backoff*2, defaultReconnectMax)
	}
}

func (s *managementReplyStream) readReplies(ctx context.Context) (caughtUp bool, err error) {
	sub, err := s.pool.SubscribeWithOptions(ctx, []nostr.Filter{{
		Kinds: []nostr.Kind{signetKindGiftWrap},
		Tags:  nostr.TagMap{"p": []string{s.provisioner.Hex()}},
		Since: nostr.Timestamp(time.Now().Add(-signetReplyWindow).Unix()),
	}}, nostrpool.SubscribeOptions{AwaitUnavailableRelays: true, ResumeOverlap: signetReplyWindow, ValidateEvent: s.plausibleReply})
	if err != nil {
		return false, fmt.Errorf("subscribe to Signet management replies: %w", err)
	}
	defer sub.Close()
	eose, closed := sub.RelayEOSE, sub.Closed
	for {
		select {
		case <-ctx.Done():
			return caughtUp, ctx.Err()
		case _, ok := <-eose:
			if !ok {
				eose = nil
				continue
			}
			if !caughtUp {
				caughtUp = true
				s.setUp()
			}
		case ev, ok := <-sub.Events:
			if !ok {
				if gaveUp := sub.GaveUp(); gaveUp != nil {
					return caughtUp, gaveUp
				}
				return caughtUp, errors.New("Signet management reply subscription closed")
			}
			if !s.markSeen(ev.ID.Hex()) {
				continue
			}
			resp, err := s.openReply(ctx, ev)
			if err != nil {
				continue
			}
			s.mu.Lock()
			waiter := s.pending[resp.ID]
			s.mu.Unlock()
			if waiter != nil {
				select {
				case waiter <- managementReply{response: resp}:
				default:
				}
			}
		case c, ok := <-closed:
			if !ok {
				closed = nil
				continue
			}
			s.logger.Warn("Signet management relay closed reply subscription", zap.String("relay", c.RelayURL), zap.String("reason", c.Reason), zap.Bool("terminal", c.Terminal))

		}
	}
}

func (s *managementReplyStream) markSeen(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.seen[id]; exists {
		return false
	}
	s.seen[id] = struct{}{}
	return true
}

func (s *managementReplyStream) plausibleReply(ev *nostr.Event) bool {
	if ev.Kind != signetKindGiftWrap || len(ev.Content) == 0 || len(ev.Content) > maxReplyContent {
		return false
	}
	addressed := false
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == s.provisioner.Hex() {
			addressed = true
		}
	}
	return addressed && ev.CheckID() && ev.VerifySignature()
}

func (s *managementReplyStream) openReply(ctx context.Context, gift *nostr.Event) (signetJSONRPCResponse, error) {
	var none signetJSONRPCResponse
	sealJSON, err := s.bunker.NIP44Decrypt(ctx, gift.PubKey, gift.Content)
	if err != nil {
		return none, err
	}
	var seal nostr.Event
	if err := json.Unmarshal([]byte(sealJSON), &seal); err != nil {
		return none, err
	}
	if seal.Kind != nostr.KindSeal || seal.PubKey != s.signetPK || !seal.CheckID() || !seal.VerifySignature() {
		return none, errors.New("Signet management seal is invalid")
	}
	rumorJSON, err := s.bunker.NIP44Decrypt(ctx, seal.PubKey, seal.Content)
	if err != nil {
		return none, err
	}
	var rumor nostr.Event
	if err := json.Unmarshal([]byte(rumorJSON), &rumor); err != nil {
		return none, err
	}
	if rumor.Kind != nostr.KindDirectMessage || rumor.PubKey != s.signetPK || !rumor.CheckID() {
		return none, errors.New("Signet management rumor is invalid")
	}
	addressed := false
	for _, tag := range rumor.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == s.provisioner.Hex() {
			addressed = true
		}
	}
	if !addressed {
		return none, errors.New("Signet management rumor has no recipient")
	}
	var resp signetJSONRPCResponse
	if err := json.Unmarshal([]byte(rumor.Content), &resp); err != nil {
		return none, err
	}
	if resp.JSONRPC != "2.0" || resp.ID == "" || len(resp.Result) == 0 && resp.Error == nil {
		return none, errors.New("not a Signet management JSON-RPC response")
	}
	return resp, nil
}
