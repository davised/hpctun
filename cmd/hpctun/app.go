package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"hpctun/internal/askpass"
	"hpctun/internal/browser"
	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/proc"
	"hpctun/internal/prompt"
	"hpctun/internal/selfupdate"
	"hpctun/internal/slurmjob"
	"hpctun/internal/sshctl"
	"hpctun/internal/sshsetup"
	"hpctun/internal/state"
	"hpctun/internal/tunnel"
)

// app holds the global flags and process environment shared by all commands.
type app struct {
	configPath string
	verbose    bool
	askpass    bool // --askpass: ssh asks in a dialog, not on the terminal
	getenv     func(string) string
	stdout     io.Writer
	stderr     io.Writer
	goos       string // the platform; runtime.GOOS except in tests

	// Set by root's PersistentPreRunE for migrate, which needs both but
	// keeps a simple (ssh bool) signature: configFlagChanged is whether
	// --config was set on this command line (config.Locate's flagPath
	// bypasses the config directory, so migrate must too), and
	// migrateSkipHint is whether the running command is itself `hpctun
	// ssh-config sync`, which needs no hint to run itself.
	configFlagChanged bool
	migrateSkipHint   bool

	// Test seams; nil means the real thing.
	run        sshctl.Runner                              // processes hpctun runs and waits for
	exec       func(argv []string) error                  // replaces hpctun with another program
	interact   func(argv []string, stdin io.Reader) error // programs that prompt on the terminal
	ask        prompt.Asker                               // questions
	tty        *bool                                      // whether hpctun may prompt
	executable func() (string, error)                     // hpctun's own path, for the askpass script; nil: os.Executable
	stream     slurmjob.Exec                              // remote scripts with stdin and live stderr; nil: streamExec
	codeBin    func() (string, bool)                      // VS Code's launcher; nil: vscode.Find
	spinTo     io.Writer                                  // where hpctun code's spinner draws; nil: the terminal, if any
	dialog     askpass.Runner                             // askpass dialogs; nil: runDialog
	lookPath   func(string) (string, error)               // nil: exec.LookPath
	start      func(argv []string) error                  // starts a browser without waiting; nil: browser.StartDetached
	ver        func() string                              // hpctun's version; nil: version()
	updates    selfupdate.Source                          // release source; nil: GitHub releases
	updateKey  string                                     // base64 release public key; "": selfupdate.ReleasePublicKey
	ident      func(pid int) (proc.Info, bool)            // a pid's program and start time; nil: proc.Lookup
	kill       func(pid int) error                        // ends a just-started ssh; nil: tunnel.KillTERM
	terminate  func(pid int, started time.Time) error     // ends a tunnel's ssh, verified on its handle; nil: proc.Terminate

	spin *spinner // hpctun code's progress line while it runs; nil: none
}

func newApp() *app {
	return &app{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr, exec: execProcess, goos: runtime.GOOS}
}

// windows reports whether hpctun runs on Windows, where OpenSSH has no
// ControlMaster.
func (a *app) windows() bool { return a.goos == "windows" }

// mac reports whether hpctun runs on macOS, where ssh can keep key passphrases
// in the Keychain.
func (a *app) mac() bool { return a.goos == "darwin" }

// home is the user's home directory as ssh sees it: USERPROFILE on Windows,
// even when HOME is set (Git Bash sets it; Windows OpenSSH ignores it).
func (a *app) home() string {
	if a.windows() {
		return a.getenv("USERPROFILE")
	}
	return a.getenv("HOME")
}

// sshPaths are the ssh files under the user's home directory.
func (a *app) sshPaths() sshsetup.Paths { return sshsetup.DefaultPaths(a.home()) }

// tunPath is hpctun's own path, or "hpctun" when it cannot be found.
func (a *app) tunPath() string {
	f := a.executable
	if f == nil {
		f = os.Executable
	}
	p, err := f()
	if err != nil {
		return "hpctun"
	}
	return p
}

// tilde shows path with the home directory as ~.
func (a *app) tilde(path string) string {
	home := a.home()
	if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// asker is how hpctun asks questions: full-screen for a run of questions under
// heading, inline for one question after printed output.
func (a *app) asker(heading string, full bool) prompt.Asker {
	if a.ask != nil {
		return a.ask
	}
	return &prompt.HuhAsker{Heading: heading, FullScreen: full}
}

var errNoGateway = errors.New("no gateway configured; run `hpctun init`")

func requireGateway(cfg *config.Config) error {
	if cfg.Gateway == "" {
		return usage(errNoGateway)
	}
	return nil
}

// browsers is how hpctun browse finds and starts browsers.
func (a *app) browsers() browser.Env {
	start, look := a.start, a.lookPath
	if start == nil {
		start = browser.StartDetached
	}
	if look == nil {
		look = exec.LookPath
	}
	return browser.Env{GOOS: a.goos, Getenv: a.getenv, Home: a.home(), Stat: os.Stat, LookPath: look, Runner: a.runner(), Start: start,
		InUse: browser.FileInUse, ReadFile: os.ReadFile, WriteFile: func(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }}
}

func (a *app) runner() sshctl.Runner {
	if a.run != nil {
		return a.run
	}
	var log *slog.Logger
	if a.verbose {
		log = slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return sshctl.ExecRunner{Log: log, Env: a.askpassEnv()}
}

// askpassOn reports whether ssh should ask through hpctun's dialog: with
// --askpass or HPCTUN_ASKPASS=1, and never on Windows, where hpctun opens
// no connection that could prompt this way.
func (a *app) askpassOn() bool {
	return !a.windows() && (a.askpass || a.getenv("HPCTUN_ASKPASS") == "1")
}

// askpassEnv is the environment that makes ssh ask through `hpctun askpass`,
// or nil when askpass is off.
// SSH_ASKPASS cannot carry arguments, so it names a wrapper script in the
// state directory. When the script cannot be written, ssh asks on the
// terminal as usual, after a warning.
func (a *app) askpassEnv() []string {
	if !a.askpassOn() {
		return nil
	}
	path, err := a.writeAskpassScript()
	if err != nil {
		fmt.Fprintf(a.stderr, "warning: --askpass: %v\n", err)
		return nil
	}
	return []string{"SSH_ASKPASS=" + path, "SSH_ASKPASS_REQUIRE=force"}
}

func (a *app) writeAskpassScript() (string, error) {
	dir, err := a.stateDir()
	if err != nil {
		return "", err
	}
	if err := state.EnsureDir(dir); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "askpass.sh")
	want := askpass.Script(a.tunPath())
	// Reused only as a regular file with the right content and mode, never
	// through a symlink.
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() == 0o700 {
		if have, err := os.ReadFile(path); err == nil && string(have) == want {
			return path, nil
		}
	}
	// A new file under a random name (os.CreateTemp opens it with O_EXCL,
	// so it is never an existing file or link), renamed over askpass.sh:
	// rename replaces a symlink rather than writing through it.
	f, err := os.CreateTemp(dir, ".askpass-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name()) // nothing left to remove after the rename
	if _, err := f.WriteString(want); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Chmod(0o700); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(f.Name(), path)
}

// slurm is the Slurm client for gateway.
func (a *app) slurm(gateway string) slurmjob.Client {
	e := a.stream
	if e == nil {
		e = a.streamExec
	}
	return slurmjob.Client{Exec: e, Gateway: gateway, Multiplexing: !a.windows()}
}

// configFile locates hpctun's config. It also requires a home directory:
// without one, ~/.ssh would resolve against the current directory.
func (a *app) configFile() (string, error) {
	if a.home() == "" {
		v := "HOME"
		if a.windows() {
			v = "USERPROFILE"
		}
		return "", usage(fmt.Errorf("cannot find your home directory: %s is not set", v))
	}
	path, err := config.Locate(a.configPath, a.getenv, a.goos)
	if err != nil {
		return "", usage(err)
	}
	return path, nil
}

func (a *app) loadConfig() (*config.Config, error) {
	path, err := a.configFile()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, usage(err)
	}
	return cfg, nil
}

func (a *app) stateDir() (string, error) {
	dir, err := state.Dir(a.getenv, a.goos)
	if err != nil {
		return "", usage(err)
	}
	// Refused before anything trusts it: the control sockets and
	// askpass.sh live there.
	if err := state.CheckDir(dir); err != nil {
		return "", failed(err)
	}
	return dir, nil
}

// interactive reports whether hpctun may prompt: stdin and stdout are terminals.
func (a *app) interactive() bool {
	if a.tty != nil {
		return *a.tty
	}
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// resolveGateway resolves the gateway tunnels go through.
func (a *app) resolveGateway(ctx context.Context, cfg *config.Config, stateDir string) (*gateway.Gateway, error) {
	g, err := gateway.Resolve(ctx, a.runner(), cfg.TunnelDest(), stateDir)
	if errors.Is(err, gateway.ErrSocketPathTooLong) {
		return nil, usage(err)
	}
	if err != nil {
		return nil, failed(err)
	}
	return g, nil
}

// openEnv locks the state directory and builds a tunnel.Env for up/down.
// Env.Gateway is nil when no gateway is configured, which down allows: state
// entries carry the gateway they were opened through. Call the returned func
// to release the lock.
func (a *app) openEnv(ctx context.Context, cfg *config.Config) (*tunnel.Env, func(), error) {
	dir, err := a.stateDir()
	if err != nil {
		return nil, nil, err
	}
	var g *gateway.Gateway
	switch {
	case cfg.TunnelDest() == "":
	case a.windows():
		// No master to resolve: each tunnel signs in by itself.
		g = &gateway.Gateway{Dest: cfg.TunnelDest(), Mode: gateway.ModeStandalone}
	default:
		g, err = a.resolveGateway(ctx, cfg, dir)
		if err != nil {
			return nil, nil, err
		}
	}
	store, err := state.Open(ctx, dir, func() { io.WriteString(a.stderr, "waiting for another hpctun command to finish...\n") })
	if err != nil {
		return nil, nil, failed(err)
	}
	st, err := store.Load()
	if err != nil {
		store.Close()
		return nil, nil, failed(err)
	}
	r := a.runner()
	env := &tunnel.Env{
		Runner: r, Gateway: g, Store: store, State: st, Config: cfg, LogDir: dir,
		GatewayFor: func(ctx context.Context, dest string) (*gateway.Gateway, error) {
			return gateway.Resolve(ctx, r, dest, dir)
		},
		Kill: a.killFn(),
		Now:  time.Now,
		Wait: 100 * time.Millisecond,

		Standalone:  a.windows(),
		Ident:       a.identFn(),
		Terminate:   a.terminateFn(),
		OpenTimeout: 30 * time.Second,
	}
	return env, func() { store.Close() }, nil
}

// identFn reports a pid's program and start time, for standalone ownership.
func (a *app) identFn() func(int) (proc.Info, bool) {
	if a.ident != nil {
		return a.ident
	}
	return proc.Lookup
}

// killFn ends a tunnel's ssh without verifying it, for an ssh just started
// that never became a tunnel.
func (a *app) killFn() func(int) error {
	if a.kill != nil {
		return a.kill
	}
	return tunnel.KillTERM
}

// terminateFn ends a tunnel's ssh, verified on the handle it ends it
// through, for standalone down.
func (a *app) terminateFn() func(int, time.Time) error {
	if a.terminate != nil {
		return a.terminate
	}
	return proc.Terminate
}
