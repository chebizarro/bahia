package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ComputeToolsetHash generates a deterministic hash for caching.
func ComputeToolsetHash(baseImageDigest string, tools []domain.ResolvedTool, installerVersion string) string {
	sorted := append([]domain.ResolvedTool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Manager != sorted[j].Manager {
			return sorted[i].Manager < sorted[j].Manager
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Version < sorted[j].Version
	})

	toolsJSON, _ := json.Marshal(sorted)
	sum := sha256.Sum256([]byte(baseImageDigest + string(toolsJSON) + installerVersion))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type lockFile struct {
	Version int                   `json:"version"`
	Tools   []domain.ResolvedTool `json:"tools"`
}

// GenerateLockFile creates tools.lock.json for reproducible builds.
func GenerateLockFile(tools []domain.ResolvedTool) ([]byte, error) {
	sorted := append([]domain.ResolvedTool(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Manager != sorted[j].Manager {
			return sorted[i].Manager < sorted[j].Manager
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Version < sorted[j].Version
	})

	payload := lockFile{Version: 1, Tools: sorted}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal tools lock file: %w", err)
	}
	return data, nil
}

// GenerateDockerfile creates the derived Dockerfile.
func GenerateDockerfile(baseImage string, tools []domain.ResolvedTool, toolsetHash string, sourceEventID string) string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)

	return fmt.Sprintf(`FROM %s
COPY tools.lock.json /tmp/tools.lock.json
COPY install-tools.sh /usr/local/bin/install-tools
COPY install-apt.sh /usr/local/bin/install-apt
COPY install-pip.sh /usr/local/bin/install-pip
COPY install-npm.sh /usr/local/bin/install-npm
COPY install-cargo.sh /usr/local/bin/install-cargo
RUN chmod +x /usr/local/bin/install-tools /usr/local/bin/install-apt /usr/local/bin/install-pip /usr/local/bin/install-npm /usr/local/bin/install-cargo && /usr/local/bin/install-tools /tmp/tools.lock.json
LABEL io.bahia.toolset.hash="%s"
LABEL io.bahia.source_event="%s"
LABEL io.bahia.tools="%s"
`, baseImage, toolsetHash, sourceEventID, strings.Join(names, ","))
}
