package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

// resolveCLITrustedPubkey reads the operator-root coordinate through the
// already connected pool before any service-authored state is trusted. A
// deadline without EOSE is an error, never an absence proof or stale fallback.
func resolveCLITrustedPubkey(cmd *cobra.Command, legacy, storePath string, pool client.SubscriptionPool, timeout time.Duration) (string, error) {
	serviceID := strings.TrimSpace(operatorServiceID)
	root := strings.ToLower(strings.TrimSpace(operatorRootPubkey))
	if serviceID == "" && root == "" {
		return legacy, nil
	}
	if serviceID == "" || root == "" || root == strings.ToLower(legacy) {
		return "", fmt.Errorf("trust epoch requires a stable service ID and independent operator root")
	}
	state, err := trustepoch.OpenBolt(storePath + ".trust-epoch.bolt")
	if err != nil {
		return "", err
	}
	defer func() { _ = state.Close() }()
	store, err := localstore.Open(storePath)
	if err != nil {
		return "", err
	}
	defer func() { _ = store.Close() }()
	v := &trustepoch.Verifier{OperatorRoot: root, LegacyPubkey: strings.ToLower(legacy),
		Source: trustepoch.LocalSource{Events: store}, Persistence: state}
	operator, err := nostr.PubKeyFromHex(root)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)},
		Authors: []nostr.PubKey{operator}, Tags: nostr.TagMap{"d": []string{"trust-epoch:" + serviceID}}}
	sub, err := pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return "", fmt.Errorf("subscribe to trust epoch: %w", err)
	}
	defer sub.Close()
	var head *nostr.Event
	consider := func(event *nostr.Event) {
		if event != nil && (head == nil || event.CreatedAt > head.CreatedAt ||
			(event.CreatedAt == head.CreatedAt && event.ID.Hex() < head.ID.Hex())) {
			copy := *event
			head = &copy
		}
	}
	for {
		select {
		case event, ok := <-sub.Events():
			if !ok {
				return "", fmt.Errorf("trust epoch subscription closed before EOSE")
			}
			consider(event)
		case <-sub.EndOfStoredEvents():
			draining := true
			for draining {
				select {
				case event, ok := <-sub.Events():
					if ok {
						consider(event)
					} else {
						draining = false
					}
				default:
					draining = false
				}
			}
			if head != nil {
				if _, err := v.AcceptHead(ctx, serviceID, *head); err != nil {
					return "", err
				}
				if _, err := store.SaveEvent(*head); err != nil {
					return "", err
				}
			}
			resolved, err := v.ServiceIdentity(ctx, serviceID)
			if err != nil {
				return "", err
			}
			return resolved.Pubkey, nil
		case <-ctx.Done():
			return "", fmt.Errorf("trust epoch query did not complete: %w", ctx.Err())
		}
	}
}

// resolveCLIServiceIdentity covers command paths whose own client creates its
// pool internally (ContextVM requests and intent publication). They must pin
// the recipient/status author before constructing that client.
func resolveCLIServiceIdentity(cmd *cobra.Command, relays []string, legacy string) (string, error) {
	if operatorServiceID == "" && operatorRootPubkey == "" {
		return legacy, nil
	}
	storePath, err := nostrServiceStorePath(legacy)
	if err != nil {
		return "", err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return "", err
	}
	pool, closePool, err := newCLIReadPool(cmd.Context(), relays, nil)
	if err != nil {
		return "", fmt.Errorf("connect to trust-epoch relays: %w", err)
	}
	defer closePool()
	return resolveCLITrustedPubkey(cmd, legacy, storePath, pool, timeout)
}
