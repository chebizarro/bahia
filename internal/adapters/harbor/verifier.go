package harbor

import (
	"context"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

// Verifier implements service.ImageVerifier using the Harbor v2 API.
type Verifier struct {
	client *Client
	logger *zap.Logger
}

// NewVerifier creates a Harbor-backed image verifier.
func NewVerifier(client *Client, logger *zap.Logger) *Verifier {
	return &Verifier{client: client, logger: logger}
}

// VerifyImage checks that the given image exists in Harbor.
// imageRepo should be in "project/repository" format (e.g. "myproject/myimage").
// reference can be a tag (e.g. "v1.0") or a digest (e.g. "sha256:abc123...").
func (v *Verifier) VerifyImage(ctx context.Context, imageRepo, reference string) (*service.ImageVerification, error) {
	project, repo, err := parseImageRepo(imageRepo)
	if err != nil {
		return nil, err
	}

	info, err := v.client.GetArtifact(ctx, project, repo, reference)
	if err != nil {
		// Distinguish between "not found" (404) and real errors.
		// The Harbor client returns a formatted error with the status code.
		if strings.Contains(err.Error(), "returned 404") {
			return &service.ImageVerification{Exists: false}, nil
		}
		return nil, fmt.Errorf("harbor lookup failed: %w", err)
	}

	return &service.ImageVerification{
		Exists:     true,
		Digest:     info.Digest,
		ScanStatus: info.ScanStatus,
	}, nil
}

// parseImageRepo splits "project/repo" into its components.
// Supports multi-level repos like "project/sub/repo" where project is the first segment.
func parseImageRepo(imageRepo string) (project, repo string, err error) {
	parts := strings.SplitN(imageRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid image repo format %q: expected \"project/repository\"", imageRepo)
	}
	return parts[0], parts[1], nil
}

// Compile-time interface check.
var _ service.ImageVerifier = (*Verifier)(nil)
