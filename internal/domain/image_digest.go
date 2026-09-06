package domain

import (
	"regexp"
	"strings"
)

var trailingSHA256Digest = regexp.MustCompile(`(?i)(sha256:[0-9a-f]+)$`)
var bareSHA256Digest = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)

// NormalizeImageDigest extracts a canonical sha256 digest from an OCI image
// reference, Docker image ID, or bare digest value. Empty and unrecognized
// values are returned as empty strings so callers can distinguish unavailable
// digests from comparable ones.
func NormalizeImageDigest(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if match := trailingSHA256Digest.FindStringSubmatch(value); len(match) == 2 {
		return strings.ToLower(match[1])
	}
	if bareSHA256Digest.MatchString(value) {
		return "sha256:" + strings.ToLower(value)
	}
	return ""
}

// ArtifactDigestDriftStatus applies the common artifact digest fallback used
// when a runtime observation has no comparable desired-state hash. Callers
// choose how a matching digest with starting health is persisted.
func ArtifactDigestDriftStatus(desiredDigest, observedDigest string, health HealthStatus, startingStatus DriftStatus) DriftStatus {
	desiredDigest = NormalizeImageDigest(desiredDigest)
	observedDigest = NormalizeImageDigest(observedDigest)
	if observedDigest == "" {
		if health == HealthStatusStopped || health == HealthStatusUnhealthy {
			return DriftStatusDrifted
		}
		return DriftStatusUnknown
	}
	if desiredDigest == "" {
		return DriftStatusUnknown
	}
	if desiredDigest != observedDigest {
		return DriftStatusDrifted
	}
	if health == HealthStatusHealthy {
		return DriftStatusInSync
	}
	if health == HealthStatusStarting {
		return startingStatus
	}
	return DriftStatusDrifted
}
