package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func decodeFamily[T any](ctx context.Context, s *Server, kind int) ([]T, error) {
	records, err := s.readStateFamily(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(records))
	for _, record := range records {
		var item T
		if err := json.Unmarshal(record.Content, &item); err != nil {
			return nil, fmt.Errorf("decode family %d record: %w", kind, err)
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *Server) storeF74aRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	switch name {
	case "bahia_llm_list_releases":
		routeID, err := parseUUIDArg(args, "route_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		releases, err := decodeFamily[domain.LLMRelease](ctx, s, nostrpool.KindLLMReleaseRegistry)
		if err != nil {
			return nil, err
		}
		filtered := make([]domain.LLMRelease, 0)
		for _, release := range releases {
			if release.RouteID == routeID {
				filtered = append(filtered, release)
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt.After(filtered[j].CreatedAt) })
		limit, offset := limitOffsetArgs(args, 100)
		if offset >= len(filtered) {
			filtered = nil
		} else {
			filtered = filtered[offset:]
		}
		if limit > 0 && len(filtered) > limit {
			filtered = filtered[:limit]
		}
		out := make([]map[string]any, 0, len(filtered))
		for i := range filtered {
			out = append(out, llmReleaseToMap(&filtered[i]))
		}
		return jsonResult(map[string]any{"route_id": routeID.String(), "releases": out, "total": len(out), "registry_kind": nostrpool.KindLLMRouteRegistry})
	case "bahia_list_signatures", "bahia_list_verified_signatures", "bahia_has_verified_signature":
		artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		signatures, err := decodeFamily[domain.ArtifactSignature](ctx, s, nostrpool.KindArtifactSignatureRegistry)
		if err != nil {
			return nil, err
		}
		filtered := make([]domain.ArtifactSignature, 0)
		for _, sig := range signatures {
			if sig.ArtifactID != artifactID {
				continue
			}
			sig.NormalizeVerificationStatus()
			if name == "bahia_list_verified_signatures" && sig.VerificationStatus != domain.SignatureStatusVerified {
				continue
			}
			filtered = append(filtered, sig)
		}
		if name == "bahia_has_verified_signature" {
			verified := false
			for _, sig := range filtered {
				if sig.VerificationStatus == domain.SignatureStatusVerified {
					verified = true
					break
				}
			}
			return jsonResult(map[string]any{"artifact_id": artifactID.String(), "has_verified_signature": verified})
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt.After(filtered[j].CreatedAt) })
		return jsonResult(map[string]any{"artifact_id": artifactID.String(), "signatures": signaturesToMaps(filtered), "total": len(filtered)})
	case "bahia_get_signature":
		id, err := parseRequiredUUIDArg(args, "signature_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		record, err := s.readStateOne(ctx, nostrpool.KindArtifactSignatureRegistry, "id", id.String())
		if err != nil {
			return nil, err
		}
		if record == nil {
			return errorResult("signature not found"), nil
		}
		var sig domain.ArtifactSignature
		if err := json.Unmarshal(record.Content, &sig); err != nil {
			return nil, err
		}
		sig.NormalizeVerificationStatus()
		return jsonResult(signatureToMap(&sig))
	case "bahia_get_sbom", "bahia_get_sbom_packages":
		artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		sboms, err := decodeFamily[domain.ArtifactSBOM](ctx, s, nostrpool.KindArtifactSBOMRegistry)
		if err != nil {
			return nil, err
		}
		var latest *domain.ArtifactSBOM
		for i := range sboms {
			if sboms[i].ArtifactID == artifactID && (latest == nil || sboms[i].CreatedAt.After(latest.CreatedAt)) {
				latest = &sboms[i]
			}
		}
		if latest == nil {
			return errorResult("SBOM not found for artifact"), nil
		}
		if name == "bahia_get_sbom" {
			return jsonResult(sbomToMap(latest))
		}
		packages, err := decodeFamily[domain.SBOMPackage](ctx, s, nostrpool.KindSBOMPackageRegistry)
		if err != nil {
			return nil, err
		}
		filtered := make([]domain.SBOMPackage, 0)
		for _, pkg := range packages {
			if pkg.SBOMID == latest.ID {
				filtered = append(filtered, pkg)
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
		return jsonResult(map[string]any{"artifact_id": artifactID.String(), "sbom_id": latest.ID.String(), "packages": sbomPackagesToMaps(filtered), "total": len(filtered)})
	case "bahia_search_sbom_packages":
		query, _ := args["query"].(string)
		if query == "" {
			query, _ = args["package"].(string)
		}
		query = strings.TrimSpace(query)
		if query == "" {
			return errorResult("query is required"), nil
		}
		packages, err := decodeFamily[domain.SBOMPackage](ctx, s, nostrpool.KindSBOMPackageRegistry)
		if err != nil {
			return nil, err
		}
		filtered := make([]domain.SBOMPackage, 0)
		for _, pkg := range packages {
			if strings.Contains(strings.ToLower(pkg.Name), strings.ToLower(query)) {
				filtered = append(filtered, pkg)
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i].Name < filtered[j].Name })
		limit := optionalIntArg(args, "limit", 100)
		if limit <= 0 {
			limit = 100
		}
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
		return jsonResult(map[string]any{"query": query, "packages": sbomPackagesToMaps(filtered), "total": len(filtered)})
	case "bahia_get_observation":
		serviceID, err := uuid.Parse(stringArg(args, "service_id"))
		if err != nil {
			return errorResult("invalid service_id: " + err.Error()), nil
		}
		envID, err := uuid.Parse(stringArg(args, "environment_id"))
		if err != nil {
			return errorResult("invalid environment_id: " + err.Error()), nil
		}
		records, err := s.readStateFamily(ctx, nostrpool.KindRuntimeObservationState)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			f := record.Fields
			if f["service_id"] != serviceID.String() || f["environment_id"] != envID.String() {
				continue
			}
			observed, _ := time.Parse(time.RFC3339Nano, stringFromRecord(f, "observed_at"))
			return jsonResult(map[string]any{
				"id": stringFromRecord(f, "id"), "service_id": serviceID.String(), "environment_id": envID.String(),
				"image_digest": stringFromRecord(f, "observed_image_digest"), "container_id": stringFromRecord(f, "observed_container_id"),
				"health_status": stringFromRecord(f, "health_status"), "observed_at": observed.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		return errorResult("no observation found"), nil
	}
	return nil, fmt.Errorf("unsupported F74a read %s", name)
}
