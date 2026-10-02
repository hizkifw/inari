#!/bin/sh
set -eu

# Builds the release archives into dist/, with checksums.txt over them.
#
# With no targets it builds all of them. CI builds each in a job of its own,
# and on a version tag publishes those archives rather than building again,
# with checksums.txt written over all of them by checksums.sh.

if [ "$#" -lt 1 ]; then
  echo "usage: $0 vX.Y.Z [os/arch...]" >&2
  exit 2
fi

version=$1
shift
targets=${*:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "version must look like v0.1.0" >&2; exit 2 ;;
esac

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
dist="$root/dist"
stage="$dist/.stage"
cd "$root"
rm -rf "$dist"
mkdir -p "$stage"

for target in $targets; do
  os=${target%/*}
  arch=${target#*/}
  name="inari_${version#v}_${os}_${arch}"
  dir="$stage/$name"
  mkdir -p "$dir"
  binary=inari
  [ "$os" = windows ] && binary=inari.exe
  echo "building $target"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=$version" -o "$dir/$binary" ./cmd/inari
  cp "$root/README.md" "$root/LICENSE" "$root/config.example.json" "$dir/"
  if [ "$os" = windows ]; then
    archive="$dist/$name.zip"
    (cd "$stage" && zip -qr "$archive" "$name")
  else
    archive="$dist/$name.tar.gz"
    (cd "$stage" && tar -czf "$archive" "$name")
  fi
done

rm -rf "$stage"
"$root/scripts/checksums.sh" "$dist"
echo "release archives written to $dist"
