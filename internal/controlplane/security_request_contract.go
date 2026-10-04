package controlplane

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type SecurityScannerControlPlane interface {
	SubmitScan(ctx context.Context, req service.SecurityScanRequest) (*service.SecurityScanAccepted, error)
	Rescan(ctx context.Context, req service.SecurityRescanRequest) (*service.SecurityScanAccepted, error)
	ListFindings(ctx context.Context, req service.SecurityFindingsListRequest) (*service.SecurityFindingsListResult, error)
	ListSchedules(ctx context.Context, req service.SecuritySchedulesListRequest) (*service.SecuritySchedulesListResult, error)
}

type securityScanParams struct {
	Target service.SecurityScanTargetInput `json:"target"`
	Force  bool                            `json:"force,omitempty"`
}

func validateSecurityTargetInput(input service.SecurityScanTargetInput) error {
	switch input.Type {
	case domain.SecurityTargetSBOM:
		if input.SBOM == nil {
			return fmt.Errorf("security/scan sbom target is required")
		}
		if input.SBOM.Subject.Type == "" || (strings.TrimSpace(input.SBOM.Subject.Digest) == "" && strings.TrimSpace(input.SBOM.Subject.ID) == "") {
			return fmt.Errorf("security/scan sbom subject type and digest or id are required")
		}
		if input.SBOM.Format == "" || input.SBOM.Storage == "" || strings.TrimSpace(input.SBOM.LocationURI) == "" || strings.TrimSpace(input.SBOM.PayloadSHA256) == "" || strings.TrimSpace(input.SBOM.ReferenceDTag) == "" {
			return fmt.Errorf("security/scan sbom format, storage, location_uri, payload_sha256, and reference_d_tag are required")
		}
	case domain.SecurityTargetPackage:
		if input.Package == nil || strings.TrimSpace(input.Package.Ecosystem) == "" || strings.TrimSpace(input.Package.Name) == "" {
			return fmt.Errorf("security/scan package ecosystem and name are required")
		}
	case domain.SecurityTargetPURL:
		if strings.TrimSpace(input.PURL) == "" {
			return fmt.Errorf("security/scan purl is required")
		}
	case domain.SecurityTargetCommit:
		if input.Commit == nil || strings.TrimSpace(input.Commit.CommitHash) == "" {
			return fmt.Errorf("security/scan commit hash is required")
		}
	default:
		return fmt.Errorf("security/scan target type must be sbom, package, purl, or commit")
	}
	return nil
}
