package sshkey

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testKey = `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey me@lap "q" $HOME (tun)`

// install runs InstallScript as the gateway would, in a temp HOME, with key
// on stdin. wrap, when set, is the login shell's -c (tcsh).
func install(t *testing.T, home, key string, wrap ...string) {
	t.Helper()
	argv := []string{"sh", "-c", InstallScript}
	if len(wrap) > 0 {
		argv = append(wrap, "sh -c '"+InstallScript+"'")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	cmd.Stdin = strings.NewReader(key + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestInstallScriptHasNoSingleQuote(t *testing.T) {
	if strings.Contains(InstallScript, "'") {
		t.Fatal("InstallScript must not contain ' (it is wrapped in sh -c '...')")
	}
}

func TestInstallScriptCreatesWithModes(t *testing.T) {
	home := t.TempDir()
	install(t, home, testKey)
	dir := filepath.Join(home, ".ssh")
	ak := filepath.Join(dir, "authorized_keys")
	b, err := os.ReadFile(ak)
	if err != nil || string(b) != testKey+"\n" {
		t.Fatalf("authorized_keys %q, %v", b, err)
	}
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(ak)
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("modes %v %v", di.Mode().Perm(), fi.Mode().Perm())
	}
}

func TestInstallScriptKeepsLinesAndDoesNotDuplicate(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	os.MkdirAll(dir, 0o775)
	ak := filepath.Join(dir, "authorized_keys")
	os.WriteFile(ak, []byte("ssh-rsa AAAAold other@host"), 0o664) // no final newline
	install(t, home, testKey)
	install(t, home, testKey)
	b, _ := os.ReadFile(ak)
	if want := "ssh-rsa AAAAold other@host\n" + testKey + "\n"; string(b) != want {
		t.Fatalf("got %q\nwant %q", b, want)
	}
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(ak)
	if di.Mode().Perm()&0o022 != 0 || fi.Mode().Perm()&0o022 != 0 {
		t.Fatalf("still group/world writable: %v %v", di.Mode().Perm(), fi.Mode().Perm())
	}
}

// TestInstallScriptUnderTcsh: a tcsh login shell hands sh -c '...' through
// intact (some clusters have tcsh users).
func TestInstallScriptUnderTcsh(t *testing.T) {
	if _, err := os.Stat("/bin/tcsh"); err != nil {
		t.Skip("no /bin/tcsh")
	}
	home := t.TempDir()
	install(t, home, testKey, "/bin/tcsh", "-c")
	if b, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys")); string(b) != testKey+"\n" {
		t.Fatalf("got %q", b)
	}
}
