package fipsbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openagentsinc/bahia/internal/atomicfile"
)

const defaultHostsFileMode os.FileMode = 0o644

// HostEntry maps a .fips alias to a Nostr npub identity.
type HostEntry struct {
	Name string
	Npub string
}

// HostsWriter atomically rewrites the Bahia-managed section of a FIPS hosts file.
type HostsWriter struct {
	Path                 string
	ManagedSectionMarker string
}

// NewHostsWriter returns a writer with defaults applied.
func NewHostsWriter(path, marker string) HostsWriter {
	if strings.TrimSpace(path) == "" {
		path = DefaultHostsPath
	}
	if strings.TrimSpace(marker) == "" {
		marker = DefaultManagedSectionMarker
	}
	return HostsWriter{Path: path, ManagedSectionMarker: marker}
}

// Write replaces only the managed section and preserves manual entries outside it.
func (w HostsWriter) Write(ctx context.Context, entries map[string]string) error {
	path := strings.TrimSpace(w.Path)
	if path == "" {
		path = DefaultHostsPath
	}
	marker := strings.TrimSpace(w.ManagedSectionMarker)
	if marker == "" {
		marker = DefaultManagedSectionMarker
	}

	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hosts file: %w", err)
	}

	mode := defaultHostsFileMode
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat hosts file: %w", statErr)
	}

	manual := stripManagedSection(string(current), marker)
	managed := renderManagedSection(entries, marker)
	updated := joinSections(manual, managed)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create hosts directory: %w", err)
	}
	if err := atomicfile.WriteFile(ctx, path, ".bahia-hosts-*", []byte(updated), mode); err != nil {
		return fmt.Errorf("write hosts file: %w", err)
	}
	return nil
}

func stripManagedSection(content, marker string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	startMarker := marker
	endMarker := marker + " end"
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	inManaged := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == startMarker:
			inManaged = true
			continue
		case inManaged && trimmed == endMarker:
			inManaged = false
			continue
		case inManaged:
			continue
		default:
			kept = append(kept, line)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

func renderManagedSection(entries map[string]string, marker string) string {
	var b strings.Builder
	b.WriteString(marker)
	b.WriteByte('\n')
	labels := make([]string, 0, len(entries))
	for label := range entries {
		label = strings.TrimSpace(label)
		if label != "" {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	for _, label := range labels {
		npub := strings.TrimSpace(entries[label])
		if npub == "" {
			continue
		}
		b.WriteString(label)
		if !strings.HasSuffix(label, ".fips") {
			b.WriteString(".fips")
		}
		b.WriteString("  ")
		b.WriteString(npub)
		b.WriteByte('\n')
	}
	b.WriteString(marker)
	b.WriteString(" end\n")
	return b.String()
}

func joinSections(manual, managed string) string {
	manual = strings.TrimRight(manual, "\n")
	if manual == "" {
		return managed
	}
	return manual + "\n\n" + managed
}
