#!/bin/sh
# Install inari from a GitHub release archive.
#
#   curl -fsSL https://raw.githubusercontent.com/hizkifw/inari/main/scripts/install.sh | sh
#
# Environment overrides:
#   INARI_VERSION      release tag to install (default: latest, e.g. v0.1.0)
#   INARI_INSTALL_DIR  where to put the binary (default: $HOME/.local/bin)
#   INARI_BASE_URL     release base, for mirrors and testing
#   NO_COLOR           disable colored output
set -eu

repo=hizkifw/inari
base_url=${INARI_BASE_URL:-https://github.com/$repo/releases}
install_dir=${INARI_INSTALL_DIR:-"$HOME/.local/bin"}
version=${INARI_VERSION:-}

# Color only a terminal, and only when the user has not opted out. Piped logs
# then stay plain text.
if [ -t 2 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-}" != dumb ]; then
  esc=$(printf '\033')
  accent="${esc}[38;2;229;83;61m"  # torii vermilion, #E5533D
  faint="${esc}[38;2;117;117;117m" # #757575
  good="${esc}[38;2;121;201;139m"  # #79C98B
  bad="${esc}[38;2;224;108;108m"   # #E06C6C
  reset="${esc}[0m"
else
  accent=
  faint=
  good=
  bad=
  reset=
fi

# A torii, the gate of an Inari shrine. It cannot wrap, so it is printed
# as-is.
banner='╺━┳━━━━━┳━╸
 ━╋━━━━━╋━
  ┃     ┃'

printf '\n' >&2
printf '%s\n' "$banner" | while IFS= read -r line; do
  printf '  %s%s%s\n' "$accent" "$line" "$reset" >&2
done
printf '  %s%s%s\n\n' "$faint" 'inari: kon in your chat' "$reset" >&2

die() {
  printf '  %s✗%s inari: %s\n' "$bad" "$reset" "$*" >&2
  exit 1
}

# stage reports work in progress; finish reports the one successful outcome.
# Each line is indented two cells to align with the mark above.
stage() { printf '  %s•%s %s\n' "$accent" "$reset" "$*" >&2; }
finish() { printf '  %s✓%s %s\n' "$good" "$reset" "$*" >&2; }

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

need curl
need tar
need mktemp

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*)
    die "this is Windows; run the PowerShell installer instead:
    iwr -useb https://raw.githubusercontent.com/$repo/main/scripts/install.ps1 | iex"
    ;;
  *) die "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# The /releases/latest redirect names the tag without API quota or a token.
# For a few minutes after a release is published GitHub serves the generic
# releases page instead, so fall back to the API before giving up.
if [ -z "$version" ]; then
  version=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$base_url/latest" | sed 's#/*$##; s#.*/##')
  case "$version" in
    '' | latest | releases)
      json=$(curl -fsSL -H 'Accept: application/vnd.github+json' \
        -H 'User-Agent: inari-installer' \
        "https://api.github.com/repos/$repo/releases/latest") ||
        die "could not determine the latest release; set INARI_VERSION to install a specific release"
      version=$(printf '%s\n' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)
      ;;
  esac
fi
[ -n "$version" ] || die "could not determine the latest release; set INARI_VERSION"
case "$version" in
  v*) ;;
  *) version="v$version" ;;
esac

name="inari_${version#v}_${os}_${arch}"
url="$base_url/download/$version"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/inari.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

stage "downloading inari $version ($os/$arch)"
curl -fsSL -o "$tmp/$name.tar.gz" "$url/$name.tar.gz" ||
  die "download failed: $url/$name.tar.gz"
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" ||
  die "download failed: $url/checksums.txt"

stage "verifying checksum"
expected=$(awk -v n="$name.tar.gz" '{ sub(/^\.\//, "", $2) } $2 == n { print $1; exit }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "no checksum for $name.tar.gz"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$name.tar.gz" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$name.tar.gz" | awk '{ print $1 }')
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || die "checksum mismatch for $name.tar.gz"

stage "installing inari $version"
tar -xzf "$tmp/$name.tar.gz" -C "$tmp" || die "could not extract $name.tar.gz"
mkdir -p "$install_dir"
# Install beside the target and rename over it, so a running inari keeps its
# file and a failed copy leaves the old one in place.
cp "$tmp/$name/inari" "$install_dir/.inari-install.$$"
chmod 0755 "$install_dir/.inari-install.$$"
mv -f "$install_dir/.inari-install.$$" "$install_dir/inari"

finish "inari $version installed to $install_dir/inari"

case ":${PATH:-}:" in
  *":$install_dir:"*) ;;
  *)
    printf '\n%s is not on your PATH. Add it:\n  export PATH="%s:$PATH"\n' \
      "$install_dir" "$install_dir" >&2
    ;;
esac

config="${XDG_CONFIG_HOME:-$HOME/.config}/inari/config.json"
if [ ! -e "$config" ]; then
  printf '\nNext, write %s, starting from the example:\n  https://github.com/%s/blob/main/config.example.json\n' \
    "$config" "$repo" >&2
fi
printf '\nUpgrade later with "inari upgrade".\n' >&2
