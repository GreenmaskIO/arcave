#!/usr/bin/env bash
#
# Installs the arcave binary from a GitHub release.
#
#   ./install.sh                      # latest release into /usr/local/bin
#   ./install.sh v1.2.3               # a specific release
#   ./install.sh --dir ~/.local/bin   # somewhere else
#
# Or without cloning anything:
#
#   curl -fsSL https://greenmask.io/arcave/install.sh | bash
#   curl -fsSL https://greenmask.io/arcave/install.sh | bash -s -- v1.2.3
#

set -euo pipefail

REPO="GreenmaskIO/arcave"
BINARY="arcave"
CHECKSUM_FILE="checksum.txt"
DEFAULT_DIR="/usr/local/bin"

BASE_URL="${ARCAVE_BASE_URL:-https://github.com}"

usage() {
	cat <<EOF
Install the arcave CLI from a GitHub release.

Usage:
  install.sh [<version>] [options]

Arguments:
  <version>              Release tag to install, e.g. v1.2.3.
                         Defaults to the latest release.

Options:
  -v, --version <tag>    Same as the positional argument.
  -d, --dir <path>       Install directory. Default: $DEFAULT_DIR
  -h, --help             Show this help.

Environment:
  ARCAVE_VERSION         Default for --version.
  ARCAVE_INSTALL_DIR     Default for --dir.
  ARCAVE_BASE_URL        Base URL of the release host. Default: https://github.com

Examples:
  ./install.sh
  ./install.sh v1.2.3
  ./install.sh --dir "\$HOME/.local/bin"
EOF
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

info() {
	printf '%s\n' "$*" >&2
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

version="${ARCAVE_VERSION:-}"
install_dir="${ARCAVE_INSTALL_DIR:-$DEFAULT_DIR}"

while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	-v | --version)
		[ $# -ge 2 ] || die "--version needs a value"
		version="$2"
		shift 2
		;;
	-d | --dir)
		[ $# -ge 2 ] || die "--dir needs a value"
		install_dir="$2"
		shift 2
		;;
	-*)
		die "unknown option: $1 (try --help)"
		;;
	*)
		[ -z "$version" ] || die "version given twice: '$version' and '$1'"
		version="$1"
		shift
		;;
	esac
done

need_cmd curl
need_cmd uname
need_cmd mktemp

# detect_platform maps uname output onto the os_arch suffix goreleaser builds.
# The arm cases carry the GOARM level because the release has both armv6 and
# armv7 — they differ only by that suffix, so guessing wrong installs a binary
# the CPU may refuse to run.
detect_platform() {
	local os arch
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported operating system: $(uname -s). arcave ships linux and darwin binaries only." ;;
	esac

	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	armv7l | armv7) arch=armv7 ;;
	armv6l | armv6) arch=armv6 ;;
	ppc64le) arch=ppc64le ;;
	riscv64) arch=riscv64 ;;
	s390x) arch=s390x ;;
	*) die "unsupported architecture: $(uname -m)" ;;
	esac

	# darwin is built for amd64 and arm64 only; the linux-only architectures
	# would otherwise produce a 404 that reads like a broken release.
	if [ "$os" = darwin ] && [ "$arch" != amd64 ] && [ "$arch" != arm64 ]; then
		die "unsupported platform: ${os}_${arch}"
	fi

	printf '%s_%s' "$os" "$arch"
}

# latest_tag reads the tag out of where /releases/latest redirects to, which is
# .../releases/tag/<tag>. No API call, so no rate limit and no JSON.
latest_tag() {
	local final
	final="$(curl --fail --silent --show-error --location --head \
		--output /dev/null --write-out '%{url_effective}' \
		"$BASE_URL/$REPO/releases/latest")" ||
		die "cannot reach $BASE_URL/$REPO/releases/latest"

	case "$final" in
	*/releases/tag/*) printf '%s' "${final##*/}" ;;
	*) die "unexpected redirect target: $final (does the project have a release yet?)" ;;
	esac
}

fetch() {
	local url="$1" out="$2"
	curl --fail --silent --show-error --location --output "$out" "$url"
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

platform="$(detect_platform)"

if [ -n "$version" ]; then
	tag="$version"
	info "installing release $tag"
else
	info "resolving latest release"
	tag="$(latest_tag)"
	info "latest release is $tag"
fi

asset="${BINARY}_${tag#v}_${platform}"
download_url="$BASE_URL/$REPO/releases/download/$tag"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

info "downloading $asset"
fetch "$download_url/$asset" "$tmp/$BINARY" ||
	die "cannot download $asset — check that release $tag exists and ships your platform"
fetch "$download_url/$CHECKSUM_FILE" "$tmp/$CHECKSUM_FILE" ||
	die "cannot download $CHECKSUM_FILE from release $tag"

expected="$(awk -v n="$asset" '$2 == n { print $1 }' "$tmp/$CHECKSUM_FILE" | head -1)"
[ -n "$expected" ] || die "$CHECKSUM_FILE has no entry for $asset"

actual="$(sha256_of "$tmp/$BINARY")"
[ "$expected" = "$actual" ] || die "checksum mismatch for $asset
  expected $expected
  actual   $actual"
info "checksum ok"

case "$install_dir" in
/*) ;;
*) install_dir="$PWD/$install_dir" ;;
esac

[ ! -e "$install_dir" ] || [ -d "$install_dir" ] ||
	die "$install_dir exists and is not a directory"

probe="$install_dir"
while [ ! -e "$probe" ]; do
	probe_parent="$(dirname "$probe")"
	[ "$probe_parent" != "$probe" ] || break
	probe="$probe_parent"
done

maybe_sudo=()
[ -w "$probe" ] || maybe_sudo=(sudo)

if [ ${#maybe_sudo[@]} -gt 0 ]; then
	command -v sudo >/dev/null 2>&1 ||
		die "$install_dir is not writable and sudo is not available. Re-run with --dir pointing somewhere you own."
	info "$install_dir is not writable; using sudo"
fi

"${maybe_sudo[@]+"${maybe_sudo[@]}"}" mkdir -p "$install_dir"
"${maybe_sudo[@]+"${maybe_sudo[@]}"}" install -m 0755 "$tmp/$BINARY" "$install_dir/$BINARY"

info "installed $install_dir/$BINARY"

if "$install_dir/$BINARY" --version >/dev/null 2>&1; then
	"$install_dir/$BINARY" --version >&2
fi

case ":$PATH:" in
*":$install_dir:"*) ;;
*) info "note: $install_dir is not on your PATH" ;;
esac
