package shells

import (
	"fmt"
	"strings"
)

// fishEnv writes fish's file in conf.d. fish has no rc file maisys
// manages, so it holds everything: fish's variables and pre-snippets, the
// environment, and fish's snippets.
func fishEnv(cfg Config) ([]byte, []Warning) {
	w := &writer{}
	w.lines(header...)
	w.lines(
		"#",
		"# Your fish settings and the environment of the modules your config",
		"# enables. fish loads it by itself, before config.fish.",
	)
	if len(cfg.Variables) > 0 {
		w.section(key("fish", "variables"))
		fishVariables(w, cfg.Variables)
	}
	fishSources(w, key("fish", "pre_snippets"), cfg.PreSnippets)

	env := cfg.Env
	if len(env.Variables) > 0 {
		w.section("Variables")
		fishVariables(w, env.Variables)
	}
	if len(env.Path) > 0 {
		dirs := make([]string, len(env.Path))
		for i, dir := range env.Path {
			dirs[i] = fishQuote(parseValue(dir))
		}
		w.section("Folders at the front of PATH, unless PATH has them already")
		w.lines(
			"begin",
			"    set -l __maisys_path",
			"    for __maisys_dir in "+strings.Join(dirs, " "),
			"        if not contains -- $__maisys_dir $__maisys_path $PATH",
			"            set -a __maisys_path $__maisys_dir",
			"        end",
			"    end",
			"    set -gx PATH $__maisys_path $PATH",
			"end",
		)
	}
	var warnings []Warning
	for _, step := range initSteps(env) {
		if step.Fish == "" {
			warnings = append(warnings, Warning{Shell: "fish", Step: step.Name})
			w.section(step.Name + " has no fish integration, so it's left out")
			continue
		}
		w.section(step.Name)
		w.code(step.Fish)
	}

	fishSources(w, key("fish", "snippets"), cfg.Snippets)
	return w.bytes(), warnings
}

// fishVariables writes variables, exported.
func fishVariables(w *writer, vars []Variable) {
	for _, v := range vars {
		w.lines(fmt.Sprintf("set -gx %s %s", v.Name, fishQuote(parseValue(v.Value))))
	}
}

// fishSources writes a section that sources files, if there are any.
func fishSources(w *writer, heading string, files []string) {
	if len(files) == 0 {
		return
	}
	w.section(heading)
	for _, f := range files {
		w.lines("source " + fishQuote(parsePath(f)))
	}
}
