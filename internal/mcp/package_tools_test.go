package mcp

import (
	"context"
	"testing"

	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

type capturePackageCommandPublisher struct {
	apply   *controlplane.PackageRepositoryApplyCommand
	upload  *controlplane.PackagePublishCommand
	promote *controlplane.PackagePromotionCommand
	yank    *controlplane.PackageYankCommand
}

func (p *capturePackageCommandPublisher) PublishPackageRepositoryApplyRequest(_ context.Context, cmd controlplane.PackageRepositoryApplyCommand) (*controlplane.PackageCommandReceipt, error) {
	p.apply = &cmd
	return packageTestReceipt(controlplane.KindPackageRepositoryApply), nil
}
func (p *capturePackageCommandPublisher) PublishPackageRepositoryDeleteRequest(context.Context, controlplane.PackageRepositoryDeleteCommand) (*controlplane.PackageCommandReceipt, error) {
	return packageTestReceipt(controlplane.KindPackageRepositoryDelete), nil
}
func (p *capturePackageCommandPublisher) PublishPackagePublishRequest(_ context.Context, cmd controlplane.PackagePublishCommand) (*controlplane.PackageCommandReceipt, error) {
	p.upload = &cmd
	return packageTestReceipt(controlplane.KindPackagePublishIntent), nil
}
func (p *capturePackageCommandPublisher) PublishPackagePromotionRequest(_ context.Context, cmd controlplane.PackagePromotionCommand) (*controlplane.PackageCommandReceipt, error) {
	p.promote = &cmd
	return packageTestReceipt(controlplane.KindPackagePromotionRequest), nil
}
func (p *capturePackageCommandPublisher) PublishPackageYankRequest(_ context.Context, cmd controlplane.PackageYankCommand) (*controlplane.PackageCommandReceipt, error) {
	p.yank = &cmd
	return packageTestReceipt(controlplane.KindPackageYankRequest), nil
}
func (p *capturePackageCommandPublisher) PublishPackageDriftDetectRequest(context.Context, controlplane.PackageDriftDetectCommand) (*controlplane.PackageCommandReceipt, error) {
	return packageTestReceipt(controlplane.KindPackageDriftDetect), nil
}

func packageTestReceipt(kind int) *controlplane.PackageCommandReceipt {
	return &controlplane.PackageCommandReceipt{RequestEventID: "event-id", RequestPubkey: "pubkey", RequestKind: kind, StatusKind: controlplane.KindPackageStatus, ResultKind: controlplane.KindPackageResult, RepositoryRegistryKind: controlplane.KindPackageRepositoryRegistry, ArtifactRegistryKind: controlplane.KindPackageArtifactRegistry, PromotionRegistryKind: controlplane.KindPackagePromotionRegistry, DriftEventKind: controlplane.KindPackageDriftEvent, PublishedRelays: 1}
}

func TestGetToolsIncludesPackageTools(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	tools := server.GetTools()
	required := map[string]bool{"bahia_package_repository_apply": false, "bahia_package_upload": false, "bahia_package_promote": false, "bahia_package_yank": false, "bahia_package_list": false, "bahia_package_get": false, "bahia_package_status": false}
	for _, tool := range tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing package tool %s", name)
		}
	}
}

func TestPackageMutatingToolsPublishSignerFirstRequests(t *testing.T) {
	ctx := context.Background()
	publisher := &capturePackageCommandPublisher{}
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{PackageCommandPublisher: publisher})
	if res, err := server.CallTool(ctx, "bahia_package_repository_apply", map[string]interface{}{"name": "libs", "format": "npm", "backend_ref": "mock", "backend_type": "filesystem_mock", "policy": map[string]interface{}{"require_sha256": true}}); err != nil || res.IsError {
		t.Fatalf("apply result=%#v err=%v", res, err)
	}
	if publisher.apply == nil || publisher.apply.Name != "libs" || publisher.apply.Format != domain.PackageRepositoryFormatNPM || !publisher.apply.Policy.RequireSHA256 {
		t.Fatalf("apply command not captured correctly: %#v", publisher.apply)
	}
	if res, err := server.CallTool(ctx, "bahia_package_upload", map[string]interface{}{"repository_name": "libs", "package_name": "pkg", "version": "1.0.0", "filename": "pkg.tgz", "source_url": "https://example.test/pkg.tgz", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "size_bytes": float64(10)}); err != nil || res.IsError {
		t.Fatalf("upload result=%#v err=%v", res, err)
	}
	if publisher.upload == nil || publisher.upload.PackageName != "pkg" || publisher.upload.SizeBytes != 10 {
		t.Fatalf("upload command not captured correctly: %#v", publisher.upload)
	}
	if res, err := server.CallTool(ctx, "bahia_package_promote", map[string]interface{}{"repository_name": "libs", "target_repository_name": "prod", "package_name": "pkg", "version": "1.0.0", "filename": "pkg.tgz", "approved_by": "operator"}); err != nil || res.IsError {
		t.Fatalf("promote result=%#v err=%v", res, err)
	}
	if publisher.promote == nil || publisher.promote.TargetRepositoryName != "prod" {
		t.Fatalf("promote command not captured correctly: %#v", publisher.promote)
	}
}

func TestPackageMutatingToolsRequireCommandPublisher(t *testing.T) {
	server := NewServerWithOptions(nil, zap.NewNop(), ServerDeps{})
	res, err := server.CallTool(context.Background(), "bahia_package_upload", map[string]interface{}{})
	if err != nil {
		t.Fatalf("call err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected missing publisher error, got %#v", res)
	}
}
