package download

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetch(t *testing.T) {
	data := []byte("the tool's archive")
	sum := sha(data)
	s := newServer(t)
	s.file("/tool.tar.gz", data)
	c := newClient(t, s, nil)
	a := Asset{URL: s.URL + "/tool.tar.gz", Name: "tool.tar.gz"}

	path, err := c.Fetch(t.Context(), a, strings.ToUpper(sum))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(c.Dir, sum[:16]+"-tool.tar.gz"); path != want {
		t.Errorf("Fetch() = %s, want %s", path, want)
	}
	if got, _ := os.ReadFile(path); string(got) != string(data) {
		t.Errorf("the downloaded file holds %q, want %q", got, data)
	}

	// The cache has it now.
	if again, err := c.Fetch(t.Context(), a, sum); err != nil || again != path {
		t.Errorf("Fetch() again = %s, %v, want %s", again, err, path)
	}
	if n := s.count("/tool.tar.gz"); n != 1 {
		t.Errorf("Fetch() twice made %d requests, want 1", n)
	}

	// A cached file that no longer matches is downloaded again.
	if err := os.WriteFile(path, []byte("corrupted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(t.Context(), a, sum); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(data) {
		t.Errorf("after a corrupted cache, the file holds %q, want %q", got, data)
	}
	if n := s.count("/tool.tar.gz"); n != 2 {
		t.Errorf("Fetch() after a corrupted cache made %d requests in all, want 2", n)
	}

	if err := c.Forget(a, sum); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Forget() left %s", path)
	}
	if err := c.Forget(a, sum); err != nil {
		t.Errorf("Forget() of a file that's gone: %v", err)
	}
}

// TestFetchStaleParts checks that a download deletes the unfinished files
// earlier downloads of the same file left behind, such as when maisys was
// killed, but not those of a download that may still be running, or of
// other files.
func TestFetchStaleParts(t *testing.T) {
	data := []byte("the tool's archive")
	sum := sha(data)
	s := newServer(t)
	s.file("/tool.tar.gz", data)
	c := newClient(t, s, nil)
	base := sum[:16] + "-tool.tar.gz"
	old := time.Now().Add(-48 * time.Hour)
	files := []struct {
		name string
		old  bool
		kept bool
	}{
		{name: "." + base + ".1234567.part", old: true},
		{name: "." + base + ".7654321.part", kept: true},
		{name: "." + sha([]byte("other"))[:16] + "-tool.tar.gz.1234567.part", old: true, kept: true},
		{name: "." + base + ".tmp", old: true, kept: true},
		{name: "." + base + ".part", old: true, kept: true},
	}
	for _, f := range files {
		path := filepath.Join(c.Dir, f.name)
		if err := os.WriteFile(path, []byte("half"), 0o644); err != nil {
			t.Fatal(err)
		}
		if f.old {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := c.Fetch(t.Context(), Asset{URL: s.URL + "/tool.tar.gz", Name: "tool.tar.gz"}, sum); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		_, err := os.Stat(filepath.Join(c.Dir, f.name))
		if kept := err == nil; kept != f.kept {
			t.Errorf("after Fetch(), %s is there: %t, want %t", f.name, kept, f.kept)
		}
	}
	if entries, _ := os.ReadDir(c.Dir); len(entries) != len(files) {
		t.Errorf("the download cache holds %d files, want the download and the %d kept", len(entries), len(files)-1)
	}
}

func TestFetchChecksumMismatch(t *testing.T) {
	s := newServer(t)
	s.file("/tool.tar.gz", []byte("tampered"))
	c := newClient(t, s, nil)
	want := sha([]byte("the real thing"))

	_, err := c.Fetch(t.Context(), Asset{URL: s.URL + "/tool.tar.gz", Name: "tool.tar.gz"}, want)
	ce, ok := errors.AsType[*ChecksumError](err)
	if !ok || ce.Want != want || ce.Got != sha([]byte("tampered")) {
		t.Fatalf("Fetch() error = %v, want a *ChecksumError", err)
	}
	if !strings.Contains(err.Error(), "checksum mismatch for tool.tar.gz: expected sha256 "+want) {
		t.Errorf("Fetch() error = %q", err)
	}
	if n := s.count("/tool.tar.gz"); n != 1 {
		t.Errorf("Fetch() made %d requests, want 1: a mismatch isn't tried again", n)
	}
	if entries, _ := os.ReadDir(c.Dir); len(entries) != 0 {
		t.Errorf("the download cache holds %v after a mismatch, want nothing", entries)
	}
}

func TestFetchRetries(t *testing.T) {
	data := []byte("the tool's archive")
	s := newServer(t)
	s.handle("/tool.tar.gz", failing(2, http.StatusServiceUnavailable, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	var w waits
	c := newClient(t, s, &w)
	c.Backoff = time.Second

	if _, err := c.Fetch(t.Context(), Asset{URL: s.URL + "/tool.tar.gz", Name: "tool.tar.gz"}, sha(data)); err != nil {
		t.Fatal(err)
	}
	if got := w.get(); len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Errorf("Fetch() waited %v, want [1s 2s]", got)
	}

	s.handle("/missing.tar.gz", failing(100, http.StatusInternalServerError, nil))
	_, err := c.Fetch(t.Context(), Asset{URL: s.URL + "/missing.tar.gz", Name: "missing.tar.gz"}, sha(data))
	if err == nil || !strings.Contains(err.Error(), "downloading missing.tar.gz: ") || !strings.Contains(err.Error(), "500 Internal Server Error") {
		t.Errorf("Fetch() error = %v, want a 500 error", err)
	}
	if n := s.count("/missing.tar.gz"); n != DefaultAttempts {
		t.Errorf("Fetch() made %d requests, want %d", n, DefaultAttempts)
	}
}

func TestFetchInvalid(t *testing.T) {
	sum := sha([]byte("x"))
	tests := []struct {
		name    string
		c       Client
		a       Asset
		sum     string
		wantErr string
	}{
		{"no cache folder", Client{}, Asset{Name: "x"}, sum, "no download cache folder"},
		{"bad name", Client{Dir: "cache"}, Asset{Name: "../x"}, sum, `"../x" isn't a valid file name`},
		{"empty name", Client{Dir: "cache"}, Asset{}, sum, `"" isn't a valid file name`},
		{"bad sum", Client{Dir: "cache"}, Asset{Name: "x"}, "abc", `"abc" isn't a sha256 checksum`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.c.Fetch(t.Context(), tt.a, tt.sum)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Fetch() error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
