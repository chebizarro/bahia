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
	if intent.Op != "import" && intent.Op != "scan" {
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
	if intent.Op == "scan" {
		var scan adoptionScanEventRequest
		if err := json.Unmarshal(raw, &scan); err != nil {
			return err
		}
		if scan.Offset < 0 || scan.Limit < 0 || scan.Limit > 100 {
			return fmt.Errorf("scan offset must be non-negative and limit must be 0..100")
		}
		targets, err := mapAdoptionEventTargets(scan.Targets)
		if err != nil {
			return err
		}
		previews, err := h.adoption.Scan(ctx, service.AdoptionScanRequest{Targets: targets})
		if err != nil {
			return err
		}
		mapped := dto.AdoptionPreviewResponsesFromService(previews)
		page, count := adoptionScanStatusPage(mapped, scan.Offset, scan.Limit)
		intent.Result = page
		intent.StatusData = page
		intent.StatusData["total_findings"] = count
		return nil
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

// The scan status is public relay data. DTO mapping redacts runtime secrets;
// this page additionally caps the serialized result below the 30315 budget.
func adoptionScanStatusPage(previews []dto.AdoptionPreviewResponse, offset, limit int) (map[string]any, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	page := map[string]any{"findings": []any{}, "offset": offset, "limit": limit, "truncated": false}
	findings := make([]any, 0)
	count := 0
	appendFinding := func(entry map[string]any) {
		if count >= offset && len(findings) < limit {
			candidate := append(findings, entry)
			encoded, _ := json.Marshal(candidate)
			if len(encoded) <= 12*1024 {
				findings = candidate
			}
		}
		count++
	}
	for _, preview := range previews {
		if len(preview.Containers) == 0 && preview.Error != "" {
			appendFinding(map[string]any{"target_name": boundedIntentStatusText(preview.Target.Name, 128), "scan_failed": true})
		}
		for _, container := range preview.Containers {
			discovered := container.Discovered
			entry := map[string]any{
				"target_name":                    boundedIntentStatusText(preview.Target.Name, 128),
				"endpoint_ref":                   boundedIntentStatusText(preview.Target.EndpointRef, 128),
				"container_id":                   boundedIntentStatusText(discovered.ContainerID, 128),
				"container_name":                 boundedIntentStatusText(discovered.ContainerName, 256),
				"image_ref":                      boundedIntentStatusText(discovered.ImageRef, 256),
				"proposed_service_name":          boundedIntentStatusText(container.ProposedServiceName, 256),
				"existing_service_id":            container.ExistingServiceID,
				"will_update":                    container.WillUpdate,
				"adoptable":                      container.Adoptable,
				"warnings_count":                 len(container.Warnings),
				"redacted_environment_key_count": len(discovered.RedactedEnvironmentKeys),
				"redacted_label_key_count":       len(discovered.RedactedLabelKeys),
			}
			if len(discovered.ContainerName) > 256 || len(discovered.ImageRef) > 256 || len(preview.Target.Name) > 128 {
				entry["item_truncated"] = true
			}
			appendFinding(entry)
		}
	}
	page["findings"] = findings
	page["next_offset"] = offset + len(findings)
	page["truncated"] = offset+len(findings) < count
	return page, count
}
