package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"hpctun/internal/vscode"
)

func (a *app) findCode() (string, bool) {
	if a.codeBin != nil {
		return a.codeBin()
	}
	return vscode.Find(a.goos, a.getenv, exec.LookPath, fileExists)
}

// codePreflight finds VS Code and its Remote - Tunnels extension, offering
// to install the extension, before anything is submitted.
func (a *app) codePreflight(ctx context.Context) (string, error) {
	bin, ok := a.findCode()
	if !ok {
		return "", failed(errors.New("VS Code not found: install it from https://code.visualstudio.com, or use --no-open"))
	}
	res, err := a.runner().Run(ctx, bin, "--list-extensions")
	if err != nil {
		return "", failed(err)
	}
	if vscode.HasExtensionID(string(res.Stdout), vscode.RemoteTunnels) {
		return bin, nil
	}
	if !a.interactive() {
		return "", failed(fmt.Errorf("VS Code's Remote - Tunnels extension (%s) is not installed; install it, or use --no-open", vscode.RemoteTunnels))
	}
	defer a.spin.Pause()()
	yes, err := a.asker("", false).Confirm("Install VS Code's Remote - Tunnels extension now?",
		"hpctun code opens VS Code on the job's node through it ("+vscode.RemoteTunnels+").", true)
	if err != nil || !yes {
		return "", failed(errors.New("hpctun code needs VS Code's Remote - Tunnels extension; install it, or use --no-open"))
	}
	if err := a.runInteractive([]string{bin, "--install-extension", vscode.RemoteTunnels}, nil); err != nil {
		return "", failed(err)
	}
	return bin, nil
}

// openCode opens folder on the tunnel name. A window already on that
// folder (one that reconnects by itself once the tunnel is back) is
// focused instead, even with --new-window, which only keeps VS Code from
// replacing whatever window was last active (checked in VS Code 1.140).
// Without a folder it opens an empty window on the tunnel, where VS Code's
// own Open Folder runs on the node; VS Code cannot match such a window, so
// each one is new.
func (a *app) openCode(ctx context.Context, bin, name, folder string) error {
	args := []string{"--new-window", "--remote", "tunnel+" + name}
	if folder != "" {
		args = []string{"--new-window", "--folder-uri", vscode.TunnelFolderURI(name, folder)}
	}
	res, err := a.runner().Run(ctx, bin, args...)
	if err != nil {
		return failed(err)
	}
	if res.ExitCode != 0 {
		return failed(fmt.Errorf("VS Code exited with status %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr))))
	}
	fmt.Fprintf(a.stdout, "opened VS Code on tunnel %s\n", name)
	return nil
}
