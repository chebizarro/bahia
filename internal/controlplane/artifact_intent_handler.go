package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

// ArtifactIntentHandler reconciles operator-supplied artifact lineage through
// the same registry methods as the encrypted transport. Those methods own
// canonical build/artifact publication; this handler must not publish again.
type ArtifactIntentHandler struct {
	registry        RegistryMutationBackend
	services        ServiceReader
	signatureRoutes *EncryptedRouteHandlers
	builds          BuildResultLoader
	buildRegistrar  BuildResultArtifactRegistrar
}

func NewArtifactIntentHandler(registry RegistryMutationBackend, services ServiceReader) *ArtifactIntentHandler {
	return &ArtifactIntentHandler{registry: registry, services: services}
}

// ConfigureSignatureVerification shares the encrypted route's verifier and
// canonical signature repository after F74a wraps the repository in app.New.
func (h *ArtifactIntentHandler) ConfigureSignatureVerification(routes *EncryptedRouteHandlers) {
	h.signatureRoutes = routes
}

func (h *ArtifactIntentHandler) ConfigureBuildResultRegistration(builds BuildResultLoader, registrar BuildResultArtifactRegistrar) {
	h.builds = builds
	h.buildRegistrar = registrar
}

func (*ArtifactIntentHandler) PermissionFor(string) domain.Permission {
	return domain.PermWriteServices
}

func (h *ArtifactIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	if h.registry == nil || h.services == nil {
		return fmt.Errorf("artifact registry is not configured")
	}
	raw, err := json.Marshal(intent.Content)
	if err != nil {
		return err
	}
	switch intent.Op {
	case "register-build-result":
		if h.builds == nil || h.buildRegistrar == nil {
			return fmt.Errorf("build-result artifact registration is not configured")
		}
		if intent.ExpectedUpdatedAt != nil {
			return fmt.Errorf("register-build-result does not support expected_updated_at")
		}
		buildID, err := uuid.Parse(firstIntentString(intent.Content, "build_id"))
		if err != nil || buildID == uuid.Nil || intent.Coordinate != "build-result:"+buildID.String() {
			return fmt.Errorf("register-build-result requires matching build_id and coordinate")
		}
		build, err := h.builds.GetByID(ctx, buildID)
		if err != nil {
			return err
		}
		if build == nil {
			return fmt.Errorf("build %s not found", buildID)
		}
		if err := h.checkServiceOrg(ctx, build.ServiceID, intent.OrgID); err != nil {
			return err
		}
		if build.Status != domain.BuildStatusSucceeded {
			return fmt.Errorf("only a successful build result can register an artifact")
		}
		artifact, err := h.buildRegistrar.RegisterBuildResult(ctx, buildID)
		if err != nil {
			return err
		}
		if artifact == nil || artifact.BuildID != buildID || artifact.ServiceID != build.ServiceID {
			return fmt.Errorf("build result did not produce a verified artifact for the authorized service")
		}
		intent.Result = map[string]any{"artifact_id": artifact.ID.String(), "build_id": buildID.String(), "service_id": build.ServiceID.String(), "manifest_digest": artifact.ImageDigest}
		intent.StatusData = intent.Result
		return nil
	case "signature-verify":
		if h.signatureRoutes == nil || h.signatureRoutes.artifacts == nil || h.signatureRoutes.signatures == nil || h.signatureRoutes.signVerifier == nil {
			return fmt.Errorf("artifact signature verification is not configured")
		}
		if intent.ExpectedUpdatedAt != nil {
			return fmt.Errorf("artifact signature-verify does not support expected_updated_at")
		}
		id, err := uuid.Parse(firstIntentString(intent.Content, "artifact_id"))
		if err != nil || id == uuid.Nil || intent.Coordinate != "artifact:"+id.String() {
			return fmt.Errorf("artifact signature-verify requires matching artifact_id and coordinate")
		}
		artifact, err := h.signatureRoutes.artifacts.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if artifact == nil {
			return fmt.Errorf("artifact not found")
		}
		if err := h.checkServiceOrg(ctx, artifact.ServiceID, intent.OrgID); err != nil {
			return err
		}
		result, err := h.signatureRoutes.verifyArtifactSignaturesDirect(ctx, artifact)
		if err != nil {
			return err
		}
		delete(result, "signatures")
		intent.Result = result
		intent.StatusData = result
		return nil
	case "register":
		var payload dto.RegisterArtifactRequest
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		id, err := uuid.Parse(firstIntentString(intent.Content, "id"))
		if err != nil || id == uuid.Nil || intent.Coordinate != "artifact:"+id.String() {
			return fmt.Errorf("artifact registration requires matching client-minted id and coordinate")
		}
		if payload.BuildID == uuid.Nil || payload.ServiceID == uuid.Nil ||
			strings.TrimSpace(payload.ImageRepo) == "" || strings.TrimSpace(payload.ImageTag) == "" ||
			strings.TrimSpace(payload.ImageDigest) == "" {
			return fmt.Errorf("build_id, service_id, image_repo, image_tag, and image_digest are required")
		}
		if err := h.checkServiceOrg(ctx, payload.ServiceID, intent.OrgID); err != nil {
			return err
		}
		scan := domain.ScanStatus(strings.TrimSpace(payload.ScanStatus))
		if scan == "" {
			scan = domain.ScanStatusUnknown
		}
		artifact := &domain.Artifact{
			ID: id, BuildID: payload.BuildID, ServiceID: payload.ServiceID,
			ImageRepo: strings.TrimSpace(payload.ImageRepo), ImageTag: strings.TrimSpace(payload.ImageTag),
			ImageDigest: strings.TrimSpace(payload.ImageDigest), ManifestMediaType: strings.TrimSpace(payload.ManifestMediaType),
			SizeBytes: payload.SizeBytes, SBOMURL: strings.TrimSpace(payload.SBOMURL),
			SignatureRef: strings.TrimSpace(payload.SignatureRef), ScanStatus: scan, Metadata: payload.Metadata,
		}
		if err := h.registry.RegisterArtifact(ctx, artifact); err != nil {
			return err
		}
		intent.Result = map[string]any{"artifact_id": artifact.ID.String(), "artifact": artifact}
		intent.StatusData = map[string]any{"artifact_id": artifact.ID.String()}
		return nil
	case "import-observed":
		var payload dto.ImportObservedArtifactRequest
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		if payload.ServiceID == uuid.Nil || payload.EnvironmentID == uuid.Nil {
			return fmt.Errorf("service_id and environment_id are required")
		}
		coordinate := artifactImportCoordinate(payload.ServiceID, payload.EnvironmentID, payload.ImageDigest)
		if intent.Coordinate != coordinate {
			return fmt.Errorf("observed artifact coordinate does not match content")
		}
		if err := h.checkServiceOrg(ctx, payload.ServiceID, intent.OrgID); err != nil {
			return err
		}
		env, err := h.registry.GetEnvironment(ctx, payload.EnvironmentID)
		if err != nil {
			return err
		}
		if env == nil || env.OrgID != intent.OrgID {
			return fmt.Errorf("artifact environment must belong to the authorized organization")
		}
		result, err := h.registry.ImportObservedArtifact(ctx, service.ImportObservedArtifactInput{
			ServiceID: payload.ServiceID, EnvironmentID: payload.EnvironmentID,
			DeploymentUnitID: payload.DeploymentUnitID, ImageRepo: strings.TrimSpace(payload.ImageRepo),
			ImageTag: strings.TrimSpace(payload.ImageTag), ImageDigest: strings.TrimSpace(payload.ImageDigest),
			GitSHA: strings.TrimSpace(payload.GitSHA), GitRef: strings.TrimSpace(payload.GitRef),
			RequestedBy: intent.Actor,
		})
		if err != nil {
			return err
		}
		intent.Result = map[string]any{"import": result}
		intent.StatusData = map[string]any{"status": result.Status, "observation_id": result.ObservationID.String()}
		if result.Artifact != nil {
			intent.StatusData["artifact_id"] = result.Artifact.ID.String()
		}
		if result.Build != nil {
			intent.StatusData["build_id"] = result.Build.ID.String()
		}
		return nil
	default:
		return fmt.Errorf("unsupported artifact operation %q", intent.Op)
	}
}

func (h *ArtifactIntentHandler) checkServiceOrg(ctx context.Context, id, orgID uuid.UUID) error {
	svc, err := h.services.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if svc == nil || svc.OrgID == uuid.Nil || svc.OrgID != orgID {
		return fmt.Errorf("artifact service must belong to the authorized organization")
	}
	return nil
}

func artifactImportCoordinate(serviceID, environmentID uuid.UUID, digest string) string {
	return "artifact-import:" + serviceID.String() + ":" + environmentID.String() + ":" + strings.ToLower(strings.TrimSpace(digest))
}
