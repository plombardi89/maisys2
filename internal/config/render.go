package config

import (
	"fmt"
	"io"
	"path"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// exampleWidth is the width ExampleTOML wraps its comments at.
const exampleWidth = 78

// ExampleTOML returns the module's table as a commented-out example for
// config init: every key at its default, with its doc above it. Tables
// keyed by name get an entry named "example" holding their entries'
// defaults.
//
// Removing the leading "# " from every line gives TOML that sets every key
// to its default, and adds the example entries.
func ExampleTOML(mod Module, docs Docs) string {
	info := mod.Info()
	var b strings.Builder
	comment := func(text string) {
		for _, line := range wrap(text, exampleWidth-len("# # ")) {
			b.WriteString("# # " + line + "\n")
		}
	}
	comment(info.Summary)
	b.WriteString("# " + tomlHeader([]string{info.Name}) + "\n")
	cfg := reflect.Indirect(reflect.ValueOf(mod.NewConfig()))
	for _, l := range tableLines(cfg, []string{info.Name}, docs, true) {
		if l.header {
			b.WriteString("#\n")
		}
		comment(l.doc)
		if l.header {
			b.WriteString("# " + tomlHeader(l.path) + "\n")
		} else {
			b.WriteString("# " + tomlKey(l.path[len(l.path)-1]) + " = " + l.value + "\n")
		}
	}
	return b.String()
}

// Render writes m's merged config as TOML, for maisys config: the
// machine's own keys, then the table of every enabled module, in the order
// of modules. A final comment lists the modules that are off, leaving out
// hidden ones and, if m declares an os, those that don't support it. With
// explain, a comment after each value says which files set it, or
// "default".
//
// Render doesn't use docs yet; it takes them so the output can describe
// keys later without changing callers.
func Render(w io.Writer, m *Machine, modules []Module, _ Docs, explain bool) error {
	origin := func(key, fallback string) string {
		if files := m.Origins[key]; len(files) > 0 {
			return strings.Join(files, ", ")
		}
		return fallback
	}

	var blocks [][]renderedLine
	machineFile := path.Join(machinesDir, m.Name+".toml")
	var own []renderedLine
	for _, kv := range []struct{ key, value string }{
		{"os", string(m.OS)},
		{"os_version", m.OSVersion},
		{"arch", string(m.Arch)},
	} {
		if kv.value != "" {
			own = append(own, renderedLine{kv.key + " = " + tomlString(kv.value), origin(kv.key, machineFile)})
		}
	}
	if len(m.Profiles) > 0 {
		own = append(own, renderedLine{"profiles = " + tomlValue(reflect.ValueOf(m.Profiles)), origin("profiles", machineFile)})
	}
	if len(own) > 0 {
		blocks = append(blocks, own)
	}

	var off []string
	for _, mod := range modules {
		info := mod.Info()
		cfg, ok := m.Modules[info.Name]
		if !ok || cfg == nil {
			return fmt.Errorf("machine %s has no config for module %q", m.Name, info.Name)
		}
		if !cfg.IsEnabled() {
			if !info.Hidden && (m.OS == "" || slices.Contains(info.OS, m.OS)) {
				off = append(off, info.Name)
			}
			continue
		}
		block := []renderedLine{{text: tomlHeader([]string{info.Name})}}
		for _, l := range tableLines(reflect.Indirect(reflect.ValueOf(cfg)), []string{info.Name}, nil, false) {
			if l.header {
				blocks = append(blocks, block)
				block = []renderedLine{{text: tomlHeader(l.path)}}
				continue
			}
			text := tomlKey(l.path[len(l.path)-1]) + " = " + l.value
			block = append(block, renderedLine{text, origin(dottedPath(l.path), "default")})
		}
		blocks = append(blocks, block)
	}

	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		writeBlock(&b, block, explain)
	}
	if len(off) > 0 {
		if len(blocks) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("# off: " + strings.Join(off, ", ") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// renderedLine is a line Render writes, with where its value came from.
// Table headers have no origin.
type renderedLine struct {
	text, origin string
}

// alignLimit is the longest line explain comments are aligned after;
// longer lines get theirs straight after the value.
const alignLimit = 40

// writeBlock writes a table's lines. With explain, each value's origin
// follows it as a comment, aligned with the others in the table.
func writeBlock(b *strings.Builder, block []renderedLine, explain bool) {
	width := 0
	for _, l := range block {
		if n := utf8.RuneCountInString(l.text); l.origin != "" && n <= alignLimit {
			width = max(width, n)
		}
	}
	for _, l := range block {
		b.WriteString(l.text)
		if explain && l.origin != "" {
			b.WriteString(strings.Repeat(" ", max(0, width-utf8.RuneCountInString(l.text))))
			b.WriteString("  # " + l.origin)
		}
		b.WriteString("\n")
	}
}

// tomlLine is a line of a config table written as TOML: a table header, or
// a key and its value.
type tomlLine struct {
	header bool
	// path is the full path of the table, for a header, or of the key.
	path []string
	// value is the key's value, encoded as TOML.
	value string
	// doc describes the table or key, if it has a doc.
	doc string
}

// exampleEntry names the example entry config init adds to every table
// keyed by name.
const exampleEntry = "example"

// tableLines returns the lines of the table at path, which holds v: its
// keys first, then its sub-tables, because TOML needs keys to come before
// sub-tables. Tables keyed by name that have no entries are left out,
// unless examples is set: then every table keyed by name gets an entry
// named "example" holding its defaults, if it doesn't have one.
func tableLines(v reflect.Value, path []string, docs Docs, examples bool) []tomlLine {
	var keys, tables []tomlLine
	for _, k := range configKeys(v) {
		keyPath := appendPath(path, k.name)
		doc := k.doc(docs)
		switch k.value.Kind() {
		case reflect.Struct:
			tables = append(tables, tomlLine{header: true, path: keyPath, doc: doc})
			tables = append(tables, tableLines(k.value, keyPath, docs, examples)...)
		case reflect.Map:
			tables = append(tables, mapLines(k.value, keyPath, doc, docs, examples)...)
		default:
			keys = append(keys, tomlLine{path: keyPath, value: tomlValue(k.value), doc: doc})
		}
	}
	return append(keys, tables...)
}

// mapLines returns the lines of a table keyed by name, at path. Entries
// that are tables themselves each get a sub-table.
func mapLines(m reflect.Value, path []string, doc string, docs Docs, examples bool) []tomlLine {
	entries := map[string]reflect.Value{}
	for it := m.MapRange(); it.Next(); {
		entries[it.Key().String()] = it.Value()
	}
	if _, ok := entries[exampleEntry]; examples && !ok {
		entries[exampleEntry] = newEntry(m.Type().Elem())
	}
	if len(entries) == 0 {
		return nil
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)

	if m.Type().Elem().Kind() != reflect.Struct {
		lines := []tomlLine{{header: true, path: path, doc: doc}}
		for _, name := range names {
			lines = append(lines, tomlLine{path: appendPath(path, name), value: tomlValue(entries[name])})
		}
		return lines
	}
	var lines []tomlLine
	for i, name := range names {
		entryPath := appendPath(path, name)
		header := tomlLine{header: true, path: entryPath}
		if i == 0 {
			header.doc = doc
		}
		lines = append(lines, header)
		lines = append(lines, tableLines(entries[name], entryPath, docs, examples)...)
	}
	return lines
}

func appendPath(path []string, key string) []string {
	return append(slices.Clip(path), key)
}

// dottedPath returns path the way Machine.Origins keys it.
func dottedPath(path []string) string {
	var dotted string
	for _, p := range path {
		dotted = joinPath(dotted, p)
	}
	return dotted
}

// wrap breaks text into lines of at most width characters, where its words
// allow.
func wrap(text string, width int) []string {
	var lines []string
	var line string
	for word := range strings.FieldsSeq(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
