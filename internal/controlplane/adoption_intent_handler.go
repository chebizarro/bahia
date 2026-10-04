package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// AdoptionIntentHandler keeps the configured adoption operator authority
// distinct from normal tenant service writes. The adoption service owns
// canonical publication for imported services, environments and lineage.
type AdoptionIntentHandler struct {
	adoption AdoptionOperatorService
	allowed  []string
}

func NewAdoptionIntentHandler(adoption AdoptionOperatorService, allowed []string) *AdoptionIntentHandler {
	return &AdoptionIntentHandler{adoption: adoption, allowed: append([]string(nil), allowed...)}
}

func (*AdoptionIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteServices
}

func (h *AdoptionIntentHandler) AuthorizeIntent(_ context.Context, _ *TrustSet, intent *Intent) error {
	if authorizedContextVMPubkey(intent.Actor, h.allowed) {
		return nil
	}
	return fmt.Errorf("requester not in authorized adoption list")
}

func (h *AdoptionIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if intent.Op != "import" {
		return fmt.Errorf("unsupported adoption operation %q", intent.Op)
	}
	if h.adoption == nil {
		return fmt.Errorf("adoption service is not configured")
	}
	coordinate := "adoption:fleet"
	if intent.OrgID != uuid.Nil {
		coordinate = "adoption:" + intent.OrgID.String()
	}
	if intent.Coordinate != coordinate {
		return fmt.Errorf("adoption coordinate does not match organization")
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	var request adoptionImportEventRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	if !request.ImportAll && len(request.Selections) == 0 {
		return fmt.Errorf("import requires import_all=true or at least one selection")
	}
	targets, err := mapAdoptionEventTargets(request.Targets)
	if err != nil {
		return err
	}
	selections, err := mapAdoptionEventSelections(request.Selections)
	if err != nil {
		return err
	}
	if request.OrgID != "" && request.OrgID != intent.OrgID.String() {
		return fmt.Errorf("adoption org_id does not match intent organization")
	}
	results, err := h.adoption.Import(ctx, service.AdoptionImportRequest{Targets: targets, Selections: selections, ImportAll: request.ImportAll, OrgID: intent.OrgID})
	if err != nil {
		return err
	}
	intent.Result = map[string]any{"imports": dto.AdoptionImportResultResponsesFromService(results)}
	intent.StatusData = map[string]any{"candidate_count": len(results)}
	return nil
}
