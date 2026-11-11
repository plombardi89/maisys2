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

// DefaultTTL is how long the versions a dry run finds are used by the sync
// that follows it.
const DefaultTTL = time.Hour

// Cache looks up the latest versions for one run of maisys: each one once,
// however many resources ask for it, even at the same time. It keeps what
// it finds in ~/.cache/maisys/latest.json, where a sync finds what the dry
// run before it found, so that it installs the versions the dry run
// showed. A sync uses a dry run's version once, and only within the TTL;
// otherwise every run looks every version up again. It's safe for
// concurrent use.
type Cache struct {
	// Path is the cache file.
	Path string
	// TTL is how long a sync uses the versions a dry run found. Zero
	// means DefaultTTL.
	TTL time.Duration
	// DryRun is set for a dry run, whose versions the sync that follows
	// uses. Otherwise, the run is a sync.
	DryRun bool
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	loaded  bool
	entries map[string]entry
	// lookups are this run's, by key, including those in progress.
	lookups map[string]*lookup
}

// entry is a resolved version, and when it was resolved.
type entry struct {
	Version  string    `json:"version"`
	Resolved time.Time `json:"resolved"`
	// DryRun reports whether a dry run resolved it, for the sync that
	// follows. It needs no new format: files from older versions of
	// maisys don't have it, so their versions are a sync's, which are
	// never used again, and older versions ignore it.
	DryRun bool `json:"dry_run,omitempty"`
}

// cacheFile is latest.json's layout.
type cacheFile struct {
	FormatVersion int `json:"format_version"`
	// Versions are keyed by each resolver's Upstream.
	Versions map[string]entry `json:"versions"`
}

// lookup is a run's lookup of one version. done is closed once version
// and err are set.
type lookup struct {
	done    chan struct{}
	version string
	err     error
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

// Resolve returns the latest version of tool from r: the first time the
// Cache is asked for it, the version a dry run found within the TTL, if the
// Cache is a sync's, or else the one r finds; from then on, the same one.
// Callers that ask while it's being looked up wait for it. A failed lookup
// is a *LookupError, and isn't kept: the next call looks it up again. A
// nil Cache always asks r.
func (c *Cache) Resolve(ctx context.Context, client *download.Client, tool string, r Resolver) (string, error) {
	key := r.Upstream()
	if c == nil {
		return lookUp(ctx, client, tool, key, r)
	}
	c.mu.Lock()
	if l, ok := c.lookups[key]; ok {
		c.mu.Unlock()
		select {
		case <-l.done:
			return l.version, l.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	l := &lookup{done: make(chan struct{})}
	if c.lookups == nil {
		c.lookups = map[string]*lookup{}
	}
	c.lookups[key] = l
	c.mu.Unlock()

	l.version, l.err = c.resolve(ctx, client, tool, key, r)
	if l.err != nil {
		c.mu.Lock()
		delete(c.lookups, key)
		c.mu.Unlock()
	}
	close(l.done)
	return l.version, l.err
}

// resolve returns the version a dry run found for key, if this is a sync
// and the dry run was within the TTL, or else looks it up, and saves it.
func (c *Cache) resolve(ctx context.Context, client *download.Client, tool, key string, r Resolver) (string, error) {
	if v, ok, err := c.fromDryRun(key); err != nil || ok {
		return v, err
	}
	v, err := lookUp(ctx, client, tool, key, r)
	if err != nil {
		return "", err
	}
	return v, c.put(key, entry{Version: v, Resolved: c.now().UTC(), DryRun: c.DryRun})
}

// lookUp asks r for the latest version of tool.
func lookUp(ctx context.Context, client *download.Client, tool, key string, r Resolver) (string, error) {
	v, err := r.Latest(ctx, client)
	if err != nil {
		return "", newLookupError(tool, key, err)
	}
	return v, nil
}

// fromDryRun returns the version a dry run found for key within the TTL,
// if this is a sync. It's saved as the sync's, so no later run uses it.
func (c *Cache) fromDryRun(key string) (string, bool, error) {
	if c.DryRun {
		return "", false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return "", false, err
	}
	e, ok := c.entries[key]
	if !ok || !e.DryRun || !c.isFresh(e) {
		return "", false, nil
	}
	e.DryRun = false
	return e.Version, true, c.putLocked(key, e)
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

// put keeps e for key, and saves the cache.
func (c *Cache) put(key string, e entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadLocked(); err != nil {
		return err
	}
	return c.putLocked(key, e)
}

// putLocked keeps e for key, and saves the cache without the versions
// older than the TTL.
func (c *Cache) putLocked(key string, e entry) error {
	c.entries[key] = e
	for k, e := range c.entries {
		if !c.isFresh(e) {
			delete(c.entries, k)
		}
	}
	return cachefile.Save(c.Path, cacheFile{FormatVersion: FormatVersion, Versions: c.entries})
}
