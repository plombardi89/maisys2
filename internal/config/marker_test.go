package config_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
)

func readMarker(t *testing.T, doc string) (*config.Marker, error) {
	t.Helper()
	repo := &config.Repo{FS: fstest.MapFS{"maisys.toml": file(doc)}}
	return repo.ReadMarker()
}

func TestReadMarker(t *testing.T) {
	mk, err := readMarker(t, `
config_version = 1
requires = ">= 0.6"        # optional minimum maisys version
`)
	if err != nil {
		t.Fatal(err)
	}
	if mk.ConfigVersion != 1 || mk.Requires != ">= 0.6" {
		t.Errorf("marker = %+v", mk)
	}
}

func TestReadMarkerIgnoresUnknownKeys(t *testing.T) {
	repo := &config.Repo{FS: fstest.MapFS{"maisys.toml": file(`
config_version = 1
Requires = ">= 99"
mirror = "https://example.com"

[newer]
key = 1
`)}}
	mk, err := repo.ReadMarker()
	if err != nil {
		t.Fatal(err)
	}
	if mk.ConfigVersion != 1 || mk.Requires != "" {
		t.Errorf("marker = %+v, want config_version 1, and no requires", mk)
	}
}

func TestReadMarkerErrors(t *testing.T) {
	tests := []struct {
		name   string
		files  fstest.MapFS
		strict bool
		want   string
		is     error
	}{
		{
			name:  "no maisys.toml",
			files: fstest.MapFS{"machines/box.toml": file(`os = "fedora"`)},
			want:  "maisys.toml doesn't exist, so this isn't a maisys config repo",
			is:    config.ErrNotConfigRepo,
		},
		{
			name:   "unknown key",
			files:  fstest.MapFS{"maisys.toml": file("config_version = 1\nrequire = \">= 0.6\"\n")},
			strict: true,
			want:   "maisys.toml:2:1: unknown key require",
			is:     config.ErrUnknownKey,
		},
		{
			name:   "a key in the wrong case",
			files:  fstest.MapFS{"maisys.toml": file("Config_Version = 1\n")},
			strict: true,
			want:   "maisys.toml:1:1: unknown key Config_Version (keys are case-sensitive: did you mean config_version?)",
			is:     config.ErrUnknownKey,
		},
		{
			name:  "a key in the wrong case, ignored",
			files: fstest.MapFS{"maisys.toml": file("Config_Version = 1\n")},
			want:  "maisys.toml: config_version is missing: set it to 1",
		},
		{
			name:  "a string where a number is expected",
			files: fstest.MapFS{"maisys.toml": file(`config_version = "1"`)},
			want:  "maisys.toml:1:18: config_version must be a whole number, not a TOML string",
		},
		{
			name:  "no config_version",
			files: fstest.MapFS{"maisys.toml": file(`requires = ">= 0.6"`)},
			want:  "maisys.toml: config_version is missing: set it to 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&config.Repo{FS: tt.files, Strict: tt.strict}).ReadMarker()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("errors.Is(err, %v) = false", tt.is)
			}
		})
	}
}

func TestMarkerCheck(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		version string
		current int // config.ConfigVersion if 0
		warning string
		err     string
		notErr  string // a string the error mustn't contain
	}{
		{name: "current config_version", doc: "config_version = 1", version: "0.1.0"},
		{
			name:    "newer config_version",
			doc:     "config_version = 2",
			version: "0.1.0",
			err:     "maisys.toml:1:18: config_version 2 needs a newer maisys: this one reads config_version 1; update maisys",
		},
		{
			name:    "config_version 0",
			doc:     "config_version = 0",
			version: "0.1.0",
			err:     "maisys.toml:1:18: config_version must be 1 or more, not 0",
		},
		{
			name:    "previous config_version",
			doc:     "config_version = 2",
			version: "0.1.0",
			current: 3,
			warning: "maisys.toml:1:18: config_version 2 is the previous version: migrate the config to config_version 3",
		},
		{
			name:    "config_version older than the previous one",
			doc:     "config_version = 1",
			version: "0.1.0",
			current: 3,
			err:     "maisys.toml:1:18: config_version 1 is too old for this maisys, which reads config_version 2 and 3",
		},
		{
			name:    "config_version newer than both",
			doc:     "config_version = 4",
			version: "0.1.0",
			current: 3,
			err:     "config_version 4 needs a newer maisys: this one reads config_version 2 and 3",
		},
		{name: "requires met", doc: "config_version = 1\nrequires = \">= 0.6\"", version: "0.6.0"},
		{name: "requires met by a later version", doc: "config_version = 1\nrequires = \">= 0.6\"", version: "1.2.3"},
		{name: "a version with a leading v", doc: "config_version = 1\nrequires = \">= v0.6\"", version: "v0.6.1"},
		{name: "a range", doc: "config_version = 1\nrequires = \">= 0.6, < 2\"", version: "1.9.0"},
		{name: "a pre-release", doc: "config_version = 1\nrequires = \">= 1.0.0-rc.1\"", version: "1.0.0-rc.2"},
		{name: "= and <=", doc: "config_version = 1\nrequires = \"= 1.2.3, <= 1.2.3\"", version: "1.2.3"},
		{
			name:    "requires not met",
			doc:     "config_version = 1\nrequires = \">= 0.6\"",
			version: "0.5.9",
			err:     "maisys.toml:2:12: this config needs maisys >= 0.6, but this is maisys 0.5.9; update maisys",
		},
		{
			name:    "a bare version means at least",
			doc:     "config_version = 1\nrequires = \"0.6\"",
			version: "0.5.0",
			err:     "this config needs maisys 0.6, but this is maisys 0.5.0; update maisys",
		},
		{
			name:    "> an equal version",
			doc:     "config_version = 1\nrequires = \"> 1.0.0\"",
			version: "1.0.0",
			err:     "this config needs maisys > 1.0.0, but this is maisys 1.0.0; update maisys",
		},
		{
			name:    "too new for an upper bound, which updating won't fix",
			doc:     "config_version = 1\nrequires = \">= 0.6, < 2\"",
			version: "2.0.0",
			err:     "maisys.toml:2:12: this config needs maisys >= 0.6, < 2, but this is maisys 2.0.0",
			notErr:  "update",
		},
		{
			name:    "!=",
			doc:     "config_version = 1\nrequires = \"!= 1.2.3\"",
			version: "1.2.3",
			err:     "this config needs maisys != 1.2.3, but this is maisys 1.2.3",
			notErr:  "update",
		},
		{
			name:    "not a version",
			doc:     "config_version = 1\nrequires = \">= banana\"",
			version: "1.0.0",
			err:     `maisys.toml:2:12: requires = ">= banana" isn't valid: "banana" isn't a version: write it like ">= 0.6" or ">= 0.6, < 2"`,
		},
		{
			name:    "an unknown operator",
			doc:     "config_version = 1\nrequires = \"~> 0.6\"",
			version: "1.0.0",
			err:     `requires = "~> 0.6" isn't valid: "~> 0.6" isn't a version`,
		},
		{
			name:    "an empty requirement",
			doc:     "config_version = 1\nrequires = \">= 0.6,\"",
			version: "1.0.0",
			err:     `maisys.toml:2:12: requires = ">= 0.6," has a requirement with no version`,
		},
		{
			name:    "a development build",
			doc:     "config_version = 1\nrequires = \">= 0.6\"",
			version: "dev",
			warning: `maisys.toml:2:12: this maisys (dev) isn't a release, so requires = ">= 0.6" isn't checked`,
		},
		{
			name:    "a development build still checks the syntax",
			doc:     "config_version = 1\nrequires = \">= 0.x\"",
			version: "dev",
			err:     `requires = ">= 0.x" isn't valid`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mk, err := readMarker(t, tt.doc)
			if err != nil {
				t.Fatal(err)
			}
			current := tt.current
			if current == 0 {
				current = config.ConfigVersion
			}
			warnings, err := config.CheckMarker(mk, tt.version, current)

			switch {
			case tt.err == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
				t.Errorf("error = %v, want one containing %q", err, tt.err)
			case tt.notErr != "" && strings.Contains(err.Error(), tt.notErr):
				t.Errorf("error = %v, want one without %q", err, tt.notErr)
			default:
			}
			switch {
			case tt.warning == "" && len(warnings) > 0:
				t.Errorf("warnings = %q, want none", warnings)
			case tt.warning != "" && (len(warnings) != 1 || !strings.Contains(warnings[0], tt.warning)):
				t.Errorf("warnings = %q, want one containing %q", warnings, tt.warning)
			default:
			}
		})
	}
}

func TestMarkerCheckWithoutPositions(t *testing.T) {
	_, err := (&config.Marker{ConfigVersion: config.ConfigVersion + 1}).Check("1.0.0")
	if want := "maisys.toml: config_version"; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %v, want one starting with %q", err, want)
	}
}
