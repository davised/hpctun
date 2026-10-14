#!/bin/sh
# Quality gate, run locally and by CI: formatting, vet and tests here, then
# a Windows cross-compile of every package and its tests (the tests cannot
# run on Windows from here, but they must build), and a macOS vet.
set -eu
cd "$(dirname "$0")/.."
# Like CI and the release: nothing here needs cgo, the macOS tray
# included (internal/mactray).
export CGO_ENABLED=0
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "$unformatted"
	echo "check: the files above need gofmt" >&2
	exit 1
fi
go vet ./...
go test ./...
GOOS=windows go vet ./...
# The macOS tray is only built for releases; vet it here so CI sees it.
GOOS=darwin go vet ./...
for pkg in $(go list ./...); do
	GOOS=windows go test -c -o /dev/null "$pkg"
done
echo "check: ok"
