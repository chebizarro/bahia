// Package redact provides safe diagnostic rendering of secret material.
package redact

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const Value = "[REDACTED]"

// Error returns an opaque error: unwrapping or Go-syntax formatting cannot
// recover an original error that holds credentials in non-message fields.
func Error(err error, secrets ...string) error {
	if err == nil {
		return nil
	}
	return errors.New(Text(err.Error(), secrets...))
}

// Text removes raw and commonly encoded secrets, longest first so overlapping
// credentials cannot leave partially exposed suffixes. Replacement is one pass:
// secret values never match and corrupt the redaction markers we insert.
func Text(message string, secrets ...string) string {
	variants := make(map[string]struct{})
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, variant := range secretRepresentations(secret) {
			if variant != "" {
				variants[variant] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(variants))
	for variant := range variants {
		ordered = append(ordered, variant)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) == len(ordered[j]) {
			return ordered[i] < ordered[j]
		}
		return len(ordered[i]) > len(ordered[j])
	})
	pairs := make([]string, 0, 2*len(ordered))
	for _, variant := range ordered {
		pairs = append(pairs, variant, Value)
	}
	return strings.NewReplacer(pairs...).Replace(message)
}

func secretRepresentations(secret string) []string {
	queryEscaped := url.QueryEscape(secret)
	pathEscaped := url.PathEscape(secret)
	userinfo := strings.TrimPrefix(url.UserPassword("_", secret).String(), "_:")
	variants := []string{
		secret,
		queryEscaped,
		pathEscaped,
		userinfo,
		html.EscapeString(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
	}
	if encoded, err := json.Marshal(secret); err == nil && len(encoded) >= 2 {
		variants = append(variants, string(encoded[1:len(encoded)-1]))
	}
	if quoted := strconv.Quote(secret); len(quoted) >= 2 {
		variants = append(variants, quoted[1:len(quoted)-1])
	}
	for _, encoded := range []string{queryEscaped, pathEscaped, userinfo} {
		variants = append(variants, lowerPercentEscapes(encoded))
	}
	return variants
}

func lowerPercentEscapes(value string) string {
	bytes := []byte(value)
	for i := 0; i+2 < len(bytes); i++ {
		if bytes[i] != '%' {
			continue
		}
		for j := i + 1; j <= i+2; j++ {
			if bytes[j] >= 'A' && bytes[j] <= 'F' {
				bytes[j] += 'a' - 'A'
			}
		}
		i += 2
	}
	return string(bytes)
}
