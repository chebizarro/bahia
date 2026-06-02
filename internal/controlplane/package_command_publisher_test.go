package controlplane

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestPackageCommandPublisherPublishesCanonicalUploadRequest(t *testing.T) {
	ctx := context.Background()
	capture := &captureNostrPublisher{published: 1}
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	publisher := NewPackageCommandPublisher(capture, signer)
	receipt, err := publisher.PublishPackagePublishRequest(ctx, PackagePublishCommand{RepositoryName: "libs", PackageName: "pkg", Version: "1.0.0", Filename: "pkg.tgz", SourceURL: "https://example.test/pkg.tgz", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 10})
	if err != nil {
		t.Fatalf("publish package upload: %v", err)
	}
	if receipt.RequestKind != KindContextVMMessage || receipt.ResultKind != KindCASControlState {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected one event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	content := assertContextVMCommand(t, ev, "package/publish")
	assertReactorTag(t, ev.Tags, "operation", string(domain.PackageOperationArtifactPublish))
	assertReactorTag(t, ev.Tags, "repository_name", "libs")
	assertReactorTag(t, ev.Tags, "package", "pkg")
	if content["source_url"] != "https://example.test/pkg.tgz" || content["sha256"] == "" {
		t.Fatalf("unexpected content: %#v", content)
	}
}
