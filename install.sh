#!/bin/sh
#
# meandr-cli installer.
#
#   curl -fsSL https://github.com/meandr-inc/meandr-cli/releases/latest/download/install.sh | sh
#
# Environment:
#   MEANDR_VERSION      release tag to install (default: latest)
#   MEANDR_INSTALL_DIR  where to put it (default: /usr/local/bin, then ~/.local/bin)

set -eu

REPO=meandr-inc/meandr-cli
BINARY=meandr

# Everything lives in main(), called on the last line: a truncated download
# then defines functions and runs none of them, instead of executing half
# an installer.
main() {
	version=${MEANDR_VERSION:-latest}
	asset=$(asset_name)
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT INT TERM

	say "Installing $BINARY ($asset, $version)"

	fetch "$(url "$version" "$asset")" "$tmp/$asset"
	fetch "$(url "$version" SHA256SUMS)" "$tmp/SHA256SUMS"
	verify "$tmp" "$asset"

	chmod 0755 "$tmp/$asset"
	dir=$(install_dir)
	place "$tmp/$asset" "$dir/$BINARY"

	say "Installed $("$dir/$BINARY" version | head -1) to $dir/$BINARY"
	check_path "$dir"
}

say() { printf '%s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# asset_name maps this machine onto a published binary, and refuses
# anything we do not build rather than downloading a 404 page.
asset_name() {
	os=$(uname -s)
	arch=$(uname -m)
	case "$os" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) die "unsupported OS: $os (build from source: https://github.com/$REPO)" ;;
	esac
	case "$arch" in
		x86_64 | amd64) arch=amd64 ;;
		aarch64 | arm64) arch=arm64 ;;
		*) die "unsupported architecture: $arch" ;;
	esac
	printf '%s-%s-%s' "$BINARY" "$os" "$arch"
}

url() {
	if [ "$1" = latest ]; then
		printf 'https://github.com/%s/releases/latest/download/%s' "$REPO" "$2"
	else
		printf 'https://github.com/%s/releases/download/%s/%s' "$REPO" "$1" "$2"
	fi
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1" || die "download failed: $1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1" || die "download failed: $1"
	else
		die "need curl or wget"
	fi
}

# verify compares against the release's own SHA256SUMS. It catches a
# truncated download; against a compromised release it proves nothing,
# since the same release served this script.
verify() {
	want=$(awk -v f="$2" '$2 == f {print $1}' "$1/SHA256SUMS")
	[ -n "$want" ] || die "$2 is not listed in SHA256SUMS"

	if command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$1/$2" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		got=$(shasum -a 256 "$1/$2" | awk '{print $1}')
	else
		die "need sha256sum or shasum to verify the download"
	fi

	[ "$want" = "$got" ] || die "checksum mismatch for $2 (expected $want, got $got)"
}

# install_dir prefers a system path and falls back to the user's own,
# so a host without sudo still installs.
install_dir() {
	if [ -n "${MEANDR_INSTALL_DIR:-}" ]; then
		mkdir -p "$MEANDR_INSTALL_DIR" || die "cannot create $MEANDR_INSTALL_DIR"
		printf '%s' "$MEANDR_INSTALL_DIR"
		return
	fi
	if [ -w /usr/local/bin ] || can_sudo; then
		printf '/usr/local/bin'
		return
	fi
	mkdir -p "$HOME/.local/bin" || die "cannot create $HOME/.local/bin"
	printf '%s' "$HOME/.local/bin"
}

can_sudo() {
	command -v sudo >/dev/null 2>&1 && [ ! -w /usr/local/bin ]
}

# place installs over any running copy. mv onto a busy binary is what
# `install` does safely on both platforms; a plain cp can fail with ETXTBSY.
place() {
	dest_dir=$(dirname "$2")
	if [ -w "$dest_dir" ]; then
		install -m 0755 "$1" "$2" || die "cannot write $2"
	else
		say "Writing to $2 needs sudo."
		sudo install -d "$dest_dir" || die "cannot create $dest_dir"
		sudo install -m 0755 "$1" "$2" || die "cannot write $2"
	fi
}

# check_path warns about the two ways the install can be invisible: not on
# PATH at all, or shadowed by an older copy earlier in it.
check_path() {
	case ":$PATH:" in
		*":$1:"*) ;;
		*)
			say ""
			say "$1 is not on your PATH. Add it:"
			say "    export PATH=\"$1:\$PATH\""
			return
			;;
	esac
	found=$(command -v "$BINARY" 2>/dev/null || true)
	if [ -n "$found" ] && [ "$found" != "$1/$BINARY" ]; then
		say ""
		say "Note: $found comes earlier on your PATH and will be used instead."
	fi
}

main "$@"
