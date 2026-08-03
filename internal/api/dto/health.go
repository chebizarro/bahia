package dto

// HealthResponse represents the health/readiness endpoint response.
type HealthResponse struct {
	Status        string            `json:"status"`
	Version       string            `json:"version,omitempty"`
	Mode          string            `json:"mode"`
	RequestedTier int               `json:"requested_tier"`
	ActiveTier    int               `json:"active_tier"`
	Ready         bool              `json:"ready"`
	Checks        []HealthCheckDTO  `json:"checks,omitempty"`
	Runners       []RunnerStatusDTO `json:"runners,omitempty"`
}

type HealthCheckDTO struct {
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Message string            `json:"message,omitempty"`
	Tier    int               `json:"tier"`
	Details map[string]string `json:"details,omitempty"`
}

type RunnerStatusDTO struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Tier    int    `json:"tier,omitempty"`
}
