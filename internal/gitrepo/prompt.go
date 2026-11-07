package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// TerminalPrompt returns an Auth.Prompt that asks for an access token in the
// terminal in, without showing what's typed, and writes the question to out.
func TerminalPrompt(in *os.File, out io.Writer) func(ctx context.Context, url string) (string, error) {
	return func(ctx context.Context, url string) (string, error) {
		_, _ = fmt.Fprintf(out, `git couldn't sign in to %s.
Enter a read-only access token for this repo (%s).
maisys saves it where only you can read it, and uses it for this repo only.
Token: `, url, tokenKind)

		fd := int(in.Fd())
		state, err := term.GetState(fd)
		if err != nil {
			_, _ = fmt.Fprintln(out)
			return "", fmt.Errorf("couldn't ask for a token: %w", err)
		}
		type answer struct {
			token []byte
			err   error
		}
		answers := make(chan answer, 1)
		go func() {
			token, err := term.ReadPassword(fd)
			answers <- answer{token, err}
		}()

		select {
		case <-ctx.Done():
			// The read can't be interrupted, so turn echo back on here
			// rather than leave the terminal without it.
			_ = term.Restore(fd, state)
			_, _ = fmt.Fprintln(out)
			return "", ctx.Err()
		case a := <-answers:
			_, _ = fmt.Fprintln(out)
			if a.err != nil {
				return "", fmt.Errorf("couldn't read the token: %w", a.err)
			}
			token := strings.TrimSpace(string(a.token))
			if token == "" {
				return "", errors.New("no token entered")
			}
			return token, nil
		}
	}
}
