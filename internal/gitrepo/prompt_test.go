//go:build linux

package gitrepo

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/creack/pty"
)

const promptURL = "https://example.com/me/config.git"

func TestTerminalPrompt(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		want  string
		err   string
	}{
		{"token", " s3cret-token \r", "s3cret-token", ""},
		{"nothing typed", "\r", "", "no token entered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := newTerminal(t)
			answer := term.ask(t.Context())
			term.waitForPrompt(t)
			term.typeIn(t, tt.typed)

			got := term.wait(t, answer)
			if tt.err != "" {
				assertErrorContains(t, got.err, tt.err)
			} else if got.err != nil || got.token != tt.want {
				t.Errorf("prompt returned %q, %v; want %q", got.token, got.err, tt.want)
			}

			out := term.output(t)
			for _, want := range []string{
				"git couldn't sign in to " + promptURL + ".",
				"Enter a read-only access token for this repo (for GitHub, a fine-grained token with Contents: read on that one repo).",
				"Token: ",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the terminal shows %q, want it to contain %q", out, want)
				}
			}
			if strings.Contains(out, "s3cret") {
				t.Errorf("the token was shown as it was typed: %q", out)
			}
		})
	}
}

// TestTerminalPromptCancelled checks echo is turned back on when the prompt
// is cancelled, for example by Ctrl-C. It's the last test that uses a
// pseudo-terminal, because the cancelled read finishes in the background.
func TestTerminalPromptCancelled(t *testing.T) {
	term := newTerminal(t)
	ctx, cancel := context.WithCancel(t.Context())
	answer := term.ask(ctx)
	term.waitForPrompt(t)
	cancel()

	if got := term.wait(t, answer); !errors.Is(got.err, context.Canceled) {
		t.Errorf("prompt returned %q, %v; want context.Canceled", got.token, got.err)
	}
	if !term.echoOn(t) {
		t.Error("echo is still off after the prompt was cancelled")
	}
}

type promptResult struct {
	token string
	err   error
}

// terminal is a pseudo-terminal whose output is collected.
type terminal struct {
	ptmx, tty *os.File
	ttyFd     uintptr
	out       lockedBuffer
}

func newTerminal(t *testing.T) *terminal {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	term := &terminal{ptmx: ptmx, tty: tty, ttyFd: tty.Fd()}
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(&term.out, ptmx)
		close(copied)
	}()
	t.Cleanup(func() {
		// Finish any read still waiting for a line, so the terminal can
		// close: pty.Open leaves ptmx in blocking mode, so closing it
		// doesn't stop the copy.
		_, _ = ptmx.WriteString("\r")
		_ = tty.Close()
		select {
		case <-copied:
		case <-time.After(10 * time.Second):
			t.Error("the pseudo-terminal didn't close after 10s")
		}
		_ = ptmx.Close()
	})
	return term
}

// ask runs TerminalPrompt in the terminal.
func (term *terminal) ask(ctx context.Context) <-chan promptResult {
	answer := make(chan promptResult, 1)
	ask := TerminalPrompt(term.tty, term.tty)
	go func() {
		token, err := ask(ctx, promptURL)
		answer <- promptResult{token, err}
	}()
	return answer
}

// waitForPrompt waits until the prompt is shown and echo is off.
func (term *terminal) waitForPrompt(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(term.out.String(), "Token: ") || term.echoOn(t) {
		if time.Now().After(deadline) {
			t.Fatalf("no prompt after 10s; the terminal shows %q", term.out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (term *terminal) typeIn(t *testing.T, s string) {
	t.Helper()
	if _, err := term.ptmx.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func (term *terminal) wait(t *testing.T, answer <-chan promptResult) promptResult {
	t.Helper()
	select {
	case got := <-answer:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("the prompt didn't return after 10s")
		return promptResult{}
	}
}

// output returns everything the terminal has shown.
func (term *terminal) output(t *testing.T) string {
	t.Helper()
	const end = "[end of output]"
	if _, err := term.tty.WriteString(end); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := term.out.String()
		if before, ok := strings.CutSuffix(out, end); ok {
			return before
		}
		if time.Now().After(deadline) {
			t.Fatalf("the terminal's output didn't end after 10s: %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// echoOn reports whether the terminal shows what's typed. The tests check it
// to know when it's safe to type a token, which is why they only run on
// Linux: TCGETS is Linux's way to read a terminal's settings.
func (term *terminal) echoOn(t *testing.T) bool {
	t.Helper()
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, term.ttyFd, syscall.TCGETS, uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		t.Fatalf("reading the terminal's settings: %v", errno)
	}
	return termios.Lflag&syscall.ECHO != 0
}
