package facts

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
)

// osReleasePaths are where os-release(5) says to look, in order. The second
// is only read if the first doesn't exist.
var osReleasePaths = []string{"/etc/os-release", "/usr/lib/os-release"}

// kernelReleasePath holds the kernel's release, which mentions Microsoft in
// WSL.
const kernelReleasePath = "/proc/sys/kernel/osrelease"

// DetectLinux detects the Linux distribution and whether it's running in
// WSL, reading files with readFile and environment variables with getenv.
// It leaves Arch, RawArch, Hostname and Root empty; Detect fills them in.
func DetectLinux(readFile func(name string) ([]byte, error), getenv func(key string) string) (*System, error) {
	release, err := readOSRelease(readFile)
	if err != nil {
		return nil, err
	}
	s := &System{
		OSID:       release["ID"],
		OSVersion:  release["VERSION_ID"],
		PrettyName: release["PRETTY_NAME"],
		WSLDistro:  getenv("WSL_DISTRO_NAME"),
	}
	// These defaults come from os-release(5).
	if s.OSID == "" {
		s.OSID = "linux"
	}
	if s.PrettyName == "" {
		name := release["NAME"]
		if name == "" {
			name = "Linux"
		}
		s.PrettyName = strings.TrimSpace(name + " " + s.OSVersion)
	}
	if id := config.OS(s.OSID); slices.Contains(config.LinuxOS(), id) {
		s.OS = id
	}

	// Some containers have no /proc; then only WSL_DISTRO_NAME can tell.
	kernel, _ := readFile(kernelReleasePath)
	s.WSL = s.WSLDistro != "" || strings.Contains(strings.ToLower(string(kernel)), "microsoft")
	return s, nil
}

// readOSRelease reads and parses the first os-release file that exists.
func readOSRelease(readFile func(name string) ([]byte, error)) (map[string]string, error) {
	for _, path := range osReleasePaths {
		data, err := readFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("can't tell which Linux distribution this is: %w", err)
		}
		release, err := ParseOSRelease(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		return release, nil
	}
	return nil, fmt.Errorf("can't tell which Linux distribution this is: neither %s exists", strings.Join(osReleasePaths, " nor "))
}

// osReleaseKey matches the variable names an os-release file may assign.
var osReleaseKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseOSRelease reads an os-release(5) file: one VARIABLE=value assignment
// per line, with blank lines and lines starting with "#" ignored. Values may
// be quoted with double quotes, inside which a backslash escapes `"`, `\`,
// `$` and "`", or with single quotes, inside which nothing is escaped.
// Outside quotes, a backslash escapes any character. Lines that aren't
// assignments are skipped, so it only returns an error if reading fails.
func ParseOSRelease(r io.Reader) (map[string]string, error) {
	vars := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !osReleaseKey.MatchString(key) {
			continue
		}
		vars[key] = unquote(strings.TrimLeft(value, " \t"))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return vars, nil
}

// unquote returns the value a shell would assign for s, except that an
// unterminated quote runs to the end of s.
func unquote(s string) string {
	var b strings.Builder
	var quote rune // the open quote, or 0 outside quotes
	escaped := false
	for _, c := range s {
		switch {
		case escaped:
			escaped = false
			if quote == '"' && !strings.ContainsRune("\"\\$`", c) {
				b.WriteRune('\\')
			}
			b.WriteRune(c)
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		default:
			b.WriteRune(c)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	return b.String()
}
