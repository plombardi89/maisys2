package tool

import (
	"strconv"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/version"
)

// The upstreams' names for the architectures, as they use them in their
// file names.
var (
	goArch     = map[config.Arch]string{config.AMD64: "amd64", config.ARM64: "arm64"}
	gnuArch    = map[config.Arch]string{config.AMD64: "x86_64", config.ARM64: "aarch64"}
	lnavArch   = map[config.Arch]string{config.AMD64: "x86_64", config.ARM64: "arm64"}
	protocArch = map[config.Arch]string{config.AMD64: "x86_64", config.ARM64: "aarch_64"}
	mageArch   = map[config.Arch]string{config.AMD64: "64bit", config.ARM64: "ARM64"}
	// JetBrains marks only the arm64 downloads.
	jetBrainsArch = map[config.Arch]string{config.AMD64: "", config.ARM64: "-aarch64"}
)

// release is a GitHub repo's releases, whose tags are a prefix followed by
// the version.
type release struct {
	repo, tagPrefix string
}

func (r release) tag(v string) string { return r.tagPrefix + v }

func (r release) url(v, file string) string {
	return "https://github.com/" + r.repo + "/releases/download/" + r.tag(v) + "/" + file
}

// asset returns a Spec.Asset for the release file that file names.
func (r release) asset(file func(v, arch string) string) func(v, arch string) download.Asset {
	return func(v, arch string) download.Asset {
		name := file(v, arch)
		return download.Asset{URL: r.url(v, name), Name: name}
	}
}

// latest returns a resolver for the latest release.
func (r release) latest() version.Resolver {
	return version.GitHub{Repo: r.repo, TagPrefix: r.tagPrefix}
}

// sums returns a Spec.Checksum for a checksums file in the release, which
// file names.
func (r release) sums(file func(v string) string) func(string, download.Asset) download.Checksum {
	return func(v string, _ download.Asset) download.Checksum {
		return download.SumsFile{URL: r.url(v, file(v))}
	}
}

// digest returns a Spec.Checksum for GitHub's digests of the release's
// files.
func (r release) digest() func(string, download.Asset) download.Checksum {
	return func(v string, _ download.Asset) download.Checksum {
		return download.GitHubDigest{Repo: r.repo, Tag: r.tag(v)}
	}
}

// sidecar is a Spec.Checksum for a ".sha256" file next to each download.
func sidecar(string, download.Asset) download.Checksum {
	return download.Sidecar{Suffix: ".sha256"}
}

// Go is the Go toolchain, from go.dev, with the go and gofmt commands. The
// toolchain keeps its go/ folder, so GOROOT is <version folder>/go.
func Go() Spec {
	return Spec{
		Name:   "go",
		Latest: version.Go{},
		Arch:   goArch,
		Asset: func(v, arch string) download.Asset {
			name := "go" + v + ".linux-" + arch + ".tar.gz"
			return download.Asset{URL: "https://go.dev/dl/" + name, Name: name}
		},
		Checksum: func(string, download.Asset) download.Checksum { return download.GoIndex{} },
		Archive:  download.Archive{Format: download.TarGz},
		Links:    []Link{{Name: "go", Target: "go/bin/go"}, {Name: "gofmt", Target: "go/bin/gofmt"}},
	}
}

// Kubectl is kubectl, the Kubernetes command-line tool, from dl.k8s.io.
func Kubectl() Spec {
	return Spec{
		Name:   "kubectl",
		Latest: version.Text{URL: version.DefaultKubernetesStable},
		Arch:   goArch,
		Asset: func(v, arch string) download.Asset {
			return download.Asset{URL: "https://dl.k8s.io/release/v" + v + "/bin/linux/" + arch + "/kubectl", Name: "kubectl"}
		},
		Checksum: sidecar,
		Archive:  download.Archive{Format: download.Binary, Binary: "kubectl"},
		Links:    []Link{{Name: "kubectl", Target: "kubectl"}},
	}
}

// Mage is mage, the make-like build tool for Go, from its GitHub releases.
func Mage() Spec {
	r := release{repo: "magefile/mage", tagPrefix: "v"}
	return Spec{
		Name:     "mage",
		Latest:   r.latest(),
		Arch:     mageArch,
		Asset:    r.asset(func(v, arch string) string { return "mage_" + v + "_Linux-" + arch + ".tar.gz" }),
		Checksum: r.sums(func(v string) string { return "mage_" + v + "_checksums.txt" }),
		Archive:  download.Archive{Format: download.TarGz},
		Links:    []Link{{Name: "mage", Target: "mage"}},
	}
}

// GolangciLint is golangci-lint, the Go linters runner, from its GitHub
// releases.
func GolangciLint() Spec {
	r := release{repo: "golangci/golangci-lint", tagPrefix: "v"}
	return Spec{
		Name:     "golangci-lint",
		Latest:   r.latest(),
		Arch:     goArch,
		Asset:    r.asset(func(v, arch string) string { return "golangci-lint-" + v + "-linux-" + arch + ".tar.gz" }),
		Checksum: r.sums(func(v string) string { return "golangci-lint-" + v + "-checksums.txt" }),
		Archive:  download.Archive{Format: download.TarGz, Strip: 1},
		Links:    []Link{{Name: "golangci-lint", Target: "golangci-lint"}},
	}
}

// Stern is stern, which tails the logs of many Kubernetes pods, from its
// GitHub releases.
func Stern() Spec {
	r := release{repo: "stern/stern", tagPrefix: "v"}
	return Spec{
		Name:     "stern",
		Latest:   r.latest(),
		Arch:     goArch,
		Asset:    r.asset(func(v, arch string) string { return "stern_" + v + "_linux_" + arch + ".tar.gz" }),
		Checksum: r.sums(func(string) string { return "checksums.txt" }),
		Archive:  download.Archive{Format: download.TarGz},
		Links:    []Link{{Name: "stern", Target: "stern"}},
	}
}

// Yq is yq, the YAML processor, from its GitHub releases: the plain binary,
// verified against the release's table of checksums.
func Yq() Spec {
	r := release{repo: "mikefarah/yq", tagPrefix: "v"}
	return Spec{
		Name:   "yq",
		Latest: r.latest(),
		Arch:   goArch,
		Asset:  r.asset(func(_, arch string) string { return "yq_linux_" + arch }),
		Checksum: func(v string, _ download.Asset) download.Checksum {
			return download.ChecksumTable{URL: r.url(v, "checksums"), OrderURL: r.url(v, "checksums_hashes_order")}
		},
		Archive: download.Archive{Format: download.Binary, Binary: "yq"},
		Links:   []Link{{Name: "yq", Target: "yq"}},
	}
}

// Lnav is lnav, the log file navigator, from its GitHub releases (the
// statically linked musl build). lnav publishes no checksums, so GitHub's
// digests are used.
func Lnav() Spec {
	r := release{repo: "tstack/lnav", tagPrefix: "v"}
	return Spec{
		Name:     "lnav",
		Latest:   r.latest(),
		Arch:     lnavArch,
		Asset:    r.asset(func(v, arch string) string { return "lnav-" + v + "-linux-musl-" + arch + ".zip" }),
		Checksum: r.digest(),
		Archive:  download.Archive{Format: download.Zip, Strip: 1},
		Links:    []Link{{Name: "lnav", Target: "lnav"}},
	}
}

// Protoc is protoc, the protocol buffers compiler, with its include/ files,
// from protobuf's GitHub releases. They publish no checksums, so GitHub's
// digests are used.
func Protoc() Spec {
	r := release{repo: "protocolbuffers/protobuf", tagPrefix: "v"}
	return Spec{
		Name:     "protoc",
		Latest:   r.latest(),
		Arch:     protocArch,
		Asset:    r.asset(func(v, arch string) string { return "protoc-" + v + "-linux-" + arch + ".zip" }),
		Checksum: r.digest(),
		Archive:  download.Archive{Format: download.Zip},
		Links:    []Link{{Name: "protoc", Target: "bin/protoc"}},
	}
}

// Mutagen is mutagen, which syncs files with remote machines, from its
// GitHub releases. Its agents bundle stays next to the binary, where
// mutagen looks for it.
func Mutagen() Spec {
	r := release{repo: "mutagen-io/mutagen", tagPrefix: "v"}
	return Spec{
		Name:     "mutagen",
		Latest:   r.latest(),
		Arch:     goArch,
		Asset:    r.asset(func(v, arch string) string { return "mutagen_linux_" + arch + "_v" + v + ".tar.gz" }),
		Checksum: r.sums(func(string) string { return "SHA256SUMS" }),
		Archive:  download.Archive{Format: download.TarGz},
		Links:    []Link{{Name: "mutagen", Target: "mutagen"}},
	}
}

// UV is uv, the Python package and project manager, from its GitHub
// releases, with the uv and uvx commands.
func UV() Spec {
	r := release{repo: "astral-sh/uv"}
	return Spec{
		Name:     "uv",
		Latest:   r.latest(),
		Arch:     gnuArch,
		Asset:    r.asset(func(_, arch string) string { return "uv-" + arch + "-unknown-linux-gnu.tar.gz" }),
		Checksum: sidecar,
		Archive:  download.Archive{Format: download.TarGz, Strip: 1},
		Links:    []Link{{Name: "uv", Target: "uv"}, {Name: "uvx", Target: "uvx"}},
	}
}

// IntelliJ is IntelliJ IDEA Ultimate, from JetBrains, with the idea
// command.
func IntelliJ() Spec {
	return Spec{
		Name:   "intellij",
		Latest: version.JetBrains{Code: "IIU"},
		Arch:   jetBrainsArch,
		Asset: func(v, arch string) download.Asset {
			name := intelliJPrefix(v) + v + arch + ".tar.gz"
			return download.Asset{URL: "https://download.jetbrains.com/idea/" + name, Name: name}
		},
		Checksum: sidecar,
		Archive:  download.Archive{Format: download.TarGz, Strip: 1},
		Links:    []Link{{Name: "idea", Target: "bin/idea"}},
	}
}

// intelliJPrefix returns how IntelliJ's download for version v starts:
// "idea-" since 2025.3, when JetBrains merged the Community and Ultimate
// editions into one, and "ideaIU-" before.
func intelliJPrefix(v string) string {
	parts := strings.SplitN(v, ".", 3)
	year, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if year > 2025 || year == 2025 && minor >= 3 {
		return "idea-"
	}
	return "ideaIU-"
}
