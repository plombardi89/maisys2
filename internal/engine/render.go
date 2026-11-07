package engine

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
)

// tagColumn is the column the plan's [sudo] and [admin] tags start at,
// unless a line is too long for it: then every tag moves right.
const tagColumn = 48

// windows11Build is the first Windows 11 build.
const windows11Build = 22000

// ANSI escape codes for the plan's colours.
const (
	bold    = "\x1b[1m"
	red     = "\x1b[31m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	magenta = "\x1b[35m"
	cyan    = "\x1b[36m"
	reset   = "\x1b[0m"
)

// symbolColor is the colour of each action's symbol.
var symbolColor = map[resource.Action]string{
	resource.Create:  green,
	resource.Update:  yellow,
	resource.Remove:  red,
	resource.Warn:    magenta,
	resource.Unknown: cyan,
	resource.Orphan:  magenta,
}

// Render writes the plan for people to read, as sync shows it before
// asking to apply it: the machine and system, the changes of each module
// that has any, the removals under "prune", and a summary line. Modules
// with nothing to change are left out; a module that failed shows its
// errors instead of its changes, and one that's skipped says so. color
// adds ANSI colours.
func (p *Plan) Render(w io.Writer, color bool) {
	r := &planWriter{color: color}
	fmt.Fprintf(&r.b, "Plan for %s (%s)\n", p.Machine, p.system())
	for _, line := range p.Header {
		r.b.WriteString(line + "\n")
	}
	r.b.WriteString("\n")
	if len(p.Warnings) > 0 {
		for _, warning := range p.Warnings {
			r.b.WriteString(r.paint(yellow, "warning:") + " " + warning + "\n")
		}
		r.b.WriteString("\n")
	}

	for _, m := range p.Modules {
		r.module(m)
	}
	r.removals(p.Removals)
	if len(r.lines) > 0 {
		r.flush(p.privilegeTag())
		r.b.WriteString("\n")
	}
	r.b.WriteString(p.summary() + "\n")
	if failed := p.failedSummary(); failed != "" {
		r.b.WriteString(failed + "\n")
	}
	_, _ = io.WriteString(w, r.b.String())
}

// system describes the live system, such as "ubuntu 26.04, amd64" or
// "windows 11 26100, amd64".
func (p *Plan) system() string {
	release := string(p.OS)
	if build, err := strconv.Atoi(p.OSVersion); err == nil && p.OS == config.Windows {
		if build >= windows11Build {
			release += " 11"
		} else {
			release += " 10"
		}
	}
	release = strings.TrimSpace(release + " " + p.OSVersion)
	if p.Arch == "" {
		return release
	}
	return release + ", " + p.Arch
}

// privilegeTag returns the tag of changes that need root or admin.
func (p *Plan) privilegeTag() string {
	if p.OS == config.Windows {
		return "[admin]"
	}
	return "[sudo]"
}

// counts returns how many of the changes that will be applied are changes
// (+, ~ and !), removals (-), and changes checked during apply.
func (p *Plan) counts() (changes, removals, unknown int) {
	count := func(a resource.Action) {
		switch a {
		case resource.Create, resource.Update, resource.Warn:
			changes++
		case resource.Remove:
			removals++
		case resource.Unknown:
			unknown++
		default:
		}
	}
	for _, m := range p.Modules {
		if m.applies() {
			for _, item := range m.Items {
				count(item.Change.Action)
			}
		}
	}
	for _, r := range p.Removals {
		if r.applies() {
			count(r.Change.Action)
		}
	}
	return changes, removals, unknown
}

// summary returns the plan's summary line, such as "6 to change, 1 to
// remove. Needs sudo."
func (p *Plan) summary() string {
	changes, removals, unknown := p.counts()
	var parts []string
	if changes > 0 {
		parts = append(parts, fmt.Sprintf("%d to change", changes))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d to check during apply", unknown))
	}
	if removals > 0 {
		parts = append(parts, fmt.Sprintf("%d to remove", removals))
	}
	if len(parts) == 0 {
		return "Nothing to change."
	}
	s := strings.Join(parts, ", ") + "."
	if p.NeedsPrivileges() {
		switch {
		case p.OS != config.Windows:
			s += " Needs sudo."
		case p.uacPrompt():
			s += " Needs admin (1 UAC prompt)."
		default:
			s += " Needs admin."
		}
	}
	if p.mayReboot() {
		s += " A reboot may be needed."
	}
	return s
}

// uacPrompt reports whether applying the changes that need admin rights on
// Windows shows a UAC prompt: unless maisys is elevated already, they run
// in the elevated helper, which needs one.
func (p *Plan) uacPrompt() bool {
	f := p.rt.Facts
	return f != nil && f.Windows != nil && !f.Windows.Elevated
}

// mayReboot reports whether a change that will be applied may need a
// reboot.
func (p *Plan) mayReboot() bool {
	for _, m := range p.Modules {
		if m.applies() && slices.ContainsFunc(m.Items, func(item *Item) bool { return item.Change.Action.Applies() && item.Change.MayReboot }) {
			return true
		}
	}
	return false
}

// failedSummary says which modules failed in the plan and which removals
// couldn't be checked, or returns "" if none.
func (p *Plan) failedSummary() string {
	var failed []string
	for _, m := range p.Modules {
		if m.failed() {
			failed = append(failed, m.Name)
		}
	}
	removals := 0
	for _, r := range p.Removals {
		if r.Err != nil {
			removals++
		}
	}
	var sentences []string
	switch len(failed) {
	case 0:
	case 1:
		sentences = append(sentences, failed[0]+" failed, so it won't be applied.")
	default:
		sentences = append(sentences, list(failed)+" failed, so they won't be applied.")
	}
	switch removals {
	case 0:
	case 1:
		sentences = append(sentences, "1 removal couldn't be checked, so it won't be applied.")
	default:
		sentences = append(sentences, fmt.Sprintf("%d removals couldn't be checked, so they won't be applied.", removals))
	}
	return strings.Join(sentences, " ")
}

// planWriter writes a plan.
type planWriter struct {
	color bool
	b     strings.Builder
	lines []planLine
}

// planLine is a line of the plan's changes.
type planLine struct {
	text   string // the line, perhaps with colours
	width  int    // how many columns it takes
	tagged bool   // whether it ends with the privilege tag
}

func (r *planWriter) paint(color, s string) string {
	if !r.color {
		return s
	}
	return color + s + reset
}

// add adds a line, indented by indent spaces, made of a coloured part and
// a plain part.
func (r *planWriter) add(indent int, color, colored, plain string, tagged bool) {
	text := strings.Repeat(" ", indent) + r.paint(color, colored)
	width := indent + utf8.RuneCountInString(colored)
	if plain != "" {
		text += " " + plain
		width += 1 + utf8.RuneCountInString(plain)
	}
	r.lines = append(r.lines, planLine{text: text, width: width, tagged: tagged})
}

// change adds a line for a change.
func (r *planWriter) change(c resource.Change, summary string, privileged bool) {
	switch c.Action {
	case resource.Unknown:
		summary += " (checked during apply)"
	case resource.Orphan:
		summary += " is no longer used (not removed)"
	default:
	}
	tagged := privileged && c.Action.Applies()
	r.add(2, symbolColor[c.Action], c.Action.Symbol(), summary, tagged)
}

// notes adds a line for each of a change's notes.
func (r *planWriter) notes(c resource.Change) {
	for _, note := range c.Notes {
		r.add(2, magenta, "!", note, false)
	}
}

// failure adds a line for an error, indenting the lines after its first.
func (r *planWriter) failure(err error) {
	r.add(2, red, "error:", strings.ReplaceAll(err.Error(), "\n", "\n    "), false)
}

// module adds the module's lines, if it has anything to show.
func (r *planWriter) module(m *ModulePlan) {
	start := len(r.lines)
	switch {
	case m.SkippedBecause != "":
		if slices.ContainsFunc(m.Items, func(item *Item) bool { return item.Change.Action.Applies() }) {
			r.add(0, bold, m.Name, fmt.Sprintf("(skipped because %s failed)", m.SkippedBecause), false)
		}
		return
	case m.Err != nil:
		r.failure(m.Err)
	case m.failed():
		for _, item := range m.Items {
			if item.Err != nil {
				r.failure(item.checkError())
			}
		}
	default:
		for _, item := range m.Items {
			if item.Change.Action.Applies() || item.Change.Action == resource.Orphan {
				r.change(item.Change, item.summary(), item.privileged())
			}
			r.notes(item.Change)
		}
	}
	r.heading(start, m.Name)
}

// removals adds the "prune" section, if there's anything to show in it:
// the removals, then what's no longer used but isn't removed.
func (r *planWriter) removals(rs []*Removal) {
	start := len(r.lines)
	for _, rm := range rs {
		switch {
		case rm.Err != nil:
			r.failure(fmt.Errorf("checking the removal of %s: %w", resource.Describe(rm.Record.ID), rm.Err))
		case rm.Change.Action.Applies():
			r.change(rm.Change, rm.summary(), rm.privileged())
			r.notes(rm.Change)
		case rm.Change.Action != resource.Orphan:
			r.notes(rm.Change)
		default:
		}
	}
	for _, rm := range rs {
		if rm.Err == nil && rm.Change.Action == resource.Orphan {
			r.change(rm.Change, rm.summary(), false)
			r.notes(rm.Change)
		}
	}
	r.heading(start, "prune")
}

// heading puts a heading before the lines added since start, if any.
func (r *planWriter) heading(start int, name string) {
	if len(r.lines) == start {
		return
	}
	r.lines = append(r.lines[:start], append([]planLine{{text: r.paint(bold, name), width: len(name)}}, r.lines[start:]...)...)
}

// flush writes the lines, with tag after the tagged ones, aligned.
func (r *planWriter) flush(tag string) {
	col := tagColumn
	for _, l := range r.lines {
		if l.tagged {
			col = max(col, l.width+2)
		}
	}
	for _, l := range r.lines {
		r.b.WriteString(l.text)
		if l.tagged {
			r.b.WriteString(strings.Repeat(" ", col-l.width) + tag)
		}
		r.b.WriteString("\n")
	}
	r.lines = nil
}

// list joins items as "a, b and c".
func list(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
