package shells

import (
	"fmt"
	"strings"
)

// header is how every file maisys writes for a shell starts.
var header = []string{
	"# Written by maisys from your config repo. The next sync backs up and",
	"# replaces any changes made here.",
}

// writer writes a shell's file in sections, each after a blank line and
// a comment saying what it is.
type writer struct {
	b strings.Builder
}

// lines writes lines as they are.
func (w *writer) lines(lines ...string) {
	for _, line := range lines {
		w.b.WriteString(line + "\n")
	}
}

// section starts a section with a heading.
func (w *writer) section(heading string) {
	w.b.WriteString("\n# " + heading + "\n")
}

// code writes code, ending it with a newline.
func (w *writer) code(code string) {
	w.b.WriteString(strings.TrimRight(code, "\n") + "\n")
}

func (w *writer) bytes() []byte {
	return []byte(w.b.String())
}

// key returns the shell module's config key for one of a shell's
// settings, such as "shell.zsh.snippets".
func key(shell, setting string) string {
	return "shell." + shell + "." + setting
}

// posix writes the files of bash and zsh, whose syntax is close enough to
// share.
type posix struct {
	shell Shell
	// step returns an init step's version for the shell.
	step func(InitStep) string
	// rc is the shell's rc file, as "~/...".
	rc string
	// system, if set, is a file of the system's settings that the rc file
	// loads first, if it exists.
	system string
	// ohMyZsh reports whether the shell can load oh-my-zsh.
	ohMyZsh bool
}

// env writes the environment file.
func (p posix) env(cfg Config) ([]byte, []Warning) {
	w := &writer{}
	w.lines(header...)
	w.lines("#")
	if cfg.ManageRC {
		w.lines("# The environment of the modules your config enables. " + p.rc + " loads it.")
	} else {
		w.lines(
			fmt.Sprintf("# Your %s settings and the environment of the modules your config", p.shell.Name()),
			fmt.Sprintf("# enables. Load it from your %s with:", p.rc),
			"#",
			"#   source "+p.shell.EnvFile(),
		)
		p.before(w, cfg)
	}
	warnings := p.environment(w, cfg.Env)
	if !cfg.ManageRC {
		p.sources(w, key(p.shell.Name(), "snippets"), cfg.Snippets)
	}
	return w.bytes(), warnings
}

// rcFile writes the rc file.
func (p posix) rcFile(cfg Config) []byte {
	name := p.shell.Name()
	w := &writer{}
	w.lines(
		fmt.Sprintf("# Written by maisys from your config repo, as %s is on. The", key(name, "manage_rc")),
		"# next sync backs up and replaces any changes made here: add your own",
		"# settings to the config repo as snippets instead.",
	)
	if p.system != "" {
		w.section("The system's settings")
		p.sourceIfExists(w, []part{{text: p.system}})
	}
	p.before(w, cfg)
	w.section("The environment of the modules your config enables")
	w.lines("source " + posixQuote(parsePath(p.shell.EnvFile())))
	p.sources(w, key(name, "snippets"), cfg.Snippets)
	w.section(SecretsFile + ", if there is one")
	p.sourceIfExists(w, parsePath(SecretsFile))
	return w.bytes()
}

// before writes what comes before the environment: the shell's variables,
// its pre-snippets and oh-my-zsh.
func (p posix) before(w *writer, cfg Config) {
	name := p.shell.Name()
	if len(cfg.Variables) > 0 {
		w.section(key(name, "variables"))
		for _, v := range cfg.Variables {
			w.lines(fmt.Sprintf("export %s=%s", v.Name, posixQuote(parseValue(v.Value))))
		}
	}
	p.sources(w, key(name, "pre_snippets"), cfg.PreSnippets)
	if omz := cfg.OhMyZsh; omz != nil && p.ohMyZsh {
		plugins := make([]string, len(omz.Plugins))
		for i, plugin := range omz.Plugins {
			plugins[i] = word(plugin)
		}
		w.section("oh-my-zsh (" + key(name, "oh_my_zsh") + "), with its own updater turned off")
		w.lines(
			"export ZSH="+posixQuote(parsePath(OhMyZshDir)),
			"ZSH_CUSTOM="+posixQuote([]part{{text: "ZSH", ref: true}, {text: strings.TrimPrefix(OhMyZshCustomDir, OhMyZshDir)}}),
			"ZSH_THEME="+posixQuote([]part{{text: omz.Theme}}),
			"plugins=("+strings.Join(plugins, " ")+")",
			"DISABLE_AUTO_UPDATE=true",
			"zstyle ':omz:update' mode disabled",
			`source "${ZSH}/oh-my-zsh.sh"`,
		)
	}
}

// environment writes env: its variables, its folders at the front of
// PATH, and its init steps, leaving out and warning about those without a
// version for the shell. The folders are added only if PATH doesn't have
// them yet, so sourcing the file again changes nothing.
func (p posix) environment(w *writer, env Env) []Warning {
	if len(env.Variables) > 0 {
		w.section("Variables")
		for _, v := range env.Variables {
			w.lines(fmt.Sprintf("export %s=%s", v.Name, posixQuote(parseValue(v.Value))))
		}
	}
	if len(env.Path) > 0 {
		dirs := make([]string, len(env.Path))
		for i, dir := range env.Path {
			dirs[i] = posixQuote(parseValue(dir))
		}
		w.section("Folders at the front of PATH, unless PATH has them already")
		w.lines(
			"__maisys_path=",
			"for __maisys_dir in "+strings.Join(dirs, " ")+"; do",
			`	case ":${__maisys_path}${PATH}:" in`,
			`	*":${__maisys_dir}:"*) ;;`,
			`	*) __maisys_path="${__maisys_path}${__maisys_dir}:" ;;`,
			"	esac",
			"done",
			`export PATH="${__maisys_path}${PATH}"`,
			"unset __maisys_dir __maisys_path",
		)
	}
	var warnings []Warning
	name := p.shell.Name()
	for _, step := range initSteps(env) {
		code := p.step(step)
		if code == "" {
			warnings = append(warnings, Warning{Shell: name, Step: step.Name})
			w.section(fmt.Sprintf("%s has no %s integration, so it's left out", step.Name, name))
			continue
		}
		w.section(step.Name)
		w.code(code)
	}
	return warnings
}

// sources writes a section that sources files, if there are any.
func (p posix) sources(w *writer, heading string, files []string) {
	if len(files) == 0 {
		return
	}
	w.section(heading)
	for _, f := range files {
		w.lines("source " + posixQuote(parsePath(f)))
	}
}

// sourceIfExists writes code that sources the file at path, if it exists.
func (p posix) sourceIfExists(w *writer, path []part) {
	quoted := posixQuote(path)
	w.lines(
		"if [ -f "+quoted+" ]; then",
		"	source "+quoted,
		"fi",
	)
}
