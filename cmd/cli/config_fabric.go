package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func configCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Publish and inspect fleet config desired state"}
	cmd.PersistentFlags().StringVar(&outboxPath, "outbox-path", "", "CLI publish outbox path")

	publishCmd := &cobra.Command{
		Use:   "publish",
		Short: "Sign and publish config desired state directly to relays",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("file")
			body, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read config request: %w", err)
			}
			var request service.ConfigPublishRequest
			if err := json.Unmarshal(body, &request); err != nil {
				return fmt.Errorf("decode config request: %w", err)
			}
			receipt, err := runConfigPublish(cmd, request)
			if err != nil {
				return err
			}
			return outputSingle(receipt)
		},
	}
	publishCmd.Flags().String("file", "", "JSON config publish request")
	_ = publishCmd.MarkFlagRequired("file")

	driftCmd := &cobra.Command{
		Use:   "drift",
		Short: "List desired-versus-applied config drift",
		RunE: func(cmd *cobra.Command, _ []string) error {
			drift, err := runConfigDrift(cmd)
			if err != nil {
				return err
			}
			return output(drift, []string{"SERVICE", "POLICY", "SCOPE", "DESIRED", "APPLIED", "DRIFT", "LAST REJECTION", "WITHDRAWN"}, func(item service.ConfigDrift) []string {
				withdrawn := ""
				if item.Withdrawn {
					withdrawn = "withdrawn: " + item.WithdrawnReason + " (last applied config kept; publish a newer version)"
				}
				return []string{
					item.ServiceID,
					item.PolicyName,
					item.Scope,
					fmt.Sprintf("%d:%s", item.DesiredVersion, item.DesiredEventID),
					fmt.Sprintf("%d:%s", item.AppliedVersion, item.AppliedEventID),
					fmt.Sprintf("%t", item.Drift),
					item.LastRejectionReason,
					withdrawn,
				}
			})
		},
	}

	rollbackCmd := &cobra.Command{
		Use:   "rollback [event-id]",
		Short: "Republish a prior desired event at the next version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			receipt, err := runConfigRollback(cmd, args[0])
			if err != nil {
				return err
			}
			return outputSingle(receipt)
		},
	}

	cmd.AddCommand(publishCmd, driftCmd, rollbackCmd)
	return cmd
}

type configCLIState struct {
	pool   *nostrpool.RelayPool
	store  *client.NostrClient
	outbox *localstore.Outbox
	relays []string
}

func (s *configCLIState) close() {
	if s.store != nil {
		_ = s.store.Close()
	}
	if s.outbox != nil {
		_ = s.outbox.Close()
	}
	if s.pool != nil {
		s.pool.Close()
	}
}

func openConfigCLIState(cmd *cobra.Command, signer nostr.Signer) (*configCLIState, error) {
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, err
	}
	servicePubkey := resolveOperatorServicePubkey(cmd)
	if servicePubkey == "" {
		return nil, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for config state")
	}
	storePath, err := nostrServiceStorePath(servicePubkey)
	if err != nil {
		return nil, err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return nil, err
	}
	var options []nostrpool.RelayPoolOption
	if signer != nil {
		options = append(options, nostrpool.WithAuthSigner(signer))
	}
	pool := nostrpool.NewRelayPool(relays, zap.NewNop(), options...)
	pool.Connect(cmd.Context())
	state := &configCLIState{pool: pool, relays: relays}
	state.store, err = client.NewNostrClient(client.NostrClientConfig{
		StorePath: storePath, ServicePubkey: servicePubkey, Pool: client.WrapRelayPool(pool), EOSETimeout: timeout,
	})
	if err != nil {
		state.close()
		return nil, err
	}
	outboxFile := outboxPath
	if outboxFile == "" {
		outboxFile = cliOutboxDefaultPath()
	}
	state.outbox, err = localstore.OpenOutbox(outboxFile)
	if err != nil {
		state.close()
		return nil, fmt.Errorf("open CLI outbox: %w", err)
	}
	return state, nil
}

func (s *configCLIState) sync(cmd *cobra.Command, requireFresh bool) error {
	result, err := s.store.SyncConfigFabric(cmd.Context())
	if err != nil {
		return err
	}
	if !result.Fresh {
		if requireFresh {
			return fmt.Errorf("cannot publish config without EOSE from every configured relay; local version history may be stale")
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: relay config data may be stale (no EOSE within timeout)")
	}
	return nil
}

func configSigner(cmd *cobra.Command, required bool) (nostr.Signer, func(), error) {
	key, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	bunkerURI, clientKey, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	if key != "" && bunkerURI != "" {
		return nil, nil, fmt.Errorf("configure either a NIP-46 signer or a local private key")
	}
	if bunkerURI != "" {
		signer, _, closeSigner, err := newCLINIP46Signer(cmd.Context(), bunkerURI, clientKey)
		if err != nil {
			return nil, nil, err
		}
		return signer, func() { _ = closeSigner() }, nil
	}
	if key == "" {
		if required {
			return nil, nil, fmt.Errorf("configure an operator nsec or NIP-46 signer")
		}
		return nil, func() {}, nil
	}
	key, err = client.NormalizeNostrPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	secret, err := nostr.SecretKeyFromHex(key)
	if err != nil {
		return nil, nil, err
	}
	return keyer.NewPlainKeySigner(secret), func() {}, nil
}

func runConfigPublish(cmd *cobra.Command, request service.ConfigPublishRequest) (*service.ConfigPublishReceipt, error) {
	signer, closeSigner, err := configSigner(cmd, true)
	if err != nil {
		return nil, err
	}
	defer closeSigner()
	state, err := openConfigCLIState(cmd, signer)
	if err != nil {
		return nil, err
	}
	defer state.close()
	if err := state.sync(cmd, true); err != nil {
		return nil, err
	}
	return state.publish(cmd.Context(), signer, request)
}

func (s *configCLIState) publish(ctx context.Context, signer nostr.Signer, request service.ConfigPublishRequest) (*service.ConfigPublishReceipt, error) {
	pubkey, err := signer.GetPublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve operator pubkey: %w", err)
	}
	maxVersion, createdAt, err := s.versionAndTimestamp(request, pubkey.Hex())
	if err != nil {
		return nil, err
	}
	if request.Version <= maxVersion {
		return nil, fmt.Errorf("version must advance monotonically: got %d, latest is %d", request.Version, maxVersion)
	}
	event, err := service.ComposeConfigEvent(request, createdAt)
	if err != nil {
		return nil, err
	}
	if err := signer.SignEvent(ctx, event); err != nil {
		return nil, fmt.Errorf("sign config event: %w", err)
	}
	if event.PubKey != pubkey || !event.CheckID() || !event.VerifySignature() {
		return nil, fmt.Errorf("operator signer returned an invalid config event")
	}
	targetRelays := append([]string(nil), s.relays...)
	sort.Strings(targetRelays)
	targetHash := sha256.Sum256([]byte(strings.Join(targetRelays, "\n")))
	publisher := nostrpool.NewPublisher(config.NostrConfig{Relays: s.relays, PublishQuorum: 1}, s.pool, nil, zap.NewNop(),
		nostrpool.WithLocalOutbox(s.outbox, nil), nostrpool.WithPublishTarget("cli:config-fabric:"+hex.EncodeToString(targetHash[:8])))
	defer publisher.Close()
	_, publishErr := publisher.PublishPresignedEvent(ctx, *event, "config-fabric.desired")
	delivery := service.ConfigDeliveryAccepted
	if nostrutil.IsPublishQueued(publishErr) {
		delivery = service.ConfigDeliveryQueued
	} else if publishErr != nil {
		return nil, fmt.Errorf("publish config event: %w", publishErr)
	}
	return &service.ConfigPublishReceipt{EventID: event.ID.Hex(), PubKey: pubkey.Hex(), Kind: request.Kind,
		Version: request.Version, DTag: "service:" + request.ServiceID + ":" + request.PolicyName, Delivery: delivery}, nil
}

func (s *configCLIState) localEvents(includeFailed bool) ([]nostr.Event, error) {
	events := s.store.QueryConfigFabric()
	entries, err := s.outbox.ListEntries(nil, 0)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		seen[event.ID.Hex()] = true
	}
	for _, entry := range entries {
		if entry.EntityType != "config-fabric.desired" || (!includeFailed && entry.State == localstore.OutboxFailed) || seen[entry.Event.ID.Hex()] {
			continue
		}
		events = append(events, entry.Event)
	}
	return events, nil
}

func (s *configCLIState) versionAndTimestamp(request service.ConfigPublishRequest, pubkey string) (int, time.Time, error) {
	events, err := s.localEvents(true)
	if err != nil {
		return 0, time.Time{}, err
	}
	maxVersion := 0
	createdAt := time.Now().UTC()
	for _, event := range events {
		if event.PubKey.Hex() != pubkey {
			continue
		}
		prior, err := service.ConfigRequestFromEvent(event)
		if err != nil || prior.ServiceID != request.ServiceID || prior.PolicyName != request.PolicyName {
			continue
		}
		if prior.Kind == request.Kind && int64(event.CreatedAt) >= createdAt.Unix() {
			createdAt = time.Unix(int64(event.CreatedAt)+1, 0).UTC()
		}
		if prior.Scope == request.Scope && prior.Version > maxVersion {
			maxVersion = prior.Version
		}
	}
	return maxVersion, createdAt, nil
}

func runConfigRollback(cmd *cobra.Command, eventID string) (*service.ConfigPublishReceipt, error) {
	signer, closeSigner, err := configSigner(cmd, true)
	if err != nil {
		return nil, err
	}
	defer closeSigner()
	state, err := openConfigCLIState(cmd, signer)
	if err != nil {
		return nil, err
	}
	defer state.close()
	if err := state.sync(cmd, true); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(eventID)))
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("rollback event_id must be a 64-character hex event id")
	}
	var id nostr.ID
	copy(id[:], raw)
	prior, found := state.store.ConfigEventByID(id)
	if !found {
		entry, exists, err := state.outbox.Get(id)
		if err != nil {
			return nil, err
		}
		if !exists || entry.EntityType != "config-fabric.desired" {
			return nil, fmt.Errorf("rollback config event not found in local store or CLI outbox")
		}
		prior = entry.Event
	}
	request, err := service.ConfigRequestFromEvent(prior)
	if err != nil {
		return nil, fmt.Errorf("rollback source is not a valid desired event: %w", err)
	}
	pubkey, err := signer.GetPublicKey(cmd.Context())
	if err != nil {
		return nil, err
	}
	if prior.PubKey != pubkey {
		return nil, fmt.Errorf("rollback source must be signed by the configured operator")
	}
	maxVersion, _, err := state.versionAndTimestamp(request, pubkey.Hex())
	if err != nil {
		return nil, err
	}
	request.Version = maxVersion + 1
	return state.publish(cmd.Context(), signer, request)
}

func runConfigDrift(cmd *cobra.Command) ([]service.ConfigDrift, error) {
	signer, closeSigner, err := configSigner(cmd, false)
	if err != nil {
		return nil, err
	}
	defer closeSigner()
	state, err := openConfigCLIState(cmd, signer)
	if err != nil {
		return nil, err
	}
	defer state.close()
	if err := state.sync(cmd, false); err != nil {
		return nil, err
	}
	events, err := state.localEvents(false)
	if err != nil {
		return nil, err
	}
	return service.ConfigDriftFromEvents(events)
}
