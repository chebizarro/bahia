// Command bahia-raw-key-crypto-census inventories SQL-visible ciphertext and
// metadata before a service-key custody change. It never decrypts or publishes.
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

const maxMetadataBytes = 16384

type family struct {
	SQLRows  int            `json:"sql_rows"`
	Classes  map[string]int `json:"classes"`
	Unknown  int            `json:"unknown"`
	Status   string         `json:"status"`
	Blockers []string       `json:"blockers"`
}

type report struct {
	ReadOnly       bool               `json:"read_only"`
	Scope          string             `json:"scope"`
	ServicePubkey  string             `json:"service_pubkey"`
	Families       map[string]*family `json:"families"`
	GlobalBlockers []string           `json:"global_blockers"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	flags := flag.NewFlagSet("bahia-raw-key-crypto-census", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pubkey := flags.String("service-pubkey", "", "existing service x-only pubkey (64 hex characters)")
	maxRows := flags.Int("max-rows", 10000, "maximum rows per SQL family (1..100000)")
	deadline := flags.Duration("deadline", 5*time.Minute, "SQL snapshot deadline (up to 30m)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *maxRows < 1 || *maxRows > 100000 || *deadline <= 0 || *deadline > 30*time.Minute {
		fmt.Fprintln(stderr, "usage: bahia-raw-key-crypto-census --service-pubkey HEX [--max-rows N] [--deadline 5m] (BAHIA_CENSUS_DATABASE_URL required)")
		return 1
	}
	*pubkey = strings.ToLower(strings.TrimSpace(*pubkey))
	decoded, err := hex.DecodeString(*pubkey)
	if err != nil || len(decoded) != 32 {
		fmt.Fprintln(stderr, "service-pubkey must be a 32-byte x-only hex pubkey")
		return 1
	}
	dsn := strings.TrimSpace(getenv("BAHIA_CENSUS_DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(stderr, "BAHIA_CENSUS_DATABASE_URL is required; no census performed")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *deadline)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		fmt.Fprintln(stderr, "SQL connection failed; no census performed")
		return 1
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		fmt.Fprintln(stderr, "read-only SQL snapshot failed; no census performed")
		return 1
	}
	defer tx.Rollback(context.Background())
	r, err := census(ctx, tx, *pubkey, *maxRows)
	if err != nil {
		// SQL errors may include connection strings or values. Never echo them.
		fmt.Fprintln(stderr, "SQL census incomplete; no partial report:", safeError(err))
		return 1
	}
	if err := tx.Commit(ctx); err != nil {
		fmt.Fprintln(stderr, "read-only SQL snapshot commit failed; no report")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(r); err != nil {
		fmt.Fprintln(stderr, "encode census report failed")
		return 1
	}
	return 0
}

func safeError(err error) string {
	var bounded *boundedError
	if errors.As(err, &bounded) {
		return bounded.Error()
	}
	return "SQL query or scan failed"
}

type boundedError struct{ family string }

func (e *boundedError) Error() string {
	return e.family + " exceeds --max-rows; increase bound only after review"
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func census(ctx context.Context, q queryer, pubkey string, maxRows int) (report, error) {
	r := report{ReadOnly: true, Scope: "postgres-repeatable-read-snapshot-only", ServicePubkey: pubkey,
		Families: map[string]*family{}, GlobalBlockers: []string{
			"canonical relay history is not enumerated; SQL is a derived index",
			"daemon local event store and outbox are not enumerated",
			"external SBOM attestation blobs are not enumerated",
		}}
	for _, name := range []string{"service_secrets", "secret_versions"} {
		f := newFamily("SQL method and ciphertext metadata do not prove decryptability")
		rows, err := q.Query(ctx, "SELECT encryption_method, octet_length(encrypted_value) FROM "+name+" ORDER BY id LIMIT $1", maxRows+1)
		if err != nil {
			return report{}, err
		}
		for rows.Next() {
			var method string
			var size int
			if err := rows.Scan(&method, &size); err != nil {
				rows.Close()
				return report{}, err
			}
			f.SQLRows++
			if f.SQLRows > maxRows {
				rows.Close()
				return report{}, &boundedError{name}
			}
			if size == 0 {
				f.Unknown++
				continue
			}
			switch method {
			case "nip44", "aes256gcm":
				f.Classes[method]++
			default:
				f.Unknown++
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report{}, err
		}
		r.Families[name] = f
	}
	for _, name := range []string{"confidential_cp_state", "confidential_service_inner", "ock_key_envelopes", "cp_state_unclassified", "assistant_transcripts", "assistant_checkpoints", "confidential_state_hash", "sbom_dsse_references"} {
		r.Families[name] = newFamily("SQL may omit canonical/local records and does not prove cryptographic access")
	}
	r.Families["ock_key_envelopes"].Blockers = append(r.Families["ock_key_envelopes"].Blockers, "NIP-44 OCK wrap recipient is opaque; SQL cannot prove the service copy is present")
	r.Families["cp_state_unclassified"].Blockers = append(r.Families["cp_state_unclassified"].Blockers, "all other service-authored cp-state is counted here; undiscovered encrypted families cannot be assumed absent")
	rows, err := q.Query(ctx, `SELECT kind, left(content, $3), octet_length(content), left(tags::text, $3), octet_length(tags::text)
		FROM nostr_events WHERE pubkey = $1 AND kind IN (30900, 30316, 4903, 30078)
		ORDER BY id LIMIT $2`, pubkey, maxRows+1, maxMetadataBytes)
	if err != nil {
		return report{}, err
	}
	seen := 0
	for rows.Next() {
		seen++
		if seen > maxRows {
			rows.Close()
			return report{}, &boundedError{"nostr_events"}
		}
		var kind, contentSize, tagsSize int
		var content, tagJSON string
		if err := rows.Scan(&kind, &content, &contentSize, &tagJSON, &tagsSize); err != nil {
			rows.Close()
			return report{}, err
		}
		classifyEvent(&r, kind, content, contentSize, tagJSON, tagsSize)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report{}, err
	}
	return r, nil
}

func newFamily(blocker string) *family {
	return &family{Classes: map[string]int{}, Status: "unproven", Blockers: []string{blocker}}
}

func classifyEvent(r *report, kind int, content string, contentSize int, tagJSON string, tagsSize int) {
	if contentSize > maxMetadataBytes || tagsSize > maxMetadataBytes {
		r.Families[familyForKind(kind)].Unknown++
		r.Families[familyForKind(kind)].SQLRows++
		return
	}
	var tags [][]string
	if json.Unmarshal([]byte(tagJSON), &tags) != nil {
		r.Families[familyForKind(kind)].Unknown++
		r.Families[familyForKind(kind)].SQLRows++
		return
	}
	topic := tagValue(tags, "t")
	switch kind {
	case 30900:
		classifyCPState(r, topic, content, tags)
	case 30316, 4903:
		name := familyForKind(kind)
		f := r.Families[name]
		f.SQLRows++
		var env struct {
			Schema     string `json:"schema"`
			KeyRef     string `json:"key_ref"`
			KeyVersion string `json:"key_version"`
			Ciphertext string `json:"ciphertext"`
		}
		if json.Unmarshal([]byte(content), &env) != nil || env.KeyRef == "" || env.Ciphertext == "" || env.KeyVersion == "" {
			f.Unknown++
			return
		}
		if tagValue(tags, "key_ref") != env.KeyRef || tagValue(tags, "key_version") != env.KeyVersion {
			f.Unknown++
			return
		}
		if (kind == 30316 && env.Schema != "bahia.assistant-transcript.v1") || (kind == 4903 && env.Schema != "bahia.audit.assistant-execution-checkpoint.v1") {
			f.Unknown++
			return
		}
		if env.KeyRef == "assistant-transcript/service-nostr-key" && env.KeyVersion == "v1" {
			f.Classes["service_nostr_key_v1"]++
		} else {
			f.Classes["other_versioned_key"]++
		}
	case 30078:
		if topic != "sbom-reference" {
			return
		}
		f := r.Families["sbom_dsse_references"]
		f.SQLRows++
		var att struct {
			Envelope *struct {
				Signatures []struct {
					KeyID string `json:"keyid"`
				} `json:"signatures"`
			} `json:"envelope"`
		}
		if json.Unmarshal([]byte(content), &att) != nil || att.Envelope == nil || len(att.Envelope.Signatures) == 0 {
			f.Unknown++
			return
		}
		for _, sig := range att.Envelope.Signatures {
			if strings.EqualFold(sig.KeyID, r.ServicePubkey) {
				f.Classes["service_pubkey_reference"]++
				return
			}
		}
		f.Classes["other_key_reference"]++
	}
}

func classifyCPState(r *report, topic, content string, tags [][]string) {
	legacy := map[string]string{"org-registry": "o1", "org-member": "o1", "org-invite": "o1", "secret-registry": "n1", "notification-channel": "n1"}
	_, legacyTopic := legacy[topic]
	hash := tagValue(tags, "state_hash")
	if legacyTopic || hash != "" {
		h := r.Families["confidential_state_hash"]
		h.SQLRows++
		if len(hash) == 64 && isHex(hash) {
			h.Classes["legacy_unversioned_tag"]++
		} else if hash == "" {
			h.Classes["absent"]++
		} else {
			h.Unknown++
		}
	}
	if topic == "org-key-envelope" {
		f := r.Families["ock_key_envelopes"]
		f.SQLRows++
		if nip44Shape(content) {
			f.Classes["nip44_wrap_candidate"]++
		} else {
			f.Unknown++
		}
		return
	}
	var env struct {
		Schema       string `json:"schema"`
		KeyRef       string `json:"key_ref"`
		KeyVersion   string `json:"key_version"`
		ServiceInner string `json:"service_inner"`
	}
	if json.Unmarshal([]byte(content), &env) == nil && env.Schema != "" {
		switch env.Schema {
		case "bahia.confidential.aead.v1":
			f := r.Families["confidential_cp_state"]
			f.SQLRows++
			if env.KeyRef != "" && env.KeyVersion != "" {
				f.Classes["ock"]++
			} else {
				f.Unknown++
			}
			inner := r.Families["confidential_service_inner"]
			inner.SQLRows++
			if env.ServiceInner == "" {
				inner.Classes["absent"]++
			} else if nip44Shape(env.ServiceInner) {
				inner.Classes["nip44_candidate"]++
			} else {
				inner.Unknown++
			}
		case "bahia.org-state.aead.v1":
			f := r.Families["confidential_cp_state"]
			f.SQLRows++
			if legacy[topic] == "o1" && env.KeyRef != "" {
				f.Classes["legacy_o1"]++
			} else {
				f.Unknown++
			}
		default:
			f := r.Families["cp_state_unclassified"]
			f.SQLRows++
			f.Unknown++
		}
	} else if legacy[topic] == "n1" && nip44Shape(content) {
		f := r.Families["confidential_cp_state"]
		f.SQLRows++
		f.Classes["legacy_n1_candidate"]++
	} else {
		f := r.Families["cp_state_unclassified"]
		f.SQLRows++
		f.Unknown++
	}
}

func familyForKind(kind int) string {
	switch kind {
	case 30900:
		return "cp_state_unclassified"
	case 30316:
		return "assistant_transcripts"
	case 4903:
		return "assistant_checkpoints"
	default:
		return "sbom_dsse_references"
	}
}
func tagValue(tags [][]string, name string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}
func isHex(s string) bool { _, err := hex.DecodeString(s); return err == nil }

// NIP-44 v2 payloads have a version byte and a minimum 99 decoded bytes.
// Shape alone does not prove the service key can decrypt the payload.
func nip44Shape(content string) bool {
	decoded, err := base64.StdEncoding.DecodeString(content)
	return err == nil && len(decoded) >= 99 && decoded[0] == 2
}
