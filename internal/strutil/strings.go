// Package strutil provides shared string validation and comparison helpers.
package strutil

import (
	"os"
	"strings"
)

// LevenshteinDistance returns the rune-wise edit distance between a and b.
func LevenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	previous := make([]int, len(rb)+1)
	current := make([]int, len(rb)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		current[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		copy(previous, current)
	}
	return previous[len(rb)]
}

// ValidEnvironmentKey reports whether key is a portable environment variable name.
func ValidEnvironmentKey(key string) bool {
	for i := 0; i < len(key); i++ {
		char := key[i]
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || i > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return key != ""
}

// Env returns the trimmed value of an environment variable,
// or fallback when it is unset or empty.
func Env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// SplitCSV splits a value on commas and returns non-empty trimmed elements.
func SplitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
