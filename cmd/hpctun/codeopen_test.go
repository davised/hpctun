package main

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"hpctun/internal/prompt"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const codeBin = "/Apps/code"

// withVSCode makes a find VS Code at codeBin, answer --list-extensions with
// list, and record every other VS Code command.
func withVSCode(t *testing.T, a *app, list string) *[]string {
	t.Helper()
	a.goos = "darwin"
	a.codeBin = func() (string, bool) { return codeBin, true }
	var ran []string
	h := a.run.(*sshctltest.Fake).Handler
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Name != codeBin {
			return h(c)
		}
		if c.Line() == codeBin+" --list-extensions" {
			return sshctl.Result{Stdout: []byte(list)}, nil
		}
		ran = append(ran, c.Line())
		return sshctl.Result{}, nil
	}}
	return &ran
}

const remoteTunnels = "ms-vscode.remote-server\n"

func TestCodeOpensFolder(t *testing.T) {
	a, _, out, _ := codeApp(t, "", running(true))
	ran := withVSCode(t, a, remoteTunnels)
	if err := runCmd(a, "code", "My Project"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if want := codeBin + " --new-window --folder-uri vscode-remote://tunnel+hpc-me/home/me/My%20Project"; len(*ran) != 1 || (*ran)[0] != want {
		t.Fatalf("ran %q", *ran)
	}
	contains(t, out.String(), "opened VS Code on tunnel hpc-me", "23:59:59 left")
}

// TestCodeWithoutFolderOpensLastFolder: with no folder, hpctun code opens
// the home folder the first time, then the folder it opened last while that
// is still there, so VS Code can reuse a window already on it.
func TestCodeWithoutFolderOpensLastFolder(t *testing.T) {
	gone := false
	base := running(true)
	a, _, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if gone && strings.Contains(c.script(), isNodePath) && c.stdin == "/home/me/proj\n" {
			return "", 3, nil
		}
		return base(ctx, c, stderr)
	})
	ran := withVSCode(t, a, remoteTunnels)
	open := func(args ...string) string {
		t.Helper()
		*ran = nil
		out.Reset()
		if err := runCmd(a, append([]string{"code"}, args...)...); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return (*ran)[len(*ran)-1]
	}
	home := codeBin + " --new-window --folder-uri vscode-remote://tunnel+hpc-me/home/me"
	if got := open(); got != home {
		t.Fatalf("first run opened %q", got)
	}
	if got := open("proj"); got != home+"/proj" {
		t.Fatalf("with a folder opened %q", got)
	}
	if got := open(); got != home+"/proj" {
		t.Fatalf("rerun opened %q", got)
	}
	contains(t, out.String(), "opening /home/me/proj, the folder hpctun code opened last")
	gone = true
	if got := open(); got != home {
		t.Fatalf("with the last folder gone opened %q", got)
	}
}

// TestCodeOpensPrintedName: the CLI fell back to a random name; hpctun opens
// it and warns.
func TestCodeOpensPrintedName(t *testing.T) {
	base := running(true)
	a, _, out, errb := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if strings.Contains(c.script(), isWatch) {
			io.WriteString(stderr, "@log [x] info hpc-me is already taken, using a random name instead\n@log   ➜  Tunnel:   chrom1hpcoregonstat\n")
			<-ctx.Done()
			return "", 255, ctx.Err()
		}
		return base(ctx, c, stderr)
	})
	ran := withVSCode(t, a, remoteTunnels)
	if err := runCmd(a, "code"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if (*ran)[0] != codeBin+" --new-window --folder-uri vscode-remote://tunnel+chrom1hpcoregonstat/home/me" {
		t.Fatalf("ran %q", *ran)
	}
	contains(t, errb.String(), "chrom1-me is taken")
}

func TestCodeWithoutVSCodeSubmitsNothing(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	a.goos = "darwin"
	a.codeBin = func() (string, bool) { return "", false }
	err := runCmd(a, "code")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "VS Code not found") || len(f.calls) != 0 {
		t.Fatalf("err = %v, remote calls %d", err, len(f.calls))
	}
}

func TestCodeMissingExtension(t *testing.T) {
	a, f, _, _ := codeApp(t, "", running(true))
	withVSCode(t, a, "ms-vscode-remote.remote-ssh\n")
	err := runCmd(a, "code")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "Remote - Tunnels") || !strings.Contains(err.Error(), "ms-vscode.remote-server") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find(isEnsure)) != 0 || len(f.calls) != 0 {
		t.Fatalf("remote calls %d", len(f.calls))
	}

	a, _, _, _ = codeApp(t, "", running(true))
	withVSCode(t, a, "")
	yes := true
	a.tty, a.ask = &yes, answerAsker{yes: true}
	var installed []string
	a.interact = func(argv []string, _ io.Reader) error { installed = argv; return nil }
	if err := runCmd(a, "code", "--no-open"); err != nil { // --no-open: no extension needed
		t.Fatal(err)
	}
	if installed != nil {
		t.Fatalf("installed with --no-open: %q", installed)
	}
	if err := runCmd(a, "code", "proj"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(installed, " ") != codeBin+" --install-extension ms-vscode.remote-server" {
		t.Fatalf("installed %q", installed)
	}
}

func TestCodeHeadlessDoesNotOpen(t *testing.T) {
	a, _, out, _ := codeApp(t, "", running(true))
	ran := withVSCode(t, a, remoteTunnels)
	a.goos = "linux" // testApp's environment has no DISPLAY
	if err := runCmd(a, "code"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 0 {
		t.Fatalf("ran %q", *ran)
	}
	contains(t, out.String(), "Remote Explorer → Tunnels → hpc-me")
}

// folderAsker answers hpctun code's folder picker with pick (and the typed
// folder with typed), recording what it was offered.
type folderAsker struct {
	answerAsker
	pick, typed string
	offered     *prompt.SelectQ
}

func (f folderAsker) Select(q prompt.SelectQ) (string, error) {
	*f.offered = q
	for _, o := range q.Options {
		if o.Label == f.pick || o.Value == f.pick {
			return o.Value, nil
		}
	}
	return "", prompt.ErrAborted
}

func (f folderAsker) Input(prompt.InputQ) (string, error) { return f.typed, nil }

// TestCodeFolderPicker: on a terminal with no folder, hpctun code offers the
// recent folders (last first, the default) that fit the job's node, home, a
// typed folder and an empty window, and opens what was picked.
func TestCodeFolderPicker(t *testing.T) {
	const recent = "/home/me/proj\nchrom1\t/scratch/me/run1\nother\t/scratch/me/old\n"
	cases := []struct {
		pick, typed, want, record string
	}{
		{"/scratch/me/run1  (on chrom1)", "", "--folder-uri vscode-remote://tunnel+hpc-me/scratch/me/run1", "chrom1\t/scratch/me/run1"},
		{"~  (your home folder)", "", "--folder-uri vscode-remote://tunnel+hpc-me/home/me", "/home/me"},
		{"Type a folder…", "proj2", "--folder-uri vscode-remote://tunnel+hpc-me/home/me/proj2", "/home/me/proj2"},
		{"Type a folder…", "/tmp/x", "--folder-uri vscode-remote://tunnel+hpc-me/tmp/x", "chrom1\t/tmp/x"},
		{"Type a folder…", "/nfs4/lab/x", "--folder-uri vscode-remote://tunnel+hpc-me/nfs4/lab/x", "/nfs4/lab/x"},
		{"Choose in VS Code  (an empty window; File → Open Folder)", "", "--remote tunnel+hpc-me", ""},
	}
	for _, c := range cases {
		a, _, _, _ := codeApp(t, "", running(false))
		ran := withVSCode(t, a, remoteTunnels)
		dir, _ := a.stateDir()
		writeFile(t, filepath.Join(dir, codeFolders), recent)
		var offered prompt.SelectQ
		yes := true
		a.tty, a.ask = &yes, folderAsker{pick: c.pick, typed: c.typed, offered: &offered}
		if err := runCmd(a, "code"); err != nil {
			t.Fatalf("%s: %v", c.pick, err)
		}
		if got := (*ran)[len(*ran)-1]; got != codeBin+" --new-window "+c.want {
			t.Errorf("%s: ran %q", c.pick, got)
		}
		// The job runs on chrom1: other's folder is not offered.
		var labels []string
		for _, o := range offered.Options {
			labels = append(labels, o.Label)
		}
		if offered.Default != offered.Options[0].Value || strings.Join(labels[:2], "|") != "/home/me/proj  (last opened)|/scratch/me/run1  (on chrom1)" || len(labels) != 5 {
			t.Errorf("offered %q", labels)
		}
		got := a.readCodeState(dir, codeFolders)
		if c.record == "" {
			if got != strings.TrimSpace(recent) {
				t.Errorf("an empty window changed the recent folders: %q", got)
			}
		} else if first, _, _ := strings.Cut(got, "\n"); first != c.record {
			t.Errorf("%s: recorded %q", c.pick, got)
		}
	}
}

// TestCodeFolderPickerPinsNode: with no job yet, a node's own folder is
// offered with its node, and picking it starts the job there.
func TestCodeFolderPickerPinsNode(t *testing.T) {
	base := running(true)
	a, f, out, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if strings.HasPrefix(c.stdin, "find\n") {
			return "none\n", 3, nil
		}
		return base(ctx, c, stderr)
	})
	withVSCode(t, a, remoteTunnels)
	dir, _ := a.stateDir()
	writeFile(t, filepath.Join(dir, codeFolders), "chrom1\t/scratch/me/run1\nother\t/scratch/me/old\n")
	var offered prompt.SelectQ
	yes := true
	a.tty, a.ask = &yes, folderAsker{pick: "/scratch/me/old  (on other)", offered: &offered}
	if err := runCmd(a, "code"); err != nil {
		t.Fatal(err)
	}
	if len(offered.Options) != 5 {
		t.Errorf("offered %+v", offered.Options)
	}
	ensure := slices.DeleteFunc(f.find(isEnsure), func(c remoteCall) bool { return strings.HasPrefix(c.stdin, "find\n") })
	if len(ensure) != 1 || !strings.HasPrefix(ensure[0].stdin, "ensure\nhpctun-code,tun-code\n4\n24:00:00\n") || !strings.Contains(ensure[0].stdin, "\n\nother\n") {
		t.Fatalf("ensure %+v", ensure)
	}
	contains(t, out.String(), "starting the job on other, where /scratch/me/old is")
}

// TestCodeNodeLocalFolder: an absolute folder is not looked for on the
// gateway; once the job runs it is checked on the node, and a missing one
// fails without cancelling the job.
func TestCodeNodeLocalFolder(t *testing.T) {
	missing := false
	base := running(true)
	a, f, _, _ := codeApp(t, "", func(ctx context.Context, c remoteCall, stderr io.Writer) (string, int, error) {
		if missing && strings.Contains(c.script(), isNodePath) {
			return "", 3, nil
		}
		return base(ctx, c, stderr)
	})
	ran := withVSCode(t, a, remoteTunnels)
	if err := runCmd(a, "code", "/scratch/me/run1"); err != nil {
		t.Fatal(err)
	}
	if len(f.find(isPath)) != 0 || len(f.find(isNodePath)) != 1 || !strings.HasSuffix(f.find(isNodePath)[0].script(), " _ 42") {
		t.Fatalf("gateway checks %d, node checks %+v", len(f.find(isPath)), f.find(isNodePath))
	}
	if (*ran)[0] != codeBin+" --new-window --folder-uri vscode-remote://tunnel+hpc-me/scratch/me/run1" {
		t.Fatalf("ran %q", *ran)
	}
	missing = true
	err := runCmd(a, "code", "/scratch/me/nope")
	if exitCode(err) != 2 || !strings.Contains(err.Error(), "/scratch/me/nope is not a folder on chrom1") || !strings.Contains(err.Error(), "keeps running") {
		t.Fatalf("err = %v", err)
	}
	if len(f.find("scancel 42")) != 0 {
		t.Fatal("cancelled the job")
	}
}

// TestCodeNoOpenNeedsNoFolder: with --no-open and no folder there is nothing
// to open, so nothing is asked or checked.
func TestCodeNoOpenNeedsNoFolder(t *testing.T) {
	a, f, out, _ := codeApp(t, "", running(true))
	yes := true
	a.tty, a.ask = &yes, answerAsker{}
	if err := runCmd(a, "code", "--no-open"); err != nil {
		t.Fatal(err)
	}
	if len(f.find(isPath))+len(f.find(isNodePath)) != 0 {
		t.Fatal("checked a folder")
	}
	contains(t, out.String(), "Remote Explorer → Tunnels → hpc-me.")
}
