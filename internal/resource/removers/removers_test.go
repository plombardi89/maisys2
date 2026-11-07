package removers_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource/checkout"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/gitconfig"
	"github.com/plombardi89/maisys2/internal/resource/goinstall"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/resource/link"
	"github.com/plombardi89/maisys2/internal/resource/loginshell"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/removers"
	"github.com/plombardi89/maisys2/internal/resource/repo"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/unit"
	"github.com/plombardi89/maisys2/internal/resource/uvtool"
)

// TestEveryKindHasARemover checks that a record of every resource kind can
// be handled once it's taken out of the config. Without its remover, a
// kind would be reported as "no longer used" for ever. Add new kinds here
// and to All.
func TestEveryKindHasARemover(t *testing.T) {
	kinds := []string{
		pkg.Kind, repo.Kind, repo.ArchKind, tool.Kind, file.Kind, link.Kind,
		gitconfig.Kind, unit.Kind, loginshell.Kind, checkout.Kind,
		installer.Kind, goinstall.Kind, uvtool.Kind,
	}
	got := slices.Sorted(maps.Keys(removers.All()))
	slices.Sort(kinds)
	if !slices.Equal(got, kinds) {
		t.Errorf("removers for %q, want one for each of %q", got, kinds)
	}
}
