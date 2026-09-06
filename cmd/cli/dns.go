package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func dnsCommands() *cobra.Command {
	cmd := &cobra.Command{Use: "dns", Short: "Manage DNS through the signer-first Nostr control plane"}
	cmd.AddCommand(dnsZoneCreateCommand(), dnsPolicyApplyCommand(), dnsRecordSetCommand(), dnsDriftRemediateCommand())
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
			result, err := runDNSZoneCreate(cmd, client.DNSZoneCreateRequest{
				Name: name, Visibility: domain.ZoneVisibility(visibility), BackendRef: backendRef, TTL: ttl, Authoritative: authoritative,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
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
			result, err := runDNSPolicyApply(cmd, policy)
			if err != nil {
				return err
			}
			return outputSingle(result)
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
			result, err := runDNSRecordSet(cmd, client.DNSRecordSetRequest{
				ZoneName: zone, RecordName: name, RecordType: domain.DNSRecordType(recordType),
				Value: value, TTL: ttl, Reason: reason, ExpiresAt: expiresAt,
			})
			if err != nil {
				return err
			}
			return outputSingle(result)
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

func dnsDriftRemediateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "drift-remediate",
		Short: "Reconcile one DNS zone or all configured zones",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			zone, _ := cmd.Flags().GetString("zone")
			result, err := runDNSDriftRemediate(cmd, client.DNSDriftRemediateRequest{Zone: zone})
			if err != nil {
				return err
			}
			return outputSingle(result)
		},
	}
	cmd.Flags().String("zone", "", "Reconcile only this DNS zone; omit to reconcile all zones")
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
