package terminal

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
	"github.com/plombardi89/maisys2/internal/state"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name.golden, or rewrites the file with
// -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (run the tests with -update to rewrite it):\n--- got\n%s--- want\n%s", path, got, want)
	}
}

// fedoraDev is the GUID of the profile of the distro with the ID
// {0a1b2c3d-4e5f-4071-8293-a4b5c6d7e8f9}, as Python's uuid module computes
// it.
const fedoraDev = "{2d71752b-afa5-5775-8684-445c330bed79}"

func TestGUIDs(t *testing.T) {
	tests := []struct {
		name, got, want string
	}{
		// The example in Windows Terminal's documentation.
		{"legacy Ubuntu", LegacyGUID("Ubuntu"), "{2c4de342-38b7-51cf-b940-2309a097f518}"},
		{"legacy fedora-dev", LegacyGUID("fedora-dev"), "{99c3c0c7-e0ff-5682-aeff-3c131e43c197}"},
		{"modern", ModernGUID("{0A1B2C3D-4E5F-4071-8293-A4B5C6D7E8F9}"), fedoraDev},
		{"modern without braces", ModernGUID("0a1b2c3d-4e5f-4071-8293-a4b5c6d7e8f9"), fedoraDev},
		// The fragment example in Windows Terminal's documentation, which
		// is made in two steps.
		{
			"fragment",
			uuid5(parseGUID(t, uuid5(parseGUID(t, "{f65ddb7e-706b-4499-8a50-40313caf510a}"), utf16LE("Git"))), utf16LE("Git Bash")),
			"{2ece5bfe-50ed-5f3a-ab87-5cd4baafed2b}",
		},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: GUID = %s, want %s", tt.name, tt.got, tt.want)
		}
	}
}

// parseGUID returns the bytes of a GUID written as Windows Terminal writes
// them.
func parseGUID(t *testing.T, s string) [16]byte {
	t.Helper()
	hex := strings.NewReplacer("{", "", "}", "", "-", "").Replace(s)
	var g [16]byte
	for i := range g {
		var b byte
		for _, c := range hex[2*i : 2*i+2] {
			b <<= 4
			switch {
			case '0' <= c && c <= '9':
				b |= byte(c - '0')
			default:
				b |= byte(c-'a') + 10
			}
		}
		g[i] = b
	}
	return g
}

func TestProfileGUID(t *testing.T) {
	fragment := filepath.Join(t.TempDir(), "fragment.json")
	data := `{
  "profiles": [
    {"updates": "{99c3c0c7-e0ff-5682-aeff-3c131e43c197}", "hidden": true},
    {"guid": "{11111111-2222-5333-8444-555555555555}", "name": "fedora-dev", "source": "Microsoft.WSL"}
  ]
}`
	if err := os.WriteFile(fragment, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	id := "{0a1b2c3d-4e5f-4071-8293-a4b5c6d7e8f9}"
	tests := []struct {
		name string
		d    wsl.Registered
		want string
	}{
		{"fragment", wsl.Registered{ID: id, Name: "fedora-dev", Modern: true, TerminalProfile: fragment}, "{11111111-2222-5333-8444-555555555555}"},
		{"fragment gone", wsl.Registered{ID: id, Name: "fedora-dev", Modern: true, TerminalProfile: fragment + ".gone"}, fedoraDev},
		{"modern", wsl.Registered{ID: id, Name: "fedora-dev", Modern: true}, fedoraDev},
		{"legacy", wsl.Registered{ID: id, Name: "Ubuntu"}, "{2c4de342-38b7-51cf-b940-2309a097f518}"},
	}
	for _, tt := range tests {
		if got := ProfileGUID(tt.d); got != tt.want {
			t.Errorf("%s: ProfileGUID() = %s, want %s", tt.name, got, tt.want)
		}
	}
}

// TestSetDefault edits settings files, checking that only the default
// profile changes and that comments and formatting are kept.
func TestSetDefault(t *testing.T) {
	for _, name := range []string{"settings", "settings-no-default", "settings-bom"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := SetDefault(data, fedoraDev)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, name, got)
			if d, err := DefaultOf(got); err != nil || d != fedoraDev {
				t.Errorf("DefaultOf() the edited file = %q, %v; want %s", d, err, fedoraDev)
			}
			if bytes.HasPrefix(data, bom) != bytes.HasPrefix(got, bom) {
				t.Error("the byte order mark wasn't kept")
			}
			again, err := SetDefault(got, fedoraDev)
			if err != nil || !bytes.Equal(again, got) {
				t.Errorf("SetDefault() again changed the file:\n%s", again)
			}
		})
	}
	if _, err := SetDefault([]byte("[]"), fedoraDev); err == nil {
		t.Error("SetDefault() of a list worked")
	}
	if _, err := SetDefault([]byte("{"), fedoraDev); err == nil {
		t.Error("SetDefault() of broken JSON worked")
	}
}

// fakeRegistry is a wsl.Registry with distros.
type fakeRegistry struct{ distros []wsl.Registered }

func (r *fakeRegistry) Distros() ([]wsl.Registered, error) { return r.distros, nil }
func (r *fakeRegistry) DefaultDistro() (string, error)     { return "", nil }
func (r *fakeRegistry) Version() (string, error)           { return "2.6.1.0", nil }
func (r *fakeRegistry) RebootPending() (bool, error)       { return false, nil }
func (r *fakeRegistry) GitDir() (string, error)            { return "", nil }

func TestDefaultProfile(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	data, err := os.ReadFile(filepath.Join("testdata", "settings-no-default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, data, 0o644); err != nil {
		t.Fatal(err)
	}
	reg := &fakeRegistry{}
	rt := &resource.Runtime{}
	rt.SetShared(wsl.RegistryKey{}, reg)
	p := &DefaultProfile{Settings: Settings{Path: settings}, Distro: "fedora-dev"}

	// Before the distro is created, the plan says what will change, but
	// applying needs the distro.
	c, err := p.Check(context.Background(), rt)
	if err != nil || c.Action != resource.Update || c.Summary != "default profile → fedora-dev" {
		t.Errorf("Check() = %+v, %v; want ~ default profile → fedora-dev", c, err)
	}
	if _, err := p.Apply(context.Background(), rt); err == nil || !strings.Contains(err.Error(), "there's no distro fedora-dev") {
		t.Errorf("Apply() before the distro exists: error = %v", err)
	}

	reg.distros = []wsl.Registered{{ID: "{0a1b2c3d-4e5f-4071-8293-a4b5c6d7e8f9}", Name: "fedora-dev", Modern: true}}
	rec, err := p.Apply(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Details[DetailGUID] != fedoraDev {
		t.Errorf("record = %+v, want the GUID", rec)
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "settings-no-default", got)
	if c, err := p.Check(context.Background(), rt); err != nil || c.Action != resource.NoChange {
		t.Errorf("Check() after Apply() = %+v, %v; want no change", c, err)
	}

	for _, p := range []*DefaultProfile{
		{Distro: "fedora-dev"},
		{Settings: Settings{Path: settings}},
		{Settings: Settings{Path: settings + ".missing"}, Distro: "fedora-dev"},
	} {
		c, err := p.Check(context.Background(), rt)
		if err != nil || c.Action != resource.NoChange || len(c.Notes) != 1 {
			t.Errorf("Check() of %+v = %+v, %v; want a note", p, c, err)
		}
	}
}

func TestFind(t *testing.T) {
	local := t.TempDir()
	preview := filepath.Join(local, "Packages", "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe", "LocalState", "settings.json")
	if err := os.MkdirAll(filepath.Dir(preview), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preview, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	if got := Find(local, none); len(got) != 1 || got[0].Path != preview || got[0].Name != "Windows Terminal Preview" {
		t.Errorf("Find() = %+v, want the Preview's settings", got)
	}
	override := func(name string) string {
		if name == SettingsEnv {
			return `C:\test\settings.json`
		}
		return ""
	}
	if got := Find(local, override); len(got) != 1 || got[0].Path != `C:\test\settings.json` {
		t.Errorf("Find() with %s = %+v, want only that file", SettingsEnv, got)
	}
}

func TestRemover(t *testing.T) {
	c, err := Remover{}.CheckRemove(context.Background(), &resource.Runtime{}, state.Record{ID: Kind, Kind: Kind})
	if err != nil || c.Action != resource.NoChange {
		t.Errorf("CheckRemove() = %+v, %v; want the default profile left as it is", c, err)
	}
}
