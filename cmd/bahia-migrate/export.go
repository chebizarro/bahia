package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// The export boundary intentionally has no publisher, signer, outbox or service key.
// SQL is a source of proposals, never an authority for a signed event.

type exportAdoption struct {
	ID, OrgID, ServiceID, EnvironmentID       uuid.UUID
	ContainerID, ServiceName, EnvironmentName string
}

type exportPolicy struct {
	ID, ServiceID, EnvironmentID                uuid.UUID
	RepoCoordinate, WorkflowPath, BranchPattern string
	Enabled                                     bool
}

type exportSecurityTarget struct {
	ID                                          uuid.UUID
	TargetType, TargetKeyHash, PURL, CommitHash string
	Subject, Package, Metadata                  json.RawMessage
}

type pgExportSource struct{ tx pgx.Tx }

const maxLegacyExportRowsPerFamily = 10000

func (s pgExportSource) Adoptions(ctx context.Context) ([]exportAdoption, error) {
	rows, err := s.tx.Query(ctx, `SELECT a.id, a.org_id, a.service_id, a.environment_id,
		a.container_id, s.name, e.name
		FROM adopted_runtime_identity a JOIN services s ON s.id=a.service_id
		JOIN environments e ON e.id=a.environment_id ORDER BY a.org_id, a.id LIMIT $1`, maxLegacyExportRowsPerFamily+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []exportAdoption{}
	for rows.Next() {
		var r exportAdoption
		if err := rows.Scan(&r.ID, &r.OrgID, &r.ServiceID, &r.EnvironmentID, &r.ContainerID, &r.ServiceName, &r.EnvironmentName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxLegacyExportRowsPerFamily {
		return nil, fmt.Errorf("adoption export exceeds %d rows", maxLegacyExportRowsPerFamily)
	}
	return out, nil
}

func (s pgExportSource) Policies(ctx context.Context) ([]exportPolicy, error) {
	rows, err := s.tx.Query(ctx, `SELECT id, repo_coordinate, workflow_path, COALESCE(branch_pattern,''),
		service_id, environment_id, enabled FROM hiveci_pipeline_policies ORDER BY id LIMIT $1`, maxLegacyExportRowsPerFamily+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []exportPolicy{}
	for rows.Next() {
		var r exportPolicy
		if err := rows.Scan(&r.ID, &r.RepoCoordinate, &r.WorkflowPath, &r.BranchPattern, &r.ServiceID, &r.EnvironmentID, &r.Enabled); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxLegacyExportRowsPerFamily {
		return nil, fmt.Errorf("Hive-CI export exceeds %d rows", maxLegacyExportRowsPerFamily)
	}
	return out, nil
}

func (s pgExportSource) SecurityTargets(ctx context.Context) ([]exportSecurityTarget, error) {
	rows, err := s.tx.Query(ctx, `SELECT id, target_type, target_key_hash, COALESCE(purl,''),
		COALESCE(commit_hash,''), subject, package, metadata
		FROM security_scan_targets ORDER BY id LIMIT $1`, maxLegacyExportRowsPerFamily+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []exportSecurityTarget{}
	for rows.Next() {
		var r exportSecurityTarget
		if err := rows.Scan(&r.ID, &r.TargetType, &r.TargetKeyHash, &r.PURL, &r.CommitHash, &r.Subject, &r.Package, &r.Metadata); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxLegacyExportRowsPerFamily {
		return nil, fmt.Errorf("security export exceeds %d rows", maxLegacyExportRowsPerFamily)
	}
	return out, nil
}

// No files are opened until the role and transaction have been checked.
func runLegacyExport(ctx context.Context, cfg *config.Config, outputDir string, relays []string, servicePubkey string, timeout time.Duration, stdout, stderr io.Writer) int {
	if !filepath.IsAbs(outputDir) {
		return reportError(stderr, "export requires an absolute --export-dir")
	}
	if (len(relays) == 0) != (servicePubkey == "") {
		return reportError(stderr, "--export-relays and --export-service-pubkey must be supplied together")
	}
	if timeout < time.Second || timeout > time.Hour {
		return reportError(stderr, "--export-timeout must be 1s..1h")
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DB.DSN())
	if err != nil {
		return reportError(stderr, "export database configuration: %v", cfg.DB.RedactError(err))
	}
	poolConfig.MaxConns, poolConfig.MinConns = 1, 0
	poolConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return reportError(stderr, "export database connection: %v", cfg.DB.RedactError(err))
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return reportError(stderr, "export read-only snapshot: %v", cfg.DB.RedactError(err))
	}
	// Read-only rollback is best-effort; the snapshot result takes precedence.
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := verifyExportReadOnly(ctx, tx); err != nil {
		return reportError(stderr, "export refused write-capable database role: %v", err)
	}
	source := pgExportSource{tx: tx}
	adoptions, err := source.Adoptions(ctx)
	if err != nil {
		return reportError(stderr, "export adoption read: %v", cfg.DB.RedactError(err))
	}
	policies, err := source.Policies(ctx)
	if err != nil {
		return reportError(stderr, "export Hive-CI read: %v", cfg.DB.RedactError(err))
	}
	targets, err := source.SecurityTargets(ctx)
	if err != nil {
		return reportError(stderr, "export security read: %v", cfg.DB.RedactError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return reportError(stderr, "export snapshot completion: %v", cfg.DB.RedactError(err))
	}
	heads := map[string]string(nil)
	if len(relays) > 0 {
		readCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		heads, err = readCanonicalHeads(readCtx, relays, servicePubkey)
		if err != nil {
			return reportError(stderr, "export canonical comparison incomplete: %v", err)
		}
	}
	index, files, err := draftLegacyExport(adoptions, policies, targets, heads)
	if err != nil {
		return reportError(stderr, "export draft: %v", err)
	}
	if err := writeLegacyExport(outputDir, files, index); err != nil {
		return reportError(stderr, "export output: %v", err)
	}
	_, err = fmt.Fprintf(stdout, "exported %d draft files and %d index rows to %s; unsigned, unpublished\n", len(files), len(index.Rows), outputDir)
	if err != nil {
		return 1
	}
	return 0
}

func verifyExportReadOnly(ctx context.Context, tx pgx.Tx) error {
	var readOnly, isolation string
	var privileged, schemaCreate, tableWrite bool
	err := tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only'), current_setting('transaction_isolation'),
		(SELECT rolsuper OR rolcreaterole OR rolcreatedb FROM pg_roles WHERE rolname=current_user),
		has_schema_privilege(current_schema(), 'CREATE'),
		EXISTS (SELECT 1 FROM (VALUES ('adopted_runtime_identity'), ('services'), ('environments'),
			('hiveci_pipeline_policies'), ('security_scan_targets')) AS source(name)
			WHERE has_table_privilege(source.name, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'))`).Scan(
		&readOnly, &isolation, &privileged, &schemaCreate, &tableWrite)
	if err != nil {
		return err
	}
	if readOnly != "on" || isolation != "repeatable read" || privileged || schemaCreate || tableWrite {
		return fmt.Errorf("requires REPEATABLE READ READ ONLY and a SELECT-only, non-privileged role on source tables/schema")
	}
	return nil
}

type unsignedDraft struct {
	Kind    int            `json:"kind"`
	Tags    [][]string     `json:"tags"`
	Content map[string]any `json:"content"`
}

func newUnsignedDraft(domain, op, coordinate, org, intentID string, content map[string]any) unsignedDraft {
	tags := [][]string{{"d", coordinate}, {"domain", domain}, {"schema", "bahia.intent." + domain + ".v1"},
		{"t", "bahia-intent"}, {"t", domain}, {"op", op}}
	if org != "" {
		tags = append(tags, []string{"org", org})
	}
	tags = append(tags, []string{"intent_id", intentID})
	return unsignedDraft{Kind: 30900, Tags: tags, Content: content}
}

type exportIndex struct {
	Version             int              `json:"version"`
	CanonicalComparison string           `json:"canonical_comparison"`
	SecurityDisposition string           `json:"security_disposition"`
	Rows                []exportIndexRow `json:"rows"`
}
type exportIndexRow struct {
	Family      string   `json:"family"`
	LegacyRowID string   `json:"legacy_row_id"`
	DraftFile   string   `json:"draft_file,omitempty"`
	Coordinate  string   `json:"coordinate,omitempty"`
	Status      string   `json:"status"`
	Notes       []string `json:"notes"`
}

func mintExportID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func draftLegacyExport(adoptions []exportAdoption, policies []exportPolicy, targets []exportSecurityTarget, heads map[string]string) (exportIndex, map[string]any, error) {
	index := exportIndex{Version: 1, CanonicalComparison: "not requested; no absence claim", Rows: []exportIndexRow{},
		SecurityDisposition: "no target-registration intent domain; reviewed scan target inputs only"}
	if heads != nil {
		index.CanonicalComparison = "all configured relays reached EOSE for scoped service-author cp-state; visible coordinates only"
	}
	files := map[string]any{}
	orgs := map[uuid.UUID][]exportAdoption{}
	for _, row := range adoptions {
		orgs[row.OrgID] = append(orgs[row.OrgID], row)
	}
	orgIDs := make([]string, 0, len(orgs))
	for id := range orgs {
		orgIDs = append(orgIDs, id.String())
	}
	sort.Strings(orgIDs)
	serviceIDs := map[uuid.UUID]string{}
	environmentIDs := map[uuid.UUID]string{}
	for _, orgText := range orgIDs {
		orgID := uuid.MustParse(orgText)
		intentID, err := mintExportID()
		if err != nil {
			return index, nil, err
		}
		file := "adoption-" + orgText + ".json"
		targetList, selections := []any{}, []any{}
		entries := []exportIndexRow{}
		for _, row := range orgs[orgID] {
			binding := "adoption:binding:" + row.ServiceID.String() + ":" + row.EnvironmentID.String()
			entry := exportIndexRow{Family: "adoption", LegacyRowID: row.ID.String(), Coordinate: binding,
				Status: "needs_live_docker_and_signet_review", Notes: []string{"Check Docker host liveness and candidate identity before signing.", "Replace endpoint_ref with a configured Signet-backed endpoint reference (bahia-2o3on); no SQL secret is exported.", "New UUIDv7 service and environment IDs are fixed in the draft; verify existing canonical ownership before signing."}}
			if head, found := heads[binding]; found {
				entry.Status = "canonical_duplicate"
				entry.Notes = append(entry.Notes, "Canonical binding already present: "+head+"; omitted from draft.")
			} else if strings.TrimSpace(row.ContainerID) == "" {
				entry.Status = "manual_review_no_container_id"
				entry.Notes = append(entry.Notes, "No container ID for a valid adoption selection; omitted from draft.")
			} else {
				serviceID := serviceIDs[row.ServiceID]
				if serviceID == "" {
					serviceID, err = mintExportID()
					if err != nil {
						return index, nil, err
					}
					serviceIDs[row.ServiceID] = serviceID
				}
				environmentID := environmentIDs[row.EnvironmentID]
				if environmentID == "" {
					environmentID, err = mintExportID()
					if err != nil {
						return index, nil, err
					}
					environmentIDs[row.EnvironmentID] = environmentID
				}
				name := "legacy-" + strings.ReplaceAll(row.ID.String(), "-", "")[:12]
				targetList = append(targetList, map[string]any{"name": name, "endpoint_ref": "REPLACE_WITH_SIGNET_ENDPOINT_REF", "environment_name": row.EnvironmentName, "environment_id": environmentID})
				selections = append(selections, map[string]any{"target_name": name, "container_id": row.ContainerID, "service_name_override": row.ServiceName, "service_id": serviceID})
				entry.DraftFile = file
			}
			entries = append(entries, entry)
		}
		if len(selections) > 0 {
			files[file] = newUnsignedDraft("adoption", "import", "adoption:"+orgText, orgText, intentID,
				map[string]any{"org_id": orgText, "targets": targetList, "selections": selections})
		}
		index.Rows = append(index.Rows, entries...)
	}
	for _, row := range policies {
		entry := exportIndexRow{Family: "hiveci-policy", LegacyRowID: row.ID.String(), Status: "needs_review", Notes: []string{"New UUIDv7 policy ID; legacy SQL ID is not reused (bahia-qdgcl).", "SQL metadata is omitted to prevent secret export; review complete desired state."}}
		if strings.TrimSpace(row.RepoCoordinate) == "" || strings.ContainsAny(row.RepoCoordinate, "@?") || strings.Contains(row.RepoCoordinate, "://") ||
			strings.TrimSpace(row.WorkflowPath) == "" || row.ServiceID == uuid.Nil || row.EnvironmentID == uuid.Nil {
			entry.Status = "manual_review_invalid_policy"
			index.Rows = append(index.Rows, entry)
			continue
		}
		id, err := mintExportID()
		if err != nil {
			return index, nil, err
		}
		intentID, err := mintExportID()
		if err != nil {
			return index, nil, err
		}
		coordinate := "hiveci:policy:" + id
		entry.Coordinate = coordinate
		if head, found := heads[coordinate]; found {
			entry.Status = "canonical_conflict"
			entry.Notes = append(entry.Notes, "Canonical coordinate already present: "+head)
			index.Rows = append(index.Rows, entry)
			continue
		}
		entry.Notes = append(entry.Notes, "Encrypted canonical selectors cannot be compared without an operator decrypt; review duplicates manually.")
		entry.DraftFile = "hiveci-policy-" + id + ".json"
		content := map[string]any{"id": id, "intent_id": intentID, "repo_coordinate": row.RepoCoordinate, "workflow_path": row.WorkflowPath,
			"service_id": row.ServiceID.String(), "environment_id": row.EnvironmentID.String(), "enabled": row.Enabled, "metadata": map[string]any{}}
		if row.BranchPattern != "" {
			content["branch_pattern"] = row.BranchPattern
		}
		files[entry.DraftFile] = newUnsignedDraft("hiveci-policy", "create", coordinate, "", intentID, content)
		index.Rows = append(index.Rows, entry)
	}
	for _, row := range targets {
		entry := exportIndexRow{Family: "security-target", LegacyRowID: row.ID.String(),
			Status: "needs_operator_review", Notes: []string{"No target-registration intent domain exists. This is scan-run target input, not a registration intent.", "SQL metadata is not exported except required non-secret SBOM reference fields; SBOM location is a review placeholder."}}
		if digest, err := hex.DecodeString(row.TargetKeyHash); err != nil || len(digest) != 32 || strings.ToLower(row.TargetKeyHash) != row.TargetKeyHash {
			entry.Status = "manual_review_invalid_target_hash"
			index.Rows = append(index.Rows, entry)
			continue
		}
		coordinate := "security:target:" + row.TargetKeyHash
		entry.Coordinate = coordinate
		if head, found := heads[coordinate]; found {
			entry.Status = "canonical_duplicate"
			entry.Notes = append(entry.Notes, "Canonical target already present: "+head)
			index.Rows = append(index.Rows, entry)
			continue
		}
		input, err := securityScanTargetInput(row)
		if err != nil {
			entry.Status = "manual_review_invalid_target"
			entry.Notes = append(entry.Notes, err.Error())
			index.Rows = append(index.Rows, entry)
			continue
		}
		entry.DraftFile = "security-target-" + row.ID.String() + ".json"
		files[entry.DraftFile] = map[string]any{"target": input}
		index.Rows = append(index.Rows, entry)
	}
	return index, files, nil
}

func securityScanTargetInput(row exportSecurityTarget) (map[string]any, error) {
	input := map[string]any{"type": row.TargetType}
	switch row.TargetType {
	case "package":
		var pkg struct{ Ecosystem, Name, Version string }
		if err := json.Unmarshal(row.Package, &pkg); err != nil || pkg.Ecosystem == "" || pkg.Name == "" {
			return nil, fmt.Errorf("package fields unavailable")
		}
		input["package"] = map[string]string{"ecosystem": pkg.Ecosystem, "name": pkg.Name, "version": pkg.Version}
	case "purl":
		if row.PURL == "" || strings.ContainsAny(row.PURL, "?#") {
			return nil, fmt.Errorf("purl is empty or may contain credentials")
		}
		input["purl"] = row.PURL
	case "commit":
		if row.CommitHash == "" {
			return nil, fmt.Errorf("commit hash is empty")
		}
		// Repository URLs are deliberately omitted: userinfo/query credentials are common.
		input["commit"] = map[string]string{"commit_hash": row.CommitHash}
	case "sbom":
		var subject struct{ Type, ID, Digest string }
		var metadata map[string]any
		if err := json.Unmarshal(row.Subject, &subject); err != nil {
			return nil, fmt.Errorf("SBOM subject unavailable")
		}
		if subject.Type == "" || (subject.ID == "" && subject.Digest == "") {
			return nil, fmt.Errorf("SBOM subject identity unavailable")
		}
		if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
			return nil, fmt.Errorf("SBOM reference unavailable")
		}
		ref := map[string]any{"subject": map[string]string{"type": subject.Type, "id": subject.ID, "digest": subject.Digest}}
		for _, key := range []string{"format", "storage", "payload_sha256", "reference_d_tag"} {
			value, ok := metadata[key].(string)
			if !ok || value == "" {
				return nil, fmt.Errorf("SBOM %s unavailable", key)
			}
			ref[key] = value
		}
		ref["location_uri"] = "REPLACE_WITH_REVIEWED_SBOM_LOCATION_URI"
		input["sbom"] = ref
	default:
		return nil, fmt.Errorf("unsupported target type")
	}
	return input, nil
}

func writeLegacyExport(dir string, files map[string]any, index exportIndex) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("output directory must not exist: %w", err)
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, name := range paths {
		if err := writeExportJSON(filepath.Join(dir, name), files[name]); err != nil {
			return err
		}
	}
	return writeExportJSON(filepath.Join(dir, "index.json"), index)
}

func writeExportJSON(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(encoded, '\n'))
	closeErr := f.Close()
	return errors.Join(writeErr, closeErr)
}

// readCanonicalHeads makes one scoped REQ and requires every configured relay's
// real EOSE. It never interprets EOSE as global absence, and never publishes.
func readCanonicalHeads(ctx context.Context, relays []string, authorText string) (map[string]string, error) {
	author, err := nostr.PubKeyFromHex(authorText)
	if err != nil {
		return nil, fmt.Errorf("invalid service pubkey: %w", err)
	}
	pool := nostradapter.NewRelayPool(relays, zap.NewNop())
	defer pool.Close()
	pool.Connect(ctx)
	filter := nostr.Filter{Kinds: []nostr.Kind{30900}, Authors: []nostr.PubKey{author}, Tags: nostr.TagMap{"#t": {
		kinds.CPStateTopicAdoptionBinding, kinds.CPStateTopicHiveCIPolicy, kinds.CPStateTopicSecurityTarget}}}
	sub, err := pool.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	heads := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, fmt.Errorf("relay subscription closed before complete EOSE")
			}
			if ev == nil || ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() {
				return nil, fmt.Errorf("invalid canonical event in comparison")
			}
			d := ""
			for _, tag := range ev.Tags {
				if len(tag) > 1 && tag[0] == "d" {
					d = tag[1]
					break
				}
			}
			if d != "" {
				heads[d] = ev.ID.Hex()
			}
		case <-sub.EndOfStoredEvents:
			if !sub.AllRelaysReachedEOSE() {
				return nil, fmt.Errorf("not every relay reached real EOSE")
			}
			for {
				select {
				case ev, ok := <-sub.Events:
					if !ok {
						return heads, nil
					}
					if ev == nil || ev.PubKey != author || !ev.CheckID() || !ev.VerifySignature() {
						return nil, fmt.Errorf("invalid canonical event in comparison")
					}
					d := ""
					for _, tag := range ev.Tags {
						if len(tag) > 1 && tag[0] == "d" {
							d = tag[1]
							break
						}
					}
					if d != "" {
						heads[d] = ev.ID.Hex()
					}
				default:
					return heads, nil
				}
			}
		}
	}
}
