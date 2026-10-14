package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteZipsDirUnderItsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hpctun-v0-windows-amd64")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "hpctun.exe"), []byte("exe"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "LICENSE"), []byte("bsd"), 0o644)
	out := filepath.Join(t.TempDir(), "a.zip")
	if err := write(out, dir); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := map[string]string{}
	for _, f := range r.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
	}
	if len(got) != 2 || got["hpctun-v0-windows-amd64/hpctun.exe"] != "exe" || got["hpctun-v0-windows-amd64/sub/LICENSE"] != "bsd" {
		t.Fatalf("got %v", got)
	}
}
