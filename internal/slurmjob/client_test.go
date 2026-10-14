package slurmjob

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type call struct {
	argv  []string
	stdin string
}

func (c call) script() string { return c.argv[len(c.argv)-1] }

type fake struct {
	calls []call
	reply func(ctx context.Context, c call, stderr io.Writer) (string, int, error)
}

func (f *fake) exec(ctx context.Context, argv []string, stdin string, stderr io.Writer) (string, int, error) {
	c := call{argv, stdin}
	f.calls = append(f.calls, c)
	return f.reply(ctx, c, stderr)
}

func client(f *fake) Client { return Client{Exec: f.exec, Gateway: "shell"} }

var params = Params{Mode: ModeEnsure, Name: "tun-code", CPUs: 4, Time: "24:00:00", Deadline: "now+1470minutes",
	Partition: "core", Extra: []string{"--gres=gpu:1", "a b"}, Script: "#!/bin/sh\nx\n"}

func TestEnsureArgvAndStdin(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) {
		return "42|RUNNING|chrom1|1:00|None\n", 0, nil
	}}
	if _, err := client(f).Ensure(context.Background(), params, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", "shell", "sh -c '" + EnsureScript + "'"}
	if !reflect.DeepEqual(f.calls[0].argv, want) {
		t.Fatalf("argv %q", f.calls[0].argv)
	}
	if got := f.calls[0].stdin; got != "ensure\ntun-code\n4\n24:00:00\nnow+1470minutes\ncore\n\n\n--gres=gpu:1\na b\n@script\n#!/bin/sh\nx\n" {
		t.Fatalf("stdin %q", got)
	}
}

// TestEnsureWaitsOnItsOwnConnection (tun-s4o): a shared master keeps the
// session open after its ssh client dies, so the script's heartbeat would
// never fail and it would poll on after VS Code gave up. Where ssh can
// share connections, a waiting Ensure opts out (and hides the login banner
// a new connection prints); find, which never waits, keeps the master.
func TestEnsureWaitsOnItsOwnConnection(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) {
		return "42|RUNNING|chrom1|1:00|None\n", 0, nil
	}}
	c := client(f)
	c.Multiplexing = true
	if _, err := c.Ensure(context.Background(), params, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	find := params
	find.Mode = ModeFind
	if _, err := c.Ensure(context.Background(), find, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	own := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "LogLevel=ERROR", "--", "shell", "sh -c '" + EnsureScript + "'"}
	shared := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", "shell", "sh -c '" + EnsureScript + "'"}
	if !reflect.DeepEqual(f.calls[0].argv, own) {
		t.Errorf("ensure argv %q", f.calls[0].argv)
	}
	if !reflect.DeepEqual(f.calls[1].argv, shared) {
		t.Errorf("find argv %q", f.calls[1].argv)
	}
}

func TestEnsureEventsAndJob(t *testing.T) {
	f := &fake{reply: func(_ context.Context, _ call, stderr io.Writer) (string, int, error) {
		// split mid-line: the writer must reassemble lines
		io.WriteString(stderr, "@submitted 42|sbatch: Job 99 to start at 2026-09-24T13:29:56 a using 4 proc")
		io.WriteString(stderr, "essors on nodes chrom1 in partition core\nssh: a warning\n@alive\n@state PENDING|Priority\n@alive\n@extra 57 \n")
		return "42|RUNNING|chrom1|23:59:59|None\n", 0, nil
	}}
	var evs []Event
	var pass strings.Builder
	job, err := client(f).Ensure(context.Background(), params, func(e Event) { evs = append(evs, e) }, &pass)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Job{ID: "42", State: "RUNNING", Node: "chrom1", Left: "23:59:59", Reason: "None", Submitted: true}); job != want {
		t.Fatalf("job %+v", job)
	}
	want := []Event{
		{Kind: "submitted", ID: "42", Start: "2026-09-24T13:29:56", Partition: "core"},
		{Kind: "state", State: "PENDING", Reason: "Priority"},
		{Kind: "extra", IDs: "57"},
	}
	if !reflect.DeepEqual(evs, want) {
		t.Fatalf("events %+v", evs)
	}
	if pass.String() != "ssh: a warning\n" {
		t.Fatalf("passed %q", pass.String())
	}
}

func TestEnsureOutcomes(t *testing.T) {
	for _, c := range []struct {
		stderr string
		code   int
		check  func(error) bool
	}{
		{"", 3, func(err error) bool { return errors.Is(err, ErrNone) }},
		{"@error test|allocation failure: Requested nodes not in this partition \n", 2, func(err error) bool {
			var re *RequestError
			return errors.As(err, &re) && strings.Contains(re.Msg, "Requested nodes not in this partition") && strings.Contains(re.Hint, "--node")
		}},
		{"@error nopart|chrom1\n", 2, func(err error) bool {
			var re *RequestError
			return errors.As(err, &re) && strings.Contains(re.Msg, "cannot submit to chrom1")
		}},
		{"@error manypart|chrom1|core ood_test \n", 2, func(err error) bool {
			var re *RequestError
			return errors.As(err, &re) && strings.Contains(re.Msg, "core, ood_test") && strings.Contains(re.Hint, "--partition")
		}},
		{"@reuse 42\n@gone DEADLINE\n", 1, func(err error) bool {
			var ge *GoneError
			return errors.As(err, &ge) && ge.ID == "42" && ge.State == "DEADLINE"
		}},
		{"@reuse 42\n@gone CANCELLED by 1104\n", 1, func(err error) bool {
			var ge *GoneError
			return errors.As(err, &ge) && ge.State == "CANCELLED"
		}},
		{"@reuse 42\n@lost PENDING\n", 1, func(err error) bool {
			var ge *GoneError
			return err != nil && !errors.As(err, &ge) && strings.Contains(err.Error(), "42") &&
				strings.Contains(err.Error(), "PENDING") && strings.Contains(err.Error(), "hpctun code --stop")
		}},
		{"@reuse 42\n@lost UNKNOWN\n", 1, func(err error) bool {
			var ge *GoneError
			return err != nil && !errors.As(err, &ge) && strings.Contains(err.Error(), "42") && !strings.Contains(err.Error(), "UNKNOWN")
		}},
		{"", 255, func(err error) bool { return err != nil && strings.Contains(err.Error(), "shell") }},
	} {
		f := &fake{reply: func(_ context.Context, _ call, stderr io.Writer) (string, int, error) {
			io.WriteString(stderr, c.stderr)
			return "", c.code, nil
		}}
		if _, err := client(f).Ensure(context.Background(), params, nil, io.Discard); !c.check(err) {
			t.Errorf("stderr %q exit %d: err = %v", c.stderr, c.code, err)
		}
	}
}

func TestEnsureCancelsWhenReasonNeverClears(t *testing.T) {
	f := &fake{}
	f.reply = func(ctx context.Context, c call, stderr io.Writer) (string, int, error) {
		if c.script() == "sh -c 'scancel 42'" {
			return "", 0, nil
		}
		io.WriteString(stderr, "@submitted 42|x\n@state PENDING|PartitionTimeLimit\n")
		<-ctx.Done()
		return "", -1, ctx.Err()
	}
	_, err := client(f).Ensure(context.Background(), params, nil, io.Discard)
	var re *RequestError
	if !errors.As(err, &re) || !strings.Contains(re.Msg, "PartitionTimeLimit") || !strings.Contains(re.Hint, "--time") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 2 || f.calls[1].script() != "sh -c 'scancel 42'" {
		t.Fatalf("calls %+v", f.calls)
	}
}

func TestEnsureInterruptedKeepsJob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fake{reply: func(_ context.Context, _ call, stderr io.Writer) (string, int, error) {
		io.WriteString(stderr, "@submitted 42|x\n")
		cancel()
		return "", -1, context.Canceled
	}}
	job, err := client(f).Ensure(ctx, params, nil, io.Discard)
	if !errors.Is(err, context.Canceled) || job.ID != "42" || !job.Submitted || len(f.calls) != 1 {
		t.Fatalf("job %+v err %v calls %d", job, err, len(f.calls))
	}
}

func TestEnsureRejectsLineBreaks(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) { t.Fatal("ran"); return "", 0, nil }}
	p := params
	p.Extra = []string{"a\nb"}
	if _, err := client(f).Ensure(context.Background(), p, nil, io.Discard); err == nil {
		t.Fatal("accepted a line break")
	}
}

// TestEnsureRejectsBadName: Name may list several comma-separated job
// names, each still validated as a job name.
func TestEnsureRejectsBadName(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) { t.Fatal("ran"); return "", 0, nil }}
	p := params
	p.Name = "hpctun-code,a;b"
	if _, err := client(f).Ensure(context.Background(), p, nil, io.Discard); err == nil {
		t.Fatal("accepted a bad name")
	}
}

func TestStopAndCancel(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) { return "", 0, nil }}
	c := client(f)
	if err := c.Stop(context.Background(), "tun-code"); err != nil {
		t.Fatal(err)
	}
	if err := c.Cancel(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}
	if f.calls[0].script() != `sh -c 'scancel -u "$USER" -n tun-code'` || f.calls[1].script() != "sh -c 'scancel 42'" {
		t.Fatalf("calls %+v", f.calls)
	}
	if c.Stop(context.Background(), "a;b") == nil || c.Cancel(context.Background(), "4 2") == nil {
		t.Fatal("accepted a bad name or id")
	}
}

// TestStopNames: Stop cancels the user's jobs under any of several names,
// so a job started under the old name is not orphaned by the rename.
func TestStopNames(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) { return "", 0, nil }}
	c := client(f)
	if err := c.Stop(context.Background(), "hpctun-code", "tun-code"); err != nil {
		t.Fatal(err)
	}
	if want := `sh -c 'scancel -u "$USER" -n hpctun-code && scancel -u "$USER" -n tun-code'`; f.calls[0].script() != want {
		t.Fatalf("script %q, want %q", f.calls[0].script(), want)
	}
	if c.Stop(context.Background(), "bad name") == nil {
		t.Fatal("accepted a bad name")
	}
}

func TestResolvePath(t *testing.T) {
	code := 0
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) {
		return "/home/me/My Project#2\n", code, nil
	}}
	got, err := client(f).ResolvePath(context.Background(), "My Project#2")
	if err != nil || got != "/home/me/My Project#2" || f.calls[0].stdin != "My Project#2\n" {
		t.Fatalf("got %q, %v, stdin %q", got, err, f.calls[0].stdin)
	}
	code = 1
	if _, err := client(f).ResolvePath(context.Background(), "x"); !errors.Is(err, ErrNoDir) {
		t.Fatalf("err = %v", err)
	}
	code = 255
	if _, err := client(f).ResolvePath(context.Background(), "x"); err == nil || errors.Is(err, ErrNoDir) {
		t.Fatalf("err = %v", err)
	}
}

// TestResolvePathIgnoresChattyOutput: a login rc (or CDPATH's cd) can print
// lines before the path; only the last one is the answer.
func TestResolvePathIgnoresChattyOutput(t *testing.T) {
	f := &fake{reply: func(context.Context, call, io.Writer) (string, int, error) {
		return "welcome\n/home/me/p\n", 0, nil
	}}
	got, err := client(f).ResolvePath(context.Background(), "p")
	if err != nil || got != "/home/me/p" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestEnsureSendsScript(t *testing.T) {
	var got string
	c := Client{Gateway: "shell", Exec: func(_ context.Context, _ []string, stdin string, stderr io.Writer) (string, int, error) {
		got = stdin
		io.WriteString(stderr, "@new\n")
		return "42|RUNNING|n1|1:00|None\n", 0, nil
	}}
	var kinds []string
	_, err := c.Ensure(context.Background(), Params{Mode: ModeEnsure, Name: "hpctun-code", CPUs: 1, Time: "1:00:00", Deadline: "now+90minutes", Script: "#!/bin/sh\nx\n"},
		func(e Event) { kinds = append(kinds, e.Kind) }, io.Discard)
	if err != nil || got != "ensure\nhpctun-code\n1\n1:00:00\nnow+90minutes\n\n\n\n@script\n#!/bin/sh\nx\n" || len(kinds) != 1 || kinds[0] != "new" {
		t.Fatalf("err %v stdin %q kinds %v", err, got, kinds)
	}
}

func TestEnsureRejectsScriptMarkerAndMissingScript(t *testing.T) {
	c := Client{Gateway: "shell", Exec: func(context.Context, []string, string, io.Writer) (string, int, error) {
		t.Fatal("ran")
		return "", 0, nil
	}}
	if _, err := c.Ensure(context.Background(), Params{Mode: ModeEnsure, Name: "j", Script: "x", Extra: []string{"@script"}}, nil, io.Discard); err == nil {
		t.Fatal("accepted @script as an argument")
	}
	if _, err := c.Ensure(context.Background(), Params{Mode: ModeEnsure, Name: "j"}, nil, io.Discard); err == nil {
		t.Fatal("accepted no script")
	}
}

func TestNodePath(t *testing.T) {
	code, out := 0, "xfs\n/scratch/me/run 1\n"
	f := &fake{reply: func(_ context.Context, _ call, stderr io.Writer) (string, int, error) {
		if code == 1 {
			io.WriteString(stderr, "srun: error: Unable to create step for job 42: Job/step already completing or completed\n")
		}
		return out, code, nil
	}}
	got, local, err := client(f).NodePath(context.Background(), "42", "/scratch/me/run 1")
	if err != nil || got != "/scratch/me/run 1" || !local || f.calls[0].stdin != "/scratch/me/run 1\n" || !strings.HasSuffix(f.calls[0].script(), " _ 42") {
		t.Fatalf("got %q, %v, call %+v", got, err, f.calls[0])
	}
	for fs, want := range map[string]bool{"nfs": false, "nfs4": false, "lustre": false, "fuse.sshfs": false, "tmpfs": true, "ext2/ext3": true, "": true} {
		out = fs + "\n/p\n"
		if _, local, err := client(f).NodePath(context.Background(), "42", "/p"); err != nil || local != want {
			t.Errorf("%q: local %v, %v", fs, local, err)
		}
	}
	code, out = 3, ""
	if _, _, err := client(f).NodePath(context.Background(), "42", "/x"); !errors.Is(err, ErrNoDir) {
		t.Fatalf("err = %v", err)
	}
	code = 1
	if _, _, err := client(f).NodePath(context.Background(), "42", "/x"); err == nil || errors.Is(err, ErrNoDir) || !strings.Contains(err.Error(), "Unable to create step") {
		t.Fatalf("err = %v", err)
	}
	code, out = 124, ""
	if _, _, err := client(f).NodePath(context.Background(), "42", "/x"); err == nil || !strings.Contains(err.Error(), "within 60 s") {
		t.Fatalf("timeout: err = %v", err)
	}
	if _, _, err := client(f).NodePath(context.Background(), "42;rm", "/x"); err == nil {
		t.Fatal("accepted a bad job id")
	}
}

// TestNodePathScript runs the gateway script under sh with an srun that
// runs its command here: the path is used as data, quotes and $(...) and
// all, and a missing folder exits 3.
func TestNodePathScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs on the gateway, under sh")
	}
	d := t.TempDir()
	bin := filepath.Join(d, "bin")
	os.MkdirAll(bin, 0o755)
	// The stub records its options and runs what follows them.
	// timeout (GNU coreutils on the gateway) records its limit and runs the rest.
	os.WriteFile(filepath.Join(bin, "timeout"), []byte("#!/bin/sh\nprintf \"timeout %s\\n\" \"$1\" >>\""+d+"/srun.args\"\nshift\nexec \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(bin, "srun"), []byte("#!/bin/sh\nwhile [ \"$1\" != sh ]; do printf \"%s\\n\" \"$1\" >>\""+d+"/srun.args\"; shift; done\nexec \"$@\"\n"), 0o755)
	odd := filepath.Join(d, `it's "odd" $(touch pwned) dir`)
	os.MkdirAll(odd, 0o755)
	run := func(p string) (string, int) {
		cmd := exec.Command("sh", "-c", nodePathScript, "_", "42")
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(p + "\n")
		out, err := cmd.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	real, _ := filepath.EvalSymlinks(odd)
	if out, code := run(odd); code != 0 || lastLine(out) != real {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(d, "pwned")); err == nil {
		t.Fatal("the path ran as a command")
	}
	if _, code := run(filepath.Join(d, "missing")); code != 3 {
		t.Fatalf("missing folder: exit %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(d, "srun.args"))
	if !strings.Contains(string(b), "timeout 60\n--jobid=42\n--overlap\n--mpi=none\n") {
		t.Fatalf("srun args %q", b)
	}
}
