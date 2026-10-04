package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var newCLIConfidentialReadPool = func(ctx context.Context, relays []string, signer nostr.Keyer) (client.SubscriptionPool, func(), error) {
	pool := nostrpool.NewRelayPool(relays, zap.NewNop(), nostrpool.WithAuthSigner(signer))
	pool.Connect(ctx)
	return client.WrapRelayPool(pool), pool.Close, nil
}

func newCLIReadSigner(cmd *cobra.Command) (nostr.Keyer, func() error, error) {
	privateKey, err := resolveNostrPrivateKeyInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	bunkerURI, clientKey, err := resolveNIP46OperatorInput(cmd)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(privateKey) != "" && bunkerURI != "" {
		return nil, nil, fmt.Errorf("configure either a NIP-46 bunker signer or a local private key, not both")
	}
	if bunkerURI != "" {
		signer, _, closeSigner, err := newCLINIP46Signer(cmd.Context(), bunkerURI, clientKey)
		if err != nil {
			return nil, nil, fmt.Errorf("connect NIP-46 read signer: %w", err)
		}
		decryptor, ok := signer.(nostr.Keyer)
		if !ok {
			_ = closeSigner()
			return nil, nil, fmt.Errorf("NIP-46 signer does not support NIP-44 decryption")
		}
		return decryptor, closeSigner, nil
	}
	if strings.TrimSpace(privateKey) == "" {
		return nil, nil, fmt.Errorf("confidential reads require --nostr-key-file or a NIP-46 bunker signer")
	}
	normalized, err := client.NormalizeNostrPrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	secret, err := nostr.SecretKeyFromHex(normalized)
	if err != nil {
		return nil, nil, fmt.Errorf("parse read signer: %w", err)
	}
	return keyer.NewPlainKeySigner(secret), nil, nil
}

func readCLIConfidentialRecords[T any](cmd *cobra.Command, family int, include ...func(*client.DecodedEvent) bool) (records []T, unreadable int, retErr error) {
	signer, closeSigner, err := newCLIReadSigner(cmd)
	if err != nil {
		return nil, 0, err
	}
	if closeSigner != nil {
		defer func() { retErr = errors.Join(retErr, closeSigner()) }()
	}
	servicePubkey := resolveOperatorServicePubkey(cmd)
	if servicePubkey == "" {
		return nil, 0, fmt.Errorf("--service-pubkey or BAHIA_NOSTR_SERVICE_PUBKEY is required for Nostr reads")
	}
	relays, err := resolveOperatorRelays(cmd)
	if err != nil {
		return nil, 0, err
	}
	storePath, err := nostrServiceStorePath(servicePubkey)
	if err != nil {
		return nil, 0, err
	}
	timeout, err := readEOSETimeout(cmd)
	if err != nil {
		return nil, 0, err
	}
	pool, closePool, err := newCLIConfidentialReadPool(cmd.Context(), relays, signer)
	if err != nil {
		return nil, 0, fmt.Errorf("connect to relays: %w", err)
	}
	defer closePool()
	nc, err := client.NewNostrClient(client.NostrClientConfig{
		StorePath: storePath, ServicePubkey: servicePubkey, Pool: pool, EOSETimeout: timeout,
	})
	if err != nil {
		return nil, 0, err
	}
	defer nc.Close()
	envelopes, envelopeResult, err := nc.SyncAndQueryFamily(cmd.Context(), kinds.OrgKeyEnvelope)
	if err != nil {
		return nil, 0, fmt.Errorf("sync key envelopes: %w", err)
	}
	events, familyResult, err := nc.SyncAndQueryFamily(cmd.Context(), family)
	if err != nil {
		return nil, 0, fmt.Errorf("sync confidential state: %w", err)
	}
	if !envelopeResult.Fresh || !familyResult.Fresh {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: relay data may be stale (no EOSE within timeout)")
	}
	reader, err := client.NewConfidentialReader(cmd.Context(), signer, servicePubkey, envelopes)
	if err != nil {
		return nil, 0, err
	}
	for _, event := range events {
		decoded, err := client.DecodeControlStateEvent(event)
		if err != nil {
			return nil, 0, err
		}
		if decoded.LegacyKind != family {
			return nil, 0, fmt.Errorf("event %s has unexpected family %d", event.GetID(), decoded.LegacyKind)
		}
		if len(include) > 0 && !include[0](decoded) {
			continue
		}
		if decoded.Deleted {
			continue
		}
		plaintext, readable, err := reader.Decrypt(event)
		if err != nil {
			return nil, 0, err
		}
		if !readable {
			unreadable++
			continue
		}
		var record T
		if err := json.Unmarshal(plaintext, &record); err != nil {
			return nil, 0, fmt.Errorf("decode confidential event %s: %w", event.GetID(), err)
		}
		records = append(records, record)
	}
	return records, unreadable, nil
}

func showUnreadable(cmd *cobra.Command, unreadable, readable int) bool {
	if unreadable == 0 {
		return false
	}
	if readable == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "not readable with this key")
		return true
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d record(s) not readable with this key\n", unreadable)
	return false
}

func withHTTPConfidentialRead(cmd *cobra.Command, run func() error) (retErr error) {
	closeSigner, err := configureNIP46HTTPClientAuth(cmd, apiClient)
	if err != nil {
		return err
	}
	if closeSigner != nil {
		defer func() { retErr = errors.Join(retErr, closeSigner()) }()
	}
	return run()
}

func listCLIOrgs(cmd *cobra.Command) error {
	if useHTTPReadFallback(cmd) {
		return withHTTPConfidentialRead(cmd, func() error {
			orgs, err := apiClient.ListOrgs(cmd.Context())
			if err != nil {
				return err
			}
			return renderCLIOrgs(orgs)
		})
	}
	orgs, unreadable, err := readCLIConfidentialRecords[domain.Organization](cmd, kinds.OrgRegistry)
	if err != nil {
		return err
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].ID.String() < orgs[j].ID.String() })
	if showUnreadable(cmd, unreadable, len(orgs)) {
		return nil
	}
	return renderCLIOrgs(orgs)
}

func renderCLIOrgs(orgs []domain.Organization) error {
	return output(orgs, []string{"ID", "NAME", "DISPLAY_NAME"}, func(o domain.Organization) []string {
		return []string{o.ID.String(), o.Name, o.DisplayName}
	})
}

func getCLIOrg(cmd *cobra.Command, idOrName string) error {
	if useHTTPReadFallback(cmd) {
		return withHTTPConfidentialRead(cmd, func() error {
			org, err := apiClient.GetOrg(cmd.Context(), idOrName)
			if err != nil {
				return err
			}
			return outputSingle(org)
		})
	}
	var include []func(*client.DecodedEvent) bool
	if parsed, err := uuid.Parse(idOrName); err == nil {
		include = append(include, func(record *client.DecodedEvent) bool { return record.DTag == parsed.String() })
	}
	orgs, unreadable, err := readCLIConfidentialRecords[domain.Organization](cmd, kinds.OrgRegistry, include...)
	if err != nil {
		return err
	}
	for i := range orgs {
		if orgs[i].ID.String() == idOrName || orgs[i].Name == idOrName {
			return outputSingle(&orgs[i])
		}
	}
	if showUnreadable(cmd, unreadable, 0) {
		return nil
	}
	return fmt.Errorf("organization %s not found", idOrName)
}

func listCLIOrgMembers(cmd *cobra.Command, orgID string) error {
	parsed, err := uuid.Parse(orgID)
	if err != nil {
		return fmt.Errorf("invalid organization ID %q: %w", orgID, err)
	}
	if useHTTPReadFallback(cmd) {
		return withHTTPConfidentialRead(cmd, func() error {
			members, err := apiClient.ListOrgMembers(cmd.Context(), orgID)
			if err != nil {
				return err
			}
			return renderCLIOrgMembers(members)
		})
	}
	all, unreadable, err := readCLIConfidentialRecords[domain.OrgMember](cmd, kinds.OrgMemberRegistry, func(record *client.DecodedEvent) bool {
		return strings.HasPrefix(record.DTag, "org:member:"+parsed.String()+":")
	})
	if err != nil {
		return err
	}
	members := make([]domain.OrgMember, 0)
	for _, member := range all {
		if member.OrgID == parsed {
			members = append(members, member)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Pubkey < members[j].Pubkey })
	if showUnreadable(cmd, unreadable, len(members)) {
		return nil
	}
	return renderCLIOrgMembers(members)
}

func renderCLIOrgMembers(members []domain.OrgMember) error {
	return output(members, []string{"PUBKEY", "ROLE", "NIP05", "JOINED"}, func(m domain.OrgMember) []string {
		return []string{truncate(m.Pubkey, 16), string(m.Role), m.NIP05, m.JoinedAt.Format("2006-01-02T15:04:05Z07:00")}
	})
}

func listCLISecrets(cmd *cobra.Command, serviceID string) error {
	parsed, err := uuid.Parse(serviceID)
	if err != nil {
		return fmt.Errorf("invalid service ID %q: %w", serviceID, err)
	}
	if useHTTPReadFallback(cmd) {
		return withHTTPConfidentialRead(cmd, func() error {
			secrets, err := apiClient.ListSecrets(cmd.Context(), serviceID)
			if err != nil {
				return err
			}
			return renderCLISecrets(secrets)
		})
	}
	all, unreadable, err := readCLIConfidentialRecords[client.SecretRef](cmd, kinds.SecretRegistry, func(record *client.DecodedEvent) bool {
		for _, tag := range record.Event.Tags {
			if len(tag) >= 2 && tag[0] == "service_id" {
				return tag[1] == parsed.String()
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	secrets := make([]client.SecretRef, 0)
	for _, secret := range all {
		if secret.ServiceID == parsed.String() {
			secrets = append(secrets, secret)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
	if showUnreadable(cmd, unreadable, len(secrets)) {
		return nil
	}
	return renderCLISecrets(secrets)
}

func renderCLISecrets(secrets []client.SecretRef) error {
	return output(secrets, []string{"ID", "NAME", "ENV", "ENCRYPTION", "VERSION"}, func(s client.SecretRef) []string {
		env := s.EnvironmentID
		if env == "" {
			env = "(all)"
		}
		return []string{s.ID, s.Name, env, s.EncryptionMethod, fmt.Sprintf("%d", s.Version)}
	})
}

func notificationMetadata(ch domain.NotificationChannel) (map[string]any, error) {
	if ch.OrgID == uuid.Nil {
		return map[string]any{"id": ch.ID.String(), "name": ch.Name, "channel_type": string(ch.ChannelType), "enabled": ch.Enabled, "fleet_scoped": true}, nil
	}
	_, content := nostrpool.NotificationChannelRegistryRecord(&ch, false, false)
	var metadata map[string]any
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func listCLINotificationChannels(cmd *cobra.Command) error {
	var channels []map[string]any
	if useHTTPReadFallback(cmd) {
		err := withHTTPConfidentialRead(cmd, func() error {
			all, err := apiClient.ListNotificationChannels(cmd.Context())
			if err != nil {
				return err
			}
			for _, ch := range all {
				metadata, err := notificationMetadata(ch)
				if err != nil {
					return err
				}
				channels = append(channels, metadata)
			}
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		var unreadable int
		var err error
		var visible []domain.NotificationChannel
		visible, unreadable, err = readCLIConfidentialRecords[domain.NotificationChannel](cmd, kinds.NotificationChannelRegistry)
		if err != nil {
			return err
		}
		if showUnreadable(cmd, unreadable, len(visible)) {
			return nil
		}
		for _, ch := range visible {
			metadata, err := notificationMetadata(ch)
			if err != nil {
				return err
			}
			channels = append(channels, metadata)
		}
	}
	sort.Slice(channels, func(i, j int) bool { return fmt.Sprint(channels[i]["id"]) < fmt.Sprint(channels[j]["id"]) })
	return output(channels, []string{"ID", "NAME", "TYPE", "ENABLED"}, func(ch map[string]any) []string {
		return []string{fmt.Sprint(ch["id"]), fmt.Sprint(ch["name"]), fmt.Sprint(ch["channel_type"]), fmt.Sprint(ch["enabled"])}
	})
}

func getCLINotificationChannel(cmd *cobra.Command, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid notification channel ID %q: %w", id, err)
	}
	if useHTTPReadFallback(cmd) {
		return withHTTPConfidentialRead(cmd, func() error {
			ch, err := apiClient.GetNotificationChannel(cmd.Context(), id)
			if err != nil {
				return err
			}
			metadata, err := notificationMetadata(*ch)
			if err != nil {
				return err
			}
			return outputSingle(metadata)
		})
	}
	channels, unreadable, err := readCLIConfidentialRecords[domain.NotificationChannel](cmd, kinds.NotificationChannelRegistry, func(record *client.DecodedEvent) bool { return record.DTag == id })
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.ID.String() == id {
			metadata, err := notificationMetadata(channel)
			if err != nil {
				return err
			}
			return outputSingle(metadata)
		}
	}
	if showUnreadable(cmd, unreadable, 0) {
		return nil
	}
	return fmt.Errorf("notification channel %s not found", id)
}

func notificationCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "notifications", Short: "Read notification channels"}
	channels := &cobra.Command{Use: "channels", Short: "Notification channels"}
	channels.AddCommand(
		&cobra.Command{Use: "list", Short: "List notification channel metadata", RunE: func(cmd *cobra.Command, _ []string) error { return listCLINotificationChannels(cmd) }},
		&cobra.Command{Use: "get [id]", Short: "Get notification channel metadata", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error { return getCLINotificationChannel(cmd, args[0]) }},
	)
	cmd.AddCommand(channels)
	return cmd
}
