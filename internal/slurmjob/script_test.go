package slurmjob

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// stubScript stands in for every Slurm tool: it logs its name and
// arguments (each in [brackets]) to $STUB/log, and for its nth call prints
// $STUB/<tool>.<n> to stdout and <tool>.<n>.err to stderr, exiting with
// <tool>.<n>.exit (default 0).
const stubScript = `#!/bin/sh
tool=$(basename "$0")
printf "%s" "$tool" >>"$STUB/log"
for a in "$@"; do printf " [%s]" "$a" >>"$STUB/log"; done
echo >>"$STUB/log"
n=$(cat "$STUB/$tool.n" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" >"$STUB/$tool.n"
if [ ! -t 0 ]; then cat >"$STUB/$tool.$n.stdin"; fi
if [ -f "$STUB/$tool.$n.err" ]; then cat "$STUB/$tool.$n.err" >&2; fi
if [ -f "$STUB/$tool.$n" ]; then cat "$STUB/$tool.$n"; fi
exit "$(cat "$STUB/$tool.$n.exit" 2>/dev/null || echo 0)"
`

type world struct {
	t        *testing.T
	dir, bin string
	env      []string // extra environment for runArgv
}

func (w *world) stdin(tool string, n int) string {
	b, _ := os.ReadFile(filepath.Join(w.dir, fmt.Sprintf("%s.%d.stdin", tool, n)))
	return string(b)
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the script runs on the gateway, under sh")
	}
	w := &world{t: t, dir: t.TempDir()}
	w.bin = filepath.Join(w.dir, "bin")
	if err := os.Mkdir(w.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"squeue", "sbatch", "scancel", "sinfo", "sacct", "scontrol", "sacctmgr", "sleep"} {
		if err := os.WriteFile(filepath.Join(w.bin, tool), []byte(stubScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// reply scripts the nth call (from 1) of tool.
func (w *world) reply(tool string, n int, stdout, stderr string, code int) {
	base := filepath.Join(w.dir, fmt.Sprintf("%s.%d", tool, n))
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
	write(base, stdout)
	if stderr != "" {
		write(base+".err", stderr)
	}
	if code != 0 {
		write(base+".exit", strconv.Itoa(code))
	}
}

func (w *world) log() string {
	b, _ := os.ReadFile(filepath.Join(w.dir, "log"))
	return string(b)
}

func (w *world) runArgv(argv, lines []string) (string, string, int) {
	w.t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + w.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "USER=me", "STUB=" + w.dir, "HOME=" + w.dir}
	cmd.Env = append(cmd.Env, w.env...)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		w.t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

func (w *world) run(lines ...string) (string, string, int) {
	return w.runArgv([]string{"sh", "-c", EnsureScript}, lines)
}

func input(mode, part, node string, extra ...string) []string {
	return append([]string{mode, "tun-code", "4", "24:00:00", "now+1470minutes", part, node, ""}, extra...)
}

// inputWith is input plus a batch script after the @script line.
func inputWith(script, mode, part, node string, extra ...string) []string {
	return append(input(mode, part, node, extra...), "@script", script)
}

const testOnly = "sbatch: Job 99 to start at 2026-09-24T13:29:56 a using 4 processors on nodes chrom1 in partition core\n"

func TestEnsureScriptShape(t *testing.T) {
	for _, bad := range []string{"'", "\n", "!", "trap"} {
		if strings.Contains(EnsureScript, bad) {
			t.Errorf("EnsureScript contains %q", bad)
		}
	}
}

func TestEnsureScriptReusesRunningJob(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|node1|10:00|None\n", "", 0)
	out, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 || out != "42|RUNNING|node1|10:00|None\n" || errs != "@reuse 42\n" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	if strings.Contains(w.log(), "sbatch") {
		t.Fatalf("submitted anyway:\n%s", w.log())
	}
	if !strings.Contains(w.log(), "squeue [-h] [-u] [me] [-n] [tun-code] [-t] [PENDING,RUNNING,CONFIGURING,SUSPENDED,REQUEUED,RESIZING]") {
		t.Fatalf("list call:\n%s", w.log())
	}
}

// TestEnsureScriptNameList: name may list several comma-separated job
// names; squeue -n already takes the whole list, and a new job is
// submitted under only the first.
func TestEnsureScriptNameList(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
	lines := []string{"ensure", "hpctun-code,tun-code", "4", "24:00:00", "now+1470minutes", "", ""}
	out, _, code := w.run(lines...)
	if code != 0 || out != "42|RUNNING|chrom1|23:59:59|None\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if !strings.Contains(w.log(), "squeue [-h] [-u] [me] [-n] [hpctun-code,tun-code] [-t] [PENDING,RUNNING,CONFIGURING,SUSPENDED,REQUEUED,RESIZING]") {
		t.Fatalf("list call:\n%s", w.log())
	}
	if !strings.Contains(w.log(), "[-J] [hpctun-code] ") {
		t.Fatalf("sbatch call:\n%s", w.log())
	}
}

// TestEnsureScriptNameListReuses: a job under either name in the list is
// found and reused.
func TestEnsureScriptNameListReuses(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|node1|10:00|None\n", "", 0)
	lines := []string{"ensure", "hpctun-code,tun-code", "4", "24:00:00", "now+1470minutes", "", ""}
	out, errs, code := w.run(lines...)
	if code != 0 || out != "42|RUNNING|node1|10:00|None\n" || errs != "@reuse 42\n" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	if strings.Contains(w.log(), "sbatch") {
		t.Fatalf("submitted anyway:\n%s", w.log())
	}
}

func TestEnsureScriptFindNone(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	out, _, code := w.run(input("find", "", "")...)
	if code != 3 || out != "none\n" || strings.Contains(w.log(), "sbatch") {
		t.Fatalf("code %d out %q log\n%s", code, out, w.log())
	}
}

func TestEnsureScriptFindPendingDoesNotWait(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|PENDING||15:00|Priority\n", "", 0)
	out, _, code := w.run(input("find", "", "")...)
	if code != 0 || out != "42|PENDING||15:00|Priority\n" || strings.Contains(w.log(), "sleep") {
		t.Fatalf("code %d out %q log\n%s", code, out, w.log())
	}
}

func TestEnsureScriptSubmitsWithEveryArgument(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
	out, errs, code := w.run(inputWith("x", "ensure", "core", "chrom1", "--gres=gpu:1", `--comment=a "b" $HOME`)...)
	if code != 0 || out != "42|RUNNING|chrom1|23:59:59|None\n" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	args := "[-J] [tun-code] [-N] [1] [-c] [4] [-t] [24:00:00] [--deadline=now+1470minutes] [-o] [" + w.dir + "/.local/state/hpctun/vscode/job-%j.log] [-p] [core] [-w] [chrom1] [--gres=gpu:1] [--comment=a \"b\" $HOME]"
	for _, want := range []string{"sbatch [--test-only] " + args, "sbatch [--parsable] " + args} {
		if !strings.Contains(w.log(), want+"\n") {
			t.Errorf("log lacks %q:\n%s", want, w.log())
		}
	}
	if !strings.Contains(errs, "@submitted 42|"+strings.TrimSuffix(testOnly, "\n")) {
		t.Fatalf("err %q", errs)
	}
}

func TestEnsureScriptPartitionFromNode(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sinfo", 1, "core*\n", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42;cluster\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|1:00|None\n", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "chrom1")...)
	if code != 0 || !strings.Contains(w.log(), "[-p] [core] [-w] [chrom1]") || !strings.Contains(errs, "@submitted 42|") {
		t.Fatalf("code %d err %q log\n%s", code, errs, w.log())
	}
}

func TestEnsureScriptNodePartitions(t *testing.T) {
	for _, c := range []struct{ sinfo, event string }{
		{"", "@error nopart|chrom1\n"},
		{"core\nood_test\n", "@error manypart|chrom1|core ood_test \n"},
	} {
		w := newWorld(t)
		w.reply("squeue", 1, "", "", 0)
		w.reply("sinfo", 1, c.sinfo, "", 0)
		_, errs, code := w.run(inputWith("x", "ensure", "", "chrom1")...)
		if code != 2 || errs != c.event || strings.Contains(w.log(), "sbatch") {
			t.Errorf("sinfo %q: code %d err %q log\n%s", c.sinfo, code, errs, w.log())
		}
	}
}

func TestEnsureScriptTestOnlyRefused(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", "allocation failure: Requested nodes not in this partition\n", 1)
	_, errs, code := w.run(inputWith("x", "ensure", "core", "cqls-gpu3")...)
	if code != 2 || errs != "@new\n@error test|allocation failure: Requested nodes not in this partition\n" {
		t.Fatalf("code %d err %q", code, errs)
	}
	if strings.Count(w.log(), "sbatch") != 1 {
		t.Fatalf("submitted after a refusal:\n%s", w.log())
	}
}

func TestEnsureScriptReportsStateChanges(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|PENDING||15:00|None\n", "", 0)
	w.reply("squeue", 3, "42|PENDING||15:00|None\n", "", 0)
	w.reply("squeue", 4, "42|PENDING||15:00|Priority\n", "", 0)
	w.reply("squeue", 5, "42|RUNNING|chrom1|14:55|None\n", "", 0)
	out, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 || out != "42|RUNNING|chrom1|14:55|None\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if want := "@reuse 42\n@state PENDING|None\n@alive\n@alive\n@state PENDING|Priority\n@alive\n"; errs != want {
		t.Fatalf("err %q, want %q", errs, want)
	}
	if n := strings.Count(w.log(), "sleep [5]"); n != 3 {
		t.Fatalf("slept %d times:\n%s", n, w.log())
	}
}

func TestEnsureScriptCancelsDuplicates(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n57\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|1:00|None\n", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 || !strings.Contains(w.log(), "scancel [57]\n") || !strings.Contains(errs, "@extra 57 \n") {
		t.Fatalf("code %d err %q log\n%s", code, errs, w.log())
	}
}

func TestEnsureScriptJobGone(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "", "", 0)
	w.reply("sacct", 1, "DEADLINE\n", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 1 || !strings.HasSuffix(errs, "@gone DEADLINE\n") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

// TestEnsureScriptUnderTcsh: a tcsh login shell hands sh -c '...' through
// intact.
func TestEnsureScriptUnderTcsh(t *testing.T) {
	if _, err := os.Stat("/bin/tcsh"); err != nil {
		t.Skip("no /bin/tcsh")
	}
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|node1|10:00|None\n", "", 0)
	out, _, code := w.runArgv([]string{"/bin/tcsh", "-c", "sh -c '" + EnsureScript + "'"}, input("ensure", "", ""))
	if code != 0 || out != "42|RUNNING|node1|10:00|None\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

// TestEnsureScriptSqueueHiccup (tun-9v0): squeue failing for a moment on a
// busy cluster is not the job ending while sacct still has it queued.
func TestEnsureScriptSqueueHiccup(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "", "slurm_load_jobs error: Socket timed out on send/recv operation\n", 1)
	w.reply("sacct", 1, "PENDING\n", "", 0)
	w.reply("squeue", 3, "42|RUNNING|chrom1|1:00|None\n", "", 0)
	out, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 || out != "42|RUNNING|chrom1|1:00|None\n" || strings.Contains(errs, "@gone") {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
}

// TestEnsureScriptLosesTrack: squeue never answering again, with sacct
// saying the job is still queued (or saying nothing), ends in "lost", not
// "gone", and only after a dozen tries.
func TestEnsureScriptLosesTrack(t *testing.T) {
	for _, sacct := range []string{"PENDING\n", ""} {
		w := newWorld(t)
		w.reply("squeue", 1, "42\n", "", 0)
		for n := 1; n <= 20; n++ {
			w.reply("sacct", n, sacct, "", 0)
		}
		_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
		want := "@lost PENDING\n"
		if sacct == "" {
			want = "@lost UNKNOWN\n"
		}
		if code != 1 || !strings.HasSuffix(errs, want) || strings.Contains(errs, "@gone") {
			t.Errorf("sacct %q: code %d err %q", sacct, code, errs)
		}
		if n := strings.Count(w.log(), "\nsacct "); n != 12 {
			t.Errorf("sacct %q: asked sacct %d times:\n%s", sacct, n, w.log())
		}
	}
}

// TestEnsureScriptWaitsThroughHolds: states Slurm shows for a job that is
// still queued but held (a requeue hold, say) are waiting, not the job
// ending.
func TestEnsureScriptWaitsThroughHolds(t *testing.T) {
	for _, st := range []string{"REQUEUE_HOLD", "SPECIAL_EXIT", "RESV_DEL_HOLD", "REQUEUE_FED", "STOPPED", "SIGNALING"} {
		w := newWorld(t)
		w.reply("squeue", 1, "42\n", "", 0)
		w.reply("squeue", 2, "42|"+st+"||15:00|JobHeldAdmin\n", "", 0)
		w.reply("squeue", 3, "42|RUNNING|chrom1|1:00|None\n", "", 0)
		out, errs, code := w.run(inputWith("x", "ensure", "", "")...)
		if code != 0 || out != "42|RUNNING|chrom1|1:00|None\n" || !strings.Contains(errs, "@state "+st+"|JobHeldAdmin\n") {
			t.Errorf("%s: code %d out %q err %q", st, code, out, errs)
		}
	}
}

// TestEnsureScriptFindDoesNotRetry: find never waits (tun status gives it
// 10 seconds), so a squeue hiccup is reported as lost at once.
func TestEnsureScriptFindDoesNotRetry(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("sacct", 1, "PENDING\n", "", 0)
	_, errs, code := w.run(input("find", "", "")...)
	if code != 1 || errs != "@reuse 42\n@lost PENDING\n" || strings.Contains(w.log(), "sleep") {
		t.Fatalf("code %d err %q log\n%s", code, errs, w.log())
	}
}

// TestEnsureScriptGoneWhenSqueueSaysSo: a finished state from squeue itself
// is the job ending, whatever sacct says.
func TestEnsureScriptGoneWhenSqueueSaysSo(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	w.reply("squeue", 2, "42|CANCELLED||0:00|None\n", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 1 || !strings.HasSuffix(errs, "@gone CANCELLED\n") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

// TestEnsureScriptBacksOff (tun-s4o): polls every 5 seconds for the first
// minute, then every 15.
func TestEnsureScriptBacksOff(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "42\n", "", 0)
	for n := 2; n <= 15; n++ {
		w.reply("squeue", n, "42|PENDING||15:00|Priority\n", "", 0)
	}
	w.reply("squeue", 16, "42|RUNNING|chrom1|14:55|None\n", "", 0)
	_, _, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	if n5, n15 := strings.Count(w.log(), "sleep [5]"), strings.Count(w.log(), "sleep [15]"); n5 != 12 || n15 != 2 {
		t.Fatalf("slept 5s %d times and 15s %d times:\n%s", n5, n15, w.log())
	}
}

// TestEnsureScriptEndsWhenConnectionCloses (tun-s4o): once nobody reads its
// stderr (the ssh connection is gone), the script stops polling rather
// than running squeue until the job starts or its deadline passes. The
// reader goes away after the first poll, while the job stays pending with
// no new state to report, so only the heartbeat can notice. Run both with
// SIGPIPE at its default (the write kills sh) and ignored, as a parent may
// leave it (the write fails with EPIPE).
func TestEnsureScriptEndsWhenConnectionCloses(t *testing.T) {
	for _, wrap := range []string{`eval "$0"`, `trap "" PIPE; eval "$0"`} {
		w := newWorld(t)
		w.reply("squeue", 1, "42\n", "", 0)
		for n := 2; n <= 30; n++ {
			w.reply("squeue", n, "42|PENDING||15:00|Priority\n", "", 0)
		}
		// sleep waits until the test has closed the reader.
		sleep := "#!/bin/sh\nwhile [ ! -f \"$STUB/go\" ]; do /bin/sleep 0.01; done\necho \"sleep [$1]\" >>\"$STUB/log\"\n"
		if err := os.WriteFile(filepath.Join(w.bin, "sleep"), []byte(sleep), 0o755); err != nil {
			t.Fatal(err)
		}
		r, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", wrap, EnsureScript)
		cmd.Env = []string{"PATH=" + w.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "USER=me", "STUB=" + w.dir, "HOME=" + w.dir}
		cmd.Stdin = strings.NewReader(strings.Join(input("ensure", "", ""), "\n") + "\n")
		cmd.Stderr = wr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		wr.Close()
		var seen []byte
		buf := make([]byte, 256)
		for !bytes.Contains(seen, []byte("@state PENDING|Priority\n")) {
			n, err := r.Read(buf)
			if err != nil {
				t.Fatalf("%s: stderr ended before the first state: %q", wrap, seen)
			}
			seen = append(seen, buf[:n]...)
		}
		r.Close()
		if err := os.WriteFile(filepath.Join(w.dir, "go"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Errorf("%s: exit status 0 with nobody listening", wrap)
		}
		if n := strings.Count(w.log(), "squeue [-h] [-j]"); n > 2 {
			t.Errorf("%s: polled squeue %d times, still polling after the connection closed:\n%s", wrap, n, w.log())
		}
	}
}

func TestEnsureScriptSubmitsScriptOnStdin(t *testing.T) {
	w := newWorld(t)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
	out, errs, code := w.run(inputWith("#!/bin/sh\necho hi", "ensure", "", "", "--gres=gpu:1")...)
	if code != 0 || out != "42|RUNNING|chrom1|23:59:59|None\n" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	log := w.log()
	if strings.Contains(log, "--wrap") || strings.Contains(log, "[/dev/null]") {
		t.Fatalf("old submission:\n%s", log)
	}
	if !strings.Contains(log, "[-o] ["+w.dir+"/.local/state/hpctun/vscode/job-%j.log]") || !strings.Contains(log, "[--gres=gpu:1]") {
		t.Fatalf("sbatch args:\n%s", log)
	}
	if got := w.stdin("sbatch", 2); got != "#!/bin/sh\necho hi\n" {
		t.Fatalf("sbatch stdin %q", got)
	}
	if !strings.Contains(errs, "@new\n") {
		t.Fatalf("no @new: %q", errs)
	}
}

func TestEnsureScriptStateDirExists(t *testing.T) {
	w := newWorld(t)
	os.MkdirAll(filepath.Join(w.dir, ".local/state/hpctun/vscode"), 0o755)
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 0 || strings.Contains(errs, "@new") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

func TestEnsureScriptXDGStateHome(t *testing.T) {
	w := newWorld(t)
	w.env = append(w.env, "XDG_STATE_HOME="+w.dir+"/xs")
	w.reply("squeue", 1, "", "", 0)
	w.reply("sbatch", 1, "", testOnly, 0)
	w.reply("sbatch", 2, "42\n", "", 0)
	w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
	if _, errs, code := w.run(inputWith("x", "ensure", "", "")...); code != 0 {
		t.Fatalf("code %d err %q", code, errs)
	}
	if !strings.Contains(w.log(), "[-o] ["+w.dir+"/xs/hpctun/vscode/job-%j.log]") {
		t.Fatalf("sbatch args:\n%s", w.log())
	}
}

func TestEnsureScriptCannotCreateStateDir(t *testing.T) {
	w := newWorld(t)
	os.WriteFile(filepath.Join(w.dir, ".local"), []byte("a file"), 0o644)
	w.reply("squeue", 1, "", "", 0)
	_, errs, code := w.run(inputWith("x", "ensure", "", "")...)
	if code != 2 || !strings.Contains(errs, "@error dir|") || strings.Contains(w.log(), "sbatch") {
		t.Fatalf("code %d err %q\n%s", code, errs, w.log())
	}
}

// TestEnsureScriptPrivateStateDir: $S, which holds the job logs (a device
// code among them), is the user's alone, even when it was there before.
// sbatch keeps the caller's umask, which Slurm passes on to the job and so
// to VS Code's terminals.
func TestEnsureScriptPrivateStateDir(t *testing.T) {
	for _, existing := range []bool{false, true} {
		w := newWorld(t)
		s := filepath.Join(w.dir, ".local/state/hpctun/vscode")
		if existing {
			os.MkdirAll(s, 0o755)
		}
		os.WriteFile(filepath.Join(w.bin, "sbatch"), []byte(strings.Replace(stubScript, "#!/bin/sh\n", "#!/bin/sh\numask >>\"$STUB/sbatch.umask\"\n", 1)), 0o755)
		w.reply("squeue", 1, "", "", 0)
		w.reply("sbatch", 1, "", testOnly, 0)
		w.reply("sbatch", 2, "42\n", "", 0)
		w.reply("squeue", 2, "42|RUNNING|chrom1|23:59:59|None\n", "", 0)
		if _, errs, code := w.runArgv([]string{"sh", "-c", "umask 027; " + EnsureScript}, inputWith("x", "ensure", "", "")); code != 0 {
			t.Fatalf("existing=%v: code %d err %q", existing, code, errs)
		}
		if fi, err := os.Stat(s); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("existing=%v: state dir %v %v", existing, fi, err)
		}
		b, _ := os.ReadFile(filepath.Join(w.dir, "sbatch.umask"))
		if got := strings.Fields(string(b)); len(got) != 2 || got[0] != "0027" || got[1] != "0027" {
			t.Fatalf("existing=%v: sbatch umask %q", existing, b)
		}
	}
}

// TestEnsureScriptPicksAccount: a partition that takes only some accounts
// gets the user's default when it is let in, else the first of the user's
// accounts that is (said with @account); none gives noaccount. An account
// given, or "-" (one is in the sbatch arguments), is left alone.
func TestEnsureScriptPicksAccount(t *testing.T) {
	const lpi = "PartitionName=lpi AllowGroups=ALL AllowAccounts=lpi AllowQos=ALL State=UP\n"
	cases := []struct {
		name, acct, part, def, assoc, wantA, wantEv, wantErr string
		sacctmgr                                             bool
	}{
		{"open partition", "", "core", "", "", "", "", "", false},
		{"default let in", "", "lpi=PartitionName=lpi AllowAccounts=core,lpi", "core", "", "", "", "", true},
		{"another account", "", "lpi", "core", "core\nlpi\nmicro\n", "lpi", "@account lpi|lpi", "", true},
		{"denied default", "", "core=PartitionName=core AllowAccounts=ALL DenyAccounts=core", "core", "core\nmicro\n", "micro", "@account micro|core", "", true},
		{"none", "", "lpi", "core", "core\nmicro\n", "", "", "@error noaccount|lpi|lpi", true},
		{"given", "grace", "lpi", "", "", "grace", "", "", false},
		{"in the arguments", "-", "lpi", "", "", "", "", "", false},
	}
	for _, c := range cases {
		w := newWorld(t)
		part, show, _ := strings.Cut(c.part, "=")
		if show == "" {
			show = lpi
			if part == "core" {
				show = "PartitionName=core AllowGroups=ALL AllowAccounts=ALL AllowQos=ALL\n"
			}
		}
		w.reply("squeue", 1, "", "", 0)
		w.reply("scontrol", 1, show+"\n", "", 0)
		w.reply("sacctmgr", 1, c.def+"\n", "", 0)
		w.reply("sacctmgr", 2, c.assoc, "", 0)
		w.reply("sbatch", 1, "", testOnly, 0)
		w.reply("sbatch", 2, "42\n", "", 0)
		w.reply("squeue", 2, "42|RUNNING|haughton|23:59:59|None\n", "", 0)
		lines := inputWith("x", "ensure", part, "")
		lines[7] = c.acct
		_, errs, code := w.run(lines...)
		log := w.log()
		if c.wantErr != "" {
			if code != 2 || !strings.Contains(errs, c.wantErr) || strings.Contains(log, "sbatch") {
				t.Errorf("%s: code %d err %q\n%s", c.name, code, errs, log)
			}
			continue
		}
		if code != 0 {
			t.Fatalf("%s: code %d err %q\n%s", c.name, code, errs, log)
		}
		if got := strings.Contains(log, " [-A] "); got != (c.wantA != "") || (c.wantA != "" && !strings.Contains(log, "[-A] ["+c.wantA+"]")) {
			t.Errorf("%s: -A in\n%s", c.name, log)
		}
		if c.wantEv != "" && !strings.Contains(errs, c.wantEv) {
			t.Errorf("%s: err %q lacks %q", c.name, errs, c.wantEv)
		}
		if strings.Contains(log, "sacctmgr") != c.sacctmgr {
			t.Errorf("%s: sacctmgr ran %v\n%s", c.name, !c.sacctmgr, log)
		}
	}
}
