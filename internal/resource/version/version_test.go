package version

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource/download"

	"github.com/plombardi89/maisys2/internal/resource"
)

// server is a test HTTP server answering each path (with its query, if any)
// with a handler, and recording the requests.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	requests []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{handlers: map[string]http.HandlerFunc{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		s.mu.Lock()
		s.requests = append(s.requests, key)
		h := s.handlers[key]
		s.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) file(path, body string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
}

func (s *server) handle(path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[path] = h
}

func (s *server) redirect(path, to string) {
	s.handle(path, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) })
}

// got returns the requests so far.
func (s *server) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func newClient(s *server) *download.Client {
	return &download.Client{
		HTTP:     s.Client(),
		Attempts: 1,
		Sleep:    func(context.Context, time.Duration) error { return nil },
	}
}

// latest runs r against s, comparing the result with want, or the error
// with wantErr.
func latest(t *testing.T, s *server, r Resolver, want, wantErr string) {
	t.Helper()
	got, err := r.Latest(t.Context(), newClient(s))
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Latest() = %q, %v, want an error containing %q", got, err, wantErr)
		}
		return
	}
	if err != nil || got != want {
		t.Errorf("Latest() = %q, %v, want %q", got, err, want)
	}
}

func TestGo(t *testing.T) {
	tests := []struct {
		name, index, want, wantErr string
	}{
		{
			name:  "first stable",
			index: `[{"version": "go1.28rc1", "stable": false}, {"version": "go1.27.3", "stable": true}, {"version": "go1.26.10", "stable": true}]`,
			want:  "1.27.3",
		},
		{name: "no stable release", index: `[{"version": "go1.28rc1", "stable": false}]`, wantErr: "the index lists no stable release"},
		{name: "not JSON", index: `<html>`, wantErr: "reading the index"},
		{name: "odd version", index: `[{"version": "go1.27/../x", "stable": true}]`, wantErr: `"1.27/../x" isn't a version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.file("/dl/?mode=json", tt.index)
			g := Go{URL: s.URL + "/dl/?mode=json"}
			latest(t, s, g, tt.want, tt.wantErr)
		})
	}
	if got := (Go{}).Upstream(); got != "https://go.dev/dl/?mode=json" {
		t.Errorf("Go{}.Upstream() = %q", got)
	}
}

func TestText(t *testing.T) {
	tests := []struct {
		name, body, want, wantErr string
	}{
		{name: "kubernetes", body: "v1.37.2\n", want: "1.37.2"},
		{name: "no v", body: "1.37.2", want: "1.37.2"},
		{name: "not a version", body: "<html>\n", wantErr: `"<html>" isn't a version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.file("/release/stable.txt", tt.body)
			latest(t, s, Text{URL: s.URL + "/release/stable.txt"}, tt.want, tt.wantErr)
		})
	}
}

func TestGitHub(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		location  string // "" means no repo
		want      string
		wantErr   string
		redirects bool
	}{
		{name: "v tag", prefix: "v", location: "/stern/stern/releases/tag/v1.35.0", want: "1.35.0"},
		{name: "plain tag", location: "/stern/stern/releases/tag/0.13.7", want: "0.13.7"},
		{name: "absolute location", prefix: "v", location: "https://github.com/stern/stern/releases/tag/v1.35.0", want: "1.35.0"},
		{name: "escaped tag", prefix: "release-", location: "/stern/stern/releases/tag/release%2D1.2", want: "1.2"},
		{name: "tag without the prefix", prefix: "v", location: "/stern/stern/releases/tag/1.35.0", wantErr: `the latest release's tag, 1.35.0, doesn't start with "v"`},
		{name: "odd tag", prefix: "v", location: "/stern/stern/releases/tag/vnext", wantErr: `"next" isn't a version`},
		{name: "no releases", prefix: "v", location: "/stern/stern/releases", wantErr: "stern/stern has no published releases"},
		{name: "no repo", prefix: "v", wantErr: "there's no public GitHub repo stern/stern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			if tt.location != "" {
				s.redirect("/stern/stern/releases/latest", tt.location)
			}
			g := GitHub{Repo: "stern/stern", TagPrefix: tt.prefix, URL: s.URL}
			latest(t, s, g, tt.want, tt.wantErr)
			if got := s.got(); len(got) != 1 || got[0] != "/stern/stern/releases/latest" {
				t.Errorf("requests = %q, want just the latest release's page", got)
			}
		})
	}
	if got := (GitHub{Repo: "stern/stern"}).Upstream(); got != "https://github.com/stern/stern/releases/latest" {
		t.Errorf("Upstream() = %q", got)
	}
}

func TestJetBrains(t *testing.T) {
	tests := []struct {
		name, body, want, wantErr string
	}{
		{
			name: "latest",
			body: `{"IIU": [{"date": "2026-11-05", "type": "release", "version": "2026.3", "build": "263.7307.200",
				"downloads": {"linux": {"link": "https://download.jetbrains.com/idea/idea-2026.3.tar.gz"}}}]}`,
			want: "2026.3",
		},
		{name: "no releases", body: `{"IIU": []}`, wantErr: "JetBrains lists no release of IIU"},
		{name: "not JSON", body: `[`, wantErr: "reading JetBrains' releases"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.file("/products/releases?code=IIU&latest=true&type=release", tt.body)
			latest(t, s, JetBrains{Code: "IIU", URL: s.URL + "/products/releases"}, tt.want, tt.wantErr)
		})
	}
	if got, want := (JetBrains{Code: "IIU"}).Upstream(), "https://data.services.jetbrains.com/products/releases?code=IIU&latest=true&type=release"; got != want {
		t.Errorf("Upstream() = %q, want %q", got, want)
	}
}

func TestCache(t *testing.T) {
	s := newServer(t)
	s.file("/stable.txt", "v1.37.2\n")
	path := filepath.Join(t.TempDir(), "latest.json")
	now := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	r := Text{URL: s.URL + "/stable.txt"}
	client := newClient(s)

	resolve := func(c *Cache) {
		t.Helper()
		if v, err := c.Resolve(t.Context(), client, "kubectl", r); err != nil || v != "1.37.2" {
			t.Fatalf("Resolve() = %q, %v, want 1.37.2", v, err)
		}
	}

	// A dry run, then a sync a few minutes later, use the same version.
	resolve(&Cache{Path: path, Now: clock})
	now = now.Add(59 * time.Minute)
	resolve(&Cache{Path: path, Now: clock})
	if n := len(s.got()); n != 1 {
		t.Errorf("two syncs within the hour made %d requests, want 1", n)
	}

	var f struct {
		FormatVersion int `json:"format_version"`
		Versions      map[string]struct {
			Version  string    `json:"version"`
			Resolved time.Time `json:"resolved"`
		} `json:"versions"`
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &f); err != nil || f.FormatVersion != 1 || f.Versions[r.Upstream()].Version != "1.37.2" {
		t.Errorf("latest.json = %s, want format_version 1 and the version keyed by its upstream", data)
	}

	// After an hour, it's looked up again.
	now = now.Add(2 * time.Minute)
	c := &Cache{Path: path, Now: clock}
	resolve(c)
	if n := len(s.got()); n != 2 {
		t.Errorf("a sync after an hour made %d requests in all, want 2", n)
	}
	// One Cache asks once.
	resolve(c)
	if n := len(s.got()); n != 2 {
		t.Errorf("resolving again made %d requests in all, want 2", n)
	}

	// A clock that went back doesn't keep a version for longer.
	now = now.Add(-2 * time.Hour)
	resolve(&Cache{Path: path, Now: clock})
	if n := len(s.got()); n != 3 {
		t.Errorf("after the clock went back, %d requests in all, want 3", n)
	}

	// A nil Cache always asks.
	var nilCache *Cache
	resolve(nilCache)
	if n := len(s.got()); n != 4 {
		t.Errorf("a nil Cache made %d requests in all, want 4", n)
	}
}

func TestCacheLookupError(t *testing.T) {
	s := newServer(t)
	s.handle("/stable.txt", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	path := filepath.Join(t.TempDir(), "latest.json")
	r := Text{URL: s.URL + "/stable.txt"}
	for _, c := range []*Cache{{Path: path}, nil} {
		_, err := c.Resolve(t.Context(), newClient(s), "kubectl", r)
		le, ok := errors.AsType[*LookupError](err)
		if !ok || le.Tool != "kubectl" || le.Upstream != r.URL {
			t.Fatalf("Resolve() error = %v, want a *LookupError", err)
		}
		want := "can't find the latest version of kubectl from " + r.URL + ": 503 Service Unavailable (down) (pin a version to stop depending on it)"
		if err.Error() != want {
			t.Errorf("Resolve() error = %q, want %q", err, want)
		}
		if he, ok := errors.AsType[*download.HTTPError](err); !ok || he.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("the *LookupError doesn't wrap the HTTP error")
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed lookup wrote latest.json")
	}
}

func TestCacheFiles(t *testing.T) {
	tests := []struct {
		name, content, wantErr string
		cached                 bool
	}{
		{name: "fresh", content: `{"format_version": 1, "versions": {"U": {"version": "1.0.0", "resolved": "2026-11-06T09:00:00Z"}}}`, cached: true},
		{name: "stale", content: `{"format_version": 1, "versions": {"U": {"version": "1.0.0", "resolved": "2026-11-06T08:00:00Z"}}}`},
		{name: "invalid version", content: `{"format_version": 1, "versions": {"U": {"version": "../x", "resolved": "2026-11-06T09:00:00Z"}}}`},
		{name: "broken", content: `{"format_version": 1, "versions": {`},
		{name: "newer", content: `{"format_version": 2, "versions": {}}`, wantErr: "was written by a newer version of maisys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "latest.json")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			c := &Cache{Path: path, Now: func() time.Time { return time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC) }}
			if err := c.Load(); tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Load() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			v, ok, err := c.fresh("U")
			if err != nil || ok != tt.cached || ok && v != "1.0.0" {
				t.Errorf("fresh() = %q, %t, %v, want cached %t", v, ok, err, tt.cached)
			}
		})
	}
}

func TestLookupErrorStopsTheSync(t *testing.T) {
	err := fmt.Errorf("checking kubectl: %w", &LookupError{Tool: "kubectl", Upstream: "dl.k8s.io", Err: errors.New("timeout")})
	if !resource.StopsSync(err) {
		t.Error("a failed lookup doesn't stop the sync, but issue #1 says it does")
	}
}
