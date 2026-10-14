#!/bin/sh
# Build release archives into dist/: hpctun for each platform, each with
# LICENSE and README.md (the BSD license asks binary redistributions to
# carry the notice) and the site files in sites/ (osu.toml), for
# `hpctun init --from`. The Linux and Windows archives also hold
# hpctun-tray, and hpctun-tray-<version>-darwin-universal.zip holds the
# macOS app with both programs (scripts/macapp).
# Refuses a dirty tree, so no archive says -dirty.
set -eu
cd "$(dirname "$0")/.."
# A CI checkout may belong to another user; git (and Go, which
# stamps the version from it) then refuses to read it. Allow it once.
if [ -n "${CI:-}" ] && ! git config --global --get-all safe.directory | grep -qxF "$PWD"; then
	git config --global --add safe.directory "$PWD"
fi
# On its own line, so set -e stops here if git cannot read the tree.
status=$(git status --porcelain)
if [ -n "$status" ]; then
	echo "dist: the tree has uncommitted changes; commit or stash them first" >&2
	exit 1
fi
export CGO_ENABLED=0
# macOS's bsdtar adds extended attributes (quarantine, provenance) as pax
# headers, which GNU tar on Linux warns about when unpacking.
tarflags=
if tar --version 2>/dev/null | grep -q bsdtar; then
	tarflags="--no-xattrs --no-mac-metadata"
fi
rm -rf dist
mkdir -p dist
go build -trimpath -o dist/hpctun-host ./cmd/hpctun
version=$(dist/hpctun-host --version | awk '{print $NF}')
rm dist/hpctun-host
case "$version" in
v*-dev | *-dirty | "")
	echo "dist: unexpected version '$version'" >&2
	exit 1
	;;
esac
# go build reads the commit from the nearest .git directory; in a git
# worktree .git is a file, so a worktree inside another checkout is stamped
# with that checkout's commit.
head=$(git rev-parse --short=7 HEAD)
case "$version" in
*-"$head") ;;
*)
	echo "dist: $version is not HEAD ($head); build from the main checkout, not a git worktree" >&2
	exit 1
	;;
esac
for target in windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64; do
	os=${target%/*}
	arch=${target#*/}
	name="hpctun-$version-$os-$arch"
	exe=hpctun
	[ "$os" = windows ] && exe=hpctun.exe
	mkdir -p "dist/$name"
	GOOS=$os GOARCH=$arch go build -trimpath -o "dist/$name/$exe" ./cmd/hpctun
	if [ "$os" = linux ]; then
		GOOS=$os GOARCH=$arch go build -trimpath -o "dist/$name/hpctun-tray" ./cmd/hpctun-tray
	fi
	if [ "$os" = windows ]; then
		# A GUI program: no console window of its own.
		GOOS=$os GOARCH=$arch go build -trimpath -ldflags -H=windowsgui -o "dist/$name/hpctun-tray.exe" ./cmd/hpctun-tray
	fi
	if [ "$os" = darwin ]; then
		# The thin builds hpctun-tray.app is made from, below.
		mkdir -p dist/app
		cp "dist/$name/hpctun" "dist/app/hpctun-$arch"
		GOOS=$os GOARCH=$arch go build -trimpath -o "dist/app/hpctun-tray-$arch" ./cmd/hpctun-tray
	fi
	cp LICENSE README.md sites/*.toml "dist/$name/"
	if [ "$os" = windows ]; then
		go run ./scripts/mkzip "dist/$name.zip" "dist/$name"
	else
		# shellcheck disable=SC2086 # tarflags is a word list
		tar $tarflags -C dist -czf "dist/$name.tar.gz" "$name"
	fi
	rm -rf "dist/$name"
done
# hpctun-tray.app, universal, with hpctun inside it too.
go run ./scripts/macapp -version "$version" -out "dist/hpctun-tray-$version-darwin-universal.zip" \
	-hpctun-arm64 dist/app/hpctun-arm64 -hpctun-amd64 dist/app/hpctun-amd64 \
	-tray-arm64 dist/app/hpctun-tray-arm64 -tray-amd64 dist/app/hpctun-tray-amd64 \
	LICENSE README.md sites/*.toml
rm -rf dist/app
(cd dist && { sha256sum -- hpctun-* 2>/dev/null || shasum -a 256 -- hpctun-*; } >SHA256SUMS)
# SHA256SUMS is signed later, by the release workflow's publish job (scripts/sign), the only place the signing key is available.
echo "$version" >dist/VERSION
ls -l dist
