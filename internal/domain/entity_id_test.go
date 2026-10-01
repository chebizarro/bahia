package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewEntityIDMintsCanonicalUUIDv7(t *testing.T) {
	id := NewEntityID()
	if id.Version() != 7 {
		t.Fatalf("NewEntityID version = %d, want 7", id.Version())
	}
	if parsed, err := ParseClientEntityID(id.String()); err != nil || parsed != id {
		t.Fatalf("minted id %s does not validate as a client id: %v", id, err)
	}
}

func TestParseClientEntityIDAcceptsV7AndV4(t *testing.T) {
	for _, raw := range []string{NewEntityID().String(), uuid.New().String()} {
		id, err := ParseClientEntityID(raw)
		if err != nil {
			t.Fatalf("ParseClientEntityID(%q) error: %v", raw, err)
		}
		if id.String() != raw {
			t.Fatalf("ParseClientEntityID(%q) = %s, want identical canonical form", raw, id)
		}
	}
}

func TestParseClientEntityIDRejectsNonCanonicalAndPredictableIDs(t *testing.T) {
	v7 := NewEntityID().String()
	cases := map[string]string{
		"empty":     "",
		"uppercase": strings.ToUpper(v7),
		"braces":    "{" + v7 + "}",
		"urn":       "urn:uuid:" + v7,
		"no dashes": strings.ReplaceAll(v7, "-", ""),
		"nil":       uuid.Nil.String(),
		"max":       uuid.Max.String(),
		"v5":        uuid.NewSHA1(uuid.NameSpaceURL, []byte("service:acme:api")).String(),
		"v1":        "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"slug":      "service:acme:api",
	}
	for name, raw := range cases {
		if _, err := ParseClientEntityID(raw); !errors.Is(err, ErrInvalidEntityID) {
			t.Errorf("%s: ParseClientEntityID(%q) error = %v, want ErrInvalidEntityID", name, raw, err)
		}
	}
}

func TestResolveCreateEntityIDMintsWhenAbsent(t *testing.T) {
	id, supplied, err := ResolveCreateEntityID("  ")
	if err != nil || supplied || id.Version() != 7 {
		t.Fatalf("ResolveCreateEntityID(blank) = %s supplied=%t err=%v, want minted v7", id, supplied, err)
	}
	client := NewEntityID()
	id, supplied, err = ResolveCreateEntityID(client.String())
	if err != nil || !supplied || id != client {
		t.Fatalf("ResolveCreateEntityID(client) = %s supplied=%t err=%v, want %s", id, supplied, err, client)
	}
	if _, _, err := ResolveCreateEntityID("not-a-uuid"); !errors.Is(err, ErrInvalidEntityID) {
		t.Fatalf("ResolveCreateEntityID(invalid) error = %v, want ErrInvalidEntityID", err)
	}
}

func TestFormatEntityCoordinateIsInputAgnostic(t *testing.T) {
	legacy := uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301") // Postgres gen_random_uuid (v4)
	client := NewEntityID()
	for _, tc := range []struct {
		prefix string
		id     uuid.UUID
		want   string
	}{
		{"", legacy, "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{"vm", legacy, "vm:3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		{"", client, client.String()},
		{"runtime-release", client, "runtime-release:" + client.String()},
	} {
		if d := FormatEntityCoordinate(tc.prefix, tc.id); d != tc.want {
			t.Fatalf("FormatEntityCoordinate(%q, %s) = %q, want %q", tc.prefix, tc.id, d, tc.want)
		}
	}
}

func TestEntityIDConflictErrorIsSentinel(t *testing.T) {
	id := NewEntityID()
	err := error(&EntityIDConflictError{Entity: "service", ID: id})
	if !errors.Is(err, ErrEntityIDConflict) {
		t.Fatal("EntityIDConflictError does not unwrap to ErrEntityIDConflict")
	}
	if !strings.Contains(err.Error(), id.String()) || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("conflict message is not actionable: %q", err)
	}
}
