package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/change"
	"hpctun/internal/sshctl"
	"hpctun/internal/vscode"
)

// machineSettings is VS Code's remote machine settings file, relative to
// the home on the cluster (ssh commands start there).
const machineSettings = ".vscode-server/data/Machine/settings.json"

// readScript prints the file $1, or exits 3 when there is none.
const readScript = `if [ -e "$1" ]; then exec cat -- "$1"; else exit 3; fi`

// writeScript replaces machineSettings with stdin when its sha256 is still
// $1 ("absent": no file), keeping a dated backup, and prints the backup's
// path. Exit 3: the file changed since hpctun read it; 1: any other failure.
// An existing file's mode is kept (the temp file starts as its copy). Like
// readScript it is one line without a single quote or "!", so a tcsh login
// shell passes it through sh -c '...' intact.
const writeScript = `d=.vscode-server/data/Machine; f=$d/settings.json; ` +
	`if [ -e "$f" ]; then ` +
	`if command -v sha256sum >/dev/null 2>&1; then now=$(sha256sum <"$f"); else now=$(shasum -a 256 <"$f"); fi; ` +
	`now=${now%% *}; else now=absent; fi; ` +
	`[ "$now" = "$1" ] || exit 3; ` +
	`mkdir -p "$d" || exit 1; t=$f.hpctun-$$; ` +
	`if [ -e "$f" ]; then b=$f.bak-$(date +%Y%m%d-%H%M%S); cp -p "$f" "$b" && cp -p "$f" "$t" || exit 1; echo "$b"; fi; ` +
	`cat >"$t" || { rm -f "$t"; exit 1; }; ` +
	`mv -f "$t" "$f" || { rm -f "$t"; exit 1; }`

func newVSCodeSettingsCmd(a *app) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "vscode-settings",
		Short: "Add recommended VS Code settings for the cluster to your remote machine settings",
		Long: "Add the VS Code settings that spare the cluster's shared file systems (search limits, file\n" +
			"watcher excludes) to ~/" + machineSettings + " on the cluster, through the\n" +
			"gateway. Settings you already have are never changed. hpctun shows the changes and asks\n" +
			"before writing, and keeps a dated backup.\n\n" +
			"A site can add, change or remove settings with a JSONC file on the cluster, named in the\n" +
			"config:\n\n  [vscode]\n  settings = \"/path/on/the/cluster/vscode.jsonc\"\n  defaults = false   # only the site's settings\n\n" +
			"In that file a null removes one of hpctun's settings, or one entry of an object setting.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runVSCodeSettings(cmd.Context(), dryRun, yes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the changes and stop")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply without asking")
	return cmd
}

func (a *app) runVSCodeSettings(ctx context.Context, dryRun, yes bool) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	if err := requireGateway(cfg); err != nil {
		return err
	}
	gw := cfg.Gateway
	var sources []map[string]any
	if cfg.VSCode == nil || cfg.VSCode.Defaults {
		sources = append(sources, vscode.Defaults())
	}
	if cfg.VSCode != nil && cfg.VSCode.Settings != "" {
		path := cfg.VSCode.Settings
		text, found, err := a.readRemote(ctx, gw, strings.TrimPrefix(path, "~/"), path)
		if err != nil {
			return err
		}
		if !found {
			return failed(fmt.Errorf("%s: no such file on %s (from [vscode] settings in %s)", path, gw, a.tilde(cfg.Path)))
		}
		site, err := vscode.ParseObject(text)
		if err != nil {
			return failed(fmt.Errorf("%s: %w", path, err))
		}
		sources = append(sources, site)
	}
	add := vscode.Layer(sources...)

	display := "~/" + machineSettings
	old, exists, err := a.readRemote(ctx, gw, machineSettings, display)
	if err != nil {
		return err
	}
	text, added, skipped, err := vscode.AddSettings(old, add)
	if err != nil {
		return failed(fmt.Errorf("%s: %w; nothing changed", display, err))
	}
	for _, s := range skipped {
		fmt.Fprintf(a.stdout, "kept your %s: it is not an object, so hpctun adds nothing to it\n", s)
	}
	switch {
	case len(added) == 0 && len(skipped) > 0:
		fmt.Fprintf(a.stdout, "%s on %s: nothing else to add\n", display, gw)
		return nil
	case len(added) == 0:
		fmt.Fprintf(a.stdout, "%s on %s already has every recommended setting\n", display, gw)
		return nil
	}
	fmt.Fprint(a.stdout, change.Change{Display: display, Old: old, Exists: exists, New: text}.Diff())
	if dryRun {
		return nil
	}
	if !yes {
		if !a.interactive() {
			return failed(errors.New("settings not changed; run again in a terminal to approve them, or with --yes"))
		}
		ok, err := a.asker("", false).Confirm("Write these settings on "+gw+"?", "", false)
		if err != nil || !ok {
			return failed(errors.New("settings not changed"))
		}
	}
	want := "absent"
	if exists {
		h := sha256.Sum256([]byte(old))
		want = hex.EncodeToString(h[:])
	}
	e := a.stream
	if e == nil {
		e = a.streamExec
	}
	out, code, err := e(ctx, a.remoteArgv(gw, writeScript, want), text, a.stderr)
	switch {
	case err != nil:
		return failed(err)
	case code == 3:
		return failed(fmt.Errorf("%s changed while hpctun was running; run it again", display))
	case code == 255:
		return failed(unreachable(gw, ""))
	case code != 0:
		return failed(fmt.Errorf("writing %s on %s failed (exit status %d)", display, gw, code))
	}
	if b := strings.TrimSpace(out); b != "" {
		fmt.Fprintf(a.stdout, "backup: ~/%s\n", b)
	}
	fmt.Fprintf(a.stdout, "wrote %s on %s\n", display, gw)
	return nil
}

// remoteArgv runs script with sh on gw, with args (already quoted).
func (a *app) remoteArgv(gw, script string, args ...string) []string {
	cmd := "sh -c '" + script + "' _"
	for _, s := range args {
		cmd += " " + s
	}
	return []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "--", gw, cmd}
}

// readRemote returns the file at path on gw (relative to the home unless
// absolute), and whether it exists. Errors name the file as display.
func (a *app) readRemote(ctx context.Context, gw, path, display string) (string, bool, error) {
	argv := a.remoteArgv(gw, readScript, sshctl.ShellQuote(path))
	res, err := a.runner().Run(ctx, argv[0], argv[1:]...)
	switch {
	case err != nil:
		return "", false, failed(err)
	case res.ExitCode == 3:
		return "", false, nil
	case res.ExitCode == 255:
		return "", false, failed(unreachable(gw, string(res.Stderr)))
	case res.ExitCode != 0:
		return "", false, failed(fmt.Errorf("cannot read %s on %s: %s", display, gw, strings.TrimSpace(string(res.Stderr))))
	}
	return string(res.Stdout), true, nil
}

func unreachable(gw, stderr string) error {
	msg := fmt.Sprintf("cannot connect to %s without a prompt", gw)
	if s := strings.TrimSpace(stderr); s != "" {
		msg += " (" + s + ")"
	}
	return errors.New(msg + "; run `hpctun login` first, or set up key login with `hpctun ssh-config key`")
}
