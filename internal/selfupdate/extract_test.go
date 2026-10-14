package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"hpctun-v1-linux-amd64/":            "",
		"hpctun-v1-linux-amd64/hpctun":      "new hpctun",
		"hpctun-v1-linux-amd64/hpctun-tray": "new tray",
		"hpctun-v1-linux-amd64/README.md":   "readme",
	}
	for _, name := range []string{"a.tar.gz", "a.zip"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(dir, name)
			if strings.HasSuffix(name, ".zip") {
				writeZip(t, archive, files)
			} else {
				writeTarGz(t, archive, files)
			}
			dest := t.TempDir()
			members := []string{"hpctun-v1-linux-amd64/hpctun", "hpctun-v1-linux-amd64/hpctun-tray"}
			if err := Extract(archive, members, dest); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(dest, "hpctun")); got != "new hpctun" {
				t.Fatalf("hpctun = %q", got)
			}
			if got := readFile(t, filepath.Join(dest, "hpctun-tray")); got != "new tray" {
				t.Fatalf("tray = %q", got)
			}
			if _, err := os.Stat(filepath.Join(dest, "README.md")); err == nil {
				t.Fatal("extracted a member nobody asked for")
			}
			if fi, _ := os.Stat(filepath.Join(dest, "hpctun")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
				t.Fatalf("mode %v", fi.Mode())
			}
			err := Extract(archive, []string{"hpctun-v1-linux-amd64/missing"}, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "has no hpctun-v1-linux-amd64/missing") {
				t.Fatalf("missing member: %v", err)
			}
			// A member in another directory is not the one asked for.
			err = Extract(archive, []string{"hpctun"}, t.TempDir())
			if err == nil {
				t.Fatal("matched a member by base name only")
			}
		})
	}
}

func TestExtractBounds(t *testing.T) {
	files := map[string]string{"r/hpctun": "new hpctun", "r/a": "a", "r/b": "b"}
	for _, name := range []string{"a.tar.gz", "a.zip"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), name)
			if strings.HasSuffix(name, ".zip") {
				writeZip(t, archive, files)
			} else {
				writeTarGz(t, archive, files)
			}
			setBound(t, &maxFileBytes, 4)
			if err := Extract(archive, []string{"r/hpctun"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unpacks to more than 4 bytes") {
				t.Fatalf("file bound: %v", err)
			}
			setBound(t, &maxFileBytes, 1<<20)
			setBound(t, &maxMembers, 2)
			if err := Extract(archive, []string{"r/hpctun"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "more than 2 entries") {
				t.Fatalf("member bound: %v", err)
			}
		})
	}
}

// A member nobody asked for must not be unpacked without bound: its
// declared size is checked even though it is skipped.
func TestExtractBoundsUnselected(t *testing.T) {
	files := map[string]string{"r/junk": "0123456789", "r/small": "ok"}
	for _, name := range []string{"a.tar.gz", "a.zip"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), name)
			if strings.HasSuffix(name, ".zip") {
				writeZip(t, archive, files)
			} else {
				writeTarGz(t, archive, files)
			}
			setBound(t, &maxFileBytes, 4)
			dest := t.TempDir()
			err := Extract(archive, []string{"r/small"}, dest)
			if err == nil || !strings.Contains(err.Error(), "unpacks to more than 4 bytes") {
				t.Fatalf("err %v", err)
			}
			if _, err := os.Stat(filepath.Join(dest, "small")); err == nil {
				t.Fatal("wrote a file despite the oversized member")
			}
		})
	}
}

// The total cap counts the whole decompressed stream, not only what is
// written, so a member nobody asked for cannot force unbounded work.
func TestExtractTarStreamBound(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	writeTarGz(t, archive, map[string]string{"r/hpctun": "new hpctun"})
	setBound(t, &maxTotalBytes, 10)
	err := Extract(archive, []string{"r/hpctun"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unpacks to more than 10 bytes") {
		t.Fatalf("err %v", err)
	}
}

// A cap reached while a member is being read is reported as the cap, not as
// the truncated stream it looks like.
func TestExtractTarStreamBoundMidMember(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "a.tar.gz")
	writeTarGz(t, archive, map[string]string{"r/hpctun": strings.Repeat("x", 600)})
	setBound(t, &maxTotalBytes, 1000)
	err := Extract(archive, []string{"r/hpctun"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unpacks to more than 1000 bytes") {
		t.Fatalf("err %v", err)
	}
}

func TestExtractTreeTotalBound(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "app.zip")
	writeZip(t, zipPath, map[string]string{"hpctun-tray.app/a": "aaaaaa", "hpctun-tray.app/b": "bbbbbb"})
	setBound(t, &maxTotalBytes, 10)
	err := ExtractTree(zipPath, "hpctun-tray.app", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unpacks to more than 4 bytes") {
		t.Fatalf("err %v", err)
	}
}

func TestExtractTree(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "app.zip")
	writeZip(t, zipPath, map[string]string{
		"hpctun-tray.app/":                           "",
		"hpctun-tray.app/Contents/":                  "",
		"hpctun-tray.app/Contents/Info.plist":        "plist",
		"hpctun-tray.app/Contents/MacOS/":            "",
		"hpctun-tray.app/Contents/MacOS/hpctun":      "new hpctun",
		"hpctun-tray.app/Contents/MacOS/hpctun-tray": "new tray",
	})
	dest := t.TempDir()
	if err := ExtractTree(zipPath, "hpctun-tray.app", dest); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dest, "hpctun-tray.app", "Contents", "MacOS", "hpctun")); got != "new hpctun" {
		t.Fatalf("hpctun = %q", got)
	}
	fi, err := os.Stat(filepath.Join(dest, "hpctun-tray.app", "Contents", "MacOS", "hpctun-tray"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755) {
		t.Fatalf("tray: %v %v", fi, err)
	}
}

func TestExtractTreeRefuses(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"dotdot":       {"hpctun-tray.app/../evil": "x"},
		"absolute":     {"/etc/evil": "x"},
		"outside root": {"other.app/Contents/x": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			zipPath := filepath.Join(t.TempDir(), "app.zip")
			writeZip(t, zipPath, files)
			dest := t.TempDir()
			if err := ExtractTree(zipPath, "hpctun-tray.app", dest); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// A zip without the bundle is an error too.
	zipPath := filepath.Join(t.TempDir(), "empty.zip")
	writeZip(t, zipPath, map[string]string{})
	if err := ExtractTree(zipPath, "hpctun-tray.app", t.TempDir()); err == nil {
		t.Fatal("empty zip accepted")
	}
}

// ditto writes resource forks as AppleDouble files: ._name beside a file,
// or under __MACOSX/. They are not part of the bundle.
func TestExtractTreeSkipsAppleDouble(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "app.zip")
	writeZip(t, zipPath, map[string]string{
		"hpctun-tray.app/Contents/MacOS/hpctun":   "new hpctun",
		"hpctun-tray.app/Contents/MacOS/._hpctun": "fork",
		"__MACOSX/hpctun-tray.app/._Contents":     "fork",
	})
	dest := t.TempDir()
	if err := ExtractTree(zipPath, "hpctun-tray.app", dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "hpctun-tray.app", "Contents", "MacOS", "._hpctun")); err == nil {
		t.Fatal("extracted an AppleDouble file")
	}
	if _, err := os.Stat(filepath.Join(dest, "__MACOSX")); err == nil {
		t.Fatal("extracted __MACOSX")
	}
}
