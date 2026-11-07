package version

import (
	"context"
	"sync"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource/cachefile"
	"github.com/plombardi89/maisys2/internal/resource/download"
)

// FormatVersion is the format of latest.json this maisys reads and writes.
const FormatVersion = 1

// DefaultTTL is how long a Cache keeps a resolved version.
const DefaultTTL = time.Hour

// Cache keeps the latest versions resolvers find for a while, in
// ~/.cache/maisys/latest.json, so that a dry run followed by a sync installs
// the same versions, and syncs in quick succession don't ask again. It's
// safe for concurrent use.
type Cache struct {
	// Path is the cache file.
	Path string
	// TTL is how long a resolved version is used. Zero means DefaultTTL.
	TTL time.Duration
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	loaded  bool
	entries map[string]entry
}

// entry is a resolved version, and when it was resolved.
type entry struct {
	Version  string    `json:"version"`
	Resolved time.Time `json:"resolved"`
}

// cacheFile is latest.json's layout.
type cacheFile struct {
	FormatVersion int `json:"format_version"`
	// Versions are keyed by each resolver's Upstream.
	Versions map[string]entry `json:"versions"`
}

// Load reads the cache file, unless it's been read already. Only a file
// from a newer maisys is an error: one that can't be read is replaced.
func (c *Cache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked()
}

func (c *Cache) loadLocked() error {
	if c.loaded {
		return nil
	}
	var f cacheFile
	ok, err := cachefile.Load(c.Path, FormatVersion, &f)
	if err != nil {
		return err
	}
	c.entries = map[string]entry{}
	if ok {
		for k, e := range f.Versions {
			if config.IsExactVersion(e.Version) {
				c.entries[k] = e
			}
		}
	}
	c.loaded = true
	return nil
}

// Resolve returns the latest version of tool from r: the one the cache has,
// if it's fresh, or else the one r finds, which the cache then keeps. A
// failed lookup is a *LookupError. A nil Cache always asks r.
func (c *Cache) Resolve(ctx context.Context, client *download.Client, tool string, r Resolver) (string, error) {
	key := r.Upstream()
	if c == nil {
		v, err := r.Latest(ctx, client)
		if err != nil {
			return "", &LookupError{Tool: tool, Upstream: key, Err: err}
		}
		return v, nil
	}
	if v, ok, err := c.fresh(key); err != nil || ok {
		return v, err
	}
	v, err := r.Latest(ctx, client)
	if err != nil {
		return "", &LookupError{Tool: tool, Upstream: key, Err: err}
	}
	return v, c.put(key, v)
}

// fresh returns the cached version for key, if it's fresh.
func (c *Cache) fresh(key string) (string, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return "", false, err
	}
	e, ok := c.entries[key]
	if !ok || !c.isFresh(e) {
		return "", false, nil
	}
	return e.Version, true, nil
}

func (c *Cache) isFresh(e entry) bool {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	age := c.now().Sub(e.Resolved)
	return age >= 0 && age < ttl
}

func (c *Cache) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// put keeps v for key, and saves the cache without the versions that are
// no longer fresh.
func (c *Cache) put(key, v string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	c.entries[key] = entry{Version: v, Resolved: c.now().UTC()}
	for k, e := range c.entries {
		if !c.isFresh(e) {
			delete(c.entries, k)
		}
	}
	return cachefile.Save(c.Path, cacheFile{FormatVersion: FormatVersion, Versions: c.entries})
}
