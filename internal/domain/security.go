package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	packageurl "github.com/anchore/packageurl-go"
	"github.com/google/uuid"
)

// SecurityTargetType identifies the kind of resource scanned by the Security subsystem.
type SecurityTargetType string

const (
	SecurityTargetSBOM    SecurityTargetType = "sbom"
	SecurityTargetPackage SecurityTargetType = "package"
	SecurityTargetPURL    SecurityTargetType = "purl"
	SecurityTargetCommit  SecurityTargetType = "commit"
)

// SecurityScanStatus records durable scan lifecycle state.
type SecurityScanStatus string

const (
	SecurityScanAccepted  SecurityScanStatus = "accepted"
	SecurityScanRunning   SecurityScanStatus = "running"
	SecurityScanCompleted SecurityScanStatus = "completed"
	SecurityScanFailed    SecurityScanStatus = "failed"
	SecurityScanCancelled SecurityScanStatus = "cancelled"
)

func (s SecurityScanStatus) IsTerminal() bool {
	switch s {
	case SecurityScanCompleted, SecurityScanFailed, SecurityScanCancelled:
		return true
	default:
		return false
	}
}

func (s SecurityScanStatus) IsSuccessful() bool {
	return s == SecurityScanCompleted
}

// SecuritySeverity records normalized finding severity.
type SecuritySeverity string

const (
	SecuritySeverityUnknown  SecuritySeverity = "unknown"
	SecuritySeverityLow      SecuritySeverity = "low"
	SecuritySeverityModerate SecuritySeverity = "moderate"
	SecuritySeverityHigh     SecuritySeverity = "high"
	SecuritySeverityCritical SecuritySeverity = "critical"
)

// SecurityPublicationState records relay publication retry state for Security observables.
type SecurityPublicationState string

const (
	SecurityPublicationPending         SecurityPublicationState = "pending"
	SecurityPublicationPublished       SecurityPublicationState = "published"
	SecurityPublicationFailedRetryable SecurityPublicationState = "failed_retryable"
	SecurityPublicationFailedTerminal  SecurityPublicationState = "failed_terminal"
)

// SecurityBreachNotificationStatus records notification lifecycle for policy-breach fingerprints.
type SecurityBreachNotificationStatus string

const (
	SecurityBreachNotificationPending     SecurityBreachNotificationStatus = "pending"
	SecurityBreachNotificationDispatched  SecurityBreachNotificationStatus = "dispatched"
	SecurityBreachNotificationFailed      SecurityBreachNotificationStatus = "failed"
	SecurityBreachNotificationSuppressed  SecurityBreachNotificationStatus = "suppressed"
	SecurityBreachNotificationNotRequired SecurityBreachNotificationStatus = "not_required"
)

// SecurityTriggerKind records why a scan run exists without starting scanner runtime work here.
type SecurityTriggerKind string

const (
	SecurityTriggerSBOMObservable SecurityTriggerKind = "sbom_observable"
	SecurityTriggerManual         SecurityTriggerKind = "manual"
	SecurityTriggerScheduled      SecurityTriggerKind = "scheduled"
	SecurityTriggerPolicy         SecurityTriggerKind = "policy"
)

// SecuritySeverityCounts records aggregate finding counts by normalized severity.
type SecuritySeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Moderate int `json:"moderate"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

func (c SecuritySeverityCounts) Total() int {
	return c.Critical + c.High + c.Moderate + c.Low + c.Unknown
}

// SecurityTarget stores canonical target identity and operator-readable diagnostics.
type SecurityTarget struct {
	ID            uuid.UUID          `json:"id"`
	Type          SecurityTargetType `json:"type"`
	TargetKey     string             `json:"target_key"`
	TargetKeyHash string             `json:"target_key_hash"`
	Display       string             `json:"display"`
	Subject       *SBOMSubject       `json:"subject,omitempty"`
	Package       *SecurityPackage   `json:"package,omitempty"`
	PURL          string             `json:"purl,omitempty"`
	RepositoryURL string             `json:"repository_url,omitempty"`
	CommitHash    string             `json:"commit_hash,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty"`
	CreatedAt     time.Time          `json:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at"`
}

// SecurityPackage identifies a package-coordinate target or finding coordinate.
type SecurityPackage struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	PURL      string `json:"purl,omitempty"`
	CPE       string `json:"cpe,omitempty"`
}

// SecurityScanRun stores one durable scan attempt for a target.
type SecurityScanRun struct {
	ID                 uuid.UUID                `json:"id"`
	TargetID           uuid.UUID                `json:"target_id"`
	TargetKeyHash      string                   `json:"target_key_hash"`
	Status             SecurityScanStatus       `json:"status"`
	Trigger            SecurityTriggerKind      `json:"trigger"`
	RequestedBy        string                   `json:"requested_by,omitempty"`
	RequestEventID     string                   `json:"request_event_id,omitempty"`
	RequestDTag        string                   `json:"request_d_tag,omitempty"`
	OSVQueryCount      int                      `json:"osv_query_count"`
	FindingCount       int                      `json:"finding_count"`
	SeverityCounts     SecuritySeverityCounts   `json:"severity_counts"`
	UnsupportedCount   int                      `json:"unsupported_count"`
	UnsupportedReasons map[string]int           `json:"unsupported_reasons,omitempty"`
	PublishState       SecurityPublicationState `json:"publish_state"`
	Error              string                   `json:"error,omitempty"`
	Metadata           map[string]any           `json:"metadata,omitempty"`
	StartedAt          *time.Time               `json:"started_at,omitempty"`
	FinishedAt         *time.Time               `json:"finished_at,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
}

// SecurityTargetLatest stores latest target state for read surfaces and subsequent policy evaluation.
type SecurityTargetLatest struct {
	TargetID       uuid.UUID              `json:"target_id"`
	TargetKeyHash  string                 `json:"target_key_hash"`
	RunID          uuid.UUID              `json:"run_id"`
	Status         SecurityScanStatus     `json:"status"`
	SeverityCounts SecuritySeverityCounts `json:"severity_counts"`
	FindingCount   int                    `json:"finding_count"`
	ScannedAt      time.Time              `json:"scanned_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// SecurityOSVFinding stores a normalized OSV vulnerability finding for a scan run.
type SecurityOSVFinding struct {
	ID             uuid.UUID        `json:"id"`
	RunID          uuid.UUID        `json:"run_id"`
	TargetKeyHash  string           `json:"target_key_hash"`
	FindingKey     string           `json:"finding_key"`
	FindingKeyHash string           `json:"finding_key_hash"`
	OSVID          string           `json:"osv_id"`
	CVE            string           `json:"cve,omitempty"`
	Summary        string           `json:"summary,omitempty"`
	Details        string           `json:"details,omitempty"`
	Severity       SecuritySeverity `json:"severity"`
	Package        SecurityPackage  `json:"package"`
	Aliases        []string         `json:"aliases,omitempty"`
	References     []string         `json:"references,omitempty"`
	WithdrawnAt    *time.Time       `json:"withdrawn_at,omitempty"`
	RawModified    string           `json:"raw_modified,omitempty"`
	Metadata       map[string]any   `json:"metadata,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// SecurityScanSchedule stores repository-backed due records; cadence execution is owned by subsequent epics.
type SecurityScanSchedule struct {
	ID               uuid.UUID      `json:"id"`
	PolicyID         uuid.UUID      `json:"policy_id"`
	TargetID         uuid.UUID      `json:"target_id"`
	TargetKeyHash    string         `json:"target_key_hash"`
	Enabled          bool           `json:"enabled"`
	IntervalSeconds  int            `json:"interval_seconds"`
	NextDueAt        time.Time      `json:"next_due_at"`
	LeaseUntil       *time.Time     `json:"lease_until,omitempty"`
	LeasedBy         string         `json:"leased_by,omitempty"`
	LastDispatchedAt *time.Time     `json:"last_dispatched_at,omitempty"`
	LastRunID        *uuid.UUID     `json:"last_run_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

// SecurityPolicyBreach records breach-fingerprint lifecycle for subsequent notification dispatch.
type SecurityPolicyBreach struct {
	ID                  uuid.UUID                        `json:"id"`
	PolicyID            uuid.UUID                        `json:"policy_id"`
	TargetKeyHash       string                           `json:"target_key_hash"`
	Fingerprint         string                           `json:"fingerprint"`
	PreviousFingerprint string                           `json:"previous_fingerprint,omitempty"`
	Enforcement         string                           `json:"enforcement"`
	ViolatedRules       []string                         `json:"violated_rules"`
	SeverityCounts      SecuritySeverityCounts           `json:"severity_counts"`
	OSVIDs              []string                         `json:"osv_ids"`
	NotificationStatus  SecurityBreachNotificationStatus `json:"notification_status"`
	FirstSeenAt         time.Time                        `json:"first_seen_at"`
	LastSeenAt          time.Time                        `json:"last_seen_at"`
	ResolvedAt          *time.Time                       `json:"resolved_at,omitempty"`
	Metadata            map[string]any                   `json:"metadata,omitempty"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
}

// SecurityBreachRecordResult reports fingerprint lifecycle effects from persistence.
type SecurityBreachRecordResult string

const (
	SecurityBreachRecordNew       SecurityBreachRecordResult = "new"
	SecurityBreachRecordChanged   SecurityBreachRecordResult = "changed"
	SecurityBreachRecordUnchanged SecurityBreachRecordResult = "unchanged"
)

// OSVVulnerabilityCache stores hydrated OSV records and retention metadata.
type OSVVulnerabilityCache struct {
	OSVID       string           `json:"osv_id"`
	Summary     string           `json:"summary,omitempty"`
	Severity    SecuritySeverity `json:"severity"`
	Aliases     []string         `json:"aliases,omitempty"`
	Raw         map[string]any   `json:"raw"`
	CachedAt    time.Time        `json:"cached_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	WithdrawnAt *time.Time       `json:"withdrawn_at,omitempty"`
}

// SecurityObservablePublication stores retryable relay-publication state for Security observables.
type SecurityObservablePublication struct {
	ID             uuid.UUID                `json:"id"`
	ObservableType string                   `json:"observable_type"`
	RunID          *uuid.UUID               `json:"run_id,omitempty"`
	TargetKeyHash  string                   `json:"target_key_hash,omitempty"`
	FindingID      *uuid.UUID               `json:"finding_id,omitempty"`
	BreachID       *uuid.UUID               `json:"breach_id,omitempty"`
	EventKind      int                      `json:"event_kind"`
	DTag           string                   `json:"d_tag"`
	Schema         string                   `json:"schema"`
	PublishState   SecurityPublicationState `json:"publish_state"`
	EventID        string                   `json:"event_id,omitempty"`
	AttemptCount   int                      `json:"attempt_count"`
	LastError      string                   `json:"last_error,omitempty"`
	NextRetryAt    *time.Time               `json:"next_retry_at,omitempty"`
	PublishedAt    *time.Time               `json:"published_at,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

// CanonicalTargetHash returns the SHA-256 lower-hex hash of a canonical target key.
func CanonicalTargetHash(targetKey string) string {
	sum := sha256.Sum256([]byte(targetKey))
	return hex.EncodeToString(sum[:])
}

// NewSBOMSecurityTarget derives the canonical SBOM target identity.
func NewSBOMSecurityTarget(subject SBOMSubject, format SBOMFormat, payloadSHA256, referenceDTag string) (SecurityTarget, error) {
	subjectType := strings.ToLower(strings.TrimSpace(string(subject.Type)))
	if subjectType == "" {
		return SecurityTarget{}, fmt.Errorf("security target sbom subject type is required")
	}
	subjectKey := strings.TrimSpace(subject.Digest)
	if subjectKey == "" {
		subjectKey = strings.TrimSpace(subject.ID)
	}
	if subjectKey == "" {
		return SecurityTarget{}, fmt.Errorf("security target sbom subject digest or id is required")
	}
	formatValue := strings.ToLower(strings.TrimSpace(string(format)))
	if formatValue == "" {
		return SecurityTarget{}, fmt.Errorf("security target sbom format is required")
	}
	payloadHash := strings.ToLower(strings.TrimSpace(payloadSHA256))
	if !sha256HexPattern.MatchString(payloadHash) {
		return SecurityTarget{}, fmt.Errorf("security target sbom payload_sha256 must be a 64-character hex digest")
	}
	dTag := strings.TrimSpace(referenceDTag)
	if dTag == "" {
		return SecurityTarget{}, fmt.Errorf("security target sbom reference d tag is required")
	}
	key := strings.Join([]string{"sbom", escapeSecurityKeyPart(subjectType), escapeSecurityKeyPart(subjectKey), escapeSecurityKeyPart(formatValue), payloadHash, escapeSecurityKeyPart(dTag)}, ":")
	copySubject := subject
	return SecurityTarget{
		Type:          SecurityTargetSBOM,
		TargetKey:     key,
		TargetKeyHash: CanonicalTargetHash(key),
		Display:       fmt.Sprintf("SBOM %s/%s %s", subjectType, subjectKey, formatValue),
		Subject:       &copySubject,
		Metadata:      map[string]any{"reference_d_tag": dTag, "payload_sha256": payloadHash, "format": formatValue},
	}, nil
}

// NewPackageSecurityTarget derives the canonical package-coordinate target identity.
func NewPackageSecurityTarget(ecosystem, name, version string) (SecurityTarget, error) {
	ecosystem = strings.ToLower(strings.TrimSpace(ecosystem))
	name = strings.ToLower(strings.TrimSpace(name))
	version = strings.TrimSpace(version)
	if ecosystem == "" {
		return SecurityTarget{}, fmt.Errorf("security target package ecosystem is required")
	}
	if name == "" {
		return SecurityTarget{}, fmt.Errorf("security target package name is required")
	}
	key := strings.Join([]string{"package", escapeSecurityKeyPart(ecosystem), escapeSecurityKeyPart(name), escapeSecurityKeyPart(version)}, ":")
	pkg := SecurityPackage{Ecosystem: ecosystem, Name: name, Version: version}
	return SecurityTarget{
		Type:          SecurityTargetPackage,
		TargetKey:     key,
		TargetKeyHash: CanonicalTargetHash(key),
		Display:       fmt.Sprintf("%s/%s@%s", ecosystem, name, version),
		Package:       &pkg,
	}, nil
}

// NewPURLSecurityTarget derives the canonical package-url target identity.
func NewPURLSecurityTarget(rawPURL string) (SecurityTarget, error) {
	parsed, err := packageurl.FromString(strings.TrimSpace(rawPURL))
	if err != nil {
		return SecurityTarget{}, fmt.Errorf("security target purl is invalid: %w", err)
	}
	normalized := parsed.ToString()
	key := "purl:" + normalized
	pkg := SecurityPackage{Ecosystem: parsed.Type, Name: parsed.Name, Version: parsed.Version, PURL: normalized}
	return SecurityTarget{
		Type:          SecurityTargetPURL,
		TargetKey:     key,
		TargetKeyHash: CanonicalTargetHash(key),
		Display:       normalized,
		Package:       &pkg,
		PURL:          normalized,
	}, nil
}

// NewCommitSecurityTarget derives the canonical Git commit target identity.
func NewCommitSecurityTarget(repoURL, commitHash string) (SecurityTarget, error) {
	repoURL = strings.TrimSpace(repoURL)
	commitHash = strings.ToLower(strings.TrimSpace(commitHash))
	if !commitHashPattern.MatchString(commitHash) {
		return SecurityTarget{}, fmt.Errorf("security target commit hash must be a full 40-character SHA-1 or 64-character SHA-256 hex digest")
	}
	repoComponent := "unknown"
	if repoURL != "" {
		repoComponent = CanonicalTargetHash(repoURL)
	}
	key := strings.Join([]string{"commit", repoComponent, commitHash}, ":")
	return SecurityTarget{
		Type:          SecurityTargetCommit,
		TargetKey:     key,
		TargetKeyHash: CanonicalTargetHash(key),
		Display:       fmt.Sprintf("commit %s in %s", commitHash, repoComponent),
		RepositoryURL: repoURL,
		CommitHash:    commitHash,
	}, nil
}

func escapeSecurityKeyPart(value string) string {
	escaped := url.QueryEscape(strings.TrimSpace(value))
	return strings.ReplaceAll(escaped, "+", "%20")
}

var (
	sha256HexPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	commitHashPattern = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)
)
