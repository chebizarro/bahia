package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func dnsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "dns", Short: "Manage DNS through the signer-first Nostr control plane"}
	cmd.AddCommand(dnsZoneCreateCommand(), dnsPolicyApplyCommand(), dnsRecordSetCommand(), dnsDriftRemediateCommand(), dnsOverrideRetireCommand())
	cmd.AddCommand(dnsD72Commands()...)
	return cmd
}

func dnsZoneCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "zone-create",
		Short: "Create and reconcile a managed DNS zone",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, _ := cmd.Flags().GetString("name")
			visibility, _ := cmd.Flags().GetString("visibility")
			backendRef, _ := cmd.Flags().GetString("backend-ref")
			ttl, _ := cmd.Flags().GetInt("ttl")
			authoritative, _ := cmd.Flags().GetBool("authoritative")
			zone := domain.DNSZone{Name: name, Visibility: domain.ZoneVisibility(visibility), BackendRef: backendRef, TTL: ttl, Authoritative: authoritative}
			if err := domain.ValidateDNSZone(&zone); err != nil {
				return err
			}
			content := map[string]interface{}{"name": zone.Name, "visibility": string(zone.Visibility), "backend_ref": zone.BackendRef, "ttl": zone.TTL, "authoritative": zone.Authoritative}
			id, err := publishMutationIntent(cmd, "dns", "zone-create", "zone:"+zone.Name, "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": id, "zone": zone.Name})
		},
	}
	cmd.Flags().String("name", "", "DNS zone name")
	cmd.Flags().String("visibility", "", "Zone visibility: internal, external, edge, or mesh")
	cmd.Flags().String("backend-ref", "", "Configured DNS backend reference")
	cmd.Flags().Int("ttl", 0, "Default zone TTL in seconds")
	cmd.Flags().Bool("authoritative", false, "Answer authoritatively for the zone without forwarding unanswered query types")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("visibility")
	_ = cmd.MarkFlagRequired("backend-ref")
	_ = cmd.MarkFlagRequired("ttl")
	return cmd
}

func dnsPolicyApplyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy-apply",
		Short: "Apply a DNS policy from a JSON file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("file")
			policy, err := readDNSPolicyFile(path)
			if err != nil {
				return err
			}
			if policy.ID == uuid.Nil {
				policy.ID, err = uuid.NewV7()
				if err != nil {
					return err
				}
			}
			content := map[string]interface{}{}
			if err := decodeJSONFile(path, &content, true); err != nil {
				return err
			}
			content["id"] = policy.ID.String()
			id, err := publishMutationIntent(cmd, "dns", "policy-apply", "dnspolicy:"+policy.ID.String(), "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": id, "policy_id": policy.ID.String()})
		},
	}
	cmd.Flags().String("file", "", "Read the DNS policy JSON document from this file")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func dnsRecordSetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "record-set",
		Short: "Set a managed DNS record override",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			zone, _ := cmd.Flags().GetString("zone")
			name, _ := cmd.Flags().GetString("name")
			recordType, _ := cmd.Flags().GetString("type")
			value, _ := cmd.Flags().GetString("value")
			ttl, _ := cmd.Flags().GetInt("ttl")
			reason, _ := cmd.Flags().GetString("reason")
			expiresAtText, _ := cmd.Flags().GetString("expires-at")
			var expiresAt *time.Time
			if strings.TrimSpace(expiresAtText) != "" {
				parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(expiresAtText))
				if err != nil {
					return fmt.Errorf("parse --expires-at as RFC3339: %w", err)
				}
				expiresAt = &parsed
			}
			overrideID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			override := domain.DNSRecordOverride{ID: overrideID, ZoneName: zone, RecordName: name, RecordType: domain.DNSRecordType(recordType), Value: value, TTL: ttl, Reason: reason, ExpiresAt: expiresAt}
			content := map[string]interface{}{"id": overrideID.String(), "zone_name": override.ZoneName, "record_name": override.RecordName, "record_type": string(override.RecordType), "value": override.Value, "ttl": override.TTL, "reason": override.Reason}
			if expiresAt != nil {
				content["expires_at"] = expiresAt.UTC().Format(time.RFC3339Nano)
			}
			id, err := publishMutationIntent(cmd, "dns", "record-set", "dns-override:"+overrideID.String(), "", "", content)
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": id, "override_id": overrideID.String()})
		},
	}
	cmd.Flags().String("zone", "", "Managed DNS zone name")
	cmd.Flags().String("name", "", "Record name within the zone")
	cmd.Flags().String("type", "", "Record type: A, AAAA, CNAME, or SRV")
	cmd.Flags().String("value", "", "Record value")
	cmd.Flags().Int("ttl", 0, "Record TTL in seconds")
	cmd.Flags().String("reason", "", "Operator reason for the override")
	cmd.Flags().String("expires-at", "", "Optional override expiration timestamp in RFC3339 format")
	for _, flag := range []string{"zone", "name", "type", "value", "ttl", "reason"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func dnsOverrideRetireCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "override-retire",
		Short: "Retire a managed DNS record override by setting its expiry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			overrideID, _ := cmd.Flags().GetString("override-id")
			reason, _ := cmd.Flags().GetString("reason")
			if _, err := uuid.Parse(overrideID); err != nil {
				return fmt.Errorf("invalid override ID: %w", err)
			}
			id, err := publishMutationIntent(cmd, "dns", "override-retire", "dns-override:"+overrideID, "", "", map[string]interface{}{"override_id": overrideID, "reason": reason})
			if err != nil {
				return err
			}
			return outputSingle(map[string]string{"intent_id": id, "override_id": overrideID})
		},
	}
	cmd.Flags().String("override-id", "", "UUID of the DNS record override to retire")
	cmd.Flags().String("reason", "", "Operator reason for retirement")
	for _, flag := range []string{"override-id", "reason"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	return cmd
}

func dnsDriftRemediateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drift-remediate",
		Short: "Reconcile one DNS zone or all configured zones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			zone, _ := cmd.Flags().GetString("zone")
			retryKey, _ := cmd.Flags().GetString("idempotency-key")
			result, err := runDNSDriftRemediateIntent(cmd, client.DNSDriftRemediateRequest{Zone: zone, IdempotencyKey: retryKey})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().String("zone", "", "Reconcile only this DNS zone; omit to reconcile all zones")
	cmd.Flags().String("idempotency-key", "", "Explicit UUIDv7 intent ID for retrying this remediation")
	return cmd
}

func readDNSPolicyFile(path string) (client.DNSPolicyApplyRequest, error) {
	var policy client.DNSPolicyApplyRequest
	if err := decodeJSONFile(path, &policy, true); err != nil {
		return policy, fmt.Errorf("read DNS policy: %w", err)
	}
	domainPolicy := domain.DNSPolicy{
		ID: policy.ID, Name: policy.Name, ZoneID: policy.ZoneID, EnvironmentID: policy.EnvironmentID,
		Rules: policy.Rules, Enabled: policy.Enabled, Metadata: policy.Metadata,
		CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
	if err := domain.ValidateDNSPolicy(&domainPolicy); err != nil {
		return policy, fmt.Errorf("validate DNS policy: %w", err)
	}
	policy.Name = domainPolicy.Name
	return policy, nil
}
