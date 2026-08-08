package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LabelEnvironmentID is the deploy-option label carrying the environment
// UUID into VM deploys. Runtime.Deploy only receives the runtime target
// name, so the lifecycle service threads environment identity through this
// label; it feeds the bahia-<envID-short>-<serviceName> instance naming.
const LabelEnvironmentID = "bahia.environment_id"

const (
	instanceNamePrefix = "bahia-"
	metadataFileName   = "metadata.json"
	instancesDirName   = "instances"
	maxServiceNamePart = 40
)

// InstancesDir returns the directory under the VM state dir that holds
// per-instance state directories.
func InstancesDir(stateDir string) string {
	return filepath.Join(stateDir, instancesDirName)
}

// InstanceMetadata is the per-instance metadata.json record. It is the
// core's source of truth for target-name -> instance resolution and feeds
// drift (image digest, spec hash) through Observe.
type InstanceMetadata struct {
	Name          string            `json:"name"`
	ServiceName   string            `json:"service_name"`
	EnvironmentID string            `json:"environment_id,omitempty"`
	RuntimeType   string            `json:"runtime_type"`
	ImageRepo     string            `json:"image_repo"`
	ImageDigest   string            `json:"image_digest"`
	ImageID       string            `json:"image_id"`
	ReleaseDir    string            `json:"release_dir"`
	SpecHash      string            `json:"spec_hash"`
	// AgentProtocolVersion is the guest-agent protocol version declared by
	// the release manifest at deploy time. Zero or 1 means the image ships
	// no service-mode agent: hypervisor-running is sufficient for healthy.
	// 2+ means Observe requires a successful guest-agent ping for healthy.
	AgentProtocolVersion int `json:"agent_protocol_version,omitempty"`
	VsockCID      uint32            `json:"vsock_cid,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

// InstanceName builds the hypervisor-visible instance name
// bahia-<envID-short>-<serviceName>. When the environment ID is unknown
// (uuid.Nil), a short hash of the raw service name keeps the scheme
// deterministic and collision-resistant after sanitization.
func InstanceName(envID uuid.UUID, serviceName string) string {
	var short string
	if envID != uuid.Nil {
		short = strings.ReplaceAll(envID.String(), "-", "")[:8]
	} else {
		sum := sha256.Sum256([]byte(serviceName))
		short = hex.EncodeToString(sum[:4])
	}
	return instanceNamePrefix + short + "-" + sanitizeNameComponent(serviceName)
}

// sanitizeNameComponent lowers the name to [a-z0-9-], collapses runs of
// dashes, and bounds the length so instance names stay valid libvirt domain
// names and filesystem path components.
func sanitizeNameComponent(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "svc"
	}
	if len(out) > maxServiceNamePart {
		out = strings.TrimRight(out[:maxServiceNamePart], "-")
	}
	return out
}

// ComputeSpecHash returns a deterministic hash over the drift-relevant
// parts of an instance spec. It feeds observation metadata so later
// desired-state comparison can detect resource/spec drift, not only image
// digest drift.
func ComputeSpecHash(runtimeType, imageDigest string, vcpus, memoryMB int, networkProfile string) string {
	canonical, _ := json.Marshal(map[string]any{
		"runtime_type":    runtimeType,
		"image_digest":    imageDigest,
		"vcpus":           vcpus,
		"memory_mb":       memoryMB,
		"network_profile": networkProfile,
	})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// deriveVsockCID deterministically maps an instance name to a guest vsock
// context ID, avoiding the reserved CIDs 0-2.
func deriveVsockCID(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return 3 + h.Sum32()%(1<<31)
}

// WriteInstanceMetadata atomically writes metadata.json into the instance
// directory.
func WriteInstanceMetadata(instanceDir string, md *InstanceMetadata) error {
	data, err := json.MarshalIndent(md, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(instanceDir, metadataFileName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(instanceDir, metadataFileName)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ReadInstanceMetadata reads metadata.json from an instance directory.
// Missing metadata returns (nil, nil).
func ReadInstanceMetadata(instanceDir string) (*InstanceMetadata, error) {
	data, err := os.ReadFile(filepath.Join(instanceDir, metadataFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var md InstanceMetadata
	if err := json.Unmarshal(data, &md); err != nil {
		return nil, fmt.Errorf("decoding instance metadata in %s: %w", instanceDir, err)
	}
	return &md, nil
}

// FindInstancesByService scans the instances directory for instances whose
// recorded service name matches, newest first. A missing instances
// directory yields an empty result.
func FindInstancesByService(instancesDir, serviceName string) ([]*InstanceMetadata, error) {
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var matches []*InstanceMetadata
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		md, err := ReadInstanceMetadata(filepath.Join(instancesDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if md == nil || md.ServiceName != serviceName {
			continue
		}
		matches = append(matches, md)
	}
	// Newest first, so callers can treat matches[0] as authoritative.
	for i := 1; i < len(matches); i++ {
		for j := i; j > 0 && matches[j].CreatedAt.After(matches[j-1].CreatedAt); j-- {
			matches[j], matches[j-1] = matches[j-1], matches[j]
		}
	}
	return matches, nil
}
