package legacy

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/cleanup"
)

var _ engine.Module = Module{}

// protocReadme holds the sha256 sums of the readme.txt in protoc's release
// zips, which the Ansible setup unpacked into the home folder: the one in
// releases 21.12 to 36.2 at least, and the one in 3.20.3 before them. They
// were taken from the linux-x86_64 zips (and the linux-aarch_64 zip of
// 32.0, which has the same readme) on 2026-11-06.
var protocReadme = []string{
	"cb87be42344000337bef2e65de178c7e2f9cd7a1b7cd0e4284377a5d375db82f",
	"aa069f5fdbfcc2afdc69a5fee33f5fd59a36e61cbe61c05d23dfea97f6e864ca",
}

// goRoot is where the Ansible setup installed Go. Tests replace it.
var goRoot = "/usr/local/go"

// leftovers are what the Ansible setup left behind, in the order they're
// deleted.
func leftovers() []*cleanup.Leftover {
	return []*cleanup.Leftover{
		{Path: goRoot, Root: true},
		{Path: "~/bin/kubectl"},
		{Path: "~/bin/stern"},
		{Path: "~/bin/yq"},
		{Path: "~/bin/lnav"},
		{Path: "~/bin/protoc"},
		{Path: "~/bin/mutagen"},
		{Path: "~/go/bin/mage"},
		{Path: "~/go/bin/golangci-lint"},
		{Path: "~/include/google"},
		{Path: "~/readme.txt", SHA256: protocReadme},
		{Path: "~/Apps/idea"},
		{Path: "~/Apps/idea.bak"},
		{Path: "~/venv/maisys"},
	}
}

// DependsOn returns the modules that replace what the Ansible setup
// installed, and the shell module, which replaces the rc files that used
// it. The cleanup comes after them, and is skipped if one of them fails,
// so nothing is deleted before its replacement is in place.
func (Module) DependsOn() []string {
	return []string{
		"shell", "golang", "mage", "golangci_lint", "kubectl", "stern", "yq",
		"lnav", "protoc", "mutagen", "python_tools", "intellij",
	}
}

// Resources returns each thing the Ansible setup left behind. Those that
// are there are listed in the plan, to be deleted once it's confirmed:
// /usr/local/go as root, and the rest in your home folder. ~/readme.txt
// is only deleted if it's protoc's readme.
func (Module) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	var rs []resource.Resource
	for _, l := range leftovers() {
		rs = append(rs, l)
	}
	return rs, nil
}
