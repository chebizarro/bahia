package packagebackend

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ArtifactPath returns the deterministic backend-relative path for an artifact.
func ArtifactPath(namespace, packageName, version, filename string) (string, error) {
	segments := make([]string, 0, 4)
	if ns := strings.TrimSpace(namespace); ns != "" {
		for _, part := range strings.Split(ns, "/") {
			if clean := strings.TrimSpace(part); clean != "" {
				segments = append(segments, clean)
			}
		}
	}
	segments = append(segments, strings.TrimSpace(packageName), strings.TrimSpace(version), strings.TrimSpace(filename))
	for _, segment := range segments {
		if err := validatePathSegment(segment); err != nil {
			return "", err
		}
	}
	return path.Join(segments...), nil
}

// SafeJoin joins backend path segments under root and rejects traversal.
func SafeJoin(root string, segments ...string) (string, error) {
	root = filepath.Clean(root)
	parts := []string{root}
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		for _, part := range strings.Split(filepath.ToSlash(segment), "/") {
			if err := validatePathSegment(part); err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
	}
	joined := filepath.Join(parts...)
	if joined != root && !strings.HasPrefix(joined, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return joined, nil
}

func validatePathSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("path segment must not be empty")
	}
	if segment == "." || segment == ".." || strings.Contains(segment, "/") || strings.Contains(segment, "\\") || strings.Contains(segment, "\x00") {
		return fmt.Errorf("invalid path segment %q", segment)
	}
	if filepath.IsAbs(segment) || path.IsAbs(segment) {
		return fmt.Errorf("path segment %q must be relative", segment)
	}
	return nil
}
