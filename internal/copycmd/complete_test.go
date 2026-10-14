package copycmd

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

var route = Route{Transfer: "files", IsNode: func(h string) bool { return strings.HasPrefix(h, "node") }}

func TestCompleteHosts(t *testing.T) {
	c := Completer{Hosts: []string{"files", "shell", "node1"}, Route: route}
	cases := []struct {
		word  string
		want  []string
		files bool
	}{
		{"fi", []string{"files:"}, false},
		{"me@sh", []string{"me@shell:"}, false},
		{"", []string{"files:", "shell:", "node1:"}, false},
		{"./x", nil, true},
		{"~/x", nil, true},
		{"/tmp", nil, true},
		{"zzz", nil, true},
		{"-oProxyCommand=x:y", nil, false},
	}
	for _, tc := range cases {
		got, files := c.Complete(context.Background(), tc.word)
		if !slices.Equal(got, tc.want) || files != tc.files {
			t.Errorf("%q: got %q files=%v", tc.word, got, files)
		}
	}
}

func TestCompleteRemoteNode(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{Stdout: []byte("/scratch/run.log\n/scratch/runs/\n")}, nil
	}}
	c := Completer{Runner: f, Route: route}
	got, files := c.Complete(context.Background(), "me@node1:/scratch/ru")
	if files || !slices.Equal(got, []string{"me@node1:/scratch/run.log", "me@node1:/scratch/runs/"}) {
		t.Fatalf("got %q", got)
	}
	want := "ssh -J files -o BatchMode=yes -o ConnectTimeout=5 -- me@node1 ls -dp -- '/scratch/ru'*"
	if len(f.Calls) != 1 || f.Calls[0].Line() != want {
		t.Fatalf("calls %v", f.Calls)
	}
}

func TestCompleteRemoteNotANodeListsHome(t *testing.T) {
	f := &sshctltest.Fake{}
	Completer{Runner: f, Route: route}.Complete(context.Background(), "files:")
	want := "ssh -o BatchMode=yes -o ConnectTimeout=5 -- files ls -dp -- *"
	if len(f.Calls) != 1 || f.Calls[0].Line() != want {
		t.Fatalf("calls %v", f.Calls)
	}
}

func TestCompleteRemoteFailureGivesNothing(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 255}, nil
	}}
	if got, files := (Completer{Runner: f, Route: route}).Complete(context.Background(), "node1:/x"); got != nil || files {
		t.Fatalf("got %q %v", got, files)
	}
	if got, _ := (Completer{Runner: f, Route: route}).Complete(context.Background(), "node1::mod"); got != nil {
		t.Fatalf("daemon syntax: %q", got)
	}
}

type blockRunner struct{}

func (blockRunner) Run(ctx context.Context, _ string, _ ...string) (sshctl.Result, error) {
	<-ctx.Done()
	return sshctl.Result{}, ctx.Err()
}
func (blockRunner) Background(ctx context.Context, _, _ string, _ ...string) (sshctl.Result, error) {
	return sshctl.Result{}, nil
}
func (blockRunner) Start(ctx context.Context, _, _ string, _ ...string) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestCompleteTimeout(t *testing.T) {
	start := time.Now()
	got, _ := Completer{Runner: blockRunner{}, Route: route, Timeout: 20 * time.Millisecond}.Complete(context.Background(), "node1:/x")
	if got != nil || time.Since(start) > time.Second {
		t.Fatalf("got %q after %v", got, time.Since(start))
	}
}

func TestRemoteGlob(t *testing.T) {
	cases := map[string]string{
		"":            "*",
		"~/":          "~/*",
		"~":           "~/*",
		"~/My Docs/a": "~/'My Docs/a'*",
		"it's":        `'it'\''s'*`,
		"/x":          "'/x'*",
		"$(rm -rf x)": "'$(rm -rf x)'*",
	}
	for in, want := range cases {
		if got := RemoteGlob(in); got != want {
			t.Errorf("RemoteGlob(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestCompleteDriveLetterIsLocal: on Windows, completing C:\Us lists local
// files instead of running ssh to a host named C.
func TestCompleteDriveLetterIsLocal(t *testing.T) {
	f := &sshctltest.Fake{}
	c := Completer{Runner: f, Route: Route{DriveLetters: true}}
	for _, w := range []string{`C:\Us`, "d:/data"} {
		cands, files := c.Complete(context.Background(), w)
		if cands != nil || !files {
			t.Errorf("%s: cands %q files %v", w, cands, files)
		}
	}
	if len(f.Calls) != 0 {
		t.Fatalf("ran %s", f.Calls[0].Line())
	}
}

// TestCompleteRemoteKeepsTilde: a remote ~/ would come back expanded to
// the home directory, which no longer starts with the word typed, so the
// shell would drop every candidate. The listing runs relative to the home
// directory instead and the ~/ is put back.
func TestCompleteRemoteKeepsTilde(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{Stdout: []byte("projects/\nprompt.txt\n")}, nil
	}}
	got, _ := Completer{Runner: f, Route: route}.Complete(context.Background(), "files:~/pro")
	if !slices.Equal(got, []string{"files:~/projects/", "files:~/prompt.txt"}) {
		t.Fatalf("got %q", got)
	}
	want := "ssh -o BatchMode=yes -o ConnectTimeout=5 -- files ls -dp -- 'pro'*"
	if len(f.Calls) != 1 || f.Calls[0].Line() != want {
		t.Fatalf("calls %v", f.Calls)
	}
}
