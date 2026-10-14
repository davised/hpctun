package copycmd

import (
	"slices"
	"strings"
	"testing"
)

func a(s ...string) []string { return s }

func TestRewrite(t *testing.T) {
	r := Route{Transfer: "files", IsNode: func(h string) bool { return strings.HasPrefix(h, "node") }}
	cases := []struct {
		name, tool string
		args, want []string
		hop        bool
		err        string
	}{
		{"scp from node", "scp", a("node1:/scratch/x", "."), a("scp", "-J", "files", "node1:/scratch/x", "."), true, ""},
		{"scp cluster with value", "scp", a("-rP", "2222", "me@node2:d", "."), a("scp", "-J", "files", "-rP", "2222", "me@node2:d", "."), true, ""},
		{"scp option value is not an operand", "scp", a("-i", "node1:key", "x", "files:y"), a("scp", "-i", "node1:key", "x", "files:y"), false, ""},
		{"scp not a node", "scp", a("files:/x", "."), a("scp", "files:/x", "."), false, ""},
		{"scp local path with colon", "scp", a("./a:b", "node1:"), a("scp", "-J", "files", "./a:b", "node1:"), true, ""},
		{"scp own -J", "scp", a("-J", "gw", "node1:x", "."), a("scp", "-J", "gw", "node1:x", "."), false, ""},
		{"scp own ProxyJump", "scp", a("-o", "ProxyJump=gw", "node1:x", "."), a("scp", "-o", "ProxyJump=gw", "node1:x", "."), false, ""},
		{"scp uri", "scp", a("scp://me@node3:2222/x", "."), a("scp", "-J", "files", "scp://me@node3:2222/x", "."), true, ""},
		{"scp ipv6", "scp", a("[fe80::1]:x", "."), a("scp", "[fe80::1]:x", "."), false, ""},
		{"scp after --", "scp", a("--", "-odd", "node1:x"), a("scp", "-J", "files", "--", "-odd", "node1:x"), true, ""},
		{"scp mixed", "scp", a("node1:x", "web:y"), nil, false, "copy in two steps"},
		{"rsync", "rsync", a("-av", "out/", "node1:/scratch/me/"), a("rsync", "-e", "ssh -J files", "-av", "out/", "node1:/scratch/me/"), true, ""},
		{"rsync own -e", "rsync", a("-e", "ssh -p 2", "node1:x", "."), nil, false, "-e 'ssh -J files'"},
		{"rsync own --rsh", "rsync", a("--rsh=ssh", "node1:x", "."), nil, false, "-e 'ssh -J files'"},
		{"rsync own -e, not a node", "rsync", a("-e", "ssh", "files:x", "."), a("rsync", "-e", "ssh", "files:x", "."), false, ""},
		{"rsync e as a -T value", "rsync", a("-avT/tmp/e", "out/", "node1:/s/"), a("rsync", "-e", "ssh -J files", "-avT/tmp/e", "out/", "node1:/s/"), true, ""},
		{"rsync -Te is a temp dir", "rsync", a("-Te", "node1:x", "."), a("rsync", "-e", "ssh -J files", "-Te", "node1:x", "."), true, ""},
		{"rsync e in a cluster", "rsync", a("-avze", "ssh -p 2", "node1:x", "."), nil, false, "-e 'ssh -J files'"},
		{"rsync -e value after a cluster", "rsync", a("-av", "-e", "ssh", "node1:x", "."), nil, false, "-e 'ssh -J files'"},
		{"rsync short value is not an operand", "rsync", a("-f", "- web:x", "out/", "node1:/s/"), a("rsync", "-e", "ssh -J files", "-f", "- web:x", "out/", "node1:/s/"), true, ""},
		{"rsync long value is not an operand", "rsync", a("--exclude", "web:x", "-av", "out/", "node1:/s/"), a("rsync", "-e", "ssh -J files", "--exclude", "web:x", "-av", "out/", "node1:/s/"), true, ""},
		{"rsync --long=value", "rsync", a("--exclude=web:x", "out/", "node1:/s/"), a("rsync", "-e", "ssh -J files", "--exclude=web:x", "out/", "node1:/s/"), true, ""},
		{"rsync --rsh value", "rsync", a("--rsh", "ssh", "node1:x", "."), nil, false, "-e 'ssh -J files'"},
		{"rsync options after operands", "rsync", a("out/", "node1:/s/", "-e", "ssh"), nil, false, "-e 'ssh -J files'"},
		{"rsync after --", "rsync", a("--", "-e", "node1:x"), a("rsync", "-e", "ssh -J files", "--", "-e", "node1:x"), true, ""},
		{"rsync daemon", "rsync", a("node1::mod/x", "."), a("rsync", "node1::mod/x", "."), false, ""},
		{"rsync url", "rsync", a("rsync://node1/mod", "."), a("rsync", "rsync://node1/mod", "."), false, ""},
		{"sftp", "sftp", a("node1"), a("sftp", "-J", "files", "node1"), true, ""},
		{"sftp port and path", "sftp", a("-P", "22", "me@node1:/scratch"), a("sftp", "-J", "files", "-P", "22", "me@node1:/scratch"), true, ""},
		{"sftp not a node", "sftp", a("files"), a("sftp", "files"), false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, hop, err := Rewrite(c.tool, c.args, r)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || hop != c.hop || !slices.Equal(got, c.want) {
				t.Fatalf("got %q hop=%v err=%v, want %q hop=%v", got, hop, err, c.want, c.hop)
			}
		})
	}
}

func TestRewriteWithoutTransfer(t *testing.T) {
	got, hop, err := Rewrite("scp", a("node1:x", "."), Route{IsNode: func(string) bool { return true }})
	if err != nil || hop || !slices.Equal(got, a("scp", "node1:x", ".")) {
		t.Fatalf("got %q %v %v", got, hop, err)
	}
}

func TestQuote(t *testing.T) {
	got := Quote(a("rsync", "-e", "ssh -J files", "it's", "node1:/scratch/x"))
	want := `rsync -e 'ssh -J files' 'it'\''s' node1:/scratch/x`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

// TestRewriteDriveLetters: on Windows, C:\x and C:/x are local paths, not a
// host named C; elsewhere a one-letter alias stays a host.
func TestRewriteDriveLetters(t *testing.T) {
	win := Route{Transfer: "files", IsNode: func(h string) bool { return strings.HasPrefix(h, "node") }, DriveLetters: true}
	for _, args := range [][]string{
		a("node1:/x", `C:\Users\me\file`),
		a(`D:\data\in.txt`, "node1:/x"),
		a("C:/Users/me", "node1:/x"),
		a("D:rel", "node1:/x"),
	} {
		got, hop, err := Rewrite("scp", args, win)
		if err != nil || !hop || !slices.Equal(got, append(a("scp", "-J", "files"), args...)) {
			t.Errorf("%q: got %q hop=%v err=%v", args, got, hop, err)
		}
	}
	unix := win
	unix.DriveLetters = false
	if _, _, err := Rewrite("scp", a("c:/x", "node1:/y"), unix); err == nil || !strings.Contains(err.Error(), "copy in two steps") {
		t.Errorf("unix: c is a host alias, err = %v", err)
	}
}
