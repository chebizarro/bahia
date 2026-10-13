package reconcile

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/build"
	"github.com/openagentsinc/bahia/internal/domain"
)

// ToolBuildEffect uses the immutable toolset label as an observation fence.
// A recovered effect_started step is never built again unless the existing
// image proves that the first build completed.
type ToolBuildEffect struct {
	Builder interface {
		CheckImageExists(context.Context, string) (string, bool, error)
		BuildImage(context.Context, build.BuildRequest) (*build.BuildResult, error)
	}
}

func (e ToolBuildEffect) Observe(ctx context.Context, snapshot ApplySnapshot) (bool, *domain.RuntimeObservation, error) {
	if e.Builder == nil || snapshot.Tool == nil || snapshot.Tool.Step != "build" {
		return false, nil, fmt.Errorf("bahia-4f10q.2.1: tool build effect is not configured")
	}
	imageID, found, err := e.Builder.CheckImageExists(ctx, snapshot.Tool.ToolsetHash)
	if err != nil || !found {
		return false, nil, err
	}
	if imageID == "" {
		return false, nil, fmt.Errorf("tool image observation has no image ID")
	}
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(snapshot.Tool.ToolsetHash+":"+imageID))
	return true, &domain.RuntimeObservation{ID: id}, nil
}

func (e ToolBuildEffect) Apply(ctx context.Context, snapshot ApplySnapshot, _ map[string]string) error {
	if e.Builder == nil || snapshot.Tool == nil || snapshot.Tool.Step != "build" {
		return fmt.Errorf("bahia-4f10q.2.1: tool build effect is not configured")
	}
	_, err := e.Builder.BuildImage(ctx, build.BuildRequest{BaseImage: snapshot.Tool.BaseImage,
		Tools: snapshot.Tool.Tools, ToolsetHash: snapshot.Tool.ToolsetHash,
		SourceEventID: snapshot.Tool.RequestEventID, TargetRepo: snapshot.Tool.DerivedImage})
	return err
}

var _ AutoApplyEffect = ToolBuildEffect{}
