package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

const testPubkey = "1111111111111111111111111111111111111111111111111111111111111111"

func TestRunRequiresExplicitSQLAndPubkeyWithoutLeakingDSN(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--service-pubkey", testPubkey}, &out, &errOut, func(string) string { return "" }); code != 1 {
		t.Fatalf("run code = %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "BAHIA_CENSUS_DATABASE_URL is required") {
		t.Fatalf("unexpected output: %q / %q", out.String(), errOut.String())
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"--service-pubkey", "not-a-key"}, &out, &errOut, func(string) string { return "postgres://secret" }); code != 1 || strings.Contains(errOut.String(), "postgres://secret") {
		t.Fatalf("invalid key response leaked DSN: %q", errOut.String())
	}
}

func TestCensusClassifiesBoundedSQLWithoutPlaintext(t *testing.T) {
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close(context.Background())
	mock.ExpectQuery("SELECT encryption_method").WithArgs(11).WillReturnRows(pgxmock.NewRows([]string{"encryption_method", "octet_length"}).AddRow("aes256gcm", 28).AddRow("nip44", 52).AddRow("unknown-method", 9))
	mock.ExpectQuery("SELECT encryption_method").WithArgs(11).WillReturnRows(pgxmock.NewRows([]string{"encryption_method", "octet_length"}).AddRow("aes256gcm", 29))
	tags := `[["t","org-member"],["state_hash","` + strings.Repeat("a", 64) + `"]]`
	ock := `{"schema":"bahia.confidential.aead.v1","key_ref":"ock/org","key_version":"1","ciphertext":"VERY_PRIVATE_CIPHERTEXT_SENTINEL"}`
	transcript := `{"schema":"bahia.assistant-transcript.v1","key_ref":"assistant-transcript/service-nostr-key","key_version":"v1","ciphertext":"VERY_PRIVATE_CIPHERTEXT_SENTINEL"}`
	transcriptTags := `[["key_ref","assistant-transcript/service-nostr-key"],["key_version","v1"]]`
	mock.ExpectQuery("SELECT kind").WithArgs(testPubkey, 11, maxMetadataBytes).WillReturnRows(pgxmock.NewRows([]string{"kind", "content", "content_size", "tags", "tags_size"}).
		AddRow(30900, ock, len(ock), tags, len(tags)).
		AddRow(30316, transcript, len(transcript), transcriptTags, len(transcriptTags)))
	r, err := census(context.Background(), mock, testPubkey, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Families["service_secrets"].Classes["aes256gcm"] != 1 || r.Families["service_secrets"].Unknown != 1 || r.Families["confidential_cp_state"].Classes["ock"] != 1 || r.Families["assistant_transcripts"].Classes["service_nostr_key_v1"] != 1 || r.Families["confidential_state_hash"].Classes["legacy_unversioned_tag"] != 1 {
		t.Fatalf("unexpected census: %+v", r.Families)
	}
	if r.Families["confidential_cp_state"].Status != "unproven" || len(r.GlobalBlockers) == 0 {
		t.Fatal("SQL census incorrectly claimed complete coverage")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("VERY_PRIVATE_CIPHERTEXT_SENTINEL")) {
		t.Fatalf("ciphertext/secret leaked in report: %s", encoded)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCensusExcessAndMissingTableFailWithoutPartialReport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rows      *pgxmock.Rows
		wantBound bool
	}{
		{"bound", pgxmock.NewRows([]string{"encryption_method", "octet_length"}).AddRow("nip44", 2).AddRow("nip44", 2), true},
		{"missing_table", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock, err := pgxmock.NewConn()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close(context.Background())
			q := mock.ExpectQuery("SELECT encryption_method").WithArgs(2)
			if tc.rows != nil {
				q.WillReturnRows(tc.rows)
			} else {
				q.WillReturnError(errors.New("relation missing: password=secret"))
			}
			r, err := census(context.Background(), mock, testPubkey, 1)
			if err == nil || len(r.Families) != 0 {
				t.Fatalf("expected no partial report, got %+v, %v", r, err)
			}
			if tc.wantBound && safeError(err) != "service_secrets exceeds --max-rows; increase bound only after review" {
				t.Fatalf("unexpected bound error: %s", safeError(err))
			}
			if !tc.wantBound && strings.Contains(safeError(err), "secret") {
				t.Fatal("SQL error leaked")
			}
		})
	}
}

func TestClassifyLegacyAndUnknown(t *testing.T) {
	r := report{Families: map[string]*family{}}
	for _, name := range []string{"confidential_cp_state", "confidential_service_inner", "ock_key_envelopes", "cp_state_unclassified", "assistant_transcripts", "assistant_checkpoints", "confidential_state_hash", "sbom_dsse_references"} {
		r.Families[name] = newFamily("unproven")
	}
	o1 := `{"schema":"bahia.org-state.aead.v1","key_ref":"org-state/service-nostr-key"}`
	tags := `[["t","org-invite"]]`
	classifyEvent(&r, 30900, o1, len(o1), tags, len(tags))
	n1Tags := `[["t","secret-registry"]]`
	n1 := base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))
	classifyEvent(&r, 30900, n1, len(n1), n1Tags, len(n1Tags))
	classifyEvent(&r, 30900, "not-nip44", 9, n1Tags, len(n1Tags))
	classifyEvent(&r, 4903, `{}`, 2, `[]`, 2)
	if r.Families["confidential_cp_state"].Classes["legacy_o1"] != 1 || r.Families["confidential_cp_state"].Classes["legacy_n1_candidate"] != 1 || r.Families["cp_state_unclassified"].Unknown != 1 || r.Families["assistant_checkpoints"].Unknown != 1 {
		t.Fatalf("bad legacy classification: %+v", r.Families)
	}
}

func TestCensusCoversAllOCKTopicsInnerAndOpaqueKeyWraps(t *testing.T) {
	r := report{ServicePubkey: testPubkey, Families: map[string]*family{}}
	for _, name := range []string{"confidential_cp_state", "confidential_service_inner", "ock_key_envelopes", "cp_state_unclassified", "assistant_transcripts", "assistant_checkpoints", "confidential_state_hash", "sbom_dsse_references"} {
		r.Families[name] = newFamily("unproven")
	}
	nip44 := base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))
	for _, topic := range []string{"operator-allowlist", "payment-record", "security-finding-detail", "soul-factory-adapter-ledger", "notification-channel"} {
		content := `{"schema":"bahia.confidential.aead.v1","key_ref":"ock/fleet","key_version":"1","service_inner":"` + nip44 + `"}`
		tags := `[["t","` + topic + `"]]`
		classifyEvent(&r, 30900, content, len(content), tags, len(tags))
	}
	wrapTags := `[["t","org-key-envelope"]]`
	classifyEvent(&r, 30900, nip44, len(nip44), wrapTags, len(wrapTags))
	if r.Families["confidential_cp_state"].Classes["ock"] != 5 || r.Families["confidential_service_inner"].Classes["nip44_candidate"] != 5 || r.Families["ock_key_envelopes"].Classes["nip44_wrap_candidate"] != 1 {
		t.Fatalf("missed OCK/inner/key-wrap: %+v", r.Families)
	}
	if r.Families["ock_key_envelopes"].Status != "unproven" {
		t.Fatal("opaque key wrap treated as proven")
	}
	unknownTags := `[["t","new-sensitive-family"]]`
	classifyEvent(&r, 30900, `{ "schema": "future.crypto.v2" }`, 32, unknownTags, len(unknownTags))
	if r.Families["cp_state_unclassified"].Unknown != 1 {
		t.Fatal("unknown kind-30900 family vanished")
	}
	classifyEvent(&r, 30900, "oversized", maxMetadataBytes+1, unknownTags, len(unknownTags))
	if r.Families["cp_state_unclassified"].Unknown != 2 {
		t.Fatal("oversized kind-30900 family vanished")
	}
}
