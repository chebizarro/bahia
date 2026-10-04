package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/pkg/client"
	"github.com/spf13/cobra"
)

func secretOrg(cmd *cobra.Command, serviceID string) (*domain.Service, error) {
	svc, err := canonicalService(cmd, serviceID)
	if err != nil {
		return nil, err
	}
	if svc.OrgID == uuid.Nil {
		return nil, fmt.Errorf("service %s has no organization", serviceID)
	}
	return svc, nil
}

func secretRefForName(cmd *cobra.Command, serviceID, name, environmentID string) (*domain.SecretRef, error) {
	refs, unreadable, err := readCLIConfidentialRecords[domain.SecretRef](cmd, kinds.SecretRegistry)
	if err != nil {
		return nil, err
	}
	if unreadable != 0 {
		return nil, fmt.Errorf("cannot safely update secret: %d secret records are unreadable with this key", unreadable)
	}
	for i := range refs {
		ref := &refs[i]
		if ref.ServiceID.String() == serviceID && ref.Name == name && ((ref.EnvironmentID == nil && environmentID == "") || (ref.EnvironmentID != nil && ref.EnvironmentID.String() == environmentID)) {
			return ref, nil
		}
	}
	return nil, nil
}

func runSecretSetIntent(cmd *cobra.Command, serviceID, name, value, environmentID string) error {
	svc, err := secretOrg(cmd, serviceID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("secret value is required")
	}
	existing, err := secretRefForName(cmd, serviceID, name, environmentID)
	if err != nil {
		return err
	}
	secretID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	op := "create"
	if existing != nil {
		secretID = existing.ID
		op = "update"
	}
	content := map[string]interface{}{"id": secretID.String(), "service_id": serviceID, "name": name, "encryption_method": "nip44"}
	if existing != nil {
		if existing.UpdatedAt.IsZero() {
			return fmt.Errorf("canonical secret %s is missing updated_at", secretID)
		}
		content["expected_updated_at"] = existing.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if environmentID != "" {
		envID, err := uuid.Parse(environmentID)
		if err != nil || envID == uuid.Nil {
			return fmt.Errorf("environment must be a non-nil UUID")
		}
		content["environment_id"] = envID.String()
	}
	intentID, err := intentUUID("")
	if err != nil {
		return err
	}
	if err := publishCLIIntent(cmd, client.PublishIntentRequest{Domain: "secret", Op: op, Coordinate: secretID.String(), OrgID: svc.OrgID.String(), IntentID: intentID, Content: content, PlaintextSecretValue: value}); err != nil {
		return err
	}
	return outputSingle(map[string]string{"intent_id": intentID, "secret_id": secretID.String()})
}

func runSecretDeleteIntent(cmd *cobra.Command, serviceID, secretID string) error {
	svc, err := secretOrg(cmd, serviceID)
	if err != nil {
		return err
	}
	id, err := uuid.Parse(secretID)
	if err != nil || id == uuid.Nil {
		return fmt.Errorf("secret ID must be a non-nil UUID")
	}
	intentID, err := publishMutationIntentForOrg(cmd, "secret", "delete", id.String(), "", "", svc.OrgID.String(), map[string]interface{}{"id": id.String()})
	if err != nil {
		return err
	}
	return outputSingle(map[string]string{"intent_id": intentID, "secret_id": id.String()})
}

func validCLIOrgRole(role domain.Role) bool {
	for _, allowed := range domain.AllRoles() {
		if allowed == role {
			return true
		}
	}
	return false
}
