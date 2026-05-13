package build

import (
	"archive/tar"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

//go:embed scripts/*
var installerScripts embed.FS

// DockerBuilder builds derived images via Docker Engine API.
type DockerBuilder struct {
	dockerHost string
	httpClient *http.Client
	logger     *zap.Logger
}

func NewDockerBuilder(dockerHost string, logger *zap.Logger) *DockerBuilder {
	if logger == nil {
		logger = zap.NewNop()
	}
	if strings.TrimSpace(dockerHost) == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}

	if strings.HasPrefix(dockerHost, "unix://") {
		socketPath := strings.TrimPrefix(dockerHost, "unix://")
		return &DockerBuilder{
			dockerHost: "http://localhost",
			httpClient: &http.Client{
				Transport: &http.Transport{
					DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
						return net.Dial("unix", socketPath)
					},
				},
				Timeout: 10 * time.Minute,
			},
			logger: logger,
		}
	}

	host := dockerHost
	if strings.HasPrefix(host, "tcp://") {
		host = "http://" + strings.TrimPrefix(host, "tcp://")
	} else if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	return &DockerBuilder{
		dockerHost: host,
		httpClient: &http.Client{Timeout: 10 * time.Minute},
		logger:     logger,
	}
}

// BuildRequest describes an image build.
type BuildRequest struct {
	BaseImage      string
	Tools          []domain.ResolvedTool
	ToolsetHash    string
	SourceEventID  string
	TargetRegistry string
	TargetRepo     string
}

// BuildResult carries build output metadata.
type BuildResult struct {
	ImageID     string
	ImageDigest string
	Size        int64
	BuildLog    string
}

// BuildImage builds a derived image from the generated Dockerfile.
func (b *DockerBuilder) BuildImage(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	lockFile, err := GenerateLockFile(req.Tools)
	if err != nil {
		return nil, err
	}
	dockerfile := GenerateDockerfile(req.BaseImage, req.Tools, req.ToolsetHash, req.SourceEventID)

	contextTar, err := b.buildContextTar(dockerfile, lockFile)
	if err != nil {
		return nil, err
	}

	tag := strings.TrimSpace(req.TargetRepo)
	if reg := strings.TrimSpace(req.TargetRegistry); reg != "" && tag != "" {
		tag = strings.TrimSuffix(reg, "/") + "/" + strings.TrimPrefix(tag, "/")
	}

	buildURL := fmt.Sprintf("%s/v1.44/build?dockerfile=Dockerfile&pull=1&rm=1", b.dockerHost)
	if tag != "" {
		buildURL += "&t=" + url.QueryEscape(tag)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, buildURL, bytes.NewReader(contextTar))
	if err != nil {
		return nil, fmt.Errorf("create docker build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-tar")

	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("docker build request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker build returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	result, err := decodeBuildStream(resp.Body)
	if err != nil {
		return nil, err
	}
	if result.ImageID == "" {
		return nil, fmt.Errorf("docker build completed without image id")
	}
	return result, nil
}

// PushImage pushes the built image to a registry.
func (b *DockerBuilder) PushImage(ctx context.Context, imageID string, targetRef string) error {
	repo, tag := splitTargetRef(targetRef)
	if repo == "" {
		return fmt.Errorf("invalid target ref: %q", targetRef)
	}

	tagURL := fmt.Sprintf("%s/v1.44/images/%s/tag?repo=%s", b.dockerHost, url.PathEscape(imageID), url.QueryEscape(repo))
	if tag != "" {
		tagURL += "&tag=" + url.QueryEscape(tag)
	}
	tagReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tagURL, nil)
	if err != nil {
		return fmt.Errorf("create docker tag request: %w", err)
	}
	tagResp, err := b.httpClient.Do(tagReq)
	if err != nil {
		return fmt.Errorf("docker tag request failed: %w", err)
	}
	defer tagResp.Body.Close()
	if tagResp.StatusCode != http.StatusCreated && tagResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tagResp.Body)
		return fmt.Errorf("docker tag returned %d: %s", tagResp.StatusCode, strings.TrimSpace(string(body)))
	}

	pushURL := fmt.Sprintf("%s/v1.44/images/%s/push", b.dockerHost, url.PathEscape(repo))
	if tag != "" {
		pushURL += "?tag=" + url.QueryEscape(tag)
	}
	pushReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pushURL, nil)
	if err != nil {
		return fmt.Errorf("create docker push request: %w", err)
	}
	pushResp, err := b.httpClient.Do(pushReq)
	if err != nil {
		return fmt.Errorf("docker push request failed: %w", err)
	}
	defer pushResp.Body.Close()
	if pushResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pushResp.Body)
		return fmt.Errorf("docker push returned %d: %s", pushResp.StatusCode, strings.TrimSpace(string(body)))
	}

	_, err = decodeBuildStream(pushResp.Body)
	if err != nil {
		return fmt.Errorf("decode docker push stream: %w", err)
	}
	return nil
}

// CheckImageExists checks if an image with the toolset hash already exists.
func (b *DockerBuilder) CheckImageExists(ctx context.Context, toolsetHash string) (string, bool, error) {
	filters, err := json.Marshal(map[string][]string{"label": {"io.bahia.toolset.hash=" + toolsetHash}})
	if err != nil {
		return "", false, fmt.Errorf("marshal image filters: %w", err)
	}

	query := url.Values{}
	query.Set("filters", string(filters))
	query.Set("all", "0")
	checkURL := fmt.Sprintf("%s/v1.44/images/json?%s", b.dockerHost, query.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		return "", false, fmt.Errorf("create image check request: %w", err)
	}
	resp, err := b.httpClient.Do(httpReq)
	if err != nil {
		return "", false, fmt.Errorf("docker image check failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("docker image check returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var images []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&images); err != nil {
		return "", false, fmt.Errorf("decode image check response: %w", err)
	}
	if len(images) == 0 {
		return "", false, nil
	}
	return images[0].ID, true, nil
}

func (b *DockerBuilder) buildContextTar(dockerfile string, lockFile []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	defer tw.Close()

	files := map[string][]byte{
		"Dockerfile":      []byte(dockerfile),
		"tools.lock.json": lockFile,
	}

	scriptEntries, err := installerScripts.ReadDir("scripts")
	if err != nil {
		return nil, fmt.Errorf("read installer scripts: %w", err)
	}
	for _, entry := range scriptEntries {
		if entry.IsDir() {
			continue
		}
		content, readErr := installerScripts.ReadFile(filepath.ToSlash("scripts/" + entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read script %s: %w", entry.Name(), readErr)
		}
		files[entry.Name()] = content
	}

	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
		if !strings.HasSuffix(name, ".sh") {
			header.Mode = 0o644
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write tar header for %s: %w", name, err)
		}
		if _, err := tw.Write(content); err != nil {
			return nil, fmt.Errorf("write tar content for %s: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar writer: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeBuildStream(r io.Reader) (*BuildResult, error) {
	dec := json.NewDecoder(r)
	var logBuilder strings.Builder
	result := &BuildResult{}

	for {
		var msg struct {
			Stream      string `json:"stream"`
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
			Aux struct {
				ID          string `json:"ID"`
				Digest      string `json:"Digest"`
				Size        int64  `json:"Size"`
				ImageDigest string `json:"imageDigest"`
			} `json:"aux"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode docker stream: %w", err)
		}
		if msg.Stream != "" {
			logBuilder.WriteString(msg.Stream)
		}
		if msg.Error != "" {
			if msg.ErrorDetail.Message != "" {
				return nil, fmt.Errorf("docker build failed: %s", msg.ErrorDetail.Message)
			}
			return nil, fmt.Errorf("docker build failed: %s", msg.Error)
		}
		if msg.Aux.ID != "" {
			result.ImageID = msg.Aux.ID
		}
		if msg.Aux.Digest != "" {
			result.ImageDigest = msg.Aux.Digest
		}
		if msg.Aux.ImageDigest != "" && result.ImageDigest == "" {
			result.ImageDigest = msg.Aux.ImageDigest
		}
		if msg.Aux.Size > 0 {
			result.Size = msg.Aux.Size
		}
	}

	result.BuildLog = strings.TrimSpace(logBuilder.String())
	return result, nil
}

func splitTargetRef(targetRef string) (string, string) {
	trimmed := strings.TrimSpace(targetRef)
	if trimmed == "" {
		return "", ""
	}
	lastSlash := strings.LastIndex(trimmed, "/")
	lastColon := strings.LastIndex(trimmed, ":")
	if lastColon > lastSlash {
		return trimmed[:lastColon], trimmed[lastColon+1:]
	}
	return trimmed, "latest"
}
