package shells

import (
	"regexp"
	"strings"
)

// validName matches the names of environment variables.
var validName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether name can be an environment variable's name:
// letters, digits and underscores, not starting with a digit.
func ValidName(name string) bool {
	return validName.MatchString(name)
}

// part is a piece of a value: text taken literally, or a reference to a
// variable.
type part struct {
	text string
	// ref reports whether text is the name of a variable to expand.
	ref bool
}

// parseValue splits a value as Env and the shells' variables write them:
// a leading "~/" (or "~" on its own) is the home folder, $NAME and ${NAME}
// refer to variables, and everything else is taken literally.
func parseValue(s string) []part {
	parts, rest := home(s)
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, part{text: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] != '$' {
			lit.WriteByte(rest[i])
			continue
		}
		after := rest[i+1:]
		if inner, ok := strings.CutPrefix(after, "{"); ok {
			if name, _, ok := strings.Cut(inner, "}"); ok && ValidName(name) {
				flush()
				parts = append(parts, part{text: name, ref: true})
				i += len(name) + 2
				continue
			}
		}
		if n := nameLen(after); n > 0 {
			flush()
			parts = append(parts, part{text: after[:n], ref: true})
			i += n
			continue
		}
		lit.WriteByte('$')
	}
	flush()
	return parts
}

// parsePath splits a path: a leading "~/" is the home folder, and
// everything else is taken literally.
func parsePath(s string) []part {
	parts, rest := home(s)
	if rest != "" {
		parts = append(parts, part{text: rest})
	}
	return parts
}

// home splits off a leading "~/", or a "~" on its own, as a reference to
// HOME.
func home(s string) ([]part, string) {
	if s == "~" {
		return []part{{text: "HOME", ref: true}}, ""
	}
	if rest, ok := strings.CutPrefix(s, "~/"); ok {
		return []part{{text: "HOME", ref: true}}, "/" + rest
	}
	return nil, s
}

// nameLen returns the length of the variable name s starts with, or 0.
func nameLen(s string) int {
	n := 0
	for n < len(s) && isNameByte(s[n], n == 0) {
		n++
	}
	return n
}

// isNameByte reports whether c can be in a variable's name, as its first
// character if first is set.
func isNameByte(c byte, first bool) bool {
	switch {
	case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		return true
	default:
		return !first && c >= '0' && c <= '9'
	}
}

// The escapes of the characters that are special in double quotes, in
// bash and zsh, and in fish.
var (
	posixEscapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	fishEscapes  = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`)
)

// posixQuote writes parts as one double-quoted word for bash and zsh.
// Variables are written as ${NAME}, so that zsh never reads what follows
// as a subscript or a modifier.
func posixQuote(parts []part) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, p := range parts {
		if p.ref {
			b.WriteString("${" + p.text + "}")
		} else {
			b.WriteString(posixEscapes.Replace(p.text))
		}
	}
	b.WriteByte('"')
	return b.String()
}

// fishQuote writes parts as one double-quoted word for fish. fish has no
// ${NAME}, so a variable followed by a character that would carry on its
// name, or index it, ends the quotes, and new ones start.
func fishQuote(parts []part) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, p := range parts {
		if p.ref {
			b.WriteString("$" + p.text)
			if i+1 < len(parts) && !parts[i+1].ref {
				if next := parts[i+1].text[0]; next == '[' || isNameByte(next, false) {
					b.WriteString(`""`)
				}
			}
			continue
		}
		b.WriteString(fishEscapes.Replace(p.text))
	}
	b.WriteByte('"')
	return b.String()
}

// safeWord matches words that need no quotes in any shell.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)

// word returns s as a word for bash or zsh: as it is if it needs no quotes,
// and in single quotes otherwise.
func word(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
