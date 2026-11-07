package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// server is a test HTTP server answering each path (with its query, if any)
// with a handler, and counting the requests for each.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	handlers map[string]http.HandlerFunc
	hits     map[string]int
	agents   []string
}

func newServer(t *testing.T) *server {
	t.Helper()
	s := &server{handlers: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		s.mu.Lock()
		s.hits[key]++
		s.agents = append(s.agents, r.UserAgent())
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

// handle answers requests for path with h.
func (s *server) handle(path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[path] = h
}

// file answers requests for path with data.
func (s *server) file(path string, data []byte) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
}

// count returns how many requests there were for path.
func (s *server) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// failing returns a handler that answers with status for the first n
// requests, then with h.
func failing(n, status int, h http.HandlerFunc) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n--
		fail := n >= 0
		mu.Unlock()
		if fail {
			http.Error(w, http.StatusText(status), status)
			return
		}
		h(w, r)
	}
}

// waits records the waits between attempts, without waiting.
type waits struct {
	mu   sync.Mutex
	list []time.Duration
}

func (w *waits) sleep(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.list = append(w.list, d)
	return nil
}

func (w *waits) get() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.list...)
}

// newClient returns a Client for s that records its waits in w, with a
// download cache in a temporary folder.
func newClient(t *testing.T, s *server, w *waits) *Client {
	t.Helper()
	if w == nil {
		w = &waits{}
	}
	return &Client{
		HTTP:      s.Client(),
		UserAgent: "maisys/test",
		Sleep:     w.sleep,
		Dir:       t.TempDir(),
	}
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
