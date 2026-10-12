package main

import (
	"io"
	"strings"
	"testing"
)

// The reference form never carries a value, and a malformed reference is
// refused before anything is signed or published.
func TestSecretsSetCredentialReferenceFlags(t *testing.T) {
	valid := "cred_" + strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"a value argument":   {[]string{"set", "svc", "DB", "hunter2", "--credential-id", valid, "--credential-version", "1"}, "accepts 2 arg(s)"},
		"a value file":       {[]string{"set", "svc", "DB", "--value-file", "/tmp/x", "--credential-id", valid, "--credential-version", "1"}, "unknown flag: --value-file"},
		"missing version":    {[]string{"set", "svc", "DB", "--credential-id", valid}, `required flag(s) "credential-version" not set`},
		"version without id": {[]string{"set", "svc", "DB", "--credential-version", "2"}, `required flag(s) "credential-id" not set`},
		"zero version":       {[]string{"set", "svc", "DB", "--credential-id", valid, "--credential-version", "0"}, "version must be >= 1"},
		"malformed id":       {[]string{"set", "svc", "DB", "--credential-id", "not a token", "--credential-version", "1"}, "credential_id must be"},
		"nothing to set":     {[]string{"set", "svc", "DB"}, "required flag(s)"},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := secretsCommands()
			cmd.SetArgs(tc.args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
