package slurmjob

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCode stands in for VS Code's CLI. It logs its arguments to
// $STUB/code.log. `--version` prints a version; `tunnel user show` prints
// $STUB/show (default "not logged in") and $STUB/show.err on stderr;
// `tunnel user login` prints the sign-in line and exits with
// $STUB/login.exit (default 0); `tunnel --name N ... --cli-data-dir D`
// writes D/code_tunnel.json (id from $STUB/id, default abc-123; name from
// $STUB/name, default N) unless it already holds an id other than the one in
// $STUB/dead (a tunnel the service no longer knows), then prints the CLI's
// banner line for that name and exits.
const fakeCode = `#!/bin/sh
printf "%s\n" "$*" >>"$STUB/code.log"
case "$1 $2 $3" in
"--version  ") echo "code 1.140.0 (commit 07f8)";;
"tunnel user show")
	cat "$STUB/show.err" >&2 2>/dev/null
	cat "$STUB/show" 2>/dev/null || echo "not logged in";;
"tunnel user login")
	echo "To sign in, use a web browser to open the page https://login.microsoft.com/device and enter the code AB12CD34 to authenticate."
	exit "$(cat "$STUB/login.exit" 2>/dev/null || echo 0)";;
"tunnel --name "*)
	name=$3; dir=
	while [ $# -gt 0 ]; do [ "$1" = --cli-data-dir ] && dir=$2; shift; done
	if [ -f "$STUB/dead" ] && grep -q "\"id\":\"$(cat "$STUB/dead")\"" "$dir/code_tunnel.json" 2>/dev/null; then
		rm -f "$dir/code_tunnel.json"
	fi
	if ! grep -q "\"id\"" "$dir/code_tunnel.json" 2>/dev/null; then
		name=$(cat "$STUB/name" 2>/dev/null || echo "$name")
		printf "{\"name\":\"%s\",\"id\":\"%s\",\"cluster\":\"usw3\"}" "$name" "$(cat "$STUB/id" 2>/dev/null || echo abc-123)" >"$dir/code_tunnel.json"
	fi
	env >"$STUB/tunnel.env"
	umask >"$STUB/tunnel.umask"
	printf "  \342\236\234  Tunnel:   %s\n" "$name";;
esac
`

// fakeTool prints $STUB/<tool> when it exists, else its default.
func fakeTool(def string) string {
	return "#!/bin/sh\ntool=$(basename \"$0\")\nif [ -f \"$STUB/$tool.out\" ]; then cat \"$STUB/$tool.out\"; else printf \"%s\\n\" \"" + def + "\"; fi\n"
}

// fakeStat answers `stat -f -c %T <path>` from $STUB/fs/<path with / as _>,
// default ext2/ext3.
const fakeStat = `#!/bin/sh
f="$STUB/fs/$(printf "%s" "$4" | tr / _)"
if [ -f "$f" ]; then cat "$f"; else echo ext2/ext3; fi
`

// fakeCurl logs its arguments to $STUB/curl.log. With -o it copies
// $STUB/cli.tgz there, or fails when that is missing; without -o it
// prints $STUB/api.json, the update service's answer.
const fakeCurl = `#!/bin/sh
printf "%s\n" "$*" >>"$STUB/curl.log"
out=
while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
if [ -z "$out" ]; then
	[ -f "$STUB/api.json" ] || { echo "curl: (6) Could not resolve host" >&2; exit 6; }
	cat "$STUB/api.json"
	exit 0
fi
[ -f "$STUB/cli.tgz" ] || { echo "curl: (6) Could not resolve host" >&2; exit 6; }
cp "$STUB/cli.tgz" "$out"
`

type nodeWorld struct {
	t              *testing.T
	dir, bin, home string
	state, cache   string   // the S and C roots
	scratch        string   // a server_dirs base
	env            []string // extra variables for the job's environment
}

func newNodeWorld(t *testing.T) *nodeWorld {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the node script runs on a compute node, under sh")
	}
	d := t.TempDir()
	w := &nodeWorld{t: t, dir: d, bin: filepath.Join(d, "bin"), home: filepath.Join(d, "home"), scratch: filepath.Join(d, "scratch")}
	w.state = filepath.Join(w.home, ".local/state/hpctun/vscode")
	w.cache = filepath.Join(w.home, ".cache/hpctun/vscode")
	for _, p := range []string{w.bin, w.home, w.scratch} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tools := map[string]string{
		"id":       fakeTool("me"),
		"hostname": fakeTool("node1"),
		"uname":    fakeTool("x86_64"),
		"stat":     fakeStat,
		"curl":     fakeCurl,
		"sleep":    "#!/bin/sh\nexit 0\n",
	}
	for name, body := range tools {
		w.write(filepath.Join(w.bin, name), body, 0o755)
	}
	w.tgz(fakeCode)
	// The steps run in a clean login environment: the login shell reads this
	// profile, which is the only way the stubs get onto PATH there.
	w.write(filepath.Join(w.home, ".profile"), "PATH=\""+w.bin+":$PATH\"; export PATH; STUB=\""+d+"\"; export STUB\n", 0o644)
	return w
}

func (w *nodeWorld) write(p, s string, mode os.FileMode) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		w.t.Fatal(err)
	}
}

// tgz makes $STUB/cli.tgz hold one executable file, code, with body.
func (w *nodeWorld) tgz(body string) { w.tgzAt(body, time.Now()) }

// tgzAt is tgz with the file's modification time set to mod.
func (w *nodeWorld) tgzAt(body string, mod time.Time) {
	w.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "code", Mode: 0o755, Size: int64(len(body)), ModTime: mod}); err != nil {
		w.t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	w.write(filepath.Join(w.dir, "cli.tgz"), buf.String(), 0o644)
	sum := sha256.Sum256(buf.Bytes())
	w.write(filepath.Join(w.dir, "api.json"), fmt.Sprintf(`{"url":"https://example.invalid/cli.tgz","name":"1.140.0","sha256hash":"%x","productVersion":"1.140.0"}`, sum), 0o644)
}

func (w *nodeWorld) read(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// cachedCLI writes body as the cached CLI at $C/bin/code-$arch together with
// the sha256 hpctun records beside it when it installs one.
func (w *nodeWorld) cachedCLI(body string) string {
	w.t.Helper()
	bin := filepath.Join(w.cache, "bin/code-x64")
	w.write(bin, body, 0o755)
	w.write(bin+".sha256", fmt.Sprintf("%x\n", sha256.Sum256([]byte(body))), 0o600)
	return bin
}

// run runs the node script with server_dirs dirs and returns its output
// (stdout and stderr together, as in the job log) and exit status.
func (w *nodeWorld) run(dirs ...string) (string, int) {
	w.t.Helper()
	cmd := exec.Command("sh", "-c", NodeScript(dirs))
	cmd.Env = append([]string{"PATH=" + w.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + w.home, "STUB=" + w.dir,
		"SHELL=/bin/sh", "SLURM_JOB_NAME=hpctun-code", "SLURM_JOB_ID=42", "FOO=from-the-gateway", "TMPDIR=/gateway-tmp"}, w.env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return out.String(), 0
}

func TestNodeScriptFirstRun(t *testing.T) {
	w := newNodeWorld(t)
	out, code := w.run(w.scratch)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	n := filepath.Join(w.state, "nodes/node1")
	for _, want := range []string{"@cache " + filepath.Join(w.scratch, "me/hpctun-vscode"), "@cli code 1.140.0 (commit 07f8)", "@login",
		"enter the code AB12CD34", "Tunnel:   node1-me"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if fi, err := os.Stat(n); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("node dir: %v %v", fi, err)
	}
	// The @step markers come in order, each before the work it names.
	at := -1
	for _, want := range []string{"@step start", "@step setup", "@cache ", "@step download", "@cli ", "@login", "@step tunnel", "Tunnel:   node1-me"} {
		i := strings.Index(out, want)
		if i <= at {
			t.Errorf("%q at %d, after %d\n%s", want, i, at, out)
		}
		at = i
	}
	if l, err := os.Readlink(filepath.Join(n, "servers")); err != nil || l != filepath.Join(w.scratch, "me/hpctun-vscode") {
		t.Fatalf("servers link %q %v", l, err)
	}
	if got := w.read(filepath.Join(n, "code_tunnel.json")); !strings.Contains(got, `"name":"node1-me","id":"abc-123"`) {
		t.Fatalf("node record %q", got)
	}
	log := w.read(filepath.Join(w.dir, "code.log"))
	for _, want := range []string{"tunnel user login --provider microsoft --cli-data-dir " + n,
		"tunnel --name node1-me --accept-server-license-terms --cli-data-dir " + n} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in code.log\n%s", want, log)
		}
	}
	if _, err := os.Stat(filepath.Join(w.cache, "bin/code-x64")); err != nil {
		t.Fatal(err)
	}
}
func TestNodeScriptServerDirs(t *testing.T) {
	w := newNodeWorld(t)
	nfs := filepath.Join(w.dir, "nfs")
	os.MkdirAll(nfs, 0o755)
	w.write(filepath.Join(w.dir, "fs", strings.ReplaceAll(nfs, "/", "_")), "nfs\n", 0o644)
	missing := filepath.Join(w.dir, "missing")
	out, code := w.run(missing, nfs, w.scratch)
	if code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w.scratch, "me/hpctun-vscode")) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// none usable: the home fallback
	w2 := newNodeWorld(t)
	out, code = w2.run(filepath.Join(w2.dir, "missing"))
	want := filepath.Join(w2.state, "nodes/node1/servers.d")
	if code != 0 || !strings.Contains(out, "@cache "+want) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestNodeScriptRenamesRealServersDir(t *testing.T) {
	w := newNodeWorld(t)
	real := filepath.Join(w.state, "nodes/node1/servers")
	w.write(filepath.Join(real, "Stable-x/keep"), "x", 0o644)
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if fi, err := os.Lstat(real); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("servers is not a link: %v %v", fi, err)
	}
	old, _ := filepath.Glob(filepath.Join(w.state, "nodes/node1/servers.old-*/Stable-x/keep"))
	if len(old) != 1 {
		t.Fatal("the real servers dir was not kept aside")
	}
}

func TestNodeScriptArch(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "uname.out"), "aarch64\n", 0o644)
	if out, code := w.run(w.scratch); code != 0 || !strings.Contains(w.read(filepath.Join(w.dir, "curl.log")), "cli-linux-arm64/stable") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	w2 := newNodeWorld(t)
	w2.write(filepath.Join(w2.dir, "uname.out"), "ppc64le\n", 0o644)
	if out, code := w2.run(w2.scratch); code != 1 || !strings.Contains(out, "@error arch ppc64le") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestNodeScriptCLIRefresh(t *testing.T) {
	w := newNodeWorld(t)
	bin := w.cachedCLI(fakeCode)
	// fresh: no download
	if out, code := w.run(w.scratch); code != 0 || fileExistsT(filepath.Join(w.dir, "curl.log")) {
		t.Fatalf("exit %d, curl ran\n%s", code, out)
	}
	// 31 days old: downloaded again
	old := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(bin, old, old)
	if out, code := w.run(w.scratch); code != 0 || !fileExistsT(filepath.Join(w.dir, "curl.log")) {
		t.Fatalf("exit %d, no refresh\n%s", code, out)
	}
	// old and the download fails: keep the old one
	os.Chtimes(bin, old, old)
	os.Remove(filepath.Join(w.dir, "cli.tgz"))
	out, code := w.run(w.scratch)
	if code != 0 || !strings.Contains(out, "@warn cli-refresh") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if left, _ := filepath.Glob(filepath.Join(w.cache, "bin/.fetch.*")); len(left) != 0 {
		t.Fatalf("temporary dirs left: %v", left)
	}
}

func TestNodeScriptCLIDownloadFails(t *testing.T) {
	w := newNodeWorld(t)
	os.Remove(filepath.Join(w.dir, "cli.tgz"))
	out, code := w.run(w.scratch)
	if code != 1 || !strings.Contains(out, "@error cli ") || !strings.Contains(out, "Could not resolve host") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestNodeScriptCLIChecked: the CLI comes over https only, redirects
// included, and is installed only when it matches the sha256 Microsoft's
// update service publishes for it.
func TestNodeScriptCLIChecked(t *testing.T) {
	w := newNodeWorld(t)
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(w.read(filepath.Join(w.dir, "curl.log"))), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "https://update.code.visualstudio.com/api/update/cli-linux-x64/stable/latest") ||
		!strings.Contains(lines[1], "https://example.invalid/cli.tgz") {
		t.Fatalf("curl calls %q", lines)
	}
	for _, l := range lines {
		if !strings.Contains(l, "--proto =https --proto-redir =https") {
			t.Errorf("not https-only: %s", l)
		}
	}
	w2 := newNodeWorld(t)
	w2.write(filepath.Join(w2.dir, "api.json"), `{"url":"https://example.invalid/cli.tgz","sha256hash":"`+strings.Repeat("0", 64)+`"}`, 0o644)
	out, code := w2.run(w2.scratch)
	if code != 1 || !strings.Contains(out, "does not match its published sha256") || !strings.Contains(out, "@error cli ") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if fileExistsT(filepath.Join(w2.cache, "bin/code-x64")) {
		t.Fatal("installed a CLI that did not match")
	}
}

// TestNodeScriptCLIRecordsSha: installing the CLI records the sha256 of the
// file that was installed, 0600, so a later run can tell it is still the same
// executable.
func TestNodeScriptCLIRecordsSha(t *testing.T) {
	w := newNodeWorld(t)
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	sum := filepath.Join(w.cache, "bin/code-x64.sha256")
	fi, err := os.Stat(sum)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("sha256 file: %v %v", fi, err)
	}
	if got, want := strings.TrimSpace(w.read(sum)), fmt.Sprintf("%x", sha256.Sum256([]byte(fakeCode))); got != want {
		t.Fatalf("recorded sha256 %q, want %q", got, want)
	}
}

// TestNodeScriptCachedCLITampered: a cached CLI whose bytes no longer match the
// sha256 recorded when it was installed is not run. Another cluster user who
// can write to an old cache could put their own executable there and give it a
// recent time; running it would hand them the job. It is removed and fetched
// again.
func TestNodeScriptCachedCLITampered(t *testing.T) {
	w := newNodeWorld(t)
	bin := w.cachedCLI(fakeCode)
	w.write(bin, "#!/bin/sh\necho ran >\"$STUB/tampered\"\n", 0o755)
	out, code := w.run(w.scratch)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if fileExistsT(filepath.Join(w.dir, "tampered")) {
		t.Fatal("the tampered CLI was run")
	}
	if got := w.read(bin); got != fakeCode {
		t.Error("the tampered CLI was kept")
	}
	if !fileExistsT(filepath.Join(w.dir, "curl.log")) {
		t.Fatal("the CLI was not fetched again")
	}
}

// TestNodeScriptCachedCLIMissingSum: a cached CLI with no recorded sha256
// cannot be checked against anything, so it is fetched again rather than
// trusted.
func TestNodeScriptCachedCLIMissingSum(t *testing.T) {
	w := newNodeWorld(t)
	bin := filepath.Join(w.cache, "bin/code-x64")
	w.write(bin, fakeCode, 0o755)
	out, code := w.run(w.scratch)
	if code != 0 || !fileExistsT(filepath.Join(w.dir, "curl.log")) {
		t.Fatalf("exit %d, no fetch\n%s", code, out)
	}
	if !fileExistsT(bin + ".sha256") {
		t.Fatal("no sha256 recorded for the fetched CLI")
	}
}

// TestNodeScriptCachedCLIUnsafeDir: a cache directory other users can write to
// makes whatever is in it untrusted, however well it matches, so the CLI is
// removed and fetched again.
func TestNodeScriptCachedCLIUnsafeDir(t *testing.T) {
	w := newNodeWorld(t)
	w.cachedCLI(fakeCode)
	if err := os.Chmod(filepath.Join(w.cache, "bin"), 0o777); err != nil {
		t.Fatal(err)
	}
	out, code := w.run(w.scratch)
	if code != 0 || !fileExistsT(filepath.Join(w.dir, "curl.log")) {
		t.Fatalf("exit %d, no fetch\n%s", code, out)
	}
}

// TestNodeScriptCLICacheDirTightened: a $C/bin the user owns but other users
// can write to is made 0700 before the CLI is installed into it, so nobody can
// swap the executable in the moment between that install and the run at the
// end of the job; the CLI is installed and run as usual.
func TestNodeScriptCLICacheDirTightened(t *testing.T) {
	w := newNodeWorld(t)
	bin := filepath.Join(w.cache, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o777); err != nil {
		t.Fatal(err)
	}
	out, code := w.run(w.scratch)
	if code != 0 || !strings.Contains(out, "@cli code 1.140.0 (commit 07f8)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, p := range []string{w.cache, bin} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v, want 0700", p, fi, err)
		}
	}
	if !fileExistsT(filepath.Join(bin, "code-x64")) {
		t.Fatal("the CLI was not installed")
	}
	if log := w.read(filepath.Join(w.dir, "code.log")); !strings.Contains(log, "tunnel --name node1-me") {
		t.Fatalf("the CLI was not run\n%s", log)
	}
}

// TestNodeScriptCLICacheDirSymlink: a $C/bin that is a symlink can point into a
// directory another user controls, so the job is refused before any CLI is
// fetched or run, the message names the path to remove, and nothing is written
// through the link.
func TestNodeScriptCLICacheDirSymlink(t *testing.T) {
	w := newNodeWorld(t)
	other := filepath.Join(w.dir, "elsewhere")
	target := filepath.Join(w.cache, "bin")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w.cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, target); err != nil {
		t.Fatal(err)
	}
	out, code := w.run(w.scratch)
	if code != 1 || !strings.Contains(out, "hpctun: "+target+" is not yours; remove it and run hpctun code again") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, f := range []string{"code.log", "curl.log"} {
		if fileExistsT(filepath.Join(w.dir, f)) {
			t.Errorf("%s: the job went on", f)
		}
	}
	if left, _ := os.ReadDir(other); len(left) != 0 {
		t.Errorf("written through the link: %v", left)
	}
}

// TestNodeScriptServerDirWritableByOthers: a base others may write to is
// used only when it is sticky (like /tmp), and <base>/<user> must be
// writable by the user alone. Otherwise someone else could rename the
// cache and put their own in its place.
func TestNodeScriptServerDirWritableByOthers(t *testing.T) {
	w := newNodeWorld(t)
	open := filepath.Join(w.dir, "open")
	os.MkdirAll(open, 0o755)
	os.Chmod(open, 0o777)
	if out, code := w.run(open, w.scratch); code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w.scratch, "me/hpctun-vscode")) {
		t.Fatalf("world-writable base: exit %d\n%s", code, out)
	}
	w2 := newNodeWorld(t)
	shared := filepath.Join(w2.dir, "shared")
	os.MkdirAll(filepath.Join(shared, "me"), 0o755)
	os.Chmod(filepath.Join(shared, "me"), 0o770)
	if out, code := w2.run(shared, w2.scratch); code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w2.scratch, "me/hpctun-vscode")) {
		t.Fatalf("group-writable <base>/me: exit %d\n%s", code, out)
	}
	w3 := newNodeWorld(t)
	tmp := filepath.Join(w3.dir, "tmp")
	os.MkdirAll(tmp, 0o755)
	os.Chmod(tmp, 0o777|os.ModeSticky)
	if out, code := w3.run(tmp, w3.scratch); code != 0 || !strings.Contains(out, "@cache "+filepath.Join(tmp, "me/hpctun-vscode")) {
		t.Fatalf("sticky base: exit %d\n%s", code, out)
	}
}

// TestNodeScriptServerDirUnsafeAncestor: a safe-looking base is still not
// usable when a parent of it can be replaced by others: whoever may write to
// the parent can rename the base aside and put their own there, cache and all.
func TestNodeScriptServerDirUnsafeAncestor(t *testing.T) {
	w := newNodeWorld(t)
	pw := filepath.Join(w.dir, "pw")
	base := filepath.Join(pw, "child")
	os.MkdirAll(base, 0o755)
	os.Chmod(pw, 0o775)
	if out, code := w.run(base, w.scratch); code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w.scratch, "me/hpctun-vscode")) {
		t.Fatalf("base under a group-writable parent: exit %d\n%s", code, out)
	}
}

func TestNodeScriptAlreadyLoggedIn(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "show"), "logged in with provider Microsoft Account\n", 0o644)
	out, code := w.run(w.scratch)
	if code != 0 || strings.Contains(out, "@login") || strings.Contains(w.read(filepath.Join(w.dir, "code.log")), "user login") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestNodeScriptLoginFails(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "login.exit"), "1\n", 0o644)
	out, code := w.run(w.scratch)
	if code != 1 || !strings.Contains(out, "@error login") || strings.Contains(w.read(filepath.Join(w.dir, "code.log")), "--name") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestNodeScriptLoginEnvironment: the steps see the login environment, not
// the one sbatch copied: FOO and the gateway's TMPDIR are gone, the
// pass-through list and SLURM_* arrive, and S comes from before the switch
// even when the profile sets XDG_STATE_HOME differently.
func TestNodeScriptLoginEnvironment(t *testing.T) {
	w := newNodeWorld(t)
	xs := filepath.Join(w.dir, "xs")
	w.env = []string{"XDG_STATE_HOME=" + xs, "https_proxy=10.0.0.1:3128", "SSH_CONNECTION=10.1.2.3 52000 10.0.0.5 22", "SSH_CLIENT=10.1.2.3 52000 22", "SSH_TTY=/dev/pts/3"}
	f, _ := os.OpenFile(filepath.Join(w.home, ".profile"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("XDG_STATE_HOME=/somewhere/else; export XDG_STATE_HOME\n")
	f.Close()
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	env := w.read(filepath.Join(w.dir, "tunnel.env"))
	for _, gone := range []string{"FOO=", "TMPDIR=/gateway-tmp", "SSH_TTY="} {
		if strings.Contains(env, gone) {
			t.Errorf("%s reached the tunnel", gone)
		}
	}
	for _, kept := range []string{"SLURM_JOB_ID=42", "https_proxy=10.0.0.1:3128", "HOME=" + w.home,
		"SSH_CONNECTION=10.1.2.3 52000 10.0.0.5 22", "SSH_CLIENT=10.1.2.3 52000 22"} {
		if !strings.Contains(env, kept) {
			t.Errorf("%s missing in the tunnel's environment", kept)
		}
	}
	if !fileExistsT(filepath.Join(xs, "hpctun/vscode/nodes/node1/code_tunnel.json")) {
		t.Fatal("S did not come from the submitting environment")
	}
}

// TestNodeScriptShellFallback: a $SHELL that cannot take -l -c (tcsh) or
// does not exist falls back to bash, which still reads the profile.
func TestNodeScriptShellFallback(t *testing.T) {
	for _, sh := range []string{"/bin/tcsh", "/no/such/shell"} {
		w := newNodeWorld(t)
		w.env = []string{"SHELL=" + sh}
		if out, code := w.run(w.scratch); code != 0 || !strings.Contains(out, "Tunnel:   node1-me") {
			t.Errorf("SHELL=%s: exit %d\n%s", sh, code, out)
		}
	}
}

// TestNodeScriptServerDirSymlink: a base whose <base>/me, or its
// hpctun-vscode, is a symlink (someone else could point it anywhere) is
// passed over for the next base.
func TestNodeScriptServerDirSymlink(t *testing.T) {
	for _, link := range []string{"me", "me/hpctun-vscode"} {
		w := newNodeWorld(t)
		bad := filepath.Join(w.dir, "bad")
		other := filepath.Join(w.dir, "other")
		os.MkdirAll(other, 0o755)
		os.MkdirAll(filepath.Dir(filepath.Join(bad, link)), 0o755)
		if err := os.Symlink(other, filepath.Join(bad, link)); err != nil {
			t.Fatal(err)
		}
		out, code := w.run(bad, w.scratch)
		if code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w.scratch, "me/hpctun-vscode")) {
			t.Fatalf("%s a symlink: exit %d\n%s", link, code, out)
		}
		if fi, err := os.Stat(filepath.Join(w.scratch, "me/hpctun-vscode")); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("cache dir: %v %v", fi, err)
		}
	}
}

// TestNodeScriptServerDirNotOwned: <base>/<user> owned by someone else is
// passed over. Ownership cannot be faked without root, so the user is "usr"
// and the base "/": /usr is root's.
func TestNodeScriptServerDirNotOwned(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns /usr")
	}
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "id.out"), "usr\n", 0o644)
	out, code := w.run("/", w.scratch)
	if code != 0 || !strings.Contains(out, "@cache "+filepath.Join(w.scratch, "usr/hpctun-vscode")) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestNodeScriptPrivateFiles: the node dir and the server cache are 0700,
// while the tunnel, and so VS Code's terminals, get the login's usual
// umask (here 027, set by the profile).
func TestNodeScriptPrivateFiles(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.home, ".profile"), w.read(filepath.Join(w.home, ".profile"))+"umask 027\n", 0o644)
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, p := range []string{filepath.Join(w.state, "nodes/node1"), filepath.Join(w.scratch, "me/hpctun-vscode")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	if got := strings.TrimSpace(w.read(filepath.Join(w.dir, "tunnel.umask"))); got != "0027" {
		t.Errorf("tunnel umask %q, want the login's 0027", got)
	}
}

// TestNodeScriptFreshCLIFromOldArchive: tar keeps the archive's mtime; a
// CLI built long ago must still count as fresh once downloaded.
func TestNodeScriptFreshCLIFromOldArchive(t *testing.T) {
	w := newNodeWorld(t)
	w.tgzAt(fakeCode, time.Now().Add(-60*24*time.Hour))
	if out, code := w.run(w.scratch); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if out, code := w.run(w.scratch); code != 0 || strings.Count(w.read(filepath.Join(w.dir, "curl.log")), "\n") != 2 {
		t.Fatalf("exit %d, curl.log %q\n%s", code, w.read(filepath.Join(w.dir, "curl.log")), out)
	}
}

// TestNodeScriptSignedInDespiteWarning: only stdout says whether the user
// is signed in; a warning on stderr does not cause a new sign-in.
func TestNodeScriptSignedInDespiteWarning(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "show.err"), "[2026-10-06 04:36:25] warn keyring unavailable\n", 0o644)
	w.write(filepath.Join(w.dir, "show"), "logged in with provider Microsoft Account\n", 0o644)
	out, code := w.run(w.scratch)
	if code != 0 || strings.Contains(out, "@login") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestNodeScriptMigratesSharedTunnel: the tunnel earlier hpctun shared
// between nodes is unregistered once and its record removed; this node then
// registers its own tunnel under <node>-<user>, and the stale lock goes.
func TestNodeScriptMigratesSharedTunnel(t *testing.T) {
	w := newNodeWorld(t)
	n := filepath.Join(w.state, "nodes/node1")
	shared := filepath.Join(w.state, "tunnel-hpctun-code.json")
	w.write(shared, `{"name":"hpc-me","id":"shared-1","cluster":"usw3"}`, 0o600)
	w.write(filepath.Join(w.state, "tunnel-tun-code.json"), "null", 0o600)
	w.write(filepath.Join(n, "code_tunnel.json"), `{"name":"hpc-me","id":"shared-1","cluster":"usw3"}`, 0o600)
	w.write(filepath.Join(n, "tunnel-stable.lock"), "stale", 0o600)
	w.write(filepath.Join(w.dir, "id"), "own-2\n", 0o644)
	if out, code := w.run(w.scratch); code != 0 || !strings.Contains(out, "Tunnel:   node1-me") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	log := w.read(filepath.Join(w.dir, "code.log"))
	if strings.Count(log, "tunnel unregister --cli-data-dir "+n) != 1 {
		t.Fatalf("code.log\n%s", log)
	}
	for _, gone := range []string{shared, filepath.Join(w.state, "tunnel-tun-code.json"), filepath.Join(n, "tunnel-stable.lock")} {
		if fileExistsT(gone) {
			t.Errorf("%s kept", gone)
		}
	}
	if got := w.read(filepath.Join(n, "code_tunnel.json")); !strings.Contains(got, `"name":"node1-me","id":"own-2"`) {
		t.Fatalf("node record %q", got)
	}
}

// TestNodeScriptDropsOldNameRecord: a node record still on the old shared
// name (its tunnel unregistered from another node) is dropped, so the node
// registers its own; one under any other name (the CLI's random fallback)
// is kept.
func TestNodeScriptDropsOldNameRecord(t *testing.T) {
	for _, c := range []struct{ name, want string }{{"hpc-me", `"id":"own-2"`}, {"aspen1hpcoregonstate", `"id":"keep-1"`}} {
		w := newNodeWorld(t)
		n := filepath.Join(w.state, "nodes/node1")
		w.write(filepath.Join(n, "code_tunnel.json"), `{"name":"`+c.name+`","id":"keep-1","cluster":"usw3"}`, 0o600)
		w.write(filepath.Join(w.dir, "id"), "own-2\n", 0o644)
		if out, code := w.run(w.scratch); code != 0 {
			t.Fatalf("%s: exit %d\n%s", c.name, code, out)
		}
		if got := w.read(filepath.Join(n, "code_tunnel.json")); !strings.Contains(got, c.want) {
			t.Errorf("%s: node record %q", c.name, got)
		}
		if strings.Contains(w.read(filepath.Join(w.dir, "code.log")), "unregister") {
			t.Errorf("%s: unregistered", c.name)
		}
	}
}

// TestNodeScriptTunnelName: the node stays whole and the user is cut, so
// the name fits VS Code's 20 characters.
func TestNodeScriptTunnelName(t *testing.T) {
	w := newNodeWorld(t)
	w.write(filepath.Join(w.dir, "hostname.out"), "yatagarasu12\n", 0o644)
	w.write(filepath.Join(w.dir, "id.out"), "longusername\n", 0o644)
	if out, code := w.run(w.scratch); code != 0 || !strings.Contains(out, "Tunnel:   yatagarasu12-longuse\n") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// TestNodeScriptPrunesOldTunnels: beyond the 8 most recently used nodes,
// tunnels are unregistered, keeping their sign-in; nodes with no tunnel are
// left alone.
func TestNodeScriptPrunesOldTunnels(t *testing.T) {
	w := newNodeWorld(t)
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 9; i++ {
		d := filepath.Join(w.state, fmt.Sprintf("nodes/old%d", i))
		w.write(filepath.Join(d, "code_tunnel.json"), fmt.Sprintf(`{"name":"old%d-me","id":"o%d"}`, i, i), 0o600)
		w.write(filepath.Join(d, "token.json"), "signed in", 0o600)
		w.write(filepath.Join(d, "last-used"), "", 0o600)
		// old1 is the oldest
		mt := base.Add(time.Duration(i) * time.Minute)
		os.Chtimes(filepath.Join(d, "last-used"), mt, mt)
	}
	w.write(filepath.Join(w.state, "nodes/old0/last-used"), "", 0o600)
	os.Chtimes(filepath.Join(w.state, "nodes/old0/last-used"), base, base)
	out, code := w.run(w.scratch)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// node1 (now) and old9..old3 are the 8 kept; old2 and old1 go, old0 has none.
	log := w.read(filepath.Join(w.dir, "code.log"))
	for i := 0; i <= 9; i++ {
		d := filepath.Join(w.state, fmt.Sprintf("nodes/old%d", i))
		gone := i == 1 || i == 2
		if strings.Contains(log, "unregister --cli-data-dir "+d+"\n") != gone || fileExistsT(filepath.Join(d, "code_tunnel.json")) == gone && i != 0 {
			t.Errorf("old%d: pruned %v?\n%s", i, gone, log)
		}
		if i != 0 && !fileExistsT(filepath.Join(d, "token.json")) {
			t.Errorf("old%d lost its sign-in", i)
		}
	}
	contains := strings.Contains(out, "@pruned old1") && strings.Contains(out, "@pruned old2")
	if !contains {
		t.Errorf("no @pruned lines\n%s", out)
	}
}

func fileExistsT(p string) bool { _, err := os.Stat(p); return err == nil }
