// Command mls-ock is a local-only experiment. It is not imported by Bahia's daemon.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	mls "github.com/thomas-vilte/mls-go"
	"github.com/thomas-vilte/mls-go/ciphersuite"
	"go.uber.org/zap"
)

const (
	orgID                     = "mls-spike-org"
	keyPackageKind nostr.Kind = 443 // Requested legacy transport kind; current Marmot specifies 30443.
	exporterLabel             = "bahia-ock-v1"
)

type measurements struct {
	KeyPackageBytes      int    `json:"key_package_bytes"`
	KeyPackageEventBytes int    `json:"key_package_event_bytes"`
	WelcomeBytes         int    `json:"welcome_bytes"`
	JoinCommitBytes      int    `json:"join_commit_bytes"`
	LeaveCommitBytes     int    `json:"leave_commit_bytes"`
	JoinEvents           int    `json:"join_events"`
	LeaveEvents          int    `json:"leave_events"`
	EpochAfterJoin       uint64 `json:"epoch_after_join"`
	EpochAfterLeave      uint64 `json:"epoch_after_leave"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := run(ctx)
	if err != nil {
		panic(err)
	}
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
}

func run(ctx context.Context) (m measurements, err error) {
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		return m, fmt.Errorf("initialize local relay store: %w", err)
	}
	defer store.Close()
	relay.UseEventstore(store, 500)
	server := httptest.NewServer(relay)
	defer server.Close()
	pool := nostradapter.NewRelayPool([]string{"ws" + strings.TrimPrefix(server.URL, "http")}, zap.NewNop())
	defer pool.Close()
	pool.Connect(ctx)
	if pool.ConnectedCount() != 1 {
		return m, errors.New("local relay did not connect")
	}

	daemon, err := mls.NewClient([]byte("daemon:"+orgID), ciphersuite.MLS128DHKEMX25519)
	if err != nil {
		return m, fmt.Errorf("daemon MLS client: %w", err)
	}
	defer func() { err = errors.Join(err, daemon.Close()) }()
	member, err := mls.NewClient([]byte("member:"+orgID), ciphersuite.MLS128DHKEMX25519)
	if err != nil {
		return m, fmt.Errorf("member MLS client: %w", err)
	}
	defer func() { err = errors.Join(err, member.Close()) }()
	groupID, err := daemon.CreateGroup(ctx)
	if err != nil {
		return m, fmt.Errorf("create org group: %w", err)
	}
	keyPackage, err := member.FreshKeyPackageBytes(ctx)
	if err != nil {
		return m, fmt.Errorf("create key package: %w", err)
	}
	m.KeyPackageBytes = len(keyPackage)

	memberSigner := nostr.Generate()
	kpEvent := nostr.Event{Kind: keyPackageKind, CreatedAt: nostr.Now(), Content: base64.StdEncoding.EncodeToString(keyPackage)}
	if err := kpEvent.Sign(memberSigner); err != nil {
		return m, fmt.Errorf("sign key package event: %w", err)
	}
	m.KeyPackageEventBytes = len(kpEvent.String())
	results, err := pool.PublishWithResults(ctx, kpEvent)
	if err != nil {
		return m, fmt.Errorf("publish key package: %w", err)
	}
	if len(results) != 1 || !results[0].Succeeded() {
		return m, fmt.Errorf("key package relay OK not accepted: %+v", results)
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{keyPackageKind}, Authors: []nostr.PubKey{memberSigner.Public()}}
	sub, err := pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return m, fmt.Errorf("subscribe key package: %w", err)
	}
	defer sub.Close()
	var received *nostr.Event
	for {
		select {
		case <-ctx.Done():
			return m, ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				return m, errors.New("key package subscription closed before EOSE")
			}
			if err := nostradapter.ValidateInboundEvent(ev, time.Now().UTC(), 0); err != nil {
				return m, fmt.Errorf("invalid key package event: %w", err)
			}
			if received != nil {
				return m, errors.New("multiple key packages for member")
			}
			received = ev
		case <-sub.EndOfStoredEvents:
			if !sub.AllRelaysReachedEOSE() {
				return m, errors.New("key package history incomplete")
			}
			if received == nil {
				return m, errors.New("key package absent at EOSE")
			}
			goto joined
		}
	}
joined:
	decoded, err := base64.StdEncoding.DecodeString(received.Content)
	if err != nil {
		return m, fmt.Errorf("decode key package event: %w", err)
	}
	joinCommit, welcome, err := daemon.InviteMember(ctx, groupID, decoded)
	if err != nil {
		return m, fmt.Errorf("invite member: %w", err)
	}
	m.JoinCommitBytes, m.WelcomeBytes = len(joinCommit), len(welcome)
	joinedGroupID, err := member.JoinGroup(ctx, welcome)
	if err != nil {
		return m, fmt.Errorf("join welcome: %w", err)
	}
	if !bytes.Equal(groupID, joinedGroupID) {
		return m, errors.New("joined wrong MLS group")
	}
	m.EpochAfterJoin, err = daemon.Epoch(ctx, groupID)
	if err != nil {
		return m, err
	}
	memberEpoch, err := member.Epoch(ctx, groupID)
	if err != nil || memberEpoch != m.EpochAfterJoin {
		return m, errors.New("member epoch differs after join")
	}
	oldSecret, err := daemon.Export(ctx, groupID, exporterLabel, []byte(orgID), 32)
	if err != nil {
		return m, fmt.Errorf("daemon exporter: %w", err)
	}
	memberSecret, err := member.Export(ctx, groupID, exporterLabel, []byte(orgID), 32)
	if err != nil || !bytes.Equal(oldSecret, memberSecret) {
		return m, errors.New("member exporter differs after join")
	}
	oldKey := contentKey(oldSecret, m.EpochAfterJoin)
	recordCtx := controlplane.ConfidentialRecordContext{LegacyKind: 32005, DTag: "org:mls-spike", Topic: "org"}
	oldRecord, err := controlplane.EncryptConfidentialContent(ctx, oldKey, []byte("confidential org record"), recordCtx, nil, nil)
	if err != nil {
		return m, fmt.Errorf("encrypt joined record: %w", err)
	}
	plaintext, err := controlplane.DecryptConfidentialContent(contentKey(memberSecret, m.EpochAfterJoin), oldRecord, recordCtx)
	if err != nil || string(plaintext) != "confidential org record" {
		return m, errors.New("member cannot decrypt joined record")
	}

	leaveCommit, err := daemon.RemoveMember(ctx, groupID, []byte("member:"+orgID))
	if err != nil {
		return m, fmt.Errorf("remove member: %w", err)
	}
	m.LeaveCommitBytes = len(leaveCommit)
	m.EpochAfterLeave, err = daemon.Epoch(ctx, groupID)
	if err != nil || m.EpochAfterLeave <= m.EpochAfterJoin {
		return m, errors.New("remove did not advance epoch")
	}
	newSecret, err := daemon.Export(ctx, groupID, exporterLabel, []byte(orgID), 32)
	if err != nil || bytes.Equal(newSecret, oldSecret) {
		return m, errors.New("remove did not rotate exporter")
	}
	newKey := contentKey(newSecret, m.EpochAfterLeave)
	newRecord, err := controlplane.EncryptConfidentialContent(ctx, newKey, []byte("post-removal record"), recordCtx, nil, nil)
	if err != nil {
		return m, fmt.Errorf("encrypt new record: %w", err)
	}
	plaintext, err = controlplane.DecryptConfidentialContent(newKey, newRecord, recordCtx)
	if err != nil || string(plaintext) != "post-removal record" {
		return m, errors.New("daemon cannot decrypt new record")
	}
	// Give the old exporter the new epoch's metadata; this proves an AEAD key failure,
	// not merely an associated-data version mismatch.
	if _, err := controlplane.DecryptConfidentialContent(contentKey(oldSecret, m.EpochAfterLeave), newRecord, recordCtx); err == nil {
		return m, errors.New("old exporter decrypted new record")
	}
	m.JoinEvents, m.LeaveEvents = 3, 1 // KP + commit + welcome; remove commit.
	return m, nil
}

func contentKey(secret []byte, epoch uint64) controlplane.OrgContentKey {
	var key [32]byte
	copy(key[:], secret)
	return controlplane.OrgContentKey{OrgID: orgID, Version: int(epoch) + 1, Key: key}
}
