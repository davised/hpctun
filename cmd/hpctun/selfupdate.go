package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"hpctun/internal/selfupdate"
)

func newSelfUpdateCmd(a *app) *cobra.Command {
	var check, force bool
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update hpctun (and hpctun-tray) to the latest release",
		Long: "Update hpctun to the latest release on GitHub, after checking that the release's\n" +
			"SHA256SUMS is signed with hpctun's release key and that the download matches it.\n" +
			"With hpctun-tray next to hpctun, or inside hpctun-tray.app on a Mac, the tray\n" +
			"is updated too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runSelfUpdate(cmd.Context(), a, check, force) },
	}
	cmd.Flags().BoolVar(&check, "check", false, "say what would happen and change nothing")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall the same version, or replace a development build")
	return cmd
}

func (a *app) updateSource() selfupdate.Source {
	if a.updates != nil {
		return a.updates
	}
	return selfupdate.GitHubSource{Base: selfupdate.DefaultReleases}
}

func (a *app) runningVersion() string {
	if a.ver != nil {
		return a.ver()
	}
	return version()
}

func runSelfUpdate(ctx context.Context, a *app, check, force bool) error {
	exeFn := a.executable
	if exeFn == nil {
		exeFn = os.Executable
	}
	exe, err := exeFn()
	if err != nil {
		return failed(fmt.Errorf("can't find hpctun's own path: %w", err))
	}
	inst, err := selfupdate.Detect(exe)
	if err != nil {
		return failed(fmt.Errorf("can't find hpctun's own path: %w", err))
	}
	// Only the Linux archive carries hpctun-tray; elsewhere a tray next to
	// hpctun is the user's own build, left alone.
	if inst.Kind == selfupdate.PlainWithTray && a.goos != "linux" {
		inst.Kind, inst.Tray = selfupdate.Plain, ""
	}
	selfupdate.CleanOld(inst.Target())

	src := a.updateSource()
	latest, err := src.Latest(ctx)
	if err != nil {
		return failed(err)
	}
	running := a.runningVersion()
	act, err := selfupdate.Decide(running, latest, force)
	if errors.Is(err, selfupdate.ErrDevBuild) {
		return failed(fmt.Errorf("this is a development build; use --force to replace it with %s", latest))
	}
	if err != nil {
		return failed(err)
	}
	if act == selfupdate.UpToDate {
		fmt.Fprintf(a.stdout, "hpctun %s is up to date\n", running)
		return nil
	}
	line := fmt.Sprintf("updating %s -> %s", running, latest)
	if act == selfupdate.Downgrade {
		line = fmt.Sprintf("downgrading %s -> %s (the release was rolled back)", running, latest)
	}
	if check {
		fmt.Fprintf(a.stdout, "%s (would replace %s)\n", line, inst.Describe())
		return nil
	}
	// Without a usable key nothing can be checked, so nothing is downloaded.
	pub, err := selfupdate.ParsePublicKey(cmp.Or(a.updateKey, selfupdate.ReleasePublicKey))
	if err != nil {
		return failed(err)
	}
	archive, members, err := inst.Asset(latest, a.goos, runtime.GOARCH)
	if err != nil {
		return failed(err)
	}

	// The work dir sits next to the install so the swap is a rename; an
	// unwritable directory shows up here, before anything is downloaded.
	target := inst.Target()
	work, err := os.MkdirTemp(filepath.Dir(target), ".hpctun-update-")
	if err != nil {
		return failed(fmt.Errorf("can't replace %s: %w", target, err))
	}
	defer os.RemoveAll(work)
	dl := filepath.Join(work, "download")
	if err := os.Mkdir(dl, 0o700); err != nil {
		return failed(err)
	}

	fmt.Fprintln(a.stdout, line)
	// The signature is checked before any line of SHA256SUMS is used, and
	// before the archive is downloaded: nothing below trusts a list the
	// release key did not sign.
	if err := src.Fetch(ctx, latest, []string{"SHA256SUMS", selfupdate.SigName}, dl); err != nil {
		return failed(fmt.Errorf("%w; hpctun installs only signed releases, so nothing was changed", err))
	}
	sums, err := os.ReadFile(filepath.Join(dl, "SHA256SUMS"))
	if err != nil {
		return failed(err)
	}
	sig, err := os.ReadFile(filepath.Join(dl, selfupdate.SigName))
	if err != nil {
		return failed(err)
	}
	if err := selfupdate.VerifySums(pub, sums, sig); err != nil {
		return failed(fmt.Errorf("release %s: %w; nothing was changed", latest, err))
	}
	// Older releases have no app: look before fetching it.
	if inst.Kind == selfupdate.App && !selfupdate.Listed(sums, archive) {
		return failed(fmt.Errorf("this release (%s) has no %s; update the app by downloading it again", latest, selfupdate.AppName))
	}
	if err := src.Fetch(ctx, latest, []string{archive}, dl); err != nil {
		return failed(err)
	}
	if err := verifyArchive(sums, dl, archive); err != nil {
		return failed(err)
	}

	var newExe string
	newTray := filepath.Join(work, "hpctun-tray")
	if inst.Kind == selfupdate.App {
		err = selfupdate.ExtractTree(filepath.Join(dl, archive), selfupdate.AppName, work)
		newExe = filepath.Join(work, selfupdate.AppName, "Contents", "MacOS", "hpctun")
	} else {
		err = selfupdate.Extract(filepath.Join(dl, archive), members, work)
		newExe = filepath.Join(work, filepath.Base(members[0]))
	}
	if err != nil {
		return failed(fmt.Errorf("%w; nothing was changed", err))
	}
	if inst.Kind == selfupdate.App {
		tray := filepath.Join(work, selfupdate.AppName, "Contents", "MacOS", "hpctun-tray")
		if fi, err := os.Stat(tray); err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			return failed(fmt.Errorf("%s has no runnable hpctun-tray; nothing was changed", selfupdate.AppName))
		}
	}
	res, err := a.runner().Run(ctx, newExe, "--version")
	fields := strings.Fields(string(res.Stdout))
	if err != nil || res.ExitCode != 0 || len(fields) == 0 || fields[len(fields)-1] != latest {
		return failed(fmt.Errorf("the downloaded hpctun does not run as %s here (%q, %v); nothing was changed", latest, strings.TrimSpace(string(res.Stdout)), err))
	}

	switch inst.Kind {
	case selfupdate.App:
		err = selfupdate.Replace(inst.Bundle, filepath.Join(work, selfupdate.AppName), false)
	default:
		err = selfupdate.Replace(inst.Exe, newExe, a.windows())
		if err == nil && inst.Kind == selfupdate.PlainWithTray {
			if terr := selfupdate.Replace(inst.Tray, newTray, false); terr != nil {
				return failed(fmt.Errorf("hpctun is updated to %s, but hpctun-tray is not: %w", latest, terr))
			}
		}
	}
	if err != nil {
		return failed(err)
	}
	verb := "updated"
	if act == selfupdate.Downgrade {
		verb = "downgraded"
	}
	fmt.Fprintf(a.stdout, "hpctun %s to %s\n", verb, latest)
	if inst.Kind != selfupdate.Plain && !a.windows() {
		if r, err := a.runner().Run(ctx, "pgrep", "-x", "hpctun-tray"); err == nil && r.ExitCode == 0 {
			fmt.Fprintln(a.stdout, "hpctun-tray is still running the old version; quit and reopen it")
		}
	}
	return nil
}

// verifyArchive checks dir/archive against sums, the SHA256SUMS whose
// signature was already checked.
func verifyArchive(sums []byte, dir, archive string) error {
	f, err := os.Open(filepath.Join(dir, archive))
	if err != nil {
		return err
	}
	defer f.Close()
	return selfupdate.CheckSum(sums, archive, f)
}
