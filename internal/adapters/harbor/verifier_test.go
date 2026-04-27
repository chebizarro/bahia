package harbor

import (
	"testing"
)

func TestParseImageRepo_Valid(t *testing.T) {
	tests := []struct {
		input       string
		wantProject string
		wantRepo    string
	}{
		{"myproject/myimage", "myproject", "myimage"},
		{"library/nginx", "library", "nginx"},
		{"project/sub/repo", "project", "sub/repo"},
	}

	for _, tt := range tests {
		project, repo, err := parseImageRepo(tt.input)
		if err != nil {
			t.Errorf("parseImageRepo(%q) returned error: %v", tt.input, err)
			continue
		}
		if project != tt.wantProject {
			t.Errorf("parseImageRepo(%q) project = %q, want %q", tt.input, project, tt.wantProject)
		}
		if repo != tt.wantRepo {
			t.Errorf("parseImageRepo(%q) repo = %q, want %q", tt.input, repo, tt.wantRepo)
		}
	}
}

func TestParseImageRepo_Invalid(t *testing.T) {
	invalid := []string{
		"",
		"noslash",
		"/leading",
		"trailing/",
	}

	for _, input := range invalid {
		_, _, err := parseImageRepo(input)
		if err == nil {
			t.Errorf("parseImageRepo(%q) expected error, got nil", input)
		}
	}
}
