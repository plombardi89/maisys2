// Package download fetches what maisys installs: it downloads files over
// HTTP with retries, looks up and verifies their sha256 checksums, keeps
// verified downloads in the download cache, and unpacks archives.
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Defaults for a Client's fields.
const (
	// DefaultAttempts is how many times a request is tried.
	DefaultAttempts = 4
	// DefaultBackoff is the wait after the first failed attempt.
	DefaultBackoff = 2 * time.Second
	// DefaultIdleTimeout is how long a request may go without receiving
	// anything before it's given up.
	DefaultIdleTimeout = time.Minute
)

// maxGet caps how much Get reads: version indexes and checksum files, not
// downloads.
const maxGet = 32 << 20

// Client makes maisys's HTTP requests: version lookups, checksum files and
// downloads. Requests that fail because of the network or the server
// (5xx, 429) are tried again, waiting longer each time. It's safe for
// concurrent use.
type Client struct {
	// HTTP makes the requests. Nil means http.DefaultClient.
	HTTP *http.Client
	// UserAgent is sent with every request, such as "maisys/0.6.0".
	UserAgent string
	// Attempts is how many times a request is tried before giving up.
	// Zero means DefaultAttempts.
	Attempts int
	// Backoff is how long to wait after the first failed attempt; the wait
	// doubles after each one. Zero means DefaultBackoff.
	Backoff time.Duration
	// Sleep waits between attempts. Nil means waiting for real, or until
	// ctx is done.
	Sleep func(ctx context.Context, d time.Duration) error
	// IdleTimeout is how long a request may go without receiving anything
	// before it's given up (and tried again). Zero means
	// DefaultIdleTimeout.
	IdleTimeout time.Duration
	// Dir is the download cache folder, ~/.cache/maisys/downloads.
	Dir string
	// Digests caches GitHub's asset digests. Nil means they're looked up
	// every time.
	Digests *Digests
}

// HTTPError is a response with a status other than 200 OK.
type HTTPError struct {
	// URL is the URL requested, without any password in it.
	URL string
	// StatusCode and Status are the response's status.
	StatusCode int
	Status     string
	// Detail is more about the error, if the server said more, such as the
	// first line of a plain-text response.
	Detail string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s: %s", e.URL, e.Status)
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	return msg
}

// NotFound reports whether the server says there's nothing at the URL
// (404 Not Found or 410 Gone).
func (e *HTTPError) NotFound() bool {
	return e.StatusCode == http.StatusNotFound || e.StatusCode == http.StatusGone
}

// IsNotFound reports whether err is an *HTTPError for a URL with nothing at
// it.
func IsNotFound(err error) bool {
	he, ok := errors.AsType[*HTTPError](err)
	return ok && he.NotFound()
}

// temporary marks an error from the network, which is worth another
// attempt.
type temporary struct{ err error }

func (t *temporary) Error() string { return t.err.Error() }
func (t *temporary) Unwrap() error { return t.err }

// errStalled is why a request that stopped receiving was cancelled.
var errStalled = errors.New("nothing received")

// retryable reports whether err, from an attempt, is worth another one.
func retryable(err error) bool {
	if he, ok := errors.AsType[*HTTPError](err); ok {
		return he.StatusCode >= 500 || he.StatusCode == http.StatusTooManyRequests || he.StatusCode == http.StatusRequestTimeout
	}
	_, ok := errors.AsType[*temporary](err)
	return ok
}

// retry runs attempt until it succeeds, fails in a way that isn't worth
// another attempt, or runs out of attempts, waiting longer after each
// failure.
func (c *Client) retry(ctx context.Context, attempt func() error) error {
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	wait := c.Backoff
	if wait <= 0 {
		wait = DefaultBackoff
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	for i := 1; ; i++ {
		err := attempt()
		if err == nil || i >= attempts || ctx.Err() != nil || !retryable(err) {
			if t, ok := errors.AsType[*temporary](err); ok {
				return t.err
			}
			return err
		}
		if sleep(ctx, wait) != nil {
			return err
		}
		wait *= 2
	}
}

// sleepContext waits for d, or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// open makes one request for url and returns the response if its status
// is 200 OK, or 3xx when redirects aren't followed; the caller closes its
// body. The request is cancelled if it goes IdleTimeout without receiving
// anything.
func (c *Client) open(ctx context.Context, rawURL string, header http.Header, follow bool) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("can't download %s: only http and https URLs are supported", u.Redacted())
	}

	idle := c.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(idle, func() { cancel(fmt.Errorf("%w for %s", errStalled, idle)) })
	stop := func() {
		timer.Stop()
		cancel(nil)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		stop()
		return nil, err
	}
	maps.Copy(req.Header, header)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	if !follow {
		noFollow := *client
		noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &noFollow
	}
	resp, err := client.Do(req)
	if err != nil {
		stop()
		return nil, &temporary{err: c.cause(ctx, u, err)}
	}
	resp.Body = &idleBody{ReadCloser: resp.Body, timer: timer, d: idle, stop: stop}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case !follow && resp.StatusCode >= 300 && resp.StatusCode < 400:
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, statusError(u, resp)
}

// cause returns err, explaining a request cancelled because it stalled.
func (c *Client) cause(ctx context.Context, u *url.URL, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
		return fmt.Errorf("%s: %w", u.Redacted(), cause)
	}
	return err
}

// statusError returns the *HTTPError for a response that isn't 200 OK.
func statusError(u *url.URL, resp *http.Response) error {
	he := &HTTPError{URL: u.Redacted(), StatusCode: resp.StatusCode, Status: resp.Status}
	if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
		he.Detail = "GitHub's API rate limit for this machine is used up"
		if reset, err := strconv.ParseInt(resp.Header.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
			he.Detail += " until " + time.Unix(reset, 0).Local().Format("15:04")
		}
		return he
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/plain" {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		if len(line) > 200 {
			line = line[:200] + "…"
		}
		he.Detail = strings.TrimSpace(line)
	}
	return he
}

// idleBody is a response's body, which resets timer to d after each read,
// and calls stop once it's closed.
type idleBody struct {
	io.ReadCloser
	timer *time.Timer
	d     time.Duration
	stop  func()
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.d)
	}
	return n, err
}

func (b *idleBody) Close() error {
	err := b.ReadCloser.Close()
	b.stop()
	return err
}

// readError returns err, from reading resp's body, as worth another
// attempt.
func (c *Client) readError(resp *http.Response, err error) error {
	return &temporary{err: c.cause(resp.Request.Context(), resp.Request.URL, err)}
}

// Get returns the file at url: a small file such as a version index or a
// checksum file, not a download, which Fetch keeps in the download cache.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	return c.get(ctx, url, nil)
}

func (c *Client) get(ctx context.Context, rawURL string, header http.Header) ([]byte, error) {
	var data []byte
	err := c.retry(ctx, func() error {
		resp, err := c.open(ctx, rawURL, header, true)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxGet+1))
		if err != nil {
			return c.readError(resp, err)
		}
		if len(data) > maxGet {
			return fmt.Errorf("%s is larger than %d bytes", resp.Request.URL.Redacted(), maxGet)
		}
		return nil
	})
	return data, err
}

// Redirect requests url without following a redirect, and returns where
// the redirect points, as an absolute URL. A response that isn't a
// redirect is an error.
func (c *Client) Redirect(ctx context.Context, rawURL string) (string, error) {
	var location string
	err := c.retry(ctx, func() error {
		resp, err := c.open(ctx, rawURL, nil, false)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		u := resp.Request.URL
		if resp.StatusCode == http.StatusOK {
			return fmt.Errorf("%s didn't redirect", u.Redacted())
		}
		loc, err := resp.Location()
		if err != nil {
			return fmt.Errorf("%s redirected without saying where: %w", u.Redacted(), err)
		}
		location = loc.String()
		return nil
	})
	return location, err
}
