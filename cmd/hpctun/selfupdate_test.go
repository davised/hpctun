package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hpctun/internal/filelock"
	"hpctun/internal/jsondoc"
	"hpctun/internal/selfupdate"
	"hpctun/internal/selfupdate/selfupdatetest"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshctl/sshctltest"
)

const (
	oldVer = "v0.20260928-9662ab3"
	newVer = "v0.20261007-ab131b6"
)

// fakeSource serves a release: files (name -> body) and latest. fetched
// records every Fetch.
type fakeSource struct {
	latest  string
	files   map[string][]byte
	err     error
	fetched [][]string
}

func (f *fakeSource) Latest(context.Context) (string, error) { return f.latest, f.err }

func (f *fakeSource) Fetch(_ context.Context, ver string, names []string, dir string) error {
	f.fetched = append(f.fetched, names)
	if ver != f.latest {
		return fmt.Errorf("fetch of %s, not latest", ver)
	}
	for _, n := range names {
		b, ok := f.files[n]
		if !ok {
			return fmt.Errorf("scp: %s: No such file or directory", n)
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// release builds the files of a release: the platform archive (with a
// tray when tray is set), optionally the app zip, and SHA256SUMS.
func release(t *testing.T, tray, app bool) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	base := "hpctun-" + newVer + "-" + runtime.GOOS + "-" + runtime.GOARCH
	members := map[string]string{base + "/hpctun": "new hpctun", base + "/README.md": "readme"}
	if tray {
		members[base+"/hpctun-tray"] = "new tray"
	}
	if runtime.GOOS == "windows" {
		files[base+".zip"] = zipBytes(t, map[string]string{base + "/hpctun.exe": "new hpctun"})
	} else {
		files[base+".tar.gz"] = tarGzBytes(t, members)
	}
	if app {
		files["hpctun-tray-"+newVer+"-darwin-universal.zip"] = zipBytes(t, map[string]string{
			"hpctun-tray.app/Contents/MacOS/hpctun":      "new hpctun",
			"hpctun-tray.app/Contents/MacOS/hpctun-tray": "new tray",
			"hpctun-tray.app/Contents/Info.plist":        "plist",
		})
	}
	var sums strings.Builder
	for n, b := range files {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(b), n)
	}
	files["SHA256SUMS"] = []byte(sums.String())
	files[selfupdate.SigName] = selfupdatetest.Sign(files["SHA256SUMS"])
	return files
}

func tarGzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b strings.Builder
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for n, body := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return []byte(b.String())
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b strings.Builder
	zw := zip.NewWriter(&b)
	for n, body := range files {
		h := &zip.FileHeader{Name: n}
		h.SetMode(0o755)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(body))
	}
	zw.Close()
	return []byte(b.String())
}

// updateApp is a testApp whose hpctun is exe (created with "old hpctun"),
// running oldVer, reading releases from src. The fake runner answers the
// new binary's --version with newVer and pgrep with trayRunning.
func updateApp(t *testing.T, exe string, src *fakeSource, trayRunning bool) (*app, *strings.Builder) {
	t.Helper()
	a, out, _ := testApp(t, "")
	a.goos = runtime.GOOS
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exe); err != nil {
		os.WriteFile(exe, []byte("old hpctun"), 0o755)
	}
	a.executable = func() (string, error) { return exe, nil }
	a.ver = func() string { return oldVer }
	a.updates = src
	a.updateKey = selfupdatetest.PublicKey()
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		switch {
		case len(c.Args) == 1 && c.Args[0] == "--version":
			return sshctl.Result{Stdout: []byte("hpctun version " + newVer + "\n")}, nil
		case c.Name == "pgrep":
			if trayRunning {
				return sshctl.Result{Stdout: []byte("123\n")}, nil
			}
			return sshctl.Result{ExitCode: 1}, nil
		}
		t.Errorf("unexpected command: %s", c.Line())
		return sshctl.Result{ExitCode: 255}, nil
	}}
	var b strings.Builder
	a.stdout = &b
	_ = out
	return a, &b
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "hpctun.exe"
	}
	return "hpctun"
}

func TestSelfUpdateUpToDate(t *testing.T) {
	src := &fakeSource{latest: oldVer}
	a, out := updateApp(t, filepath.Join(t.TempDir(), "bin", exeName()), src, false)
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "hpctun "+oldVer+" is up to date\n" || len(src.fetched) != 0 {
		t.Fatalf("out %q fetched %v", out, src.fetched)
	}
}

func TestSelfUpdateCheck(t *testing.T) {
	src := &fakeSource{latest: newVer}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, out := updateApp(t, exe, src, false)
	if err := runCmd(a, "self-update", "--check"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "updating "+oldVer+" -> "+newVer+" (would replace hpctun)\n" || len(src.fetched) != 0 {
		t.Fatalf("out %q fetched %v", out, src.fetched)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("--check changed hpctun")
	}
}

func TestSelfUpdateDevBuild(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	a.ver = func() string { return "v0-dev" }
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || err.Error() != "this is a development build; use --force to replace it with "+newVer {
		t.Fatalf("err %v", err)
	}
	if err := runCmd(a, "self-update", "--force"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
}

func TestSelfUpdatePlain(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, out := updateApp(t, exe, src, false)
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
	want := "updating " + oldVer + " -> " + newVer + "\nhpctun updated to " + newVer + "\n"
	if out.String() != want {
		t.Fatalf("out %q", out)
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hpctun-update-") {
			t.Fatalf("temp dir left: %s", e.Name())
		}
	}
}

func TestSelfUpdateDowngrade(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	a, out := updateApp(t, filepath.Join(t.TempDir(), "bin", exeName()), src, false)
	a.ver = func() string { return "v0.20261009-1234567" }
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "downgrading v0.20261009-1234567 -> "+newVer+" (the release was rolled back)\n") ||
		!strings.HasSuffix(out.String(), "hpctun downgraded to "+newVer+"\n") {
		t.Fatalf("out %q", out)
	}
}

// Only the Linux archive carries hpctun-tray (CI on Linux runs this).
func TestSelfUpdateWithTray(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only the Linux archive has a tray")
	}
	src := &fakeSource{latest: newVer, files: release(t, true, false)}
	dir := t.TempDir()
	exe := filepath.Join(dir, "hpctun")
	os.WriteFile(exe, []byte("old hpctun"), 0o755)
	os.WriteFile(filepath.Join(dir, "hpctun-tray"), []byte("old tray"), 0o755)
	a, out := updateApp(t, exe, src, true)
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "hpctun-tray")); string(b) != "new tray" {
		t.Fatalf("tray = %q", b)
	}
	if !strings.HasSuffix(out.String(), "hpctun-tray is still running the old version; quit and reopen it\n") {
		t.Fatalf("out %q", out)
	}
}

// The usual Mac install: hpctun on PATH is a symlink into the app.
func TestSelfUpdateApp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bundle := filepath.Join(root, "Applications", "hpctun-tray.app")
	inApp := filepath.Join(bundle, "Contents", "MacOS", "hpctun")
	os.MkdirAll(filepath.Dir(inApp), 0o755)
	os.WriteFile(inApp, []byte("old hpctun"), 0o755)
	link := filepath.Join(root, "bin", "hpctun")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(inApp, link); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{latest: newVer, files: release(t, false, true)}
	a, out := updateApp(t, link, src, false)
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(bundle, "Contents", "MacOS", "hpctun-tray")); string(b) != "new tray" {
		t.Fatalf("tray in app = %q", b)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the PATH symlink was replaced")
	}
	if b, _ := os.ReadFile(link); string(b) != "new hpctun" {
		t.Fatalf("hpctun via link = %q", b)
	}
	if _, err := os.Stat(bundle + ".old"); err == nil {
		t.Fatal(".old left")
	}
	if !strings.HasSuffix(out.String(), "hpctun updated to "+newVer+"\n") {
		t.Fatalf("out %q", out)
	}
}

func TestSelfUpdateAppWithoutAppZip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS layout")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	inApp := filepath.Join(root, "hpctun-tray.app", "Contents", "MacOS", "hpctun")
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	a, _ := updateApp(t, inApp, src, false)
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || err.Error() != "this release ("+newVer+") has no hpctun-tray.app; update the app by downloading it again" {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(inApp); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
}

func TestSelfUpdateChecksumMismatch(t *testing.T) {
	files := release(t, false, false)
	for n := range files {
		if n != "SHA256SUMS" && n != selfupdate.SigName {
			files[n] = append(files[n], 'x')
		}
	}
	src := &fakeSource{latest: newVer, files: files}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	err := runCmd(a, "self-update")
	if err == nil || !strings.HasPrefix(err.Error(), "checksum mismatch for hpctun-"+newVer) {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
}

func TestSelfUpdateRefusesUnsignedRelease(t *testing.T) {
	files := release(t, false, false)
	delete(files, selfupdate.SigName)
	src := &fakeSource{latest: newVer, files: files}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "hpctun installs only signed releases") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
	if len(src.fetched) != 1 {
		t.Fatalf("fetched %v after the signature was missing", src.fetched)
	}
}

// The attack signing stops: archive and SHA256SUMS replaced together, so
// the checksums match but the signature does not.
func TestSelfUpdateRefusesBadSignature(t *testing.T) {
	files := release(t, false, false)
	var sums strings.Builder
	for n := range files {
		if strings.HasPrefix(n, "hpctun-") {
			files[n] = append(files[n], 'x')
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(files[n]), n)
		}
	}
	files["SHA256SUMS"] = []byte(sums.String()) // the old signature stays
	src := &fakeSource{latest: newVer, files: files}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "not signed with hpctun's release key") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
	if len(src.fetched) != 1 {
		t.Fatalf("downloaded the archive of a badly signed release: %v", src.fetched)
	}
}

func TestSelfUpdateRefusesWithoutReleaseKey(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	a.updateKey = selfupdate.PlaceholderKey
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "no release signing key") || len(src.fetched) != 0 {
		t.Fatalf("err %v, fetched %v", err, src.fetched)
	}
}

func TestSelfUpdateWrongVersionInArchive(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	a.run = &sshctltest.Fake{Handler: func(c sshctltest.Call) (sshctl.Result, error) {
		return sshctl.Result{Stdout: []byte("hpctun version v0.20200101-0000000\n")}, nil
	}}
	err := runCmd(a, "self-update")
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
}

// TestSelfUpdateLatestError: a failed check is exit 1 with the source's
// message and no ssh advice, since nothing here uses ssh.
func TestSelfUpdateLatestError(t *testing.T) {
	src := &fakeSource{err: errors.New("can't check for updates at https://example.org/releases/latest/download/VERSION: dial tcp: no such host")}
	a, _ := updateApp(t, filepath.Join(t.TempDir(), "bin", exeName()), src, false)
	err := runCmd(a, "self-update")
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "no such host") ||
		strings.Contains(err.Error(), "hpctun login") || strings.Contains(err.Error(), "ssh-config key") {
		t.Fatalf("err %v", err)
	}
}

func TestSelfUpdateUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a non-root Unix user")
	}
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	root, _ := filepath.EvalSymlinks(t.TempDir()) // the error names the resolved path
	dir := filepath.Join(root, "bin")
	exe := filepath.Join(dir, "hpctun")
	a, _ := updateApp(t, exe, src, false)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	err := runCmd(a, "self-update")
	if err == nil || !strings.HasPrefix(err.Error(), "can't replace "+exe+": ") {
		t.Fatalf("err %v", err)
	}
	if len(src.fetched) != 0 {
		t.Fatal("downloaded before finding the directory unwritable")
	}
}

func TestSelfUpdateDefaultSource(t *testing.T) {
	a, _, _ := testApp(t, "")
	src, ok := a.updateSource().(selfupdate.GitHubSource)
	if !ok || src.Base != selfupdate.DefaultReleases {
		t.Fatalf("source %#v", a.updateSource())
	}
}

// An app zip without hpctun-tray inside is refused before anything changes.
func TestSelfUpdateAppMissingTray(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS layout")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bundle := filepath.Join(root, "hpctun-tray.app")
	inApp := filepath.Join(bundle, "Contents", "MacOS", "hpctun")
	files := release(t, false, false)
	name := "hpctun-tray-" + newVer + "-darwin-universal.zip"
	files[name] = zipBytes(t, map[string]string{"hpctun-tray.app/Contents/MacOS/hpctun": "new hpctun"})
	files["SHA256SUMS"] = append(files["SHA256SUMS"], []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(files[name]), name))...)
	files[selfupdate.SigName] = selfupdatetest.Sign(files["SHA256SUMS"])
	a, _ := updateApp(t, inApp, &fakeSource{latest: newVer, files: files}, false)
	err := runCmd(a, "self-update")
	if err == nil || !strings.Contains(err.Error(), "hpctun-tray") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(inApp); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
}

// Only the Linux archive carries hpctun-tray. Elsewhere a tray next to
// hpctun (a build from a checkout) is left alone and hpctun is updated.
func TestSelfUpdateTrayNextToHpctunOutsideLinux(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		t.Skip("needs a platform whose archive has no tray")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	exe := filepath.Join(dir, "hpctun")
	os.WriteFile(exe, []byte("old hpctun"), 0o755)
	os.WriteFile(filepath.Join(dir, "hpctun-tray"), []byte("my tray"), 0o755)
	a, _ := updateApp(t, exe, &fakeSource{latest: newVer, files: release(t, false, false)}, false)
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "hpctun-tray")); string(b) != "my tray" {
		t.Fatalf("tray = %q", b)
	}
}

func updateDoc(t *testing.T, out string) jsondoc.Update {
	t.Helper()
	var d jsondoc.Update
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not an update document: %v\n%s", err, out)
	}
	return d
}

func TestSelfUpdateCheckJSON(t *testing.T) {
	src := &fakeSource{latest: newVer}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, out := updateApp(t, exe, src, false)
	if err := runCmd(a, "self-update", "--check", "--json"); err != nil {
		t.Fatal(err)
	}
	d := updateDoc(t, out.String())
	real, _ := filepath.EvalSymlinks(exe)
	if d.Version != 1 || d.Running != oldVer || d.Latest != newVer || d.Action != "update" ||
		d.Blocked != nil || d.Updated || d.Install.Kind != "plain" || d.Install.Target != real || d.Install.Tray != nil {
		t.Fatalf("doc %+v", d)
	}
	// The probe left nothing behind.
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries", len(entries))
	}
}

func TestSelfUpdateCheckJSONBlocked(t *testing.T) {
	src := &fakeSource{latest: newVer}
	a, out := updateApp(t, filepath.Join(t.TempDir(), "bin", exeName()), src, false)
	a.ver = func() string { return "v0-dev" }
	if err := runCmd(a, "self-update", "--check", "--json"); err != nil {
		t.Fatal(err)
	}
	d := updateDoc(t, out.String())
	if d.Action != "update" || d.Blocked == nil || !strings.Contains(*d.Blocked, "development build") {
		t.Fatalf("doc %+v", d)
	}
}

func TestSelfUpdateCheckJSONUnwritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix permissions and a non-root user")
	}
	src := &fakeSource{latest: newVer}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, out := updateApp(t, exe, src, false)
	os.Chmod(filepath.Dir(exe), 0o555)
	t.Cleanup(func() { os.Chmod(filepath.Dir(exe), 0o755) })
	if err := runCmd(a, "self-update", "--check", "--json"); err != nil {
		t.Fatal(err)
	}
	if d := updateDoc(t, out.String()); d.Blocked == nil || !strings.Contains(*d.Blocked, "can't write") {
		t.Fatalf("doc %+v", d)
	}
}

// --check changes nothing: it no longer cleans an earlier update's .old,
// which a running update may still need.
func TestSelfUpdateCheckLeavesOld(t *testing.T) {
	src := &fakeSource{latest: newVer}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	os.WriteFile(exe+".old", []byte("backup"), 0o755)
	if err := runCmd(a, "self-update", "--check"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exe + ".old"); err != nil {
		t.Fatalf(".old removed by --check: %v", err)
	}
}

func TestSelfUpdateJSONUpdated(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, out := updateApp(t, exe, src, false)
	if err := runCmd(a, "self-update", "--json", "--expect", newVer); err != nil {
		t.Fatal(err)
	}
	// Only the document: no progress lines on stdout.
	if d := updateDoc(t, out.String()); !d.Updated || d.Latest != newVer || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("out %q", out)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
}

func TestSelfUpdateExpect(t *testing.T) {
	for _, tc := range []struct {
		name, running, latest, expect string
		wantErr                       bool
	}{
		{"newer release since the offer", oldVer, newVer, "v0.20261001-1111111", true},
		{"rolled back", "v0.20261009-1234567", newVer, newVer, true},
		{"already there", newVer, newVer, newVer, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{latest: tc.latest, files: release(t, false, false)}
			exe := filepath.Join(t.TempDir(), "bin", exeName())
			a, out := updateApp(t, exe, src, false)
			a.ver = func() string { return tc.running }
			err := runCmd(a, "self-update", "--json", "--expect", tc.expect)
			if tc.wantErr {
				if err == nil || !strings.Contains(out.String(), `"error"`) {
					t.Fatalf("err %v out %q", err, out)
				}
				if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
					t.Fatal("changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d := updateDoc(t, out.String()); d.Updated || d.Action != "up_to_date" {
				t.Fatalf("doc %+v", d)
			}
		})
	}
}

func TestSelfUpdateJSONError(t *testing.T) {
	src := &fakeSource{latest: newVer, err: errors.New("no network")}
	a, out := updateApp(t, filepath.Join(t.TempDir(), "bin", exeName()), src, false)
	err := runCmd(a, "self-update", "--check", "--json")
	var e jsondoc.Error
	if err == nil || json.Unmarshal([]byte(out.String()), &e) != nil || !strings.Contains(e.Error.Message, "no network") {
		t.Fatalf("err %v out %q", err, out)
	}
}

func TestSelfUpdateLockHeld(t *testing.T) {
	src := &fakeSource{latest: newVer, files: release(t, false, false)}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, src, false)
	real, _ := filepath.EvalSymlinks(exe)
	f, err := os.OpenFile(filepath.Join(filepath.Dir(real), "."+filepath.Base(real)+".update-lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if busy, err := filelock.TryLock(f); busy || err != nil {
		t.Fatal(busy, err)
	}
	err = runCmd(a, "self-update")
	if err == nil || !strings.Contains(err.Error(), "another update of") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed while locked")
	}
	f.Close()
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

// A source whose download never finishes stands in for a trickling
// connection: the preparation limit ends the update, changing nothing.
type stuckSource struct{ fakeSource }

func (s *stuckSource) Fetch(ctx context.Context, _ string, _ []string, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestSelfUpdatePrepLimit(t *testing.T) {
	src := &stuckSource{fakeSource{latest: newVer}}
	exe := filepath.Join(t.TempDir(), "bin", exeName())
	a, _ := updateApp(t, exe, &src.fakeSource, false)
	a.updates = src
	a.updatePrep = 50 * time.Millisecond
	err := runCmd(a, "self-update")
	if err == nil || !strings.Contains(err.Error(), "took longer than") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatal("changed")
	}
}

// windowsRelease is a Windows release whose zip carries both programs.
func windowsRelease(t *testing.T) map[string][]byte {
	t.Helper()
	base := "hpctun-" + newVer + "-windows-" + runtime.GOARCH
	files := map[string][]byte{base + ".zip": zipBytes(t, map[string]string{
		base + "/hpctun.exe": "new hpctun", base + "/hpctun-tray.exe": "new tray",
	})}
	var sums strings.Builder
	for n, b := range files {
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(b), n)
	}
	files["SHA256SUMS"] = []byte(sums.String())
	files[selfupdate.SigName] = selfupdatetest.Sign(files["SHA256SUMS"])
	return files
}

func windowsInstall(t *testing.T) (dir, exe, tray string) {
	dir, _ = filepath.EvalSymlinks(t.TempDir())
	exe, tray = filepath.Join(dir, "hpctun.exe"), filepath.Join(dir, "hpctun-tray.exe")
	os.WriteFile(exe, []byte("old hpctun"), 0o755)
	os.WriteFile(tray, []byte("old tray"), 0o755)
	return
}

func TestSelfUpdateWindowsTray(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("no Windows release for this arch")
	}
	_, exe, tray := windowsInstall(t)
	a, _ := updateApp(t, exe, &fakeSource{latest: newVer, files: windowsRelease(t)}, false)
	a.goos = "windows"
	if err := runCmd(a, "self-update"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tray); string(b) != "new tray" {
		t.Fatalf("tray = %q", b)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
	// Windows swaps rename the running programs aside; the next update's
	// CleanOld removes these.
	for _, p := range []string{exe + ".old", tray + ".old"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestSelfUpdateWindowsUndoesTrayWhenHpctunFails(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("no Windows release for this arch")
	}
	_, exe, tray := windowsInstall(t)
	a, _ := updateApp(t, exe, &fakeSource{latest: newVer, files: windowsRelease(t)}, false)
	a.goos = "windows"
	// hpctun's swap fails after the tray's succeeded.
	a.replace = func(old, newPath string, windows bool) error {
		if old == exe {
			return errors.New("can't replace " + old + ": access denied")
		}
		return selfupdate.Replace(old, newPath, windows)
	}
	if err := runCmd(a, "self-update"); err == nil {
		t.Fatal("update succeeded")
	}
	if b, _ := os.ReadFile(tray); string(b) != "old tray" {
		t.Fatalf("tray = %q, want the old one back", b)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old hpctun" {
		t.Fatalf("hpctun = %q", b)
	}
}

func TestSelfUpdateWindowsTrayHint(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("no Windows release for this arch")
	}
	const hint = "If hpctun-tray is running, quit it and open it again"
	for _, asJSON := range []bool{false, true} {
		_, exe, _ := windowsInstall(t)
		a, out := updateApp(t, exe, &fakeSource{latest: newVer, files: windowsRelease(t)}, false)
		a.goos = "windows"
		args := []string{"self-update"}
		if asJSON {
			args = append(args, "--json")
		}
		if err := runCmd(a, args...); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out.String(), hint); got == asJSON {
			t.Errorf("json=%v: hint present = %v in %q", asJSON, got, out)
		}
	}
}

func TestSelfUpdateWindowsTrayReplaceFails(t *testing.T) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("no Windows release for this arch")
	}
	_, exe, _ := windowsInstall(t)
	a, _ := updateApp(t, exe, &fakeSource{latest: newVer, files: windowsRelease(t)}, false)
	a.goos = "windows"
	a.replace = func(old, _ string, _ bool) error { return errors.New("Access is denied") }
	err := runCmd(a, "self-update")
	if err == nil || !strings.Contains(err.Error(), "Access is denied") || !strings.Contains(err.Error(), "quit hpctun-tray, open it again") {
		t.Fatalf("err %v", err)
	}
}
