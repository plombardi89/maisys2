package buildinfo

import "testing"

func TestString(t *testing.T) {
	tests := []struct {
		name                  string
		version, commit, date string
		want                  string
	}{
		{"release", "0.1.0", "1a2b3c4", "2026-11-06T05:31:35Z", "0.1.0 (commit 1a2b3c4, built 2026-11-06T05:31:35Z)"},
		{"commit only", "0.1.0", "1a2b3c4", "", "0.1.0 (commit 1a2b3c4)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBuildInfo(t, tt.version, tt.commit, tt.date)
			if got := String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsDev(t *testing.T) {
	setBuildInfo(t, "dev", "", "")
	if !IsDev() {
		t.Error("IsDev() = false for version \"dev\"")
	}
	setBuildInfo(t, "0.1.0", "", "")
	if IsDev() {
		t.Error("IsDev() = true for version \"0.1.0\"")
	}
}

func setBuildInfo(t *testing.T, v, c, d string) {
	t.Helper()
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() { version, commit, date = oldVersion, oldCommit, oldDate })
	version, commit, date = v, c, d
}
