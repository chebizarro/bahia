package controlplane

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

func requestPrincipal(request EncryptedRequest) *auth.Principal {
	pubkey := normalizeEncryptedPubkey(request.Event.PubKey.Hex())
	return &auth.Principal{Subject: pubkey, Method: auth.MethodNIP98, PubKey: pubkey}
}

func decodeEncryptedPayload(request EncryptedRequest, out any) error {
	if len(request.Envelope.Payload) == 0 || string(request.Envelope.Payload) == "null" {
		return nil
	}
	if err := json.Unmarshal(request.Envelope.Payload, out); err != nil {
		return fmt.Errorf("invalid encrypted request payload: %w", err)
	}
	return nil
}

func parseEncryptedUUID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid %s", field)
	}
	return id, nil
}

func normalizeEncryptedPubkey(pubkey string) string {
	return strings.ToLower(strings.TrimSpace(pubkey))
}

func validEncryptedRole(role domain.Role) bool {
	for _, candidate := range domain.AllRoles() {
		if candidate == role {
			return true
		}
	}
	return false
}
