package facts

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
)

func TestSupport(t *testing.T) {
	fedora := func(version string) System {
		return System{OS: config.Fedora, OSID: "fedora", OSVersion: version, PrettyName: "Fedora Linux " + version, Arch: config.AMD64, RawArch: "amd64"}
	}
	ubuntu := func(version string) System {
		return System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: version, PrettyName: "Ubuntu " + version, Arch: config.ARM64, RawArch: "arm64"}
	}
	windows := func(build uint32) System {
		return System{
			OS: config.Windows, OSID: "windows", OSVersion: strconv.FormatUint(uint64(build), 10),
			PrettyName: windowsName(build), Arch: config.AMD64, RawArch: "amd64",
		}
	}
	riscv := fedora("44")
	riscv.Arch, riscv.RawArch = "", "riscv64"
	windowsARM := windows(26100)
	windowsARM.Arch, windowsARM.RawArch = config.ARM64, "arm64"

	today := time.Date(2026, time.November, 6, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		system System
		now    time.Time
		want   Support
	}{
		{"Fedora 43", fedora("43"), today, Support{Supported: true}},
		{"Fedora 44", fedora("44"), today, Support{Supported: true}},
		{"Fedora 45", fedora("45"), today, Support{Supported: true}},
		{
			name:   "Fedora 43 the moment before its end of life",
			system: fedora("43"),
			now:    time.Date(2026, time.December, 8, 23, 59, 59, 0, time.UTC),
			want:   Support{Supported: true},
		},
		{
			name:   "Fedora 43 on its end of life",
			system: fedora("43"),
			now:    time.Date(2026, time.December, 9, 0, 0, 0, 0, time.UTC),
			want:   Support{Reason: "Fedora 43 isn't supported any more (end of life 2026-12-09); maisys may still work"},
		},
		{
			name:   "Fedora 42, past its end of life",
			system: fedora("42"),
			now:    today,
			want:   Support{Reason: "Fedora 42 isn't supported any more (end of life 2026-05-13); maisys may still work"},
		},
		{
			name:   "Fedora 42 before its end of life",
			system: fedora("42"),
			now:    time.Date(2026, time.May, 12, 0, 0, 0, 0, time.UTC),
			want:   Support{Supported: true},
		},
		{
			name:   "a Fedora release older than the list",
			system: fedora("40"),
			now:    today,
			want:   Support{Reason: "Fedora 40 isn't one of the releases maisys tests on (43, 44 and 45); maisys may still work"},
		},
		{
			name:   "a Fedora release newer than the list",
			system: fedora("46"),
			now:    time.Date(2027, time.June, 1, 0, 0, 0, 0, time.UTC),
			want:   Support{Reason: "Fedora 46 isn't one of the releases maisys tests on (45); maisys may still work"},
		},
		{
			name:   "every Fedora release in the list has reached its end of life",
			system: fedora("47"),
			now:    time.Date(2028, time.January, 1, 0, 0, 0, 0, time.UTC),
			want:   Support{Reason: "Fedora 47 isn't one of the releases maisys tests on; maisys may still work"},
		},
		{"Ubuntu 24.04", ubuntu("24.04"), today, Support{Supported: true}},
		{"Ubuntu 26.04", ubuntu("26.04"), today, Support{Supported: true}},
		{
			name:   "an Ubuntu release that isn't LTS",
			system: ubuntu("25.10"),
			now:    today,
			want:   Support{Reason: "Ubuntu 25.10 isn't one of the LTS releases maisys tests on (24.04 and 26.04); maisys may still work"},
		},
		{
			name:   "an older Ubuntu LTS release",
			system: ubuntu("22.04"),
			now:    today,
			want:   Support{Reason: "Ubuntu 22.04 isn't one of the LTS releases maisys tests on (24.04 and 26.04); maisys may still work"},
		},
		{"Windows 11", windows(26100), today, Support{Supported: true}},
		{"the first Windows 11 build", windows(22000), today, Support{Supported: true}},
		{
			name:   "Windows 10",
			system: windows(19045),
			now:    today,
			want:   Support{Reason: "Windows 10 (build 19045) isn't supported: maisys needs Windows 11 (build 22000 or newer)"},
		},
		{
			name:   "Windows on arm64",
			system: windowsARM,
			now:    today,
			want:   Support{Reason: "Windows hosts must be amd64, but this system is arm64"},
		},
		{
			name:   "a Linux distribution maisys doesn't run on",
			system: System{OSID: "debian", OSVersion: "13", PrettyName: "Debian GNU/Linux 13 (trixie)", Arch: config.AMD64, RawArch: "amd64"},
			now:    today,
			want:   Support{Reason: "maisys runs on Fedora, Ubuntu and Windows 11, but this is Debian GNU/Linux 13 (trixie)"},
		},
		{
			name:   "an architecture maisys doesn't run on",
			system: riscv,
			now:    today,
			want:   Support{Reason: "maisys runs on amd64 and arm64, but this system is riscv64"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.system.Support(tt.now); got != tt.want {
				t.Errorf("Support = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestPolicyData checks that the support policy's lists are in order, so
// the messages list releases oldest first.
func TestPolicyData(t *testing.T) {
	for i, r := range fedoraReleases {
		v, err := strconv.Atoi(r.version)
		if err != nil {
			t.Errorf("Fedora release %q isn't a number", r.version)
			continue
		}
		if i == 0 {
			continue
		}
		prev := fedoraReleases[i-1]
		if pv, _ := strconv.Atoi(prev.version); v <= pv || !r.eol.After(prev.eol) {
			t.Errorf("Fedora %s (end of life %s) comes after Fedora %s (end of life %s), want both later",
				r.version, r.eol.Format(time.DateOnly), prev.version, prev.eol.Format(time.DateOnly))
		}
	}

	lts := regexp.MustCompile(`^[0-9][02468]\.04$`)
	if len(ubuntuLTS) != 2 {
		t.Errorf("ubuntuLTS = %q, want the two newest LTS releases", ubuntuLTS)
	}
	for _, v := range ubuntuLTS {
		if !lts.MatchString(v) {
			t.Errorf("Ubuntu %q isn't an LTS release", v)
		}
	}
	if !slices.IsSorted(ubuntuLTS) {
		t.Errorf("ubuntuLTS = %q, want them oldest first", ubuntuLTS)
	}
}

// TestPolicyMatchesCI checks that CI tests the releases the support policy
// lists: the Ubuntu LTS releases, the newest of them on arm64, and the
// newest Fedora releases. The Fedora list may also hold releases that have
// reached end of life, which CI no longer tests.
func TestPolicyMatchesCI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var ubuntu, ubuntuARM []string
	for _, m := range regexp.MustCompile(`ubuntu-([0-9]{2}\.[0-9]{2})(-arm)?\b`).FindAllStringSubmatch(string(data), -1) {
		if m[2] != "" {
			ubuntuARM = append(ubuntuARM, m[1])
		} else {
			ubuntu = append(ubuntu, m[1])
		}
	}
	slices.Sort(ubuntu)
	if ubuntu = slices.Compact(ubuntu); !slices.Equal(ubuntu, ubuntuLTS) {
		t.Errorf("ci.yml tests Ubuntu %q, but ubuntuLTS is %q", ubuntu, ubuntuLTS)
	}
	if newest := ubuntuLTS[len(ubuntuLTS)-1]; !slices.Equal(ubuntuARM, []string{newest}) {
		t.Errorf("ci.yml tests Ubuntu %q on arm64, want only the newest LTS release, %s", ubuntuARM, newest)
	}

	matrix := regexp.MustCompile(`(?m)^\s*version: \[(.*)\]\s*$`).FindStringSubmatch(string(data))
	if matrix == nil {
		t.Fatal("can't find the Fedora versions in ci.yml's test-fedora matrix")
	}
	var fedora []string
	for _, m := range regexp.MustCompile(`"([0-9]+)"`).FindAllStringSubmatch(matrix[1], -1) {
		fedora = append(fedora, m[1])
	}
	var newest []string
	for _, r := range fedoraReleases[max(0, len(fedoraReleases)-len(fedora)):] {
		newest = append(newest, r.version)
	}
	if len(fedora) == 0 || !slices.Equal(fedora, newest) {
		t.Errorf("ci.yml tests Fedora %q, want the newest releases in fedoraReleases, %q", fedora, newest)
	}
}
