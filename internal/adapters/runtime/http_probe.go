package runtime

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const maxHTTPProbeBodyBytes = 512

// HTTPProbeConfig describes one bounded HTTP readiness probe.
type HTTPProbeConfig struct {
	Method            string
	URL               string
	Timeout           time.Duration
	ExpectedStatusMin int
	ExpectedStatusMax int
}

// HTTPProbeResult contains the observable outcome of an HTTP readiness probe.
type HTTPProbeResult struct {
	Successful bool          `json:"successful"`
	StatusCode int           `json:"status_code,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
	ObservedAt time.Time     `json:"observed_at"`
}

// ProbeResult is the optional readiness-probe result attached to an instance observation.
type ProbeResult = HTTPProbeResult

// HTTPProber executes bounded readiness probes.
type HTTPProber struct{}

// Probe performs one HTTP probe with an enforced timeout and at most three redirects.
func (HTTPProber) Probe(ctx context.Context, config HTTPProbeConfig) HTTPProbeResult {
	started := time.Now()
	result := HTTPProbeResult{ObservedAt: started.UTC()}
	method := strings.ToUpper(strings.TrimSpace(config.Method))
	if method == "" {
		method = http.MethodGet
	}
	if strings.TrimSpace(config.URL) == "" {
		result.Error = "probe URL is required"
		result.Duration = time.Since(started)
		return result
	}
	if config.Timeout <= 0 {
		result.Error = "probe timeout must be positive"
		result.Duration = time.Since(started)
		return result
	}
	minimum, maximum := config.ExpectedStatusMin, config.ExpectedStatusMax
	if minimum == 0 && maximum == 0 {
		minimum, maximum = http.StatusOK, 299
	}
	if minimum < 100 || maximum > 599 || minimum > maximum {
		result.Error = "invalid expected status range"
		result.Duration = time.Since(started)
		return result
	}

	probeCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, method, config.URL, nil)
	if err != nil {
		result.Error = domain.SanitizeEvidence(err.Error())
		result.Duration = time.Since(started)
		return result
	}
	client := &http.Client{
		Timeout: config.Timeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("redirect limit exceeded")
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		result.Error = domain.SanitizeEvidence(err.Error())
		result.Duration = time.Since(started)
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeBodyBytes))
	result.Detail = domain.SanitizeEvidence(string(body))
	if readErr != nil {
		result.Error = domain.SanitizeEvidence(readErr.Error())
	} else {
		result.Successful = resp.StatusCode >= minimum && resp.StatusCode <= maximum
	}
	result.Duration = time.Since(started)
	return result
}
