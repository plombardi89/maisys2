package wslhost_test

import (
	"testing"

	"github.com/plombardi89/maisys2/internal/modules/wslhost"
)

func TestHTTPSURL(t *testing.T) {
	tests := []struct {
		url, want string
	}{
		{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git"},
		{"git@gitlab.com:/group/sub/repo.git", "https://gitlab.com/group/sub/repo.git"},
		{"ssh://git@github.com/owner/repo.git", "https://github.com/owner/repo.git"},
		{"ssh://git@example.com:2222/owner/repo", "https://example.com/owner/repo"},
		{"git+ssh://example.com/repo.git", "https://example.com/repo.git"},
		{"ssh://example.com", ""},
		{"ssh:///repo.git", ""},
		{"git@github.com:", ""},
		{"https://github.com/owner/repo.git", ""},
		{"/srv/git/config.git", ""},
		{`C:\config.git`, ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := wslhost.HTTPSURL(tt.url); got != tt.want {
			t.Errorf("HTTPSURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestValidUser(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"phil", true},
		{"_svc", true},
		{"dev-user_2", true},
		{"a2345678901234567890123456789012", true},
		{"a23456789012345678901234567890123", false},
		{"root", false},
		{"", false},
		{"Phil", false},
		{"phil lombardi", false},
		{"2phil", false},
		{"-phil", false},
	}
	for _, tt := range tests {
		if got := wslhost.ValidUser(tt.name); got != tt.valid {
			t.Errorf("ValidUser(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}
