// Package modules lists the built-in modules. Each module is a package
// below this one.
package modules

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules/files"
	"github.com/plombardi89/maisys2/internal/modules/golang"
	"github.com/plombardi89/maisys2/internal/modules/golangcilint"
	"github.com/plombardi89/maisys2/internal/modules/hashicorp"
	"github.com/plombardi89/maisys2/internal/modules/intellij"
	"github.com/plombardi89/maisys2/internal/modules/kubectl"
	"github.com/plombardi89/maisys2/internal/modules/legacy"
	"github.com/plombardi89/maisys2/internal/modules/lnav"
	"github.com/plombardi89/maisys2/internal/modules/mage"
	"github.com/plombardi89/maisys2/internal/modules/mutagen"
	"github.com/plombardi89/maisys2/internal/modules/opencode"
	"github.com/plombardi89/maisys2/internal/modules/packages"
	"github.com/plombardi89/maisys2/internal/modules/podman"
	"github.com/plombardi89/maisys2/internal/modules/protoc"
	"github.com/plombardi89/maisys2/internal/modules/pythontools"
	"github.com/plombardi89/maisys2/internal/modules/sdkman"
	"github.com/plombardi89/maisys2/internal/modules/shell"
	"github.com/plombardi89/maisys2/internal/modules/steam"
	"github.com/plombardi89/maisys2/internal/modules/stern"
	"github.com/plombardi89/maisys2/internal/modules/sudo"
	"github.com/plombardi89/maisys2/internal/modules/windowsterminal"
	"github.com/plombardi89/maisys2/internal/modules/winget"
	"github.com/plombardi89/maisys2/internal/modules/wsl"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
	"github.com/plombardi89/maisys2/internal/modules/yq"
)

// All returns every built-in module: the Linux modules, then the Windows
// ones, in the order the docs list them.
func All() []config.Module {
	return []config.Module{
		sudo.Module,
		packages.Module,
		files.Module,
		shell.Module,
		golang.Module,
		mage.Module,
		golangcilint.Module,
		kubectl.Module,
		stern.Module,
		yq.Module,
		lnav.Module,
		protoc.Module,
		mutagen.Module,
		hashicorp.Module,
		podman.Module,
		sdkman.Module,
		opencode.Module,
		pythontools.Module,
		steam.Module,
		intellij.Module,
		wsl.Module,
		legacy.Module,
		winget.Module,
		wslhost.Module,
		windowsterminal.Module,
	}
}
