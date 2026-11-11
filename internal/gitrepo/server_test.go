package gitrepo

import (
	"bytes"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// sandbox is a home directory and git setup of its own, so tests never read
// or change the user's git config, credentials or identity.
type sandbox struct {
	t    *testing.T
	home string
	opts Options
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	git, err := Find()
	if err != nil {
		t.Skipf("these tests need git: %v", err)
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	writeFile(t, global, "")
	return &sandbox{
		t:    t,
		home: home,
		opts: Options{
			Git: git,
			Env: []string{
				"HOME=" + home,
				"USERPROFILE=" + home,
				"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
				"GIT_CONFIG_NOSYSTEM=1",
				"GIT_CONFIG_GLOBAL=" + global,
				"GIT_AUTHOR_NAME=maisys test",
				"GIT_AUTHOR_EMAIL=test@example.com",
				"GIT_COMMITTER_NAME=maisys test",
				"GIT_COMMITTER_EMAIL=test@example.com",
				// The test server's certificate is self-signed.
				"GIT_SSL_NO_VERIFY=1",
			},
		},
	}
}

// credentialsFile is where tokens typed at the prompt are saved.
func (s *sandbox) credentialsFile() string {
	return filepath.Join(s.home, ".config", "maisys", "git-credentials")
}

// git runs a git command in dir (the sandbox's home if it's "") and returns
// its output.
func (s *sandbox) git(dir string, args ...string) string {
	s.t.Helper()
	if dir == "" {
		dir = s.home
	}
	out, err := s.opts.run(s.t.Context(), call{dir: dir, args: args})
	if err != nil {
		s.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(out)
}

// setGlobal sets a value in the sandbox's global git config.
func (s *sandbox) setGlobal(key, value string) {
	s.t.Helper()
	s.git("", "config", "--global", key, value)
}

// bareRepo creates a bare repo at dir whose main branch has maisys.toml.
func (s *sandbox) bareRepo(dir string) {
	s.t.Helper()
	s.git("", "init", "--quiet", "--bare", "--initial-branch=main", dir)
	s.commitUpstream(dir, "maisys.toml", "config_version = 1\n")
}

// commitUpstream commits a file to the main branch of the bare repo at bare.
func (s *sandbox) commitUpstream(bare, name, content string) {
	s.t.Helper()
	work := filepath.Join(s.t.TempDir(), "work")
	s.git("", "clone", "--quiet", bare, work)
	writeFile(s.t, filepath.Join(work, name), content)
	s.git(work, "add", name)
	s.git(work, "commit", "--quiet", "--message", "Add "+name)
	s.git(work, "push", "--quiet", "origin", "HEAD:main")
}

// gitServer serves bare repos over HTTPS with git http-backend: under
// /public/ to anyone, and under /private/ only with the right token as the
// password.
type gitServer struct {
	url  string
	root string // the bare repos

	mu    sync.Mutex
	token string
}

// newGitServer starts a server with a repo called config.git, whose private
// URL accepts testToken.
func newGitServer(t *testing.T, s *sandbox) *gitServer {
	t.Helper()
	execPath := s.git("", "--exec-path")
	backend := filepath.Join(execPath, "git-http-backend")
	if runtime.GOOS == "windows" {
		backend += ".exe"
	}
	if _, err := os.Stat(backend); err != nil {
		t.Skipf("these tests need git http-backend: %v", err)
	}

	srv := &gitServer{root: t.TempDir(), token: testToken}
	s.bareRepo(filepath.Join(srv.root, "config.git"))

	env := []string{
		"GIT_PROJECT_ROOT=" + srv.root,
		"GIT_HTTP_EXPORT_ALL=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=" + srv.root,
	}
	if runtime.GOOS == "windows" {
		// Git for Windows keeps the DLLs its programs need next to git.exe.
		bin := filepath.Join(execPath, "..", "..", "bin")
		env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	var logs lockedBuffer
	backendAt := func(root string) http.Handler {
		return &cgi.Handler{Path: backend, Root: root, Env: env, Stderr: &logs}
	}

	mux := http.NewServeMux()
	mux.Handle("/public/", backendAt("/public"))
	private := backendAt("/private")
	mux.HandleFunc("/private/", func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != srv.currentToken() {
			w.Header().Set("WWW-Authenticate", `Basic realm="maisys test"`)
			http.Error(w, "sign in", http.StatusUnauthorized)
			return
		}
		private.ServeHTTP(w, r)
	})

	hs := httptest.NewTLSServer(mux)
	srv.url = hs.URL
	t.Cleanup(func() {
		hs.Close()
		if t.Failed() && logs.Len() > 0 {
			t.Logf("git http-backend's errors:\n%s", logs.String())
		}
	})
	return srv
}

func (srv *gitServer) publicURL(repo string) string  { return srv.url + "/public/" + repo }
func (srv *gitServer) privateURL(repo string) string { return srv.url + "/private/" + repo }

// host is the host and port in the server's URLs.
func (srv *gitServer) host() string { return strings.TrimPrefix(srv.url, "https://") }

func (srv *gitServer) currentToken() string {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.token
}

// setToken changes the token the server accepts, as if the old one expired.
func (srv *gitServer) setToken(token string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	srv.token = token
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
