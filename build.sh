#!/bin/sh
# Reproducible build of the two release files. The same source, Go version and SOURCE_DATE_EPOCH give the same bytes.
# Usage: ./build.sh [version]   Output: dist/Fileion-Installer.exe and dist/Fileion-Installer (the AppImage step packs the latter).
set -eu
cd "$(dirname "$0")"
VERSION="${1:-dev}"
export CGO_ENABLED=0
mkdir -p dist
FLAGS="-trimpath -buildvcs=false -ldflags=-s\ -w\ -buildid=\ -X\ main.version=${VERSION}"
# exported in a subshell: a GOOS=… prefix on `eval` is not passed on to `go` by dash (the CI shell), which built a Linux file named .exe
( export GOOS=windows GOARCH=amd64; eval go build $FLAGS -o dist/Fileion-Installer.exe . )
( export GOOS=linux GOARCH=amd64; eval go build $FLAGS -o dist/Fileion-Installer . )
( cd dist && shasum -a 256 Fileion-Installer.exe Fileion-Installer > SHA256SUMS )
cat dist/SHA256SUMS
