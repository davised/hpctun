package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hpctun/internal/filelock"
	"hpctun/internal/jsondoc"
	"hpctun/internal/selfupdate"
)

func newSelfUpdateCmd(a *app) *cobra.Command {
	var check, force, asJSON bool
	var expect string
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update hpctun (and hpctun-tray) to the latest release",
		Long: "Update hpctun to the latest release on GitHub, after checking that the release's\n" +
			"SHA256SUMS is signed with hpctun's release key and that the download matches it.\n" +
			"With hpctun-tray next to hpctun, or inside hpctun-tray.app on a Mac, the tray\n" +
			"is updated too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return jsonErrors(a, asJSON, func() error {
				return runSelfUpdate(cmd.Context(), a, updateOpts{check: check, force: force, json: asJSON, expect: expect})
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "say what would happen and change nothing")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall the same version, or replace a development build")
	cmd.Flags().BoolVar(&asJSON, "json", false, jsonFlagUsage)
	cmd.Flags().StringVar(&expect, "expect", "", "update only to this version, and only if it is newer than hpctun (succeeds without change if already there; for hpctun-tray)")
	return cmd
}

type updateOpts struct {
	check, force, json bool
	expect             string
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

func runSelfUpdate(ctx context.Context, a *app, o updateOpts) error {
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
	// Only the Linux and Windows archives carry hpctun-tray; on macOS a tray
	// next to hpctun is the user's own build, left alone.
	if inst.Kind == selfupdate.PlainWithTray && a.goos != "linux" && a.goos != "windows" {
		inst.Kind, inst.Tray = selfupdate.Plain, ""
	}
	// Progress lines go to out; with --json the one document goes to stdout.
	out := a.stdout
	if o.json {
		out = io.Discard
	}

	src := a.updateSource()
	latest, err := src.Latest(ctx)
	if err != nil {
		return failed(err)
	}
	running := a.runningVersion()
	doc := jsondoc.Update{Version: jsondoc.Version, Running: running, Latest: latest, Install: installDoc(inst)}
	act, err := selfupdate.Decide(running, latest, o.force)
	switch {
	case errors.Is(err, selfupdate.ErrDevBuild):
		blocked := fmt.Sprintf("this is a development build; use --force to replace it with %s", latest)
		if !o.check || !o.json {
			return failed(errors.New(blocked))
		}
		doc.Action, doc.Blocked = "update", &blocked
		return writeJSON(a.stdout, doc)
	case err != nil:
		return failed(err)
	}
	doc.Action = map[selfupdate.Action]string{selfupdate.UpToDate: "up_to_date", selfupdate.Update: "update", selfupdate.Downgrade: "downgrade"}[act]
	if o.expect != "" && (latest != o.expect || act == selfupdate.Downgrade) {
		return failed(fmt.Errorf("the latest release is now %s; nothing was changed", latest))
	}
	if act == selfupdate.UpToDate {
		if o.json {
			return writeJSON(a.stdout, doc)
		}
		fmt.Fprintf(a.stdout, "hpctun %s is up to date\n", running)
		return nil
	}
	line := fmt.Sprintf("updating %s -> %s", running, latest)
	if act == selfupdate.Downgrade {
		line = fmt.Sprintf("downgrading %s -> %s (the release was rolled back)", running, latest)
	}
	if o.check {
		if o.json {
			if reason := probeWritable(inst.Target()); reason != "" {
				doc.Blocked = &reason
			}
			return writeJSON(a.stdout, doc)
		}
		fmt.Fprintf(a.stdout, "%s (would replace %s)\n", line, inst.Describe())
		return nil
	}
	// One update of an install at a time: a tray click and a terminal
	// update would otherwise delete each other's .old. The system drops
	// the lock when hpctun exits, crash included.
	// The lock file is hidden so it does not show up next to the install.
	lockTarget := inst.Target()
	lf, err := os.OpenFile(filepath.Join(filepath.Dir(lockTarget), "."+filepath.Base(lockTarget)+".update-lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return failed(fmt.Errorf("can't replace %s: %w", inst.Target(), err))
	}
	defer lf.Close()
	if busy, err := filelock.TryLock(lf); err != nil {
		return failed(fmt.Errorf("can't lock %s: %w", lf.Name(), err))
	} else if busy {
		return failed(fmt.Errorf("another update of %s is running; try again when it has finished", inst.Target()))
	}
	selfupdate.CleanOld(inst.Target())
	if inst.Kind == selfupdate.PlainWithTray {
		selfupdate.CleanOld(inst.Tray)
	}
	// A trickling connection must not hold the lock, and the install, for
	// ever; nothing is changed until the download is checked.
	prep := cmp.Or(a.updatePrep, 10*time.Minute)
	pctx, cancel := context.WithTimeout(ctx, prep)
	defer cancel()
	tooSlow := func() error {
		return failed(fmt.Errorf("downloading the update took longer than %v; nothing was changed", prep))
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

	fmt.Fprintln(out, line)
	// The signature is checked before any line of SHA256SUMS is used, and
	// before the archive is downloaded: nothing below trusts a list the
	// release key did not sign.
	if err := src.Fetch(pctx, latest, []string{"SHA256SUMS", selfupdate.SigName}, dl); err != nil {
		if pctx.Err() == context.DeadlineExceeded {
			return tooSlow()
		}
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
	if err := src.Fetch(pctx, latest, []string{archive}, dl); err != nil {
		if pctx.Err() == context.DeadlineExceeded {
			return tooSlow()
		}
		return failed(err)
	}
	if err := verifyArchive(sums, dl, archive); err != nil {
		return failed(err)
	}

	var newExe, newTray string
	if inst.Kind == selfupdate.App {
		err = selfupdate.ExtractTree(filepath.Join(dl, archive), selfupdate.AppName, work)
		newExe = filepath.Join(work, selfupdate.AppName, "Contents", "MacOS", "hpctun")
	} else {
		err = selfupdate.Extract(filepath.Join(dl, archive), members, work)
		newExe = filepath.Join(work, filepath.Base(members[0]))
		if len(members) > 1 {
			newTray = filepath.Join(work, filepath.Base(members[1]))
		}
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
	res, err := a.runner().Run(pctx, newExe, "--version")
	if pctx.Err() == context.DeadlineExceeded {
		return tooSlow()
	}
	fields := strings.Fields(string(res.Stdout))
	if err != nil || res.ExitCode != 0 || len(fields) == 0 || fields[len(fields)-1] != latest {
		return failed(fmt.Errorf("the downloaded hpctun does not run as %s here (%q, %v); nothing was changed", latest, strings.TrimSpace(string(res.Stdout)), err))
	}

	replace := a.replace
	if replace == nil {
		replace = selfupdate.Replace
	}
	stop := holdSignals()
	switch inst.Kind {
	case selfupdate.App:
		err = replace(inst.Bundle, filepath.Join(work, selfupdate.AppName), false)
	case selfupdate.PlainWithTray:
		// The tray first: if hpctun's swap then fails, hpctun is still old,
		// so the next update does both again.
		if err = replace(inst.Tray, newTray, a.windows()); err != nil {
			if a.windows() {
				// A tray still running from an earlier update sits in its
				// .old, which Windows will not let us clear.
				err = fmt.Errorf("%w; quit hpctun-tray, open it again, and try the update again", err)
			}
			break
		}
		if err = replace(inst.Exe, newExe, a.windows()); err != nil && a.windows() {
			// On Windows the old tray now sits in its .old, in use; a retry
			// could not swap the tray again, so put it back now.
			if uerr := selfupdate.Undo(inst.Tray); uerr != nil {
				err = fmt.Errorf("%w (and %v)", err, uerr)
			}
		}
	default:
		err = replace(inst.Exe, newExe, a.windows())
	}
	stop()
	if err != nil {
		return failed(err)
	}
	if o.json {
		doc.Updated = true
		return writeJSON(a.stdout, doc)
	}
	verb := "updated"
	if act == selfupdate.Downgrade {
		verb = "downgraded"
	}
	fmt.Fprintf(a.stdout, "hpctun %s to %s\n", verb, latest)
	if inst.Kind == selfupdate.PlainWithTray && a.windows() {
		fmt.Fprintln(a.stdout, "If hpctun-tray is running, quit it and open it again to use the new version.")
	}
	if inst.Kind != selfupdate.Plain && !a.windows() {
		if r, err := a.runner().Run(ctx, "pgrep", "-x", "hpctun-tray"); err == nil && r.ExitCode == 0 {
			fmt.Fprintln(a.stdout, "hpctun-tray is still running the old version; quit and reopen it")
		}
	}
	return nil
}

// probeWritable says why hpctun cannot create a file next to target, or "".
// It finds some blockers, not all: a swap can still fail on the target's own
// permissions. It never touches the install or its .old.
func probeWritable(target string) string {
	f, err := os.CreateTemp(filepath.Dir(target), ".hpctun-probe-")
	if err != nil {
		return fmt.Sprintf("hpctun can't write to %s, so it can't update itself there", filepath.Dir(target))
	}
	f.Close()
	os.Remove(f.Name())
	return ""
}

func installDoc(inst selfupdate.Install) jsondoc.UpdateInstall {
	d := jsondoc.UpdateInstall{Kind: map[selfupdate.Kind]string{selfupdate.Plain: "plain", selfupdate.PlainWithTray: "plain_with_tray", selfupdate.App: "app"}[inst.Kind], Target: inst.Target()}
	switch inst.Kind {
	case selfupdate.PlainWithTray:
		d.Tray = &inst.Tray
	case selfupdate.App:
		t := filepath.Join(inst.Bundle, "Contents", "MacOS", "hpctun-tray")
		d.Tray = &t
	}
	return d
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
