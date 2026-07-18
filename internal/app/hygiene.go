package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// loadHygienePolicy reads and validates a versioned hygiene policy document
// (schemas/hygiene_policy.json describes the format).
func loadHygienePolicy(path string) (domain.HygienePolicy, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return domain.HygienePolicy{}, fmt.Errorf("hygiene.policy_path is required when hygiene is enabled")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.HygienePolicy{}, fmt.Errorf("read hygiene policy %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy domain.HygienePolicy
	if err := decoder.Decode(&policy); err != nil {
		return domain.HygienePolicy{}, fmt.Errorf("decode hygiene policy %s: %w", path, err)
	}
	if policy.RetentionDays < 0 {
		return domain.HygienePolicy{}, fmt.Errorf("hygiene policy %s: retention_days must be positive", path)
	}
	policy = policy.WithDefaults()
	if err := policy.Validate(); err != nil {
		return domain.HygienePolicy{}, fmt.Errorf("hygiene policy %s: %w", path, err)
	}
	return policy, nil
}
