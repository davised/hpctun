package slurmjob

import "strings"

// nodeScript is the batch script of hpctun code's job (spec: "The node
// script"). It runs on the compute node under sh and writes "@kind ..."
// marker lines to stdout, the job log, which the watcher sends back to
// hpctun. It travels to sbatch on stdin, so unlike the gateway scripts it
// may use quotes and "!". server_dirs is filled in by NodeScript.
const nodeScript = `#!/bin/sh
# hpctun code's job: VS Code's tunnel on this node. This outer part runs in
# the environment sbatch copied from the gateway: it fixes S and C (so they
# match the gateway's), then runs the steps in a clean login environment on
# the node, so the node's own login setup (architecture, modules, proxy)
# applies, as after salloc (spec: step 0).
S=${XDG_STATE_HOME:-$HOME/.local/state}/hpctun/vscode
C=${XDG_CACHE_HOME:-$HOME/.cache}/hpctun/vscode
steps=$(cat <<'HPCTUN_STEPS'
server_dirs=
usual=$(umask)
umask 077
S=$HPCTUN_S
C=$HPCTUN_C
u=$(id -un)
host=$(hostname -s)
N=$S/nodes/$host
fail() { echo "@error $*"; exit 1; }
# refuse stops the job when a directory the CLI is installed into or run from
# is not the user's own. Whoever else owns it, or can write to it, or reaches
# it through a link, could put their own executable there, so it is the user's
# to remove rather than something to work around.
refuse() { echo "hpctun: $1 is not yours; remove it and run hpctun code again" >&2; exit 1; }
valid() { [ -s "$1" ] && grep -q '"id"' "$1"; }
field() { sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$2" 2>/dev/null | head -n 1; }
mine() { [ -d "$1" ] && [ ! -L "$1" ] && [ -O "$1" ]; }
# minefile is true when $1 is a regular file (not a link) belonging to the user.
minefile() { [ -f "$1" ] && [ ! -L "$1" ] && [ -O "$1" ]; }
# lsmode is $1's mode as ls shows it (drwxrwxrwt), which reads the same on
# every system, unlike stat's or find's options. ACLs are not looked at.
lsmode() { ls -ld "$1" 2>/dev/null | cut -c 1-10; }
# lsowner is $1's owner name as ls shows it (root, or the user's name).
lsowner() { set -- $(ls -ld "$1" 2>/dev/null); [ $# -ge 3 ] && printf '%s\n' "$3"; }
open_to_others() { case $(lsmode "$1") in (?????w????|????????w?) return 0;; esac; return 1; }
sticky() { case $(lsmode "$1") in (?????????[tT]) return 0;; esac; return 1; }
# sha_of is $1's sha256 as sha256sum (Linux) or shasum -a 256 (macOS) prints it.
sha_of() { { sha256sum "$1" 2>/dev/null || shasum -a 256 "$1"; } | cut -d ' ' -f 1; }
# fetch_cli downloads VS Code's CLI to $1/cli.tgz and checks it against
# the sha256 that Microsoft's update service publishes for that build.
# https only, redirects included.
fetch_cli() {
	meta=$(curl -fsS --proto '=https' --proto-redir '=https' --max-time 60 "https://update.code.visualstudio.com/api/update/cli-linux-$arch/stable/latest") || return 1
	url=$(printf '%s\n' "$meta" | sed -n 's/.*"url":"\(https:[^"]*\)".*/\1/p')
	want=$(printf '%s\n' "$meta" | sed -n 's/.*"sha256hash":"\([0-9a-f]\{64\}\)".*/\1/p')
	if [ -z "$url" ] || [ -z "$want" ]; then
		echo "the update service's answer has no download link or checksum" >&2
		return 1
	fi
	curl -fsS -L --proto '=https' --proto-redir '=https' --max-time 300 -o "$1/cli.tgz" "$url" || return 1
	got=$(sha_of "$1/cli.tgz")
	if [ "$got" != "$want" ]; then
		echo "the VS Code CLI download does not match its published sha256" >&2
		return 1
	fi
}
echo "@step setup"
local_fs() {
	case $(stat -f -c %T "$1" 2>/dev/null) in
	(""|nfs|nfs4|lustre|gpfs|beegfs|cifs|smb2|fuseblk|fuse.*|tmpfs) return 1;;
	esac
	return 0
}

case $(uname -m) in
(x86_64) arch=x64;;
(aarch64) arch=arm64;;
(*) fail "arch $(uname -m)";;
esac

mkdir -p "$N" "$C/bin" || fail "dir cannot create $N or $C/bin"
chmod 700 "$N"
cache=
for b in $server_dirs; do
	[ -d "$b" ] && local_fs "$b" || continue
	# Anyone who may write to $b can rename $b/$u and put their own in its
	# place, unless $b is sticky (as /tmp is). $b/$u must be ours alone for
	# the same reason: a group-writable one lets the group swap the cache.
	if open_to_others "$b" && ! sticky "$b"; then continue; fi
	# The base's own mode is not enough either: whoever owns the base or any
	# directory above it can rename it and put their own cache in its place,
	# so the base and every parent up to / must belong to the user or to root,
	# and no parent another user can write to (without the sticky bit) may be
	# in the way. cd/pwd -P follows the base's links first, so a safe-looking
	# base that is a link through storage nobody checked is passed over too.
	real=$(cd "$b" 2>/dev/null && pwd -P) || continue
	safe=1 p=$real
	while [ -n "$p" ]; do
		# -O is ours, as the shell sees it, whatever name the files carry.
		if [ ! -O "$p" ] && [ "$(lsowner "$p")" != root ]; then safe=; break; fi
		if open_to_others "$p" && ! sticky "$p"; then safe=; break; fi
		up=${p%/*}; [ -n "$up" ] || up=/
		[ "$up" = "$p" ] && break
		p=$up
	done
	[ "$safe" ] || continue
	mkdir "$b/$u" 2>/dev/null
	mine "$b/$u" || continue
	open_to_others "$b/$u" && continue
	mkdir "$b/$u/hpctun-vscode" 2>/dev/null
	if mine "$b/$u/hpctun-vscode" && chmod 700 "$b/$u/hpctun-vscode"; then
		cache=$b/$u/hpctun-vscode
		break
	fi
done
if [ -z "$cache" ]; then
	cache=$N/servers.d
	mkdir -p "$cache" || fail "dir cannot create $cache"
fi
if [ -d "$N/servers" ] && [ ! -L "$N/servers" ]; then
	mv -f "$N/servers" "$N/servers.old-$(date +%Y%m%d%H%M%S)"
fi
ln -sfn "$cache" "$N/servers" || fail "dir cannot link $N/servers to $cache"
echo "@cache $cache"

code=$C/bin/code-$arch
sum=$code.sha256
# A cached CLI is used only while its cache cannot be replaced by another user
# and the executable still matches the sha256 recorded when hpctun installed
# it. Its permissions and timestamp alone say nothing: another cluster user
# with write access to an old cache could put their own executable there and
# touch it, and hpctun would run it.
cached() {
	mine "$C" || return 1
	open_to_others "$C" && return 1
	mine "$C/bin" || return 1
	open_to_others "$C/bin" && return 1
	minefile "$code" || return 1
	minefile "$sum" || return 1
	[ "$(sha_of "$code")" = "$(cat "$sum")" ]
}
ok=n
cached && ok=y
if [ "$ok" = n ] || [ -n "$(find "$code" -mtime +30 2>/dev/null)" ]; then
	echo "@step download"
	# The CLI is installed into $C/bin just below and run at the end of this
	# job. Anyone who owns either directory, can write to it, or reaches it
	# through a symlink could replace the executable in the moment between the
	# install and that run, so both are made the user's own 0700 first, or the
	# job is refused. chmod can fail without saying so, so its result is read
	# back rather than assumed.
	for d in "$C" "$C/bin"; do
		if [ -L "$d" ] || [ ! -O "$d" ] || [ ! -d "$d" ]; then refuse "$d"; fi
		chmod 700 "$d"
		if open_to_others "$d"; then refuse "$d"; fi
	done
	# A cache that failed a check must not be used, so it is removed first. A
	# cache that is merely old stays until the download succeeds, so a failed
	# refresh still falls back to it.
	[ "$ok" = y ] || rm -f "$code" "$sum"
	tmp=$(mktemp -d "$C/bin/.fetch.XXXXXX") || fail "cli cannot create a temporary directory in $C/bin"
	if fetch_cli "$tmp" && tar -xzf "$tmp/cli.tgz" -C "$tmp" && [ -x "$tmp/code" ]; then
		got=$(sha_of "$tmp/code")
		mv -f "$tmp/code" "$code" && touch "$code" && printf '%s\n' "$got" >"$sum" && chmod 600 "$sum"
	elif [ -x "$code" ]; then
		echo "@warn cli-refresh the download failed; using the CLI already there"
	else
		rm -rf "$tmp"
		fail "cli the download from update.code.visualstudio.com failed"
	fi
	rm -rf "$tmp"
fi
echo "@cli $("$code" --version 2>/dev/null | head -n 1)"

case $("$code" tunnel user show --cli-data-dir "$N" 2>/dev/null) in
(*"logged in with provider"*) ;;
(*)
	echo "@login"
	"$code" tunnel user login --provider microsoft --cli-data-dir "$N" || fail "login the sign-in did not finish"
	;;
esac

# This node's own tunnel, named <node>-<user>: the node whole, the user cut
# so the name stays within VS Code's 20 characters. Its record lives in $N
# alone (spec: "Per-node tunnels").
name=$(printf "%s-%s" "$host" "$u" | cut -c 1-20)
unreg() { "$code" tunnel unregister --cli-data-dir "$1" >/dev/null 2>&1; rm -f "$1/code_tunnel.json"; }
# Once: the tunnel earlier hpctun shared between nodes (hpc-<user>) is
# unregistered and its shared record removed; a node record still on it is
# dropped, so this node registers its own.
for r in "$S"/tunnel-*.json; do
	[ -f "$r" ] || continue
	if valid "$r"; then
		cp -f "$r" "$N/code_tunnel.json" && unreg "$N"
	fi
	rm -f "$r"
done
[ "$(field name "$N/code_tunnel.json")" = "hpc-$u" ] && rm -f "$N/code_tunnel.json"
rm -f "$N/tunnel-stable.lock"
# At most 8 node tunnels stay registered, the most recently used: the
# others are unregistered, keeping their sign-in, so coming back to that
# node registers again without a new device code.
touch "$N/last-used"
ls -t "$S"/nodes/*/last-used 2>/dev/null | sed 1,8d | while IFS= read -r f; do
	d=${f%/last-used}
	if valid "$d/code_tunnel.json"; then
		echo "@pruned ${d##*/}"
		unreg "$d"
	fi
done
# The tunnel, and the VS Code server and terminals it starts, get the
# login's usual umask: files made from VS Code, in group project
# directories too, follow it. $N stays 0700, so the CLI's files are still
# the user's alone.
umask "$usual"
echo "@step tunnel"
exec "$code" tunnel --name "$name" --accept-server-license-terms --cli-data-dir "$N"
HPCTUN_STEPS
)
set -- "HPCTUN_S=$S" "HPCTUN_C=$C"
# SSH_CONNECTION and SSH_CLIENT are the gateway session's, as after salloc:
# prompts (starship's user@host) and scripts take the terminal for remote.
for v in HOME USER LOGNAME SHELL LANG http_proxy https_proxy no_proxy HTTP_PROXY HTTPS_PROXY NO_PROXY \
	SSH_CONNECTION SSH_CLIENT XDG_STATE_HOME XDG_CACHE_HOME XDG_CONFIG_HOME XDG_DATA_HOME $(env | sed -n 's/^\(SLURM_[A-Za-z0-9_]*\)=.*/\1/p'); do
	eval "x=\${$v+set}"
	if [ "$x" = set ]; then eval "set -- \"\$@\" \"$v=\${$v}\""; fi
done
case ${SHELL##*/} in
sh|bash|zsh|ksh) login=$SHELL;;
*) login=/bin/bash;;
esac
[ -x "$login" ] || login=/bin/bash
echo "@step start"
exec env -i PATH=/usr/bin:/bin "$@" "$login" -l -c 'exec /bin/sh -c "$0"' "$steps"
`

// NodeScript is the job's batch script with serverDirs as the candidate
// bases for the node-local server cache. Each dir must already be
// validated (config: absolute, letters, digits, . _ - /).
func NodeScript(serverDirs []string) string {
	return strings.Replace(nodeScript, "\nserver_dirs=\n", "\nserver_dirs=\""+strings.Join(serverDirs, " ")+"\"\n", 1)
}
