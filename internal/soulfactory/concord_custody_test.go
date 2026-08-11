package soulfactory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestSealedConcordCustodyRoundTripsThroughSignet(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, staff, `{"community_id":"seed"}`)
	custody, err := newSealedConcordCustody(path, staff)
	if err != nil {
		t.Fatalf("newSealedConcordCustody() error = %v", err)
	}

	bundle := json.RawMessage(`{"community_id":"cord","community_root":"` + strings.Repeat("a", 64) + `"}`)
	controlRoot := strings.Repeat("b", 64)
	if err := custody.Store(t.Context(), concordCustodyRecord{Bundle: bundle, ControlRoot: controlRoot}); err != nil {
		t.Fatalf("Store() error = %v", err)
	}

	sealed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read custody file: %v", err)
	}
	for _, secret := range []string{"community_root", strings.Repeat("a", 64), controlRoot} {
		if strings.Contains(string(sealed), secret) {
			t.Fatalf("custody file leaks %q at rest", secret)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat custody file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("custody file mode = %v, want 0600", info.Mode().Perm())
	}

	record, err := custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(record.Bundle) != string(bundle) {
		t.Fatalf("Load() bundle = %s", record.Bundle)
	}
	if record.ControlRoot != controlRoot {
		t.Fatalf("Load() control root = %s", record.ControlRoot)
	}
	if !custody.Writable() || !strings.Contains(custody.Source(), path) {
		t.Fatalf("custody metadata = %q writable=%v", custody.Source(), custody.Writable())
	}
}

func TestSealedConcordCustodyAcceptsBareSealedBundle(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	path := filepath.Join(t.TempDir(), "custody.sealed")
	bare := `{"community_id":"cord","channels":[]}`
	writeSealedConcordCustodyFile(t, path, staff, bare)
	custody, err := newSealedConcordCustody(path, staff)
	if err != nil {
		t.Fatalf("newSealedConcordCustody() error = %v", err)
	}

	record, err := custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(record.Bundle) != bare || record.ControlRoot != "" {
		t.Fatalf("Load() record = %s / %q", record.Bundle, record.ControlRoot)
	}
}

func TestSealedConcordCustodyFailsClosedWhenSignetCannotUnseal(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t), decryptErr: errors.New("bunker denied nip44_decrypt")}
	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, fakeConcordSigner{fakeSigner: staff.fakeSigner}, `{"community_id":"cord"}`)
	custody, err := newSealedConcordCustody(path, staff)
	if err != nil {
		t.Fatalf("newSealedConcordCustody() error = %v", err)
	}

	if _, err := custody.Load(t.Context()); err == nil || !strings.Contains(err.Error(), "bunker denied nip44_decrypt") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestSealedConcordCustodyStoreVerifiesBeforeReplacingMaterial(t *testing.T) {
	sealer := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, sealer, `{"community_id":"cord"}`)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read custody file: %v", err)
	}

	blind := fakeConcordSigner{fakeSigner: sealer.fakeSigner, decryptErr: errors.New("bunker denied nip44_decrypt")}
	custody, err := newSealedConcordCustody(path, blind)
	if err != nil {
		t.Fatalf("newSealedConcordCustody() error = %v", err)
	}
	err = custody.Store(t.Context(), concordCustodyRecord{Bundle: json.RawMessage(`{"community_id":"rotated"}`)})
	if err == nil || !strings.Contains(err.Error(), "verify sealed custody") {
		t.Fatalf("Store() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read custody file: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("Store() replaced custody with material Signet could not reopen")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read custody dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("custody dir entries = %d, want only the custody file", len(entries))
	}
}

func TestSealedConcordCustodyRejectsUnsupportedDocumentVersion(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	path := filepath.Join(t.TempDir(), "custody.sealed")
	writeSealedConcordCustodyFile(t, path, staff, `{"version":99,"invite_bundle":{"community_id":"cord"}}`)
	custody, err := newSealedConcordCustody(path, staff)
	if err != nil {
		t.Fatalf("newSealedConcordCustody() error = %v", err)
	}

	if _, err := custody.Load(t.Context()); err == nil || !strings.Contains(err.Error(), "version 99 is unsupported") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestSealedConcordCustodyRejectsMalformedDocuments(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	for name, payload := range map[string]string{
		"not json":            "definitely-not-json",
		"empty bundle":        `{"version":1,"invite_bundle":null}`,
		"bad control root":    `{"version":1,"invite_bundle":{"a":1},"control_root":"nope"}`,
		"empty sealed string": `   `,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "custody.sealed")
			writeSealedConcordCustodyFile(t, path, staff, payload)
			custody, err := newSealedConcordCustody(path, staff)
			if err != nil {
				t.Fatalf("newSealedConcordCustody() error = %v", err)
			}
			if _, err := custody.Load(t.Context()); err == nil {
				t.Fatal("Load() accepted a malformed custody document")
			}
		})
	}
}

func TestNewSealedConcordCustodyRequiresAbsoluteExistingFile(t *testing.T) {
	staff := fakeConcordSigner{fakeSigner: newFakeSigner(t)}
	dir := t.TempDir()
	if _, err := newSealedConcordCustody("relative/custody.sealed", staff); err == nil {
		t.Fatal("newSealedConcordCustody() accepted a relative path")
	}
	if _, err := newSealedConcordCustody(filepath.Join(dir, "missing.sealed"), staff); err == nil {
		t.Fatal("newSealedConcordCustody() accepted a missing file")
	}
	empty := filepath.Join(dir, "empty.sealed")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty custody file: %v", err)
	}
	if _, err := newSealedConcordCustody(empty, staff); err == nil {
		t.Fatal("newSealedConcordCustody() accepted an empty file")
	}
	if _, err := newSealedConcordCustody(dir, staff); err == nil {
		t.Fatal("newSealedConcordCustody() accepted a directory")
	}
	sealed := filepath.Join(dir, "custody.sealed")
	writeSealedConcordCustodyFile(t, sealed, staff, `{"community_id":"cord"}`)
	if _, err := newSealedConcordCustody(sealed, nil); err == nil {
		t.Fatal("newSealedConcordCustody() accepted a missing signer")
	}
}

func TestStaticConcordCustodyIsReadOnly(t *testing.T) {
	custody := &staticConcordCustody{bundle: json.RawMessage(`{"community_id":"cord"}`)}
	if custody.Writable() {
		t.Fatal("operator-supplied custody must not be writable")
	}
	record, err := custody.Load(t.Context())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if string(record.Bundle) != `{"community_id":"cord"}` {
		t.Fatalf("Load() bundle = %s", record.Bundle)
	}
	err = custody.Store(t.Context(), record)
	if err == nil || !strings.Contains(err.Error(), "invite_bundle_sealed_file") {
		t.Fatalf("Store() error = %v", err)
	}
}

func writeSealedConcordCustodyFile(t *testing.T, path string, signer fakeConcordSigner, plaintext string) {
	t.Helper()
	staffPK, err := nostr.PubKeyFromHex(signer.pubkey)
	if err != nil {
		t.Fatalf("staff pubkey: %v", err)
	}
	sealed, err := signer.NIP44Encrypt(t.Context(), staffPK, plaintext)
	if err != nil {
		t.Fatalf("seal custody payload: %v", err)
	}
	if err := os.WriteFile(path, []byte(sealed+"\n"), 0o600); err != nil {
		t.Fatalf("write custody file: %v", err)
	}
}
