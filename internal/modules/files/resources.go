package files

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
)

var _ engine.Module = Module{}

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns a file for each file the enabled items copy, item by
// item in order of their names: the source file, or every file in the
// source directory. Items turned off have none, so the files they copied
// before are removed as the removal rules say.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the files module can't use a %T config", cfg)
	}
	var rs []resource.Resource
	for _, name := range slices.Sorted(maps.Keys(c.Items)) {
		item := c.Items[name]
		if !item.Enabled {
			continue
		}
		if rt.RepoFS == nil {
			return nil, errors.New("the config repo's files aren't available")
		}
		mode, err := file.ParseMode(item.Mode)
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", name, err)
		}
		src := file.Source{
			FS:       rt.RepoFS,
			Path:     item.Source,
			Target:   item.Target,
			Template: item.Template,
			Vars:     c.Vars,
			Mode:     mode,
			CRLF:     item.EOL == "crlf",
		}
		files, err := src.Files()
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", name, err)
		}
		for _, f := range files {
			rs = append(rs, f)
		}
	}
	return rs, nil
}
