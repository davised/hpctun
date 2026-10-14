package sshconfig

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListHosts(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config"), `
# comment
Host shell.hpc hpc
    HostName shell.example.org
Host *.internal !bastion web?
Include conf.d/*.conf
host=lower
Host shell.hpc
Include missing-file
`)
	write(t, filepath.Join(dir, "conf.d", "a.conf"), "Host alpha\n  User me\nInclude b.inc\n")
	write(t, filepath.Join(dir, "b.inc"), "Host \"beta\"\n")
	got, err := ListHosts(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"shell.hpc", "hpc", "alpha", "beta", "lower"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestListHostsMissingFile(t *testing.T) {
	got, err := ListHosts(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestListHostsIncludeLoop(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config"), "Host a\nInclude config\n")
	got, err := ListHosts(filepath.Join(dir, "config"))
	if err != nil || !slices.Equal(got, []string{"a"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestListHostsStopsAtTrailingComment(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config"), "Host chrom1 # my box\n")
	got, err := ListHosts(filepath.Join(dir, "config"))
	if err != nil || !slices.Equal(got, []string{"chrom1"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestListHostsSkipsFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "config"), "Include tun_config\nHost mine\n")
	write(t, filepath.Join(dir, "tun_config"), "Host generated\n")
	got, err := ListHosts(filepath.Join(dir, "config"), filepath.Join(dir, "tun_config"))
	if err != nil || !slices.Equal(got, []string{"mine"}) {
		t.Fatalf("got %q, %v", got, err)
	}
}
