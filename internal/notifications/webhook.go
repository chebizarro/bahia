package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

// WebhookSender delivers notifications via HTTP POST.
type WebhookSender struct {
	client *http.Client
}

// NewWebhookSender creates a new WebhookSender.
func NewWebhookSender() *WebhookSender {
	return &WebhookSender{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send delivers a notification to the webhook URL configured in the channel.
// Config keys:
//   - "url" (required): the webhook endpoint
//   - "secret" (optional): HMAC-SHA256 signing secret
//   - "headers" (optional): additional headers map[string]string
func (s *WebhookSender) Send(ctx context.Context, ch *domain.NotificationChannel, eventType string, payload map[string]any) error {
	url, ok := ch.Config["url"].(string)
	if !ok || url == "" {
		return fmt.Errorf("webhook channel %q missing url config", ch.Name)
	}

	body := map[string]any{
		"event":     eventType,
		"payload":   payload,
		"channel":   ch.Name,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling webhook body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Bahia-Event", eventType)

	// Sign with HMAC if secret is configured.
	if secret, ok := ch.Config["secret"].(string); ok && secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bodyBytes)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Bahia-Signature", "sha256="+sig)
	}

	// Apply custom headers.
	if headers, ok := ch.Config["headers"].(map[string]any); ok {
		for k, v := range headers {
			if sv, ok := v.(string); ok {
				req.Header.Set(k, sv)
			}
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}

	return nil
}
