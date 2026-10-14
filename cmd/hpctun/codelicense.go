package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hpctun/internal/state"
)

// codeLicenseFile, in the local state directory, records that the user
// accepted the VS Code Server License Terms on this computer.
const codeLicenseFile = "vscode-license-accepted"

const codeLicenseNotice = `hpctun code runs Microsoft's VS Code tunnel in your Slurm job and starts it with
--accept-server-license-terms, which accepts these for you:
  VS Code Server License Terms: https://aka.ms/vscode-server-license
  Microsoft Privacy Statement:  https://privacy.microsoft.com/en-US/privacystatement
VS Code on this computer reaches the job through Microsoft's dev tunnels service,
so your files, terminals and edits pass through it, and the tunnel's sign-in is
kept in your home directory on the cluster. Check your institution's rules before
working with restricted data (for example health records or export-controlled
data) this way.
`

// codeConsent asks once per computer, before the first hpctun code starts
// anything, whether the user accepts the VS Code Server License Terms, and
// records a yes. Without a terminal and without a recorded answer it
// refuses: accepting terms is the user's decision, not a default.
func (a *app) codeConsent() error {
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, codeLicenseFile)
	if b, err := os.ReadFile(path); err == nil && strings.HasPrefix(string(b), "accepted") {
		return nil
	}
	if !a.interactive() {
		return usage(errors.New("hpctun code needs your answer to the VS Code Server License Terms once on this computer; run `hpctun code` in a terminal first"))
	}
	fmt.Fprint(a.stdout, codeLicenseNotice)
	ok, err := a.asker("", false).Confirm("Accept these terms and continue?", "Asked once on this computer; hpctun remembers your answer.", false)
	if err != nil {
		return failed(err)
	}
	if !ok {
		return failed(errors.New("hpctun code cannot start VS Code's tunnel without the VS Code Server License Terms; nothing was started"))
	}
	if err := state.EnsureDir(dir); err != nil {
		return failed(err)
	}
	if err := os.WriteFile(path, []byte("accepted "+time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		return failed(err)
	}
	return nil
}
