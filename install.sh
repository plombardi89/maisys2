#!/usr/bin/env bash
#
# Install or uninstall maisys on Linux.
#
#   curl -fsSL https://github.com/plombardi89/maisys2/releases/latest/download/install.sh | bash -s -- [ARGS...]
#
# Installs the latest release to ~/.local/bin/maisys. Any ARGS are passed to
# "maisys sync", for example a config repo URL and --machine.
#
#   ... | bash -s -- --uninstall
#
# Removes maisys itself: the binary, its settings, state and caches. Tools,
# files and packages that maisys installed are left in place.
#
# MAISYS_RELEASE_URL overrides where release files are downloaded from (used by
# tests).

set -euo pipefail

release_url="${MAISYS_RELEASE_URL:-https://github.com/plombardi89/maisys2/releases/latest/download}"
bin_dir="$HOME/.local/bin"
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/maisys"
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/maisys"
cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/maisys"

say() {
	printf 'maisys-install: %s\n' "$*" >&2
}

die() {
	say "error: $*"
	exit 1
}

usage() {
	cat <<'EOF'
Usage: install.sh [SYNC-ARGS...]
       install.sh --uninstall

Installs the latest maisys release to ~/.local/bin/maisys. Any arguments are
passed to "maisys sync", for example:

  install.sh https://github.com/you/maisys-config.git
  install.sh https://github.com/you/maisys-config.git --machine laptop

--uninstall removes the maisys binary, settings, state and caches. Tools,
files and packages that maisys installed are left in place.
EOF
}

download() {
	local url=$1 dest=$2
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$dest" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q --tries=3 -O "$dest" "$url"
	else
		die "curl or wget is required"
	fi
}

platform() {
	local os arch
	os=$(uname -s)
	arch=$(uname -m)
	[ "$os" = Linux ] || die "unsupported operating system: $os (on Windows, use install.ps1)"
	case "$arch" in
	x86_64 | amd64) echo linux_amd64 ;;
	*) die "unsupported architecture: $arch" ;;
	esac
}

install_maisys() {
	local target tmp sum name asset="" want="" version got
	[ "$(id -u)" -ne 0 ] || die "run this as your normal user, not root; maisys uses sudo when it needs to"
	target=$(platform)

	tmp=$(mktemp -d)
	# shellcheck disable=SC2064 # expand $tmp now
	trap "rm -rf '$tmp'" EXIT

	download "$release_url/checksums.txt" "$tmp/checksums.txt" ||
		die "couldn't download checksums.txt from $release_url"
	while read -r sum name; do
		case "$name" in
		maisys_*_"$target".tar.gz)
			asset=$name
			want=$sum
			;;
		esac
	done <"$tmp/checksums.txt"
	[ -n "$asset" ] || die "the release has no $target archive"
	version=${asset#maisys_}
	version=${version%_"$target".tar.gz}

	say "downloading maisys $version"
	download "$release_url/$asset" "$tmp/$asset" || die "couldn't download $asset"
	got=$(sha256sum "$tmp/$asset")
	got=${got%% *}
	[ "$got" = "$want" ] || die "checksum mismatch for $asset (expected $want, got $got)"
	tar -xzf "$tmp/$asset" -C "$tmp" maisys || die "couldn't unpack $asset"

	# Copy next to the destination, then rename, so the binary is replaced in
	# one step even if an older maisys is running.
	mkdir -p "$bin_dir"
	cp "$tmp/maisys" "$bin_dir/.maisys.new"
	chmod 0755 "$bin_dir/.maisys.new"
	mv -f "$bin_dir/.maisys.new" "$bin_dir/maisys"
	say "installed maisys $version to $bin_dir/maisys"

	case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) say "note: $bin_dir isn't on your PATH yet; maisys adds it when it sets up your shell" ;;
	esac

	if [ $# -eq 0 ]; then
		say "next: run 'maisys sync CONFIG-URL' to set up this machine, or 'maisys config init' to start a new config repo"
		return
	fi

	say "running maisys sync $*"
	# When this script is piped into bash, stdin is the script itself. Give
	# maisys the terminal instead so it can ask for confirmation.
	if (exec </dev/tty) 2>/dev/null; then
		"$bin_dir/maisys" sync "$@" </dev/tty
	else
		"$bin_dir/maisys" sync "$@"
	fi
}

uninstall_maisys() {
	local path removed=0
	for path in "$bin_dir/maisys" "$config_dir/maisys.toml" "$config_dir/git-credentials" "$state_dir" "$cache_dir"; do
		if [ -e "$path" ]; then
			rm -rf "$path"
			say "removed $path"
			removed=1
		fi
	done
	# Generated shell files stay, because rc files may still source them.
	rmdir "$config_dir" 2>/dev/null || true

	if [ "$removed" -eq 0 ]; then
		say "maisys isn't installed; nothing to remove"
	else
		say "done; tools, files and packages that maisys installed are still in place"
	fi
}

main() {
	case "${1:-}" in
	--uninstall)
		[ $# -eq 1 ] || die "--uninstall takes no other arguments"
		uninstall_maisys
		;;
	-h | --help)
		usage
		;;
	*)
		install_maisys "$@"
		;;
	esac
}

main "$@"
