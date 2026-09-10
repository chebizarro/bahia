package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
)

// contextVMRequestFingerprint hashes a canonical representation of JSON-RPC
// params. Object order, whitespace, string escape choices, and equivalent
// decimal spellings do not affect the digest. Numbers are normalized exactly
// rather than through float64 so distinct large integers cannot collide.
func contextVMRequestFingerprint(params json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("decode ContextVM request params: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", err
	}
	var canonical bytes.Buffer
	if err := writeCanonicalJSON(&canonical, value); err != nil {
		return "", fmt.Errorf("canonicalize ContextVM request params: %w", err)
	}
	digest := sha256.Sum256(canonical.Bytes())
	return fmt.Sprintf("%x", digest), nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode ContextVM request params: multiple JSON values")
		}
		return fmt.Errorf("decode ContextVM request params: %w", err)
	}
	return nil
}

func writeCanonicalJSON(dst *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		dst.WriteString("null")
	case bool:
		if typed {
			dst.WriteString("true")
		} else {
			dst.WriteString("false")
		}
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		dst.Write(encoded)
	case json.Number:
		number, err := canonicalJSONNumber(string(typed))
		if err != nil {
			return err
		}
		dst.WriteString(number)
	case []any:
		dst.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				dst.WriteByte(',')
			}
			if err := writeCanonicalJSON(dst, item); err != nil {
				return err
			}
		}
		dst.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dst.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				dst.WriteByte(',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return err
			}
			dst.Write(encodedKey)
			dst.WriteByte(':')
			if err := writeCanonicalJSON(dst, typed[key]); err != nil {
				return err
			}
		}
		dst.WriteByte('}')
	default:
		return fmt.Errorf("unsupported JSON value %T", value)
	}
	return nil
}

// canonicalJSONNumber converts a valid JSON number into an exact coefficient
// and base-10 exponent. For example, 1, 1.0, and 10e-1 all become 1e0.
func canonicalJSONNumber(value string) (string, error) {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = value[1:]
	}
	mantissa := value
	exponentText := "0"
	if exponentAt := strings.IndexAny(value, "eE"); exponentAt >= 0 {
		mantissa = value[:exponentAt]
		exponentText = value[exponentAt+1:]
	}
	parts := strings.SplitN(mantissa, ".", 2)
	digits := parts[0]
	fractionDigits := 0
	if len(parts) == 2 {
		digits += parts[1]
		fractionDigits = len(parts[1])
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	exponent, ok := new(big.Int).SetString(exponentText, 10)
	if !ok {
		return "", fmt.Errorf("invalid JSON number %q", value)
	}
	exponent.Sub(exponent, big.NewInt(int64(fractionDigits)))
	trimmedDigits := strings.TrimRight(digits, "0")
	exponent.Add(exponent, big.NewInt(int64(len(digits)-len(trimmedDigits))))
	if negative {
		trimmedDigits = "-" + trimmedDigits
	}
	return trimmedDigits + "e" + exponent.String(), nil
}
