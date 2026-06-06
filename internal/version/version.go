// Package version centralizes Bahia component version metadata.
package version

import (
	"regexp"
	"strings"
)

const defaultBase = "0.1.0"

// Base is the semantic base version. It is intentionally overridden by release
// builds with -ldflags while defaulting to the initial Bahia version.
var Base = defaultBase

// Commit is the source commit hash for this build. Release builds set it with
// -ldflags from git rev-parse HEAD.
var Commit = "dev"

// Full overrides the computed semantic version when release automation provides
// an exact version string.
var Full = ""

// Component describes one Bahia artifact that may be packaged or deployed
// independently from the other artifacts in this repository.
type Component struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Version    string `json:"version"`
	Base       string `json:"base"`
	Commit     string `json:"commit"`
	PackagedAs string `json:"packaged_as"`
}

var versionTokenPattern = regexp.MustCompile(`[^0-9A-Za-z.-]+`)
var numericIdentifierWithLeadingZero = regexp.MustCompile(`^0[0-9]+$`)

// Semantic returns the full SemVer-compatible version for this source build.
func Semantic() string {
	if cleaned := cleanVersion(Full); cleaned != "" {
		return cleaned
	}
	base := cleanBase(Base)
	commit := cleanIdentifier(Commit)
	if commit == "" {
		commit = "dev"
	}
	return base + "-" + commit
}

// Components returns version records for Bahia artifacts that are built from the
// backend source tree and packaged independently.
func Components() []Component {
	version := Semantic()
	base := cleanBase(Base)
	commit := cleanIdentifier(Commit)
	return []Component{
		component("backend", "Bahia backend", "backend", "cmd/server", version, base, commit),
		component("cli", "Bahia CLI", "cli", "cmd/cli", version, base, commit),
		component("relay", "Bahia relay", "service", "cmd/relay", version, base, commit),
		component("fips-bahia-bridge", "FIPS Bahia bridge", "bridge", "cmd/fips-bahia-bridge", version, base, commit),
		component("openclaw-soulfactory-sidecar", "OpenClaw SoulFactory sidecar", "sidecar", "cmd/openclaw-soulfactory-sidecar", version, base, commit),
	}
}

func component(id, name, kind, packagedAs, version, base, commit string) Component {
	return Component{ID: id, Name: name, Kind: kind, PackagedAs: packagedAs, Version: version, Base: base, Commit: commit}
}

func cleanBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultBase
	}
	return cleanVersion(value)
}

func cleanVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	return versionTokenPattern.ReplaceAllString(value, ".")
}

func cleanIdentifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	value = strings.TrimPrefix(value, "sha:")
	value = versionTokenPattern.ReplaceAllString(value, ".")
	value = strings.Trim(value, ".-")
	if value == "" {
		return ""
	}
	parts := strings.Split(value, ".")
	for i, part := range parts {
		if numericIdentifierWithLeadingZero.MatchString(part) {
			parts[i] = "g" + part
		}
	}
	return strings.Join(parts, ".")
}
