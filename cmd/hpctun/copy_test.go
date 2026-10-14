package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func runCmd(a *app, args ...string) error {
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.Execute()
}

func runCmdContext(ctx context.Context, a *app, args ...string) error {
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

func TestCopyHopsForNodes(t *testing.T) {
	a, _, errb := testApp(t, sshCfg)
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	if err := runCmd(a, "cp", "-r", "node1:/scratch/x", "."); err != nil {
		t.Fatal(err)
	}
	if want := []string{"scp", "-J", "files", "-r", "node1:/scratch/x", "."}; !slices.Equal(got, want) {
		t.Fatalf("argv %q", got)
	}
	if errb.String() != "→ scp -J files -r node1:/scratch/x .\n" {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestCopyPassesThroughQuietly(t *testing.T) {
	a, _, errb := testApp(t, sshCfg)
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	if err := runCmd(a, "rsync", "-av", "files:/lab/x", "."); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"rsync", "-av", "files:/lab/x", "."}) || errb.Len() != 0 {
		t.Fatalf("argv %q stderr %q", got, errb.String())
	}
}

func TestCopyWithoutSSHTable(t *testing.T) {
	a, _, _ := testApp(t, "gateway = \"me@gw.example.org\"\n")
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	if err := runCmd(a, "sftp", "node1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"sftp", "node1"}) {
		t.Fatalf("argv %q", got)
	}
}

func TestCopyRefusal(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	err := runCmd(a, "rsync", "-e", "ssh -p 2", "node1:x", ".")
	if err == nil || exitCode(err) != 2 || !strings.Contains(err.Error(), "-e 'ssh -J files'") {
		t.Fatalf("err = %v", err)
	}
}

func TestCopyGlobalFlagsBeforeArgs(t *testing.T) {
	a, _, _ := testApp(t, "")
	cfgPath := a.configPath
	a.configPath = "/nonexistent"
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	writeFile(t, cfgPath, sshCfg)
	if err := runCmd(a, "cp", "--config", cfgPath, "node1:x", "."); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"scp", "-J", "files", "node1:x", "."}) {
		t.Fatalf("argv %q", got)
	}
}

// TestCopyDashVNotConsumedAsTunFlag pins that -v is scp's/rsync's/sftp's own
// flag, not tun's --verbose: it must reach the tool unchanged.
func TestCopyDashVNotConsumedAsTunFlag(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	var got []string
	a.exec = func(argv []string) error { got = argv; return nil }
	if err := runCmd(a, "rsync", "-v", "-a", "x", "node1:y"); err != nil {
		t.Fatal(err)
	}
	want := []string{"rsync", "-e", "ssh -J files", "-v", "-a", "x", "node1:y"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv %q", got)
	}
	if a.verbose {
		t.Fatal("-v should not set tun's own --verbose")
	}
}

func TestCompleteCopyHosts(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	got, dir := completeCopy(a)(&cobra.Command{}, nil, "fi")
	if !slices.Equal(got, []string{"files:"}) || dir != cobra.ShellCompDirectiveNoSpace|cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("got %q %v", got, dir)
	}
	if _, dir := completeCopy(a)(&cobra.Command{}, nil, "./"); dir != cobra.ShellCompDirectiveDefault {
		t.Fatalf("local path directive %v", dir)
	}
}

// TestCopyKeepsToolExitStatus covers Windows, where the copy tool runs as a
// child: its exit status, not a generic 1, is tun's.
func TestCopyKeepsToolExitStatus(t *testing.T) {
	a, _, _ := testApp(t, sshCfg)
	a.exec = func([]string) error { return &exitError{code: 4, err: errors.New("scp exited with status 4")} }
	if err := runCmd(a, "cp", "node1:x", "."); exitCode(err) != 4 {
		t.Fatalf("err = %v, code %d", err, exitCode(err))
	}
}
