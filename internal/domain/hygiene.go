package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

// HygienePolicySchemaVersion is the current hygiene policy document version.
const HygienePolicySchemaVersion = "hygiene/v1"

// Hygiene candidate classes reported by the per-host maintenance driver
// (cascadia-go/worker/drivers/maintenance).
const (
	HygieneClassDupClone        = "dup-clone"
	HygieneClassOrphanClone     = "orphan-clone"
	HygieneClassMisplacedBackup = "misplaced-backup"
	HygieneClassCruft           = "cruft"
)

// HygienePolicy is the declarative, versioned fleet hygiene policy (epic
// fp-jan, J2). Bahia owns this document; the per-host maintenance driver
// enforces the projected subset. Cleanup is per-host, so the policy targets
// worker pubkeys and is delivered/converged by the hygiene reconciler.
type HygienePolicy struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	Enabled       bool   `json:"enabled"`

	// Workers this policy applies to (worker pubkeys). Empty = all
	// registered maintenance workers.
	Workers []string `json:"workers,omitempty"`

	// ScanRoots are the only directories the janitor may look at.
	ScanRoots []string `json:"scan_roots"`
	// ProtectedPaths are never scanned, quarantined, or purged. The driver
	// additionally refuses ancestors of protected paths.
	ProtectedPaths []string `json:"protected_paths"`
	QuarantineDir  string   `json:"quarantine_dir,omitempty"`
	RetentionDays  int      `json:"retention_days,omitempty"`
	// BackupDomain is where backups belong; misplaced backups are
	// relocated into it, never deleted.
	BackupDomain string   `json:"backup_domain,omitempty"`
	BackupGlobs  []string `json:"backup_globs,omitempty"`
	CruftGlobs   []string `json:"cruft_globs,omitempty"`
	// CanonicalClones maps git origin URL -> canonical checkout path.
	CanonicalClones map[string]string `json:"canonical_clones,omitempty"`
	GCCommands      [][]string        `json:"gc_commands,omitempty"`
	// MethodACL restricts tier-gated methods (purge/relocate) to specific
	// requester pubkeys (Tier-2 / Majordomo).
	MethodACL map[string][]string `json:"method_acl,omitempty"`

	Thresholds HygieneThresholds `json:"thresholds"`

	// AutoQuarantine enables Tier-1 automatic quarantine of unblocked
	// dup-clone/cruft candidates. Tier-2 actions (relocate, purge) are
	// NEVER issued automatically regardless of this flag.
	AutoQuarantine bool `json:"auto_quarantine"`
	// AutoGC enables Tier-1 automatic gc when pressure breaches a
	// threshold (doctrine already permits docker prune on own node).
	AutoGC bool `json:"auto_gc"`
}

// HygieneThresholds are the pressure alert thresholds (J5).
type HygieneThresholds struct {
	DiskUsedPct  float64 `json:"disk_used_pct,omitempty"`  // default 85
	InodeUsedPct float64 `json:"inode_used_pct,omitempty"` // default 90
}

// WithDefaults returns the policy with defaulted optional fields.
func (p HygienePolicy) WithDefaults() HygienePolicy {
	if p.SchemaVersion == "" {
		p.SchemaVersion = HygienePolicySchemaVersion
	}
	if p.RetentionDays <= 0 {
		p.RetentionDays = 14
	}
	if p.Thresholds.DiskUsedPct <= 0 {
		p.Thresholds.DiskUsedPct = 85
	}
	if p.Thresholds.InodeUsedPct <= 0 {
		p.Thresholds.InodeUsedPct = 90
	}
	return p
}

// Validate rejects unsafe or unusable hygiene policies.
func (p HygienePolicy) Validate() error {
	if p.SchemaVersion != HygienePolicySchemaVersion {
		return fmt.Errorf("unsupported hygiene policy schema_version %q", p.SchemaVersion)
	}
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("hygiene policy id is required")
	}
	if len(p.ScanRoots) == 0 {
		return fmt.Errorf("hygiene policy requires scan_roots")
	}
	for _, root := range p.ScanRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("scan root %q must be absolute", root)
		}
	}
	if len(p.ProtectedPaths) == 0 {
		return fmt.Errorf("hygiene policy requires protected_paths (never ship a policy that protects nothing)")
	}
	for _, protected := range p.ProtectedPaths {
		if !filepath.IsAbs(protected) {
			return fmt.Errorf("protected path %q must be absolute", protected)
		}
	}
	if p.QuarantineDir != "" && !filepath.IsAbs(p.QuarantineDir) {
		return fmt.Errorf("quarantine_dir %q must be absolute", p.QuarantineDir)
	}
	if p.BackupDomain != "" && !filepath.IsAbs(p.BackupDomain) {
		return fmt.Errorf("backup_domain %q must be absolute", p.BackupDomain)
	}
	if p.Thresholds.DiskUsedPct < 1 || p.Thresholds.DiskUsedPct > 100 {
		return fmt.Errorf("thresholds.disk_used_pct %.1f out of range [1,100]", p.Thresholds.DiskUsedPct)
	}
	if p.Thresholds.InodeUsedPct < 1 || p.Thresholds.InodeUsedPct > 100 {
		return fmt.Errorf("thresholds.inode_used_pct %.1f out of range [1,100]", p.Thresholds.InodeUsedPct)
	}
	for _, worker := range p.Workers {
		if len(worker) != 64 {
			return fmt.Errorf("worker %q is not a 64-hex pubkey", worker)
		}
	}
	return nil
}

// DriverPolicy projects the policy into the shape consumed by the per-host
// maintenance driver (cascadia-go maintenance.Policy JSON).
func (p HygienePolicy) DriverPolicy() map[string]any {
	out := map[string]any{
		"scan_roots":      p.ScanRoots,
		"protected_paths": p.ProtectedPaths,
		"retention_days":  p.RetentionDays,
	}
	if p.QuarantineDir != "" {
		out["quarantine_dir"] = p.QuarantineDir
	}
	if p.BackupDomain != "" {
		out["backup_domain"] = p.BackupDomain
	}
	if len(p.BackupGlobs) > 0 {
		out["backup_globs"] = p.BackupGlobs
	}
	if len(p.CruftGlobs) > 0 {
		out["cruft_globs"] = p.CruftGlobs
	}
	if len(p.CanonicalClones) > 0 {
		out["canonical_clones"] = p.CanonicalClones
	}
	if len(p.GCCommands) > 0 {
		out["gc_commands"] = p.GCCommands
	}
	if len(p.MethodACL) > 0 {
		out["method_acl"] = p.MethodACL
	}
	return out
}
