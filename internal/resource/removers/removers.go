// Package removers lists the remover of every resource kind, which handles
// the records of things taken out of the config, as issue #1's removal
// rules say.
package removers

import (
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/checkout"
	"github.com/plombardi89/maisys2/internal/resource/cleanup"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/gitconfig"
	"github.com/plombardi89/maisys2/internal/resource/goinstall"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/resource/link"
	"github.com/plombardi89/maisys2/internal/resource/loginshell"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/repo"
	"github.com/plombardi89/maisys2/internal/resource/sudoers"
	"github.com/plombardi89/maisys2/internal/resource/terminal"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/unit"
	"github.com/plombardi89/maisys2/internal/resource/uvtool"
	"github.com/plombardi89/maisys2/internal/resource/winget"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
)

// All returns the remover of every resource kind.
func All() resource.Removers {
	return resource.NewRemovers(
		pkg.Remover{},
		repo.Remover{},
		repo.ArchRemover{},
		tool.Remover{},
		file.Remover{},
		link.Remover{},
		gitconfig.Remover{},
		unit.Remover{},
		loginshell.Remover{},
		checkout.Remover{},
		installer.Remover{},
		goinstall.Remover{},
		uvtool.Remover{},
		sudoers.Remover{},
		cleanup.Remover{},
		winget.Remover{},
		wsl.Remover{},
		wsl.DistroRemover{},
		wsl.DefaultRemover{},
		terminal.Remover{},
	)
}
