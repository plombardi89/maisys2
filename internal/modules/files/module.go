// Package files is the files module.
package files

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the files module.
var Module = config.Module{
	Name:      "files",
	Summary:   "Copies files, directories and templates from the config repo into your home folder, and reports files changed since.",
	OS:        config.AllOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the files module's config.
type Config struct {
	config.Toggle
	// Vars holds values for templates, which read them as .Vars.
	Vars map[string]string `toml:"vars"`
	// Items are the files and directories to copy, each under a name you
	// choose. A later profile or the machine file can change one item, or
	// turn it off with enabled = false, without repeating the rest.
	Items map[string]Item `toml:"items"`
}

// Item is a file or directory to copy.
type Item struct {
	// Enabled copies the item. Items are on unless turned off.
	Enabled bool `toml:"enabled"`
	// Source is the path in the config repo. A directory is copied file by
	// file, and files in the target directory that don't come from it are
	// left alone.
	Source string `toml:"source"`
	// Target is where the item goes, inside your home folder, such as
	// "~/.gitconfig". On Windows, ~ is %USERPROFILE%.
	Target string `toml:"target"`
	// Template renders the source as a Go text/template, which can use
	// .Vars, .Facts and .Machine.
	Template bool `toml:"template"`
	// Mode sets the permissions, such as "0600". Without it, files keep
	// the executable bit they have in the config repo.
	Mode string `toml:"mode"`
	// EOL set to "crlf" converts line endings to CRLF, for Windows programs
	// that need them. Without it, line endings stay as they are in the
	// config repo.
	EOL string `toml:"eol"`
}

// SetDefaults turns new items on.
func (i *Item) SetDefaults() {
	i.Enabled = true
}
