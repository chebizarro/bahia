package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// AvatarGenerator generates agent avatars via FLUX/ComfyUI.
type AvatarGenerator struct {
	lemmyURL   string
	httpClient *http.Client
	logger     *slog.Logger
}

// AvatarConfig holds avatar generator configuration.
type AvatarConfig struct {
	LemmyURL string        // Lemmy ComfyUI API endpoint
	Timeout  time.Duration // Request timeout
}

// AvatarResult contains the generated avatar.
type AvatarResult struct {
	ImageData   []byte // PNG image data
	ContentType string // MIME type (image/png)
	Seed        string // Generation seed for reproducibility
}

// NewAvatarGenerator creates a new avatar generator.
func NewAvatarGenerator(config AvatarConfig, logger *slog.Logger) *AvatarGenerator {
	if config.LemmyURL == "" {
		config.LemmyURL = "http://192.168.30.10:8188"
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &AvatarGenerator{
		lemmyURL: config.LemmyURL,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
		logger: logger.With("component", "avatar"),
	}
}

// Generate creates an avatar from a prompt.
func (g *AvatarGenerator) Generate(ctx context.Context, prompt string, seed string) (*AvatarResult, error) {
	g.logger.Info("generating avatar", "prompt_length", len(prompt))

	// Build request
	reqBody := map[string]interface{}{
		"prompt": prompt,
		"seed":   seed,
		"width":  512,
		"height": 512,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Create request
	url := fmt.Sprintf("%s/api/generate", g.lemmyURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	// Check response content type
	contentType := resp.Header.Get("Content-Type")

	// If JSON response, extract image path and fetch it
	if contentType == "application/json" {
		var apiResp struct {
			OutputPath string `json:"output_path"`
			ImageURL   string `json:"image_url"`
			Seed       string `json:"seed"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		// Fetch the actual image
		imageURL := apiResp.ImageURL
		if imageURL == "" && apiResp.OutputPath != "" {
			imageURL = fmt.Sprintf("%s/outputs/%s", g.lemmyURL, apiResp.OutputPath)
		}

		imageData, err := g.fetchImage(ctx, imageURL)
		if err != nil {
			return nil, fmt.Errorf("fetch image: %w", err)
		}

		return &AvatarResult{
			ImageData:   imageData,
			ContentType: "image/png",
			Seed:        apiResp.Seed,
		}, nil
	}

	// Direct image response
	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}

	return &AvatarResult{
		ImageData:   imageData,
		ContentType: contentType,
		Seed:        seed,
	}, nil
}

// fetchImage downloads an image from a URL.
func (g *AvatarGenerator) fetchImage(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image fetch error %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// GenerateDefault generates an avatar with default styling.
func (g *AvatarGenerator) GenerateDefault(ctx context.Context, agentName, purpose string) (*AvatarResult, error) {
	prompt := fmt.Sprintf(
		"Pixel art style robot avatar for an AI agent named %s. %s. "+
			"Clean design, friendly appearance, consistent lighting, centered composition, "+
			"simple background, high quality, detailed",
		agentName, purpose,
	)

	return g.Generate(ctx, prompt, agentName)
}
