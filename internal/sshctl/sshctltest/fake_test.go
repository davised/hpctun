package sshctltest

import (
	"context"
	"testing"
)

func TestFakeStart(t *testing.T) {
	f := &Fake{OnStart: func(c Call) (int, error) { return 77, nil }}
	pid, err := f.Start(context.Background(), "/l", "ssh", "-N", "gw")
	if err != nil || pid != 77 || len(f.Calls) != 1 || !f.Calls[0].Start || f.Calls[0].LogPath != "/l" {
		t.Fatalf("pid %d err %v calls %+v", pid, err, f.Calls)
	}
}
