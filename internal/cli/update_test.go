package cli

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// actAsRelease makes updateSelf behave as if this binary were release v. The
// servers in these tests never offer a newer release, so the test binary is
// never replaced.
func actAsRelease(t *testing.T, v string) {
	t.Helper()
	old := buildVersion
	buildVersion = func() string { return v }
	t.Cleanup(func() { buildVersion = old })
}

func runWithContext(ctx context.Context, env map[string]string, args ...string) (code int, stderr string) {
	var out, errOut bytes.Buffer
	code = Execute(ctx, args, &out, &errOut, func(key string) string { return env[key] })
	return code, errOut.String()
}

func TestSelfUpdateFailureIsAWarning(t *testing.T) {
	actAsRelease(t, "0.1.0")
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)

	code, stderr := runWithContext(t.Context(), map[string]string{"MAISYS_RELEASE_URL": srv.URL}, "sync")
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{
		"maisys: warning: couldn't update maisys: ",
		"(carrying on with 0.1.0)",
		`maisys: "sync" is not implemented yet`, // the sync carried on
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
		}
	}
}

func TestSelfUpdateInterrupted(t *testing.T) {
	actAsRelease(t, "0.1.0")
	requested := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(requested) })
		<-r.Context().Done() // never answer; wait for the client to give up
	}))
	t.Cleanup(srv.Close)

	// Cancel, as Ctrl-C does, while the update check is waiting.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-requested:
			cancel()
		case <-ctx.Done():
		}
	}()

	code, stderr := runWithContext(ctx, map[string]string{"MAISYS_RELEASE_URL": srv.URL}, "sync")
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	want := "maisys: interrupted while checking for a newer maisys: context canceled"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
	}
	for _, unwanted := range []string{"warning", "not implemented"} {
		if strings.Contains(stderr, unwanted) {
			t.Errorf("stderr contains %q, want the sync to stop:\n%s", unwanted, stderr)
		}
	}
}

func TestSelfUpdateSkipped(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"--local", nil, []string{"sync", "--local"}},
		{"just restarted after an update", map[string]string{selfUpdatedEnv: "1"}, []string{"sync"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actAsRelease(t, "0.1.0")
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.NotFound(w, r)
			}))
			t.Cleanup(srv.Close)

			env := map[string]string{"MAISYS_RELEASE_URL": srv.URL}
			maps.Copy(env, tt.env)
			_, stderr := runWithContext(t.Context(), env, tt.args...)
			if n := requests.Load(); n != 0 {
				t.Errorf("made %d requests to the release server, want none", n)
			}
			if !strings.Contains(stderr, `"sync" is not implemented yet`) {
				t.Errorf("stderr = %q, want the sync to carry on", stderr)
			}
		})
	}
}
