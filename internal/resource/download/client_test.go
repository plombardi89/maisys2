package download

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGet(t *testing.T) {
	s := newServer(t)
	s.file("/ok", []byte("hello"))
	var w waits
	c := newClient(t, s, &w)

	got, err := c.Get(t.Context(), s.URL+"/ok")
	if err != nil || string(got) != "hello" {
		t.Fatalf("Get() = %q, %v, want hello", got, err)
	}
	if !slices.Equal(s.agents, []string{"maisys/test"}) {
		t.Errorf("User-Agent = %q, want maisys/test", s.agents)
	}
}

func TestGetRetries(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		attempts  int
		wantErr   string
		wantWaits []time.Duration
	}{
		{
			name:      "server error, then success",
			handler:   failing(2, http.StatusServiceUnavailable, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
			attempts:  3,
			wantWaits: []time.Duration{2 * time.Second, 4 * time.Second},
		},
		{
			name:      "too many requests, then success",
			handler:   failing(1, http.StatusTooManyRequests, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }),
			attempts:  2,
			wantWaits: []time.Duration{2 * time.Second},
		},
		{
			name:      "server error every time",
			handler:   failing(100, http.StatusBadGateway, nil),
			attempts:  DefaultAttempts,
			wantErr:   "502 Bad Gateway",
			wantWaits: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second},
		},
		{
			name:     "not found isn't tried again",
			handler:  http.NotFound,
			attempts: 1,
			wantErr:  "404 Not Found (404 page not found)",
		},
		{
			name:     "forbidden isn't tried again",
			handler:  failing(100, http.StatusForbidden, nil),
			attempts: 1,
			wantErr:  "403 Forbidden",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(t)
			s.handle("/file", tt.handler)
			var w waits
			c := newClient(t, s, &w)
			got, err := c.Get(t.Context(), s.URL+"/file")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Get() error = %v, want one containing %q", err, tt.wantErr)
				}
			} else if err != nil || string(got) != "ok" {
				t.Errorf("Get() = %q, %v, want ok", got, err)
			}
			if n := s.count("/file"); n != tt.attempts {
				t.Errorf("Get() made %d requests, want %d", n, tt.attempts)
			}
			if got := w.get(); !slices.Equal(got, tt.wantWaits) {
				t.Errorf("Get() waited %v, want %v", got, tt.wantWaits)
			}
		})
	}
}

func TestGetStalled(t *testing.T) {
	s := newServer(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.handle("/slow", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("abc"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	var w waits
	c := newClient(t, s, &w)
	c.IdleTimeout = 50 * time.Millisecond
	c.Attempts = 2

	_, err := c.Get(t.Context(), s.URL+"/slow")
	if err == nil || !strings.Contains(err.Error(), "nothing received for 50ms") {
		t.Errorf("Get() error = %v, want one saying nothing was received", err)
	}
	if n := s.count("/slow"); n != 2 {
		t.Errorf("Get() made %d requests, want 2", n)
	}
}

func TestGetCancelled(t *testing.T) {
	s := newServer(t)
	s.handle("/file", failing(100, http.StatusInternalServerError, nil))
	ctx, cancel := context.WithCancel(t.Context())
	c := newClient(t, s, nil)
	c.Sleep = func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	}
	if _, err := c.Get(ctx, s.URL+"/file"); err == nil {
		t.Error("Get() worked, want an error")
	}
	if n := s.count("/file"); n != 1 {
		t.Errorf("Get() made %d requests after being cancelled, want 1", n)
	}
}

func TestHTTPErrorDetails(t *testing.T) {
	s := newServer(t)
	s.handle("/limited", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "1800000000")
		http.Error(w, `{"message": "API rate limit exceeded"}`, http.StatusForbidden)
	})
	s.handle("/text", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found: module example.com/x: no matching versions\nmore", http.StatusGone)
	})
	c := newClient(t, s, nil)

	_, err := c.Get(t.Context(), s.URL+"/limited")
	if err == nil || !strings.Contains(err.Error(), "403 Forbidden (GitHub's API rate limit for this machine is used up until ") {
		t.Errorf("Get() error = %v, want one about GitHub's rate limit", err)
	}

	_, err = c.Get(t.Context(), s.URL+"/text")
	he, ok := errors.AsType[*HTTPError](err)
	if !ok || !he.NotFound() || !IsNotFound(err) {
		t.Fatalf("Get() error = %v, want a not found *HTTPError", err)
	}
	if want := s.URL + "/text: 410 Gone (not found: module example.com/x: no matching versions)"; err.Error() != want {
		t.Errorf("Get() error = %q, want %q", err, want)
	}
}

func TestGetHidesPasswords(t *testing.T) {
	s := newServer(t)
	s.handle("/private", func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "me" || pass != "secret" {
			http.Error(w, "who are you?", http.StatusUnauthorized)
			return
		}
		http.Error(w, "nope", http.StatusTeapot)
	})
	c := newClient(t, s, nil)
	_, err := c.Get(t.Context(), strings.Replace(s.URL, "://", "://me:secret@", 1)+"/private")
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "418") {
		t.Errorf("Get() error = %v, want a 418 error without the password", err)
	}
}

func TestGetUnsupportedScheme(t *testing.T) {
	c := &Client{}
	if _, err := c.Get(t.Context(), "file:///etc/passwd"); err == nil || !strings.Contains(err.Error(), "only http and https") {
		t.Errorf("Get(file://) error = %v, want one saying only http and https work", err)
	}
}

func TestRedirect(t *testing.T) {
	s := newServer(t)
	s.handle("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tag/v1.2.3", http.StatusFound)
	})
	s.file("/tag/v1.2.3", []byte("the release page"))
	s.file("/plain", []byte("no redirect"))
	c := newClient(t, s, nil)

	got, err := c.Redirect(t.Context(), s.URL+"/latest")
	if want := s.URL + "/tag/v1.2.3"; err != nil || got != want {
		t.Errorf("Redirect() = %q, %v, want %q", got, err, want)
	}
	if n := s.count("/tag/v1.2.3"); n != 0 {
		t.Errorf("Redirect() followed the redirect")
	}
	if _, err := c.Redirect(t.Context(), s.URL+"/plain"); err == nil || !strings.Contains(err.Error(), "didn't redirect") {
		t.Errorf("Redirect() of a page that doesn't redirect: error = %v", err)
	}
}
