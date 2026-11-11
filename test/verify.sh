#!/usr/bin/env bash
#
# Checks a test machine that maisys has synced from the end-to-end tests'
# fixture (test/fixture). test/e2e runs it as the test user, with EXPECT_*
# set to what the fixture pins. It prints a line for each check, and exits
# with 1 if any failed.
#
#   verify.sh
#
# It runs itself in each login shell to check the environment that shell
# sets up:
#
#   verify.sh shell-env SHELL
#
# shellcheck disable=SC2016 # Commands for other shells are single-quoted on purpose.
# shellcheck disable=SC2088 # Check descriptions show paths as ~/... on purpose.
set -uo pipefail

script=$(readlink -f "${BASH_SOURCE[0]}")
failures=0

pass() {
	echo "  ok    $1"
}

fail() {
	echo "  FAIL  $1"
	failures=$((failures + 1))
}

# show TEXT: prints TEXT, indented, under a check that failed.
show() {
	local line
	[[ -n $1 ]] || return 0
	while IFS= read -r line; do
		echo "          $line"
	done <<<"$1"
}

# check DESCRIPTION COMMAND...: passes if the command succeeds.
check() {
	local description=$1 output
	shift
	if output=$("$@" 2>&1); then
		pass "$description"
	else
		fail "$description"
		show "$output"
	fi
}

# check_output DESCRIPTION EXPECTED COMMAND...: passes if the command
# succeeds and its output contains EXPECTED.
check_output() {
	local description=$1 expected=$2 output
	shift 2
	if output=$("$@" 2>&1) && [[ $output == *"$expected"* ]]; then
		pass "$description"
	else
		fail "$description"
		show "expected the output to contain: $expected"
		show "$output"
	fi
}

# check_equal DESCRIPTION WANT GOT: passes if GOT is WANT.
check_equal() {
	if [[ $3 == "$2" ]]; then
		pass "$1"
	else
		fail "$1"
		show "want: $2"
		show "got:  $3"
	fi
}

# check_same DESCRIPTION SOURCE TARGET: passes if TARGET is a copy of
# SOURCE.
check_same() {
	local want got
	want=$(sha256sum <"$2" 2>&1)
	got=$(sha256sum <"$3" 2>&1)
	check_equal "$1" "$want" "$got"
}

# check_on_path DIR: passes if DIR is on PATH before the system's folders.
check_on_path() {
	local dir=$1 entry entries
	IFS=: read -ra entries <<<"$PATH"
	for entry in "${entries[@]}"; do
		case $entry in
		"$dir")
			pass "${dir/#$HOME/\~} is on PATH, before the system's folders"
			return
			;;
		/usr/bin | /bin) break ;;
		esac
	done
	fail "${dir/#$HOME/\~} is on PATH, before the system's folders"
	show "PATH=$PATH"
}

# login_shell SHELL COMMAND: runs COMMAND in SHELL started as an interactive
# login shell, as a terminal starts it. Its errors go to $shell_errors.
login_shell() {
	SHELL=/usr/bin/$1 "$1" -l -i -c "$2" </dev/null 2>>"$shell_errors"
}

# shell_env SHELL checks the environment SHELL has set up for the commands
# it runs, of which this script is one.
shell_env() {
	local shell=$1 cmd program
	check_equal "MAISYS_E2E_SHELL is set from shell.$shell.variables" "$shell" "${MAISYS_E2E_SHELL:-}"
	check_equal "MAISYS_E2E_SNIPPET is set by $shell's snippet" "$shell" "${MAISYS_E2E_SNIPPET:-}"
	check_equal "GOPATH is ~/go" "$HOME/go" "${GOPATH:-}"
	check_on_path "$HOME/.local/bin"
	check_on_path "$HOME/go/bin"
	for cmd in maisys go gofmt kubectl yq uv uvx "$EXPECT_PYTHON_TOOL"; do
		check_equal "$cmd is ~/.local/bin/$cmd" "$HOME/.local/bin/$cmd" "$(command -v "$cmd")"
	done
	program=${EXPECT_GO_PROGRAM##*/}
	check_equal "$program is ~/go/bin/$program" "$HOME/go/bin/$program" "$(command -v "$program")"

	check_output "go is $EXPECT_GO_VERSION" "go version go$EXPECT_GO_VERSION linux/" go version
	check_output "kubectl is $EXPECT_KUBECTL_VERSION" "Client Version: v$EXPECT_KUBECTL_VERSION" kubectl version --client
	check_output "yq is $EXPECT_YQ_VERSION" "version v$EXPECT_YQ_VERSION" yq --version
	check_output "$program is $EXPECT_GO_PROGRAM_VERSION" \
		$'\t'"$EXPECT_GO_PROGRAM_VERSION"$'\t' go version -m "$(command -v "$program")"
	check "$EXPECT_PYTHON_TOOL runs" "$EXPECT_PYTHON_TOOL" --version
}

if [[ $# -eq 2 && $1 == shell-env ]]; then
	shell_env "$2"
	((failures == 0))
	exit
fi

for name in MACHINE CONFIG_URL PACKAGES GO_VERSION GO_PROGRAM GO_PROGRAM_VERSION KUBECTL_VERSION \
	YQ_VERSION PYTHON_TOOL OH_MY_ZSH_COMMIT ZSH_PLUGIN ZSH_PLUGIN_COMMIT OWNER; do
	var=EXPECT_$name
	if [[ -z ${!var:-} ]]; then
		echo "verify.sh: $var isn't set" >&2
		exit 2
	fi
done

# shellcheck source=/dev/null
source /etc/os-release
case $(uname -m) in
x86_64) arch=amd64 ;;
aarch64) arch=arm64 ;;
*) arch=$(uname -m) ;;
esac
maisys=$HOME/.local/bin/maisys
repo=$HOME/maisys-config

echo "==> maisys"
status=$("$maisys" status 2>&1)
check_output "maisys status shows the machine" "machine:" grep -E "^machine: +$EXPECT_MACHINE\$" <<<"$status"
check_output "maisys status shows the config repo's URL" "url:" grep -E "^url: +$EXPECT_CONFIG_URL\$" <<<"$status"
check_output "maisys status shows the system is supported" "support:" grep -E '^support: +supported$' <<<"$status"
check_output "maisys status shows the last sync went well" "last sync:" grep -E '^last sync: +[0-9].* \(ok\)$' <<<"$status"
[[ $failures -eq 0 ]] || show "$status"

echo "==> sudo"
check "sudo works without a password" sudo -n true
check_output "/etc/sudoers.d/$USER lets $USER use sudo without a password" \
	"$USER ALL=(ALL) NOPASSWD: ALL" sudo -n cat "/etc/sudoers.d/$USER"
check_output "/etc/sudoers.d/$USER belongs to root, with mode 440" "440 root:root" \
	sudo -n stat -c '%a %U:%G' "/etc/sudoers.d/$USER"
check "the image's temporary rule is gone" sudo -n test ! -e /etc/sudoers.d/maisys-setup
check "visudo -c passes" sudo -n visudo -c

echo "==> packages ($ID $VERSION_ID)"
case $ID in
fedora)
	installed() { rpm -q "$1"; }
	git_package=git-core
	;;
*)
	installed() { dpkg-query -W -f '${Status}\n' "$1" | grep -qx 'install ok installed'; }
	git_package=git
	;;
esac
read -ra packages <<<"$EXPECT_PACKAGES"
for package in "${packages[@]}" bash zsh fish "$git_package"; do
	check "$package is installed" installed "$package"
done

echo "==> files"
check_same "~/e2e/plain.txt is files/e2e/plain.txt" "$repo/files/e2e/plain.txt" "$HOME/e2e/plain.txt"
check_equal "~/e2e/plain.txt has mode 600" 600 "$(stat -c %a "$HOME/e2e/plain.txt" 2>&1)"
check_same "~/.config/maisys-e2e/settings.conf is files/e2e/dir/settings.conf" \
	"$repo/files/e2e/dir/settings.conf" "$HOME/.config/maisys-e2e/settings.conf"
check_same "~/.config/maisys-e2e/bin/hello.sh is files/e2e/dir/bin/hello.sh" \
	"$repo/files/e2e/dir/bin/hello.sh" "$HOME/.config/maisys-e2e/bin/hello.sh"
check "~/.config/maisys-e2e/bin/hello.sh kept its executable bit" test -x "$HOME/.config/maisys-e2e/bin/hello.sh"
check_equal "~/e2e/about.txt is rendered from files/e2e/about.tmpl" \
	"$(printf 'owner: %s\nmachine: %s (%s %s)\nsystem: %s %s %s' "$EXPECT_OWNER" "$EXPECT_MACHINE" "$ID" "$VERSION_ID" "$ID" "$VERSION_ID" "$arch")" \
	"$(cat "$HOME/e2e/about.txt" 2>&1)"

echo "==> tools"
for tool in "go $EXPECT_GO_VERSION go/bin/go" "kubectl $EXPECT_KUBECTL_VERSION kubectl" "yq $EXPECT_YQ_VERSION yq"; do
	read -r name version target <<<"$tool"
	dir=$HOME/.local/share/maisys/tools/$name
	check "$name $version is installed, and complete" test -f "$dir/$version/.maisys-complete"
	check_equal "~/.local/bin/$name runs $name $version" "$dir/$version/$target" "$(readlink -f "$HOME/.local/bin/$name")"
	check_equal "$name $version is the only version installed" "$version" "$(ls "$dir" 2>&1)"
done
check "uv is installed, and complete" compgen -G "$HOME/.local/share/maisys/tools/uv/*/.maisys-complete"
program=${EXPECT_GO_PROGRAM##*/}
check "$program is installed with go install" test -x "$HOME/go/bin/$program"
uv_tools=$("$HOME/.local/bin/uv" tool list 2>&1)
tool_version=$(sed -n "s/^$EXPECT_PYTHON_TOOL v\([^ ]*\).*/\1/p" <<<"$uv_tools")
if [[ -n $tool_version ]]; then
	pass "uv installed $EXPECT_PYTHON_TOOL $tool_version"
	check_output "$EXPECT_PYTHON_TOOL is $tool_version" "$tool_version" "$HOME/.local/bin/$EXPECT_PYTHON_TOOL" --version
else
	fail "uv installed $EXPECT_PYTHON_TOOL"
	show "$uv_tools"
fi

echo "==> shells"
check_equal "the login shell is zsh" /usr/bin/zsh "$(getent passwd "$USER" | cut -d: -f7)"
for file in .config/maisys/shell/env.sh .config/maisys/shell/env.zsh .config/fish/conf.d/maisys.fish .bashrc .zshrc; do
	check_output "~/$file is written by maisys" "Written by maisys" cat "$HOME/$file"
done
check_equal "oh-my-zsh is checked out at $EXPECT_OH_MY_ZSH_COMMIT" \
	"$EXPECT_OH_MY_ZSH_COMMIT" "$(git -C "$HOME/.oh-my-zsh" rev-parse HEAD 2>&1)"
check_equal "the $EXPECT_ZSH_PLUGIN plugin is checked out at $EXPECT_ZSH_PLUGIN_COMMIT" \
	"$EXPECT_ZSH_PLUGIN_COMMIT" "$(git -C "$HOME/.oh-my-zsh/custom/plugins/$EXPECT_ZSH_PLUGIN" rev-parse HEAD 2>&1)"

shell_errors=$(mktemp)
trap 'rm -f "$shell_errors"' EXIT
for shell in bash zsh fish; do
	echo "==> $shell, as a login shell"
	: >"$shell_errors"
	before=$failures
	output=$(login_shell "$shell" "exec bash '$script' shell-env $shell")
	echo -n "${output:+$output$'\n'}"
	failures=$((failures + $(grep -c '^  FAIL' <<<"$output")))
	if [[ -z $output ]]; then
		fail "$shell starts"
	fi
	if [[ $shell == zsh ]]; then
		check_equal "zsh loads oh-my-zsh, with its default theme" "$HOME/.oh-my-zsh robbyrussell" \
			"$(login_shell zsh 'print -r -- "$ZSH $ZSH_THEME"')"
		check_equal "zsh loads the $EXPECT_ZSH_PLUGIN plugin" yes \
			"$(login_shell zsh '(( ${+functions[_zsh_autosuggest_start]} )) && print yes')"
		check_equal "zsh loads oh-my-zsh's git plugin" yes \
			"$(login_shell zsh '(( ${+aliases[gst]} )) && print yes')"
	fi
	if ((failures > before)) && [[ -s $shell_errors ]]; then
		show "$shell's errors:"
		show "$(cat "$shell_errors")"
	fi
done

if ((failures > 0)); then
	echo "==> $failures checks failed"
	exit 1
fi
echo "==> all checks passed"
