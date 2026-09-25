package redact

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestTextRedactsEncodedAndOverlappingSecrets(t *testing.T) {
	const secret = `secret +:/?@&%\"<>`
	encoded, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{
		secret, url.QueryEscape(secret), url.PathEscape(secret),
		strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:"),
		html.EscapeString(secret), string(encoded[1 : len(encoded)-1]),
		strings.Trim(strconv.Quote(secret), `"`),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
		lowerPercentEscapes(url.QueryEscape(secret)),
	} {
		if got := Text("failed: "+variant, "secret", secret); got != "failed: "+Value {
			t.Errorf("encoded/overlapping secret not fully redacted: %q", got)
		}
	}
	if got := Text("abc-secret abc", "abc", "abc-secret", "REDACTED"); got != Value+" "+Value {
		t.Errorf("overlapping values or inserted markers were corrupted: %q", got)
	}
	if got := Text("unchanged", ""); got != "unchanged" {
		t.Fatalf("empty secret changed text: %q", got)
	}
}

type credentialError struct{ secret string }

func (credentialError) Error() string { return "invalid endpoint" }

func TestErrorDoesNotRetainUnsafeCause(t *testing.T) {
	err := Error(credentialError{secret: "hidden-credential"}, "hidden-credential")
	if err.Error() != "invalid endpoint" || errors.Unwrap(err) != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%#v", err), "hidden-credential") {
		t.Fatal("Go-syntax formatting exposed an error field")
	}
	if Error(nil) != nil {
		t.Fatal("nil error changed")
	}
}
