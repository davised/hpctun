package sshkey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hpctun/internal/sshctl"
	"hpctun/internal/sshsetup"
)

// AppleSSHAdd is macOS's own ssh-add, the one that knows the Keychain.
const AppleSSHAdd = "/usr/bin/ssh-add"

// Flow sets up key login to Host. Each step says what it does and skips
// itself when there is nothing to do, so Run is safe to repeat.
type Flow struct {
	Key      string // private key path; the public key is Key+".pub"
	Show     string // how messages name Key, e.g. ~/.ssh/id_ed25519
	Host     string
	Gateway  bool   // Host is the configured gateway, whose home the nodes share
	Comment  string // ssh-keygen -C for a new key
	Keychain bool   // macOS: /usr/bin/ssh-add --apple-use-keychain
	Windows  bool   // Windows: no Control* options; the agent-service note
	Runner   sshctl.Runner
	Interact func(argv []string, stdin io.Reader) error // stdin nil: the terminal's
	Out      io.Writer
}

// Run makes sure Host accepts the key: create it, load it into the agent,
// test it, and install it if needed.
func (f *Flow) Run(ctx context.Context) error {
	if err := f.ensureKey(); err != nil {
		return err
	}
	noPass := f.noPassphrase(ctx)
	if noPass {
		fmt.Fprintf(f.Out, "note: %s has no passphrase, so anyone who copies the file can use it; add one with `ssh-keygen -p -f %s`\n", f.Show, f.Show)
	}
	agent, err := f.ensureAgent(ctx)
	if err != nil {
		return err
	}
	canTest := agent || noPass // otherwise a no-prompt login cannot use the key
	if canTest && f.Accepted(ctx) {
		// Possibly through another key from the user's ssh config (-i adds
		// to IdentityFile lines, it does not replace them): either way no
		// password is needed, so say only that.
		fmt.Fprintf(f.Out, "%s already lets you log in with a key.\n", f.Host)
		return nil
	}
	if err := f.install(); err != nil {
		return err
	}
	if !canTest {
		fmt.Fprintln(f.Out, "installed; ssh will ask for the key's passphrase until an agent holds it")
		return nil
	}
	if !f.Accepted(ctx) {
		return fmt.Errorf("%s does not accept the key: the server may not allow keys, or your home directory or ~/.ssh there is writable by others", f.Host)
	}
	fmt.Fprintf(f.Out, "Done: %s accepts your key.", f.Host)
	if f.Gateway {
		fmt.Fprint(f.Out, " Nodes and the transfer host share your home directory, so they do too.")
	}
	fmt.Fprintln(f.Out)
	return nil
}

// ensureKey reuses Key, or has ssh-keygen create it (asking the user for a
// passphrase itself).
func (f *Flow) ensureKey() error {
	if _, err := os.Stat(f.Key); err == nil {
		if _, err := os.Stat(f.Key + ".pub"); err != nil {
			return fmt.Errorf("%s.pub is missing; recreate it with `ssh-keygen -y -f %s > %s.pub`", f.Show, f.Show, f.Show)
		}
		fmt.Fprintf(f.Out, "using %s\n", f.Show)
		return nil
	}
	fmt.Fprintf(f.Out, "Creating %s. ssh-keygen asks for a passphrase: it protects the key if this computer is lost.\n", f.Show)
	if err := os.MkdirAll(filepath.Dir(f.Key), 0o700); err != nil {
		return err
	}
	if err := f.Interact([]string{"ssh-keygen", "-t", "ed25519", "-f", f.Key, "-C", f.Comment}, nil); err != nil {
		return fmt.Errorf("could not create %s: %w", f.Show, err)
	}
	if _, err := os.Stat(f.Key + ".pub"); err != nil {
		return fmt.Errorf("could not create %s: %w", f.Show, err)
	}
	return nil
}

// noPassphrase reports whether the key opens with an empty passphrase.
func (f *Flow) noPassphrase(ctx context.Context) bool {
	res, err := f.Runner.Run(ctx, "ssh-keygen", "-y", "-P", "", "-f", f.Key)
	return err == nil && res.ExitCode == 0
}

// ensureAgent loads the key into the ssh agent unless it is there. It
// reports whether an agent holds the key; no agent (or no ssh-add) is a
// note, not an error.
func (f *Flow) ensureAgent(ctx context.Context) (bool, error) {
	res, err := f.Runner.Run(ctx, "ssh-add", "-l")
	if err != nil || res.ExitCode == 2 {
		if f.Windows {
			fmt.Fprintf(f.Out, "note: %s\n", sshsetup.AgentNote)
		} else {
			fmt.Fprintln(f.Out, "note: no ssh agent is running, so ssh will ask for the key's passphrase at each login")
		}
		return false, nil
	}
	if fp := f.fingerprint(ctx); fp != "" && strings.Contains(string(res.Stdout), fp) {
		return true, nil
	}
	argv := []string{"ssh-add"}
	if f.Keychain {
		// Apple's ssh-add, whatever comes first on PATH: an OpenSSH from
		// Homebrew rejects --apple-use-keychain.
		argv = []string{AppleSSHAdd, "--apple-use-keychain"}
	}
	fmt.Fprintf(f.Out, "Adding %s to the ssh agent; ssh-add asks for its passphrase once.\n", f.Show)
	if err := f.Interact(append(argv, f.Key), nil); err != nil {
		return false, fmt.Errorf("could not add the key to the ssh agent: %w", err)
	}
	return true, nil
}

// fingerprint is the key's SHA256 fingerprint, or "".
func (f *Flow) fingerprint(ctx context.Context) string {
	res, err := f.Runner.Run(ctx, "ssh-keygen", "-lf", f.Key+".pub")
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	if fields := strings.Fields(string(res.Stdout)); len(fields) > 1 {
		return fields[1]
	}
	return ""
}

// Accepted reports whether Host lets this key, and nothing else, log in
// without a prompt. No shared connection takes part.
func (f *Flow) Accepted(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := []string{"-o", "BatchMode=yes", "-o", "PreferredAuthentications=publickey", "-o", "IdentitiesOnly=yes", "-i", f.Key}
	if !f.Windows {
		args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
	}
	args = append(args, "-o", "ConnectTimeout=10", "--", f.Host, "true")
	res, err := f.Runner.Run(ctx, "ssh", args...)
	return err == nil && res.ExitCode == 0
}

// install appends the public key on Host through one interactive login.
func (f *Flow) install() error {
	b, err := os.ReadFile(f.Key + ".pub")
	if err != nil {
		return err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return errors.New(f.Show + ".pub is empty")
	}
	fmt.Fprintf(f.Out, "Installing the key on %s; ssh asks for your password there once.\n", f.Host)
	argv := []string{"ssh"}
	if !f.Windows {
		argv = append(argv, "-o", "ControlMaster=no")
	}
	// ConnectTimeout bounds only reaching the host, not the password prompt.
	argv = append(argv, "-o", "ConnectTimeout=10", "--", f.Host, "sh -c '"+InstallScript+"'")
	if err := f.Interact(argv, strings.NewReader(line+"\n")); err != nil {
		return fmt.Errorf("could not install the key on %s: %w", f.Host, err)
	}
	return nil
}
