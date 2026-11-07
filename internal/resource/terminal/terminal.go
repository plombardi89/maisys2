// Package terminal sets Windows Terminal's default profile, in its
// settings file, keeping the file's comments and formatting: the file is
// JSON with comments, edited with github.com/tailscale/hujson, and only the
// value of "defaultProfile" changes.
package terminal

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // UUIDv5 is defined with SHA-1.
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/tailscale/hujson"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
	"github.com/plombardi89/maisys2/internal/state"
)

// Kind is the kind of resource a DefaultProfile is.
const Kind = "terminal-profile"

// DetailGUID is the key of the profile's GUID in a record's Details.
const DetailGUID = "guid"

// SettingsEnv names the environment variable that, if set, is the only
// settings file maisys edits, so tests can edit one where Windows Terminal
// isn't installed.
const SettingsEnv = "MAISYS_TERMINAL_SETTINGS"

// Settings is a Windows Terminal settings file.
type Settings struct {
	// Path is the file's path.
	Path string
	// Name names the Terminal it belongs to in the plan, such as "Windows
	// Terminal Preview". Empty means Windows Terminal.
	Name string
}

// Find returns the settings files of the Windows Terminals installed for
// the user whose %LOCALAPPDATA% is localAppData, reading environment
// variables with getenv: those of Windows Terminal, Windows Terminal
// Preview and unpackaged Windows Terminal that exist, or only the one
// $MAISYS_TERMINAL_SETTINGS names, if it's set.
func Find(localAppData string, getenv func(string) string) []Settings {
	if p := getenv(SettingsEnv); p != "" {
		return []Settings{{Path: p}}
	}
	candidates := []Settings{
		{Path: filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "LocalState", "settings.json")},
		{Path: filepath.Join(localAppData, "Packages", "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe", "LocalState", "settings.json"), Name: "Windows Terminal Preview"},
		{Path: filepath.Join(localAppData, "Microsoft", "Windows Terminal", "settings.json"), Name: "Windows Terminal (unpackaged)"},
	}
	var found []Settings
	for _, s := range candidates {
		if info, err := os.Stat(s.Path); err == nil && info.Mode().IsRegular() {
			found = append(found, s)
		}
	}
	return found
}

// DefaultProfile makes the profile of a WSL distro the default profile in
// a Windows Terminal settings file. Its check reports "~ default profile →
// fedora-dev" if the file's defaultProfile is another profile, and
// nothing otherwise. Without a settings file, which Windows Terminal writes
// the first time it starts, it only says why there's nothing to do.
//
// The distro's profile is found from the registry, as WSL and Windows
// Terminal make it: the profile in the Terminal fragment WSL wrote for the
// distro; for a distro installed from a .wsl image, which WSL writes a
// fragment for, a version 5 UUID of the distro's ID in WSL's namespace
// (see ModernGUID); and for an older distro, a version 5 UUID of its name
// in Windows Terminal's (see LegacyGUID). A distro the same sync creates is
// looked up when it's applied, once it exists.
type DefaultProfile struct {
	// Settings is the settings file, or has an empty Path if Windows
	// Terminal has none.
	Settings Settings
	// Distro is the distro whose profile becomes the default, or "" if
	// none is chosen.
	Distro string
}

var _ resource.Resource = (*DefaultProfile)(nil)

// ID returns "terminal-profile:PATH", or "terminal-profile" without a
// settings file.
func (p *DefaultProfile) ID() string {
	if p.Settings.Path == "" {
		return Kind
	}
	return Kind + ":" + filepath.ToSlash(p.Settings.Path)
}

// Kind returns "terminal-profile".
func (p *DefaultProfile) Kind() string { return Kind }

// Targets returns nothing: the settings file is Windows Terminal's, and
// maisys only sets one value in it, so it doesn't claim the file.
func (p *DefaultProfile) Targets() []string { return nil }

// Privileged returns false.
func (p *DefaultProfile) Privileged() bool { return false }

// Check compares the settings file's default profile with the distro's.
func (p *DefaultProfile) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	switch {
	case p.Distro == "":
		return resource.Change{Notes: []string{"wsl_host.default_distro isn't set, so there's no default profile to set in Windows Terminal"}}, nil
	case p.Settings.Path == "":
		return resource.Change{Notes: []string{"Windows Terminal has no settings file yet, which it writes the first time it starts: the default profile is set by the first maisys sync after that"}}, nil
	}
	data, err := os.ReadFile(p.Settings.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return resource.Change{Notes: []string{fmt.Sprintf("there's no %s for Windows Terminal: the default profile is set by the first maisys sync after Windows Terminal has written it", p.Settings.Path)}}, nil
	}
	if err != nil {
		return resource.Change{}, fmt.Errorf("reading %s: %w", p.Settings.Path, err)
	}
	current, err := DefaultOf(data)
	if err != nil {
		return resource.Change{}, fmt.Errorf("reading %s: %w", p.Settings.Path, err)
	}
	guid, err := p.guid(rt)
	if err != nil {
		return resource.Change{}, err
	}
	if guid != "" && strings.EqualFold(current, guid) {
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Update, Summary: p.summary()}, nil
}

// summary describes the change.
func (p *DefaultProfile) summary() string {
	s := "default profile → " + p.Distro
	if p.Settings.Name != "" {
		s += " (" + p.Settings.Name + ")"
	}
	return s
}

// Apply sets the settings file's default profile to the distro's.
func (p *DefaultProfile) Apply(_ context.Context, rt *resource.Runtime) (state.Record, error) {
	guid, err := p.guid(rt)
	if err != nil {
		return state.Record{}, err
	}
	if guid == "" {
		return state.Record{}, fmt.Errorf("there's no distro %s, so Windows Terminal has no profile for it", p.Distro)
	}
	info, err := os.Stat(p.Settings.Path)
	if err != nil {
		return state.Record{}, err
	}
	data, err := os.ReadFile(p.Settings.Path)
	if err != nil {
		return state.Record{}, err
	}
	edited, err := SetDefault(data, guid)
	if err != nil {
		return state.Record{}, fmt.Errorf("editing %s: %w", p.Settings.Path, err)
	}
	if !bytes.Equal(edited, data) {
		if err := write(p.Settings.Path, edited, info.Mode().Perm()); err != nil {
			return state.Record{}, err
		}
	}
	return state.Record{Paths: []string{p.Settings.Path}, Details: map[string]string{DetailGUID: guid, "distro": p.Distro}}, nil
}

// guid returns the GUID of the distro's profile, or "" if the distro
// doesn't exist yet.
func (p *DefaultProfile) guid(rt *resource.Runtime) (string, error) {
	reg, err := wsl.RegistryFor(rt)
	if err != nil {
		return "", err
	}
	distros, err := reg.Distros()
	if err != nil {
		return "", err
	}
	d, ok := wsl.Find(distros, p.Distro)
	if !ok {
		return "", nil
	}
	return ProfileGUID(d), nil
}

// The namespaces of the version 5 UUIDs of WSL distros' profiles.
var (
	// wslNamespace is WSL's, for the profiles it writes in fragments
	// (WslTerminalNamespace in WSL's source).
	wslNamespace = [16]byte{0xbe, 0x93, 0x72, 0xfe, 0x59, 0xe1, 0x48, 0x76, 0xbd, 0xa9, 0xc3, 0x3c, 0x8f, 0x2f, 0x1a, 0xf1}
	// terminalNamespace is Windows Terminal's, for the profiles it
	// generates itself.
	terminalNamespace = [16]byte{0x2b, 0xde, 0x4a, 0x90, 0xd0, 0x5f, 0x40, 0x1c, 0x94, 0x92, 0xe4, 0x08, 0x84, 0xea, 0xd1, 0xd8}
)

// ProfileGUID returns the GUID of the Windows Terminal profile of the
// distro d: the one in the fragment WSL wrote for it, if it can be read;
// otherwise, for a distro installed from a .wsl image, ModernGUID, and for
// an older one, LegacyGUID.
func ProfileGUID(d wsl.Registered) string {
	if d.TerminalProfile != "" {
		if guid, err := fragmentGUID(d.TerminalProfile); err == nil && guid != "" {
			return guid
		}
	}
	if d.Modern {
		return ModernGUID(d.ID)
	}
	return LegacyGUID(d.Name)
}

// fragmentGUID returns the GUID of the first profile in the fragment at
// path.
func fragmentGUID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v, err := hujson.Parse(data)
	if err != nil {
		return "", err
	}
	profiles := v.Find("/profiles")
	if profiles == nil {
		return "", errors.New("no profiles")
	}
	arr, ok := profiles.Value.(*hujson.Array)
	if !ok {
		return "", errors.New("profiles isn't a list")
	}
	for _, e := range arr.Elements {
		if g, ok := stringMember(e, "guid"); ok {
			return g, nil
		}
	}
	return "", nil
}

// ModernGUID returns the GUID of the profile WSL writes for a distro
// installed from a .wsl image, whose ID is id, such as
// "{0a1b2c3d-...}": a version 5 UUID of the ID, in lower case with braces
// and in UTF-16, in WSL's namespace.
func ModernGUID(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if !strings.HasPrefix(id, "{") {
		id = "{" + id + "}"
	}
	return uuid5(wslNamespace, utf16LE(id))
}

// LegacyGUID returns the GUID of the profile Windows Terminal generates for
// an older distro called name: a version 5 UUID of its name, in UTF-16,
// in Windows Terminal's namespace.
func LegacyGUID(name string) string {
	return uuid5(terminalNamespace, utf16LE(name))
}

// uuid5 returns the version 5 UUID of name in namespace, in lower case
// with braces, as Windows Terminal writes GUIDs.
func uuid5(namespace [16]byte, name []byte) string {
	h := sha1.New() //nolint:gosec // UUIDv5 is defined with SHA-1.
	h.Write(namespace[:])
	h.Write(name)
	var u [16]byte
	copy(u[:], h.Sum(nil))
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("{%08x-%04x-%04x-%04x-%012x}",
		binary.BigEndian.Uint32(u[0:4]), binary.BigEndian.Uint16(u[4:6]), binary.BigEndian.Uint16(u[6:8]),
		binary.BigEndian.Uint16(u[8:10]), u[10:16])
}

// utf16LE returns s in UTF-16, little-endian, without a byte order mark.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(units))
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

// bom is the UTF-8 byte order mark, which a settings file may start with.
var bom = []byte{0xef, 0xbb, 0xbf}

// DefaultOf returns the defaultProfile of the settings file data, or "" if
// it has none.
func DefaultOf(data []byte) (string, error) {
	v, err := hujson.Parse(bytes.TrimPrefix(data, bom))
	if err != nil {
		return "", err
	}
	g, _ := stringMember(v, "defaultProfile")
	return g, nil
}

// SetDefault returns the settings file data with its defaultProfile set to
// guid, and everything else as it was, comments and formatting included.
// If there's no defaultProfile, it's added at the top, indented like the
// other settings.
func SetDefault(data []byte, guid string) ([]byte, error) {
	hasBOM := bytes.HasPrefix(data, bom)
	v, err := hujson.Parse(bytes.TrimPrefix(data, bom))
	if err != nil {
		return nil, err
	}
	obj, ok := v.Value.(*hujson.Object)
	if !ok {
		return nil, errors.New("the settings aren't a JSON object")
	}
	set := false
	for i := range obj.Members {
		if name, ok := obj.Members[i].Name.Value.(hujson.Literal); ok && name.String() == "defaultProfile" {
			obj.Members[i].Value.Value = hujson.String(guid)
			set = true
		}
	}
	if !set {
		indent := hujson.Extra("\n    ")
		if len(obj.Members) > 0 {
			indent = lastLineOf(obj.Members[0].Name.BeforeExtra)
		}
		obj.Members = append([]hujson.ObjectMember{{
			Name:  hujson.Value{BeforeExtra: indent, Value: hujson.String("defaultProfile")},
			Value: hujson.Value{BeforeExtra: hujson.Extra(" "), Value: hujson.String(guid)},
		}}, obj.Members...)
		if len(obj.Members) == 1 {
			obj.AfterExtra = hujson.Extra("\n")
		}
	}
	out := v.Pack()
	if hasBOM {
		out = append(bytes.Clone(bom), out...)
	}
	return out, nil
}

// lastLineOf returns the line break and indentation at the end of extra,
// the space before a member, without the comments before it.
func lastLineOf(extra hujson.Extra) hujson.Extra {
	i := bytes.LastIndexByte(extra, '\n')
	if i < 0 || len(bytes.TrimSpace(extra[i:])) > 0 {
		return hujson.Extra("\n    ")
	}
	return append(hujson.Extra(nil), extra[i:]...)
}

// stringMember returns the string value of the member called name of the
// object v, and whether it has one.
func stringMember(v hujson.Value, name string) (string, bool) {
	obj, ok := v.Value.(*hujson.Object)
	if !ok {
		return "", false
	}
	for _, m := range obj.Members {
		n, ok := m.Name.Value.(hujson.Literal)
		if !ok || n.String() != name {
			continue
		}
		if lit, ok := m.Value.Value.(hujson.Literal); ok && lit.Kind() == '"' {
			return lit.String(), true
		}
	}
	return "", false
}

// write writes data to path with permissions perm, through a temporary
// file in the same folder, so the file is never half written.
func write(path string, data []byte, perm fs.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".maisys-*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			err = fmt.Errorf("writing %s: %w", path, err)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(perm); err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Remover handles the records of default profiles once windows_terminal is
// turned off: the default profile stays as it is, and the record is
// dropped without a line in the plan.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "terminal-profile".
func (Remover) Kind() string { return Kind }

// CheckRemove returns no change: the default profile stays as it is.
func (Remover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

// Remove does nothing.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }
