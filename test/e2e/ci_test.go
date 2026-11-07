package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/test/fixture"
)

// TestDistrosMatchCI checks that CI's e2e job runs every distro in
// fixture.Distros, and the newest Ubuntu release among them on arm64 too.
// It also checks that they're the Fedora releases CI's unit tests run on;
// internal/facts checks those, and the Ubuntu releases, against the
// support policy.
func TestDistrosMatchCI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := section(string(data), "  e2e:\n")
	if err != nil {
		t.Fatal(err)
	}

	var amd64, arm64 []string
	for _, entry := range strings.Split(job, "- distro: ")[1:] {
		distro, rest, _ := strings.Cut(entry, "\n")
		runner, _, _ := strings.Cut(strings.TrimSpace(rest), "\n")
		if strings.HasSuffix(strings.TrimPrefix(runner, "runner: "), "-arm") {
			arm64 = append(arm64, distro)
		} else {
			amd64 = append(amd64, distro)
		}
	}
	var want, fedora []string
	var newestUbuntu string
	for _, d := range fixture.Distros {
		want = append(want, d.Name())
		if d.OS == config.Fedora {
			fedora = append(fedora, d.Version)
		} else {
			newestUbuntu = d.Name()
		}
	}
	if !slices.Equal(amd64, want) {
		t.Errorf("ci.yml's e2e job runs %q, want fixture.Distros, %q", amd64, want)
	}
	if !slices.Equal(arm64, []string{newestUbuntu}) {
		t.Errorf("ci.yml's e2e job runs %q on arm64, want the newest Ubuntu release in fixture.Distros, %s", arm64, newestUbuntu)
	}

	matrix := regexp.MustCompile(`(?m)^\s*version: \[(.*)\]\s*$`).FindStringSubmatch(string(data))
	if matrix == nil {
		t.Fatal("can't find the Fedora versions in ci.yml's test-fedora matrix")
	}
	var unit []string
	for _, m := range regexp.MustCompile(`"([0-9]+)"`).FindAllStringSubmatch(matrix[1], -1) {
		unit = append(unit, m[1])
	}
	if !slices.Equal(fedora, unit) {
		t.Errorf("fixture.Distros has Fedora %q, but ci.yml's unit tests run on Fedora %q", fedora, unit)
	}
}

// TestContainerfiles checks that there's a containerfile for the OS of each
// distro in fixture.Distros.
func TestContainerfiles(t *testing.T) {
	for _, d := range fixture.Distros {
		if _, err := os.Stat(string(d.OS) + ".containerfile"); err != nil {
			t.Errorf("no containerfile for %s: %v", d.Name(), err)
		}
	}
}

// section returns the part of a YAML file after the line start, up to the
// next line that's indented no more than start.
func section(data, start string) (string, error) {
	_, rest, ok := strings.Cut(data, start)
	if !ok {
		return "", errors.New("can't find " + strings.TrimSpace(start))
	}
	indent := start[:len(start)-len(strings.TrimLeft(start, " "))]
	for j := 0; j < len(rest); {
		line, _, _ := strings.Cut(rest[j:], "\n")
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, indent+" ") {
			return rest[:j], nil
		}
		j += len(line) + 1
	}
	return rest, nil
}
