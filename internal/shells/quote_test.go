package shells

import "testing"

func TestQuote(t *testing.T) {
	tests := []struct {
		value string
		path  bool // parse it as a path, not a value
		posix string
		fish  string
	}{
		{value: "", posix: `""`, fish: `""`},
		{value: "plain", posix: `"plain"`, fish: `"plain"`},
		{value: "~", posix: `"${HOME}"`, fish: `"$HOME"`},
		{value: "~/go", posix: `"${HOME}/go"`, fish: `"$HOME/go"`},
		{value: "a~/b", posix: `"a~/b"`, fish: `"a~/b"`},
		{value: "$GOPATH/bin", posix: `"${GOPATH}/bin"`, fish: `"$GOPATH/bin"`},
		{value: "${GOPATH}bin", posix: `"${GOPATH}bin"`, fish: `"$GOPATH""bin"`},
		{value: "$HOME[1]", posix: `"${HOME}[1]"`, fish: `"$HOME""[1]"`},
		{value: "$A$B", posix: `"${A}${B}"`, fish: `"$A$B"`},
		{value: "$HOME:h", posix: `"${HOME}:h"`, fish: `"$HOME:h"`},
		{value: "costs $5, or $", posix: `"costs \$5, or \$"`, fish: `"costs \$5, or \$"`},
		{value: "${not closed", posix: `"\${not closed"`, fish: `"\${not closed"`},
		{value: "${1}", posix: `"\${1}"`, fish: `"\${1}"`},
		{value: `say "hi" \ there`, posix: `"say \"hi\" \\ there"`, fish: `"say \"hi\" \\ there"`},
		{value: "`id` $(id) (id)", posix: "\"\\`id\\` \\$(id) (id)\"", fish: "\"`id` \\$(id) (id)\""},
		{value: "it's", posix: `"it's"`, fish: `"it's"`},
		{value: "~/$x/a b", path: true, posix: `"${HOME}/\$x/a b"`, fish: `"$HOME/\$x/a b"`},
		{value: "/etc/bashrc", path: true, posix: `"/etc/bashrc"`, fish: `"/etc/bashrc"`},
	}
	for _, tt := range tests {
		parts := parseValue(tt.value)
		if tt.path {
			parts = parsePath(tt.value)
		}
		if got := posixQuote(parts); got != tt.posix {
			t.Errorf("posixQuote(%q) = %s, want %s", tt.value, got, tt.posix)
		}
		if got := fishQuote(parts); got != tt.fish {
			t.Errorf("fishQuote(%q) = %s, want %s", tt.value, got, tt.fish)
		}
	}
}

func TestWord(t *testing.T) {
	tests := map[string]string{
		"git":                     "git",
		"zsh-autosuggestions":     "zsh-autosuggestions",
		"1password":               "1password",
		"a b":                     "'a b'",
		"it's":                    `'it'\''s'`,
		"$(id)":                   "'$(id)'",
		"-dash":                   "'-dash'",
		"zsh_reload.plugin+extra": "zsh_reload.plugin+extra",
	}
	for in, want := range tests {
		if got := word(in); got != want {
			t.Errorf("word(%q) = %s, want %s", in, got, want)
		}
	}
}
