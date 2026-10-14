package probe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseListeners(t *testing.T) {
	got := ParseListeners(fixture(t, "listen.txt"))
	want := map[int]Listener{
		4001: {Port: 4001, PID: 52216, Command: "ssh"},
		8099: {Port: 8099, PID: 25298, Command: "ssh"},
		5005: {Port: 5005, PID: 25298, Command: "ssh"},
		8888: {Port: 8888, PID: 999, Command: "python3"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for port, l := range want {
		if got[port] != l {
			t.Errorf("port %d = %+v, want %+v", port, got[port], l)
		}
	}
}

func TestParseEstablishedCountsDistinctSockets(t *testing.T) {
	got := ParseEstablished(fixture(t, "established.txt"))
	if got[8099] != 2 {
		t.Fatalf("8099 conns = %d, want 2 (two distinct accepted sockets)", got[8099])
	}
	if got[60001] != 1 || got[57593] != 1 {
		t.Fatalf("got %v", got)
	}
}

func lsofFake(listen, est []byte, code int) *sshctltest.Fake {
	return &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if slices.Contains(c.Args, "-sTCP:LISTEN") {
			return sshctl.Result{Stdout: listen, ExitCode: code}, nil
		}
		return sshctl.Result{Stdout: est, ExitCode: code}, nil
	}}
}

func TestTake(t *testing.T) {
	f := lsofFake(fixture(t, "listen.txt"), fixture(t, "established.txt"), 0)
	snap, err := Take(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Listeners[4001].PID != 52216 || snap.Conns[8099] != 2 {
		t.Fatalf("got %+v", snap)
	}
}

// lsof exits 1 when nothing matches: that is an empty snapshot, not an error.
func TestTakeNothingListening(t *testing.T) {
	snap, err := Take(context.Background(), lsofFake(nil, nil, 1))
	if err != nil || len(snap.Listeners) != 0 || len(snap.Conns) != 0 {
		t.Fatalf("got %+v, %v", snap, err)
	}
}

func TestTakeLsofFailure(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{ExitCode: 2, Stderr: []byte("lsof: bad flag\n")}, nil
	}}
	if _, err := Take(context.Background(), f); err == nil {
		t.Fatal("expected error for lsof exit 2")
	}
}

func TestTakeNoLsof(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{}, &exec.Error{Name: "lsof", Err: exec.ErrNotFound}
	}}
	if _, err := Take(context.Background(), f); !errors.Is(err, ErrNoLsof) {
		t.Fatalf("err = %v, want ErrNoLsof", err)
	}
}

func TestProcessCommand(t *testing.T) {
	f := &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		if c.Args[len(c.Args)-1] == "52216" {
			return sshctl.Result{Stdout: []byte("ssh -Nf -L4001:localhost:4001 node1\n")}, nil
		}
		return sshctl.Result{ExitCode: 1}, nil
	}}
	cmd, ok, err := ProcessCommand(context.Background(), f, 52216)
	if err != nil || !ok || cmd != "ssh -Nf -L4001:localhost:4001 node1" {
		t.Fatalf("got %q %v %v", cmd, ok, err)
	}
	if _, ok, err := ProcessCommand(context.Background(), f, 1); ok || err != nil {
		t.Fatalf("missing pid: ok=%v err=%v", ok, err)
	}
}
