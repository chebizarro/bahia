//go:build integration

package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
	"go.uber.org/zap"
)

func TestDockerInspectContainerLive(t *testing.T) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	imageName := "alpine:latest"

	pullResp, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		t.Skipf("docker daemon not available (image pull failed): %v", err)
	}
	io.Copy(io.Discard, pullResp)
	pullResp.Close()

	createResp, err := cli.ContainerCreate(ctx,
		&container.Config{Image: imageName, Cmd: []string{"echo", "hello"}, Labels: map[string]string{"bahia.desired_hash": "sha256:testhash"}},
		nil, nil, nil, "bahia-test-"+fmt.Sprint(time.Now().UnixNano()),
	)
	if err != nil {
		t.Fatalf("container create failed: %v", err)
	}
	containerID := createResp.ID
	t.Cleanup(func() { _ = cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}) })

	r := &ComposeRuntime{logger: zap.NewNop(), dockerClient: cli}

	imageID, configuredImage, desiredHash, ok := r.inspectComposeContainerImage(ctx, r.logger, containerID)
	if !ok {
		t.Fatal("inspectComposeContainerImage returned ok=false")
	}
	if imageID == "" {
		t.Fatal("Image field is empty")
	}
	t.Logf("Container Image ID: %s", imageID)
	if !strings.HasPrefix(imageID, "sha256:") {
		t.Errorf("Image ID should start with sha256:, got %q", imageID)
	}
	if configuredImage == "" {
		t.Fatal("Config.Image field is empty")
	}
	t.Logf("Configured Image: %s", configuredImage)
	if desiredHash != "sha256:testhash" {
		t.Errorf("Labels[bahia.desired_hash] = %q, want sha256:testhash", desiredHash)
	}
	t.Logf("Labels[bahia.desired_hash]: %s", desiredHash)
}

func TestDockerInspectImageLive(t *testing.T) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Skipf("docker daemon not available: %v", err)
	}
	defer cli.Close()

	ctx := context.Background()
	imageName := "alpine:latest"

	pullResp, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		t.Skipf("docker daemon not available (image pull failed): %v", err)
	}
	io.Copy(io.Discard, pullResp)
	pullResp.Close()

	inspected, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		t.Fatalf("image inspect failed: %v", err)
	}
	imageRef := inspected.ID

	r := &ComposeRuntime{logger: zap.NewNop(), dockerClient: cli}

	repo, digest := r.inspectDockerImage(ctx, r.logger, imageRef, imageName)
	t.Logf("Image inspect repo=%q digest=%q", repo, digest)
	if digest == "" {
		t.Fatal("digest is empty, expected a sha256 digest")
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest should start with sha256:, got %q", digest)
	}
	if repo == "" {
		t.Fatal("repo is empty")
	}
}
