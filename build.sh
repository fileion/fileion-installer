#!/bin/sh
# Reproducible build of the two release files. The same source, Go version and SOURCE_DATE_EPOCH give the same bytes.
# Usage: ./build.sh [version]   Output: dist/Fileion-Installer.exe and dist/Fileion-Installer (the AppImage step packs the latter).
set -eu
cd "$(dirname "$0")"
VERSION="${1:-dev}"
export CGO_ENABLED=0
mkdir -p dist

# The Windows icon and version info (winres/): one .syso next to main.go, linked only into the windows/amd64 build. go-winres is pinned to
# an exact release (v0.3.3, 2024-04-11), run with `go run` (no global install; the Go module proxy and checksum database verify it).
# The version is the tag: v0.2.0 gives 0.2.0.0; anything else (dev, a branch) gives 0.0.0.0.
case "${VERSION#v}" in
  [0-9]*.[0-9]*.[0-9]*) FILEVERSION="$(echo "${VERSION#v}" | sed 's/[^0-9.].*//').0" ;;
  *) FILEVERSION="0.0.0.0" ;;
esac
rm -f rsrc_windows_amd64.syso
sed -e "s/@VERSION@/${VERSION#v}/g" -e "s/@FILEVERSION@/$FILEVERSION/g" winres/winres.json.in > winres/winres.json
( cd winres && go run github.com/tc-hib/go-winres@v0.3.3 make --in winres.json --out ../rsrc --arch amd64 )
rm -f winres/winres.json
FLAGS="-trimpath -buildvcs=false -ldflags=-s\ -w\ -buildid=\ -X\ main.version=${VERSION}"
# exported in a subshell: a GOOS=… prefix on `eval` is not passed on to `go` by dash (the CI shell), which built a Linux file named .exe
( export GOOS=windows GOARCH=amd64; eval go build $FLAGS -o dist/Fileion-Installer.exe . )
( export GOOS=linux GOARCH=amd64; eval go build $FLAGS -o dist/Fileion-Installer . )
( cd dist && shasum -a 256 Fileion-Installer.exe Fileion-Installer > SHA256SUMS )
cat dist/SHA256SUMS
