package domain

import (
	"bytes"
	"fmt"
	"regexp"
	"regexp/syntax"
	"unicode/utf8"
)

const (
	// MaxRouteCanaryBodyRegexLength bounds the source length of a body regex.
	//
	// Go's RE2 engine matches in time linear in the input, so there is no
	// catastrophic backtracking to defend against. The bound exists so a pattern
	// stays reviewable in repo configuration and cannot be used to smuggle an
	// arbitrarily large program into every probe.
	MaxRouteCanaryBodyRegexLength = 256
	// MaxRouteCanaryBodyRegexInstructions bounds the compiled program size.
	//
	// Source length alone does not bound cost: the 21-byte pattern
	// `.{1000}.{1000}.{1000}` expands to over 3000 instructions. RE2 match time is
	// proportional to body length times program size, and the body is already
	// bounded, so bounding the program bounds the work of every assertion.
	MaxRouteCanaryBodyRegexInstructions = 2048
)

// CompileRouteCanaryBodyRegex validates and compiles a body assertion pattern.
//
// Anchoring semantics are fixed, not left to the pattern author: the pattern
// must match the *entire* bounded response body, as if written \A(?:pattern)\z.
// This is the same whole-value contract Prometheus relabel regexes use, and it
// means a pattern can never pass by matching an incidental fragment. To assert
// on part of a body, say so explicitly, for example
//
//	(?s).*"status"\s*:\s*"ok".*
//
// where (?s) lets . cross newlines in a pretty-printed JSON document. Because
// the body is bounded, the pattern sees at most the prefix of the response the
// probe reads; a marker beyond that bound is invisible to every assertion.
//
// The anchors are \A and \z rather than ^ and $ so a (?m) flag inside the
// pattern cannot weaken them into line anchors. The pattern is parsed on its own
// before it is wrapped, so an unbalanced group such as `a)|(b` is rejected
// instead of escaping the anchoring group.
//
// A pattern that matches an empty body is rejected: it asserts nothing, and a
// vacuous assertion would also suppress the catch-all warning, which is exactly
// the false confidence route canaries exist to prevent.
func CompileRouteCanaryBodyRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, fmt.Errorf("expected_body_regex is empty")
	}
	if len(pattern) > MaxRouteCanaryBodyRegexLength {
		return nil, fmt.Errorf("expected_body_regex is %d bytes, exceeding the %d byte limit",
			len(pattern), MaxRouteCanaryBodyRegexLength)
	}
	if !utf8.ValidString(pattern) {
		return nil, fmt.Errorf("expected_body_regex is not valid UTF-8")
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, fmt.Errorf("expected_body_regex is not a valid RE2 pattern: %w", err)
	}
	program, err := syntax.Compile(parsed.Simplify())
	if err != nil {
		return nil, fmt.Errorf("expected_body_regex cannot be compiled: %w", err)
	}
	if len(program.Inst) > MaxRouteCanaryBodyRegexInstructions {
		return nil, fmt.Errorf("expected_body_regex compiles to %d instructions, exceeding the %d instruction limit",
			len(program.Inst), MaxRouteCanaryBodyRegexInstructions)
	}
	anchored, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
	if err != nil {
		// A pattern that parses alone but not inside the anchoring group, such as
		// an unterminated \Q quote, would otherwise change meaning when wrapped.
		return nil, fmt.Errorf("expected_body_regex cannot be anchored as a single expression: %w", err)
	}
	if anchored.MatchString("") {
		return nil, fmt.Errorf("expected_body_regex matches an empty body, so it asserts nothing")
	}
	return anchored, nil
}

// HasBodyAssertion reports whether the target asserts anything about the
// response body, in either substring or regex form.
func (t RouteCanaryTarget) HasBodyAssertion() bool {
	return t.ExpectedBodyContains != "" || t.ExpectedBodyRegex != ""
}

// MatchBody evaluates every configured body assertion against a bounded body.
//
// Callers must pass the raw bounded body, never the sanitized evidence copy, so
// that redaction can never change whether an assertion passes. When both forms
// are configured both must hold. A target with no body assertion matches.
//
// A regex that fails to compile fails the assertion rather than passing it.
// Validate rejects such a target before it is probed, so this only guards
// against a caller that skipped validation.
func (t RouteCanaryTarget) MatchBody(body []byte) bool {
	if t.ExpectedBodyContains != "" && !bytes.Contains(body, []byte(t.ExpectedBodyContains)) {
		return false
	}
	if t.ExpectedBodyRegex != "" {
		pattern, err := CompileRouteCanaryBodyRegex(t.ExpectedBodyRegex)
		if err != nil || !pattern.Match(body) {
			return false
		}
	}
	return true
}

// describeBodyMismatch names the body assertions a target carries, for the
// operator-facing mismatch reason. It never echoes the expected values.
func (t RouteCanaryTarget) describeBodyMismatch() string {
	switch {
	case t.ExpectedBodyContains != "" && t.ExpectedBodyRegex != "":
		return "response body did not satisfy both expected_body_contains and the anchored expected_body_regex"
	case t.ExpectedBodyRegex != "":
		return "response body did not match the anchored expected_body_regex"
	default:
		return "response body did not contain the expected marker"
	}
}
