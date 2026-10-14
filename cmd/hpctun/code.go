package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"hpctun/internal/config"
	"hpctun/internal/prompt"
	"hpctun/internal/slurmjob"
	"hpctun/internal/state"
	"hpctun/internal/vscode"
)

const (
	codeJob     = "hpctun-code"             // the Slurm job name of hpctun code's job
	codeJobs    = codeJob + "," + legacyJob // names to find: hpctun's job is reused, not orphaned
	legacyJob   = "tun-code"                // the name before the rename; drop at the first public release (tun-5kj)
	codeFolders = "code-folders"            // state-dir file: folders hpctun code opened, newest first
	codeLock    = "code.lock"               // state-dir lock around finding or starting the job
)

// How long hpctun code waits for the tunnel once the job runs, and how long
// once the node shows a device code (the user may take a while to sign in).
var tunnelWait, tunnelWaitAfterCode = 5 * time.Minute, 20 * time.Minute

type codeFlags struct {
	cpus            int
	time            string
	node, partition string
	account         string
	noOpen          bool
	status, stop    bool
	extra           []string // after --
}

// resources reports whether any option shapes a new job.
func (f codeFlags) resources() bool {
	return f.cpus != 0 || f.time != "" || f.node != "" || f.partition != "" || f.account != "" || len(f.extra) > 0
}

func newCodeCmd(a *app) *cobra.Command {
	var f codeFlags
	cmd := &cobra.Command{
		Use:   "code [path] [-- sbatch args]",
		Short: "Open VS Code on your Slurm job on a compute node",
		Long: "hpctun code makes sure you have a Slurm job for VS Code, starting one if\n" +
			"needed, and opens VS Code on its node through VS Code's Remote - Tunnels.\n" +
			"path is a folder on the cluster, relative to your home directory there, or\n" +
			"absolute (a node's own /scratch too). Without one, hpctun code asks which\n" +
			"folder to open: a recent one, your home, another, or an empty window. The\n" +
			"first time a job lands on a node, you sign in once there with your ONID. The\n" +
			"job keeps running when VS Code closes, until its time limit or\n" +
			"`hpctun code --stop`; after that, run hpctun code again.",
		ValidArgsFunction: a.completeCodeFolder,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, extra, err := codeArgs(cmd, args)
			if err != nil {
				return usage(err)
			}
			f.extra = extra
			return runCode(cmd.Context(), a, f, path)
		},
	}
	fl := cmd.Flags()
	fl.IntVarP(&f.cpus, "cpus", "c", 0, "CPUs for a new job (default from [code], else 4)")
	fl.StringVarP(&f.time, "time", "t", "", "time limit for a new job, as sbatch --time (default from [code], else 24:00:00)")
	fl.StringVarP(&f.node, "node", "w", "", "run a new job on this node (sbatch -w)")
	fl.StringVarP(&f.partition, "partition", "p", "", "partition for a new job (sbatch -p)")
	fl.StringVarP(&f.account, "account", "A", "", "Slurm account for a new job (sbatch -A; default: one the partition takes)")
	fl.BoolVar(&f.noOpen, "no-open", false, "start the job but do not open VS Code")
	fl.BoolVar(&f.status, "status", false, "show your job; never start one")
	fl.BoolVar(&f.stop, "stop", false, "cancel your job")
	cmd.MarkFlagsMutuallyExclusive("status", "stop")
	_ = cmd.RegisterFlagCompletionFunc("node", a.completeCodeNodes)
	_ = cmd.RegisterFlagCompletionFunc("partition", a.completeCodePartitions)
	return cmd
}

// codeArgs splits args into the path (before --) and sbatch arguments.
func codeArgs(cmd *cobra.Command, args []string) (string, []string, error) {
	pos, extra := args, []string(nil)
	if d := cmd.ArgsLenAtDash(); d >= 0 {
		pos, extra = args[:d], args[d:]
	}
	if len(pos) > 1 {
		return "", nil, fmt.Errorf("one folder at most, got %d", len(pos))
	}
	if len(pos) == 1 {
		return pos[0], extra, nil
	}
	return "", extra, nil
}

func runCode(ctx context.Context, a *app, f codeFlags, path string) error {
	cfg, err := a.loadConfig()
	if err != nil {
		return err
	}
	c, err := codeConfig(cfg)
	if err != nil {
		return err
	}
	if n := len(c.Obsolete); n > 0 {
		verb, pron := "are", "them"
		if n == 1 {
			verb, pron = "is", "it"
		}
		fmt.Fprintf(a.stderr, "warning: [code] %s %s no longer used: hpctun code connects through VS Code's Remote - Tunnels; remove %s from %s\n",
			strings.Join(c.Obsolete, ", "), verb, pron, a.tilde(cfg.Path))
	}
	if (f.status || f.stop) && (path != "" || f.resources()) {
		return usage(errors.New("--status and --stop take no folder or job options"))
	}
	switch {
	case f.status:
		return runCodeStatus(ctx, a, cfg)
	case f.stop:
		return runCodeStop(ctx, a, cfg)
	}
	return runCodeStart(ctx, a, cfg, c, f, path)
}

func codeConfig(cfg *config.Config) (*config.Code, error) {
	if cfg.Code == nil {
		return nil, usage(fmt.Errorf("%s has no [code] table; add a line `[code]` (the defaults suit most people)", cfg.Path))
	}
	return cfg.Code, nil
}

// codeParams is the job hpctun code asks for: [code] with the flags on top.
func codeParams(c *config.Code, f codeFlags) (slurmjob.Params, error) {
	if f.cpus < 0 {
		return slurmjob.Params{}, errors.New("--cpus must be 1 or more")
	}
	limit := cmp.Or(f.time, c.Time)
	d, err := slurmjob.ParseTime(limit)
	if err != nil {
		return slurmjob.Params{}, fmt.Errorf("--time: %w", err)
	}
	extra := append(slices.Clone(c.Sbatch), f.extra...)
	account := f.account
	if account == "" && slices.ContainsFunc(extra, isAccountArg) {
		account = "-"
	}
	return slurmjob.Params{
		Mode: slurmjob.ModeEnsure, Name: codeJobs, CPUs: cmp.Or(f.cpus, c.CPUs), Time: limit,
		Deadline: slurmjob.Deadline(d, c.StartWithin), Partition: cmp.Or(f.partition, c.Partition),
		Node: f.node, Account: account, Extra: extra,
		Script: slurmjob.NodeScript(c.ServerDirs),
	}, nil
}

// isAccountArg reports whether an sbatch argument names the account
// (-A x, -Ax, --account x, --account=x), so the ensure script leaves it be.
func isAccountArg(a string) bool {
	return a == "-A" || a == "--account" || strings.HasPrefix(a, "--account=") || (strings.HasPrefix(a, "-A") && !strings.HasPrefix(a, "--"))
}

// codeLogin makes sure the gateway lets the user in without a prompt: hpctun
// code makes several connections, and without a key each would ask for a
// password and a second factor. On non-Windows the probe leaves out any
// shared connection (ControlMaster=no, ControlPath=none), as
// sshkey.Flow.Accepted does, so a live master from `hpctun login` cannot make a
// keyless user pass.
func (a *app) codeLogin(ctx context.Context, gw string) error {
	ok := func() bool {
		args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10"}
		if !a.windows() {
			args = append(args, "-o", "ControlMaster=no", "-o", "ControlPath=none")
		}
		args = append(args, "--", gw, "true")
		res, err := a.runner().Run(ctx, "ssh", args...)
		return err == nil && res.ExitCode == 0
	}
	if ok() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.interactive() {
		resume := a.spin.Pause()
		err := a.offerKey(ctx, gw)
		resume()
		if err != nil {
			return failed(err)
		}
		if ok() {
			return nil
		}
	}
	return usage(fmt.Errorf("%s asks for a password, and hpctun code needs key login: run `hpctun ssh-config key`", gw))
}

// clusterPath turns the folder argument into a path the gateway's shell
// can cd to. A path under this computer's home directory means the shell
// expanded ~ here, so it becomes relative to the cluster home; so does ~/.
func (a *app) clusterPath(arg string) (string, string) {
	p, note := arg, ""
	if h := a.home(); h != "" {
		sep := string(filepath.Separator)
		switch {
		case p == h:
			p, note = ".", "using your home directory on the cluster"
		case strings.HasPrefix(p, h+sep):
			p = filepath.ToSlash(p[len(h)+1:])
			note = fmt.Sprintf("%s is a path on this computer; using ~/%s on the cluster", arg, p)
		}
	}
	switch {
	case p == "~":
		p = "."
	case strings.HasPrefix(p, "~/"):
		p = p[2:]
	}
	return p, note
}

// resolveCodeFolder resolves the folder argument on the cluster.
func (a *app) resolveCodeFolder(ctx context.Context, cl slurmjob.Client, arg string) (string, error) {
	rel, note := a.clusterPath(arg)
	if note != "" {
		fmt.Fprintln(a.stdout, note)
	}
	dir, err := cl.ResolvePath(ctx, rel)
	switch {
	case errors.Is(err, slurmjob.ErrNoDir):
		return "", usage(fmt.Errorf("%s is not a folder on the cluster", arg))
	case err != nil:
		return "", failed(err)
	}
	return dir, nil
}

// codeTarget is what hpctun code opens (spec: "Opening VS Code").
type codeTarget struct {
	folder string // "": an empty window, to pick a folder in VS Code
	// onNode: folder is an absolute path to check on the job's node once it
	// runs, since only the node's view of it is right (node-local /scratch,
	// /tmp). Folders under the home directory, which the nodes share, are
	// checked on the gateway before anything is submitted.
	onNode bool
	// fallback: hpctun chose the folder itself (the last one opened); if
	// the node lacks it, it opens the home folder instead of failing.
	fallback bool
	// local: the node check found the folder on the node's own storage, so
	// it is remembered for that node only.
	local bool
}

// recentFolder is a folder hpctun code opened: node is "" for one the nodes
// share, else the node whose own storage holds it.
type recentFolder struct{ path, node string }

// chooseCodeTarget turns the folder argument into a target. Without one, a
// terminal gets a picker (recent folders, home, a typed folder, or an
// empty window); otherwise hpctun takes the shared folder it opened last,
// else home. With --no-open there is nothing to open, so no folder is
// needed. Picking a node's own folder when no job runs yet starts the new
// job on that node (p.Node).
func (a *app) chooseCodeTarget(ctx context.Context, cl slurmjob.Client, dir, path string, open bool, p *slurmjob.Params) (codeTarget, error) {
	if path != "" {
		return a.codeTargetFor(ctx, cl, path)
	}
	if !open {
		return codeTarget{}, nil
	}
	recent := a.recentCodeFolders(dir)
	if !a.interactive() {
		for _, r := range recent {
			if r.node == "" {
				fmt.Fprintf(a.stdout, "opening %s, the folder hpctun code opened last; name a folder to open another\n", r.path)
				return codeTarget{folder: r.path, onNode: true, fallback: true}, nil
			}
		}
		return a.codeTargetFor(ctx, cl, "~")
	}
	// A node's own folders are offered for the node the job will run on:
	// the running job's, else --node; with neither and no job, each is
	// offered with its node; a job still waiting has no node yet.
	node, known := p.Node, p.Node != ""
	if slices.ContainsFunc(recent, func(r recentFolder) bool { return r.node != "" }) {
		a.spin.Step("looking for your " + codeJob + " job")
		job, err := cl.Ensure(ctx, slurmjob.Params{Mode: slurmjob.ModeFind, Name: codeJobs}, nil, io.Discard)
		switch {
		case err == nil && job.State == "RUNNING":
			node, known = job.Node, true
		case err == nil:
			node, known = "", true
		case ctx.Err() != nil:
			return codeTarget{}, ctx.Err()
		}
	}
	const typed, empty, home = "\x00type", "\x00empty", "~"
	var opts []prompt.Option
	var shown []recentFolder
	for _, r := range recent {
		if r.node != "" && known && r.node != node {
			continue
		}
		var notes []string
		if r.node != "" {
			notes = append(notes, "on "+r.node)
		}
		if len(shown) == 0 {
			notes = append(notes, "last opened")
		}
		label := r.path
		if len(notes) > 0 {
			label += "  (" + strings.Join(notes, ", ") + ")"
		}
		opts = append(opts, prompt.Option{Label: label, Value: strconv.Itoa(len(shown))})
		shown = append(shown, r)
	}
	opts = append(opts,
		prompt.Option{Label: "~  (your home folder)", Value: home},
		prompt.Option{Label: "Type a folder…", Value: typed},
		prompt.Option{Label: "Choose in VS Code  (an empty window; File → Open Folder)", Value: empty})
	resume := a.spin.Pause()
	defer resume()
	ask := a.asker("", false)
	pick, err := ask.Select(prompt.SelectQ{Title: "Open which folder on the cluster?", Options: opts, Default: opts[0].Value,
		Description: "A window already on that folder is brought forward."})
	if err == nil && pick == typed {
		pick, err = ask.Input(prompt.InputQ{Title: "Folder",
			Description: "Relative to your home on the cluster, or absolute: /scratch/... is the node's own",
			Validate: func(s string) error {
				if strings.TrimSpace(s) == "" {
					return errors.New("type a folder")
				}
				return nil
			}})
		if err == nil {
			return a.codeTargetFor(ctx, cl, pick)
		}
	}
	switch {
	case errors.Is(err, prompt.ErrAborted) || errors.Is(err, prompt.ErrBack):
		return codeTarget{}, failed(prompt.ErrAborted)
	case err != nil:
		return codeTarget{}, failed(err)
	case pick == empty:
		return codeTarget{}, nil
	case pick == home:
		return a.codeTargetFor(ctx, cl, home)
	}
	i, err := strconv.Atoi(pick)
	if err != nil || i < 0 || i >= len(shown) {
		return codeTarget{}, failed(fmt.Errorf("unexpected answer %q", pick))
	}
	r := shown[i]
	if r.node != "" && !known {
		fmt.Fprintf(a.stdout, "starting the job on %s, where %s is\n", r.node, r.path)
		p.Node = r.node
	}
	return codeTarget{folder: r.path, onNode: true}, nil
}

// codeTargetFor is the target for a folder the user named: a path relative
// to the home directory is checked on the gateway now; an absolute one on
// the node once the job runs.
func (a *app) codeTargetFor(ctx context.Context, cl slurmjob.Client, arg string) (codeTarget, error) {
	rel, note := a.clusterPath(arg)
	if strings.HasPrefix(rel, "/") {
		if note != "" {
			fmt.Fprintln(a.stdout, note)
		}
		return codeTarget{folder: rel, onNode: true}, nil
	}
	a.spin.Step("checking " + arg + " on the cluster")
	folder, err := a.resolveCodeFolder(ctx, cl, arg)
	return codeTarget{folder: folder}, err
}

// checkOnNode resolves an onNode target inside the running job.
func (a *app) checkOnNode(ctx context.Context, cl slurmjob.Client, job slurmjob.Job, t codeTarget) (codeTarget, error) {
	if !t.onNode {
		return t, nil
	}
	a.spin.Step("checking " + t.folder + " on " + job.Node)
	p, local, err := cl.NodePath(ctx, job.ID, t.folder)
	switch {
	case err == nil:
		return codeTarget{folder: p, local: local}, nil
	case ctx.Err() != nil:
		return t, ctx.Err()
	case errors.Is(err, slurmjob.ErrNoDir) && t.fallback:
		fmt.Fprintf(a.stdout, "%s is not on %s; opening your home folder instead\n", t.folder, job.Node)
		return a.codeTargetFor(ctx, cl, "~")
	case errors.Is(err, slurmjob.ErrNoDir):
		return t, usage(fmt.Errorf("%s is not a folder on %s; job %s keeps running, so run hpctun code again with another folder", t.folder, job.Node, job.ID))
	}
	return t, failed(err)
}

// recentCodeFolders are the folders hpctun code opened, newest first. The
// state file has one per line: a shared folder's path, or a node's own
// folder as node, tab, path.
func (a *app) recentCodeFolders(dir string) []recentFolder {
	var out []recentFolder
	for _, l := range strings.Split(a.readCodeState(dir, codeFolders), "\n") {
		var r recentFolder
		if node, path, ok := strings.Cut(strings.TrimSpace(l), "\t"); ok {
			r = recentFolder{path: path, node: node}
		} else {
			r = recentFolder{path: node}
		}
		if strings.HasPrefix(r.path, "/") && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// recordCodeFolder puts r first in the recent folders, keeping eight.
func (a *app) recordCodeFolder(dir string, r recentFolder) {
	list := append([]recentFolder{r}, slices.DeleteFunc(a.recentCodeFolders(dir), func(o recentFolder) bool { return o == r })...)
	if len(list) > 8 {
		list = list[:8]
	}
	lines := make([]string, len(list))
	for i, r := range list {
		lines[i] = r.path
		if r.node != "" {
			lines[i] = r.node + "\t" + r.path
		}
	}
	a.recordCodeState(dir, codeFolders, strings.Join(lines, "\n"))
}

// ensureJob holds the code lock while it finds or starts the job, reporting
// progress to w. With cancelOnInterrupt, a job this call submitted is
// cancelled if ctx ends while it is still pending.
func (a *app) ensureJob(ctx context.Context, cl slurmjob.Client, dir string, p slurmjob.Params, w io.Writer, resources, cancelOnInterrupt bool) (slurmjob.Job, error) {
	a.spin.Step("looking for your " + codeJob + " job")
	lock, err := state.Lock(ctx, filepath.Join(dir, codeLock), func() { fmt.Fprintln(w, "waiting for another hpctun code to finish...") })
	if err != nil {
		return slurmjob.Job{}, failed(err)
	}
	defer lock.Close()
	job, err := cl.Ensure(ctx, p, func(e slurmjob.Event) {
		if m := eventMessage(e, p, time.Now()); m != "" {
			fmt.Fprintln(w, m)
		}
		switch e.Kind {
		case "submitted":
			a.spin.Step("waiting for Slurm to start job " + e.ID)
		case "state":
			if m := eventMessage(e, p, time.Now()); m != "" {
				a.spin.Step(m)
			}
		}
	}, a.stderr)
	if ctx.Err() != nil {
		if cancelOnInterrupt && job.Submitted && job.ID != "" {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if cerr := cl.Cancel(cctx, job.ID); cerr != nil {
				fmt.Fprintf(w, "could not cancel job %s: %v\n", job.ID, cerr)
			} else {
				fmt.Fprintf(w, "cancelled job %s\n", job.ID)
			}
		}
		return slurmjob.Job{}, ctx.Err()
	}
	var re *slurmjob.RequestError
	switch {
	case errors.As(err, &re):
		return slurmjob.Job{}, usage(err)
	case err != nil:
		return slurmjob.Job{}, failed(err)
	}
	if job.Submitted {
		fmt.Fprintf(w, "job %s running on %s\n", job.ID, job.Node)
	} else {
		note := ""
		if resources {
			note = " (options apply to new jobs only)"
		}
		fmt.Fprintf(w, "using job %s on %s%s\n", job.ID, job.Node, note)
	}
	return job, nil
}

func eventMessage(e slurmjob.Event, p slurmjob.Params, now time.Time) string {
	switch e.Kind {
	case "submitted":
		s := fmt.Sprintf("submitted job %s (%d CPUs, %s)", e.ID, p.CPUs, p.Time)
		if e.Partition != "" {
			s += " on " + e.Partition
		}
		if t := startClock(e.Start, now); t != "" {
			s += ", expected to start around " + t
		}
		return s
	case "state":
		switch {
		case e.State == "CONFIGURING":
			return "the node is starting up"
		case e.Reason == "" || e.Reason == "None":
			return "waiting for a node"
		}
		return "waiting for a node: " + e.Reason
	case "extra":
		return "cancelled duplicate " + codeJob + " jobs: " + e.IDs
	case "account":
		return fmt.Sprintf("using Slurm account %s, which partition %s takes (choose another with --account)", e.Account, e.Partition)
	case "new":
		return "hpctun starts VS Code's tunnel for you with --accept-server-license-terms, which accepts the VS Code Server License Terms (https://aka.ms/vscode-server-license) and the Microsoft Privacy Statement (https://privacy.microsoft.com/en-US/privacystatement)."
	}
	return ""
}

// startClock shows Slurm's expected start (cluster local time) as a clock
// time today, else with the date.
func startClock(iso string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02T15:04:05", iso, time.Local)
	if err != nil {
		return ""
	}
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}

func jobLine(j slurmjob.Job) string {
	if j.State == "RUNNING" {
		return fmt.Sprintf("job %s: running on %s, %s left", j.ID, j.Node, j.Left)
	}
	s := fmt.Sprintf("job %s: %s", j.ID, strings.ToLower(j.State))
	if j.Reason != "" && j.Reason != "None" {
		s += " (" + j.Reason + ")"
	}
	return s
}

func runCodeStart(ctx context.Context, a *app, cfg *config.Config, c *config.Code, f codeFlags, path string) error {
	p, err := codeParams(c, f)
	if err != nil {
		return usage(err)
	}
	if err := a.codeConsent(); err != nil {
		return err
	}
	if a.spin = a.newSpinner(); a.spin != nil {
		stdout, stderr := a.stdout, a.stderr
		a.stdout, a.stderr = a.spin.Writer(stdout), a.spin.Writer(stderr)
		defer func() { a.spin.Stop(); a.stdout, a.stderr, a.spin = stdout, stderr, nil }()
	}
	a.spin.Step("checking key login to " + cfg.Gateway)
	if err := a.codeLogin(ctx, cfg.Gateway); err != nil {
		return err
	}
	open := !f.noOpen && !vscode.Headless(a.goos, a.getenv)
	var bin string
	if open {
		a.spin.Step("checking VS Code's Remote - Tunnels extension")
		if bin, err = a.codePreflight(ctx); err != nil {
			return err
		}
	}
	cl := a.slurm(cfg.Gateway)
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	target, err := a.chooseCodeTarget(ctx, cl, dir, path, open, &p)
	if err != nil {
		return err
	}
	job, err := a.ensureJob(ctx, cl, dir, p, a.stdout, f.resources(), true)
	if err != nil {
		return err
	}
	if target, err = a.checkOnNode(ctx, cl, job, target); err != nil {
		return err
	}
	folder := target.folder
	name, err := a.waitTunnel(ctx, cl, job, tunnelNameFor(cfg, job.Node))
	if err != nil {
		return err
	}
	if folder != "" {
		r := recentFolder{path: folder}
		if target.local {
			r.node = job.Node
		}
		a.recordCodeFolder(dir, r)
	}
	a.spin.Stop()
	if open {
		if err := a.openCode(ctx, bin, name, folder); err != nil {
			return err
		}
	} else {
		a.codeConnectHint(name, folder)
	}
	fmt.Fprintf(a.stdout, "The job has %s left; end it sooner with `hpctun code --stop`.\n", job.Left)
	fmt.Fprintf(a.stdout, "For a web app on the node: start it on 127.0.0.1 there, then run hpctun up <port> --host %s --loopback.\n", job.Node)
	return nil
}

// readCodeState reads the one-line state file name, "" if there is none.
func (a *app) readCodeState(dir, name string) string {
	old, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(old))
}

// recordCodeState writes value to the one-line state file name, once the
// tunnel is up: a wait that is interrupted or times out keeps the old one.
func (a *app) recordCodeState(dir, name, value string) {
	p := filepath.Join(dir, name)
	if err := state.EnsureDir(dir); err == nil {
		if werr := os.WriteFile(p, []byte(value+"\n"), 0o600); werr != nil {
			fmt.Fprintf(a.stderr, "warning: cannot write %s: %v\n", a.tilde(p), werr)
		}
	}
}

// tunnelNameFor names the tunnel the node script asks for on node,
// <node>-<user> cut to 20 characters (spec: "Per-node tunnels"), for
// messages; without a cluster user name, it describes it.
func tunnelNameFor(cfg *config.Config, node string) string {
	if cfg.SSH != nil && cfg.SSH.User != "" {
		name := node + "-" + cfg.SSH.User
		return "the tunnel name " + name[:min(len(name), 20)]
	}
	return "this node's usual tunnel name"
}

func (a *app) codeConnectHint(name, folder string) {
	fmt.Fprintf(a.stdout, "In VS Code: Remote Explorer → Tunnels → %s", name)
	if folder != "" {
		fmt.Fprintf(a.stdout, ", then open %s", folder)
	}
	fmt.Fprintln(a.stdout, ".")
}

// nodeStepMessage is the spinner's line for the node script's "@step"
// markers (spec: "The node script").
func nodeStepMessage(step, node string) string {
	switch step {
	case "start":
		return "running your login setup on " + node
	case "setup":
		return "setting up on " + node
	case "download":
		return "downloading VS Code's CLI on " + node
	case "tunnel":
		return "starting VS Code's tunnel on " + node
	}
	return ""
}

// waitTunnel follows the job's log until VS Code's CLI says the tunnel is
// up, relaying a first-time device code, and returns the tunnel's name.
// A reused job whose log already names the tunnel is not watched: the log
// would replay an old device code. usual names the tunnel asked for
// (tunnelNameFor), for the warning when VS Code picked another.
func (a *app) waitTunnel(ctx context.Context, cl slurmjob.Client, job slurmjob.Job, usual string) (string, error) {
	interrupted := func() error {
		return failed(fmt.Errorf("job %s keeps running: `hpctun code` reconnects to it, and `hpctun code --stop` ends it (%w)", job.ID, ctx.Err()))
	}
	// A job hpctun did not just submit should have its log by now: after
	// 15 polls (30 s) it is an older hpctun's. A new job's log may be slow
	// to appear (NFS, a slow prolog); the timer bounds that wait.
	nologPolls := 0
	if !job.Submitted {
		nologPolls = 15
		a.spin.Step("checking job " + job.ID + "'s tunnel")
		name, ready, err := cl.TunnelName(ctx, job.ID)
		switch {
		case ctx.Err() != nil:
			return "", interrupted()
		case err == nil && ready:
			return name, nil
		}
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(tunnelWait, cancel)
	defer timer.Stop()
	var name string
	var nodeErr error
	var last []string // the log's last lines, for the timeout message
	a.spin.Step("waiting for job " + job.ID + "'s output on " + job.Node)
	err := cl.Watch(wctx, job.ID, nologPolls, func(e slurmjob.LogEvent) bool {
		switch e.Kind {
		case "step":
			if m := nodeStepMessage(e.Text, job.Node); m != "" {
				a.spin.Step(m)
			}
		case "cli":
			a.spin.Step("checking VS Code sign-in on " + job.Node)
		case "login":
			a.spin.Step("starting VS Code's sign-in on " + job.Node)
		case "code":
			timer.Reset(tunnelWaitAfterCode)
			fmt.Fprintf(a.stdout, "First time on %s: open %s and enter %s (sign in with your ONID)\n", job.Node, e.URL, e.Code)
			a.spin.Step("waiting for you to sign in with code " + e.Code)
		case "ready":
			name = e.Name
			return true
		case "renamed":
			fmt.Fprintf(a.stderr, "warning: %s is taken by another tunnel, so VS Code registered a new one under a random name and replaced this node's tunnel record; hpctun opens that one\n", usual)
		case "warn":
			fmt.Fprintf(a.stderr, "warning: on %s: %s\n", job.Node, e.Reason)
		case "error":
			nodeErr = nodeScriptError(job.Node, e)
			return true
		case "":
			last = append(last, e.Text)
			if len(last) > 20 {
				last = last[1:]
			}
		}
		return false
	}, a.stderr)
	switch {
	case nodeErr != nil:
		return "", failed(nodeErr)
	case err == nil:
		return name, nil
	case ctx.Err() != nil:
		return "", interrupted()
	case wctx.Err() != nil: // our timer
		return "", failed(fmt.Errorf("VS Code's tunnel did not start on %s within the time allowed; the job is still running (`hpctun code --stop` ends it). The last lines of its log:\n%s", job.Node, strings.Join(last, "\n")))
	case errors.Is(err, slurmjob.ErrNoLog):
		return "", failed(fmt.Errorf("job %s was started by an older hpctun and has no VS Code tunnel; run `hpctun code --stop`, then `hpctun code`", job.ID))
	}
	return "", failed(err)
}

func nodeScriptError(node string, e slurmjob.LogEvent) error {
	switch e.What {
	case "cli":
		return fmt.Errorf("could not download VS Code's CLI on %s: %s; the job has ended", node, e.Reason)
	case "arch":
		return fmt.Errorf("%s is %s; VS Code's CLI is available for x86_64 and aarch64; the job has ended", node, e.Reason)
	case "login":
		return fmt.Errorf("the sign-in on %s did not finish; run hpctun code again", node)
	}
	return fmt.Errorf("could not prepare %s for VS Code: %s; the job has ended", node, e.Reason)
}

func runCodeStatus(ctx context.Context, a *app, cfg *config.Config) error {
	job, err := a.slurm(cfg.Gateway).Ensure(ctx, slurmjob.Params{Mode: slurmjob.ModeFind, Name: codeJobs}, nil, a.stderr)
	switch {
	case errors.Is(err, slurmjob.ErrNone):
		fmt.Fprintln(a.stdout, "no job running")
	case err != nil:
		return failed(err)
	default:
		fmt.Fprintln(a.stdout, jobLine(job))
		if job.State == "RUNNING" {
			switch name, ready, err := a.slurm(cfg.Gateway).TunnelName(ctx, job.ID); {
			case errors.Is(err, slurmjob.ErrNoLog):
				fmt.Fprintln(a.stdout, "tunnel: none (job started by an older hpctun; run hpctun code --stop, then hpctun code)")
			case err != nil:
				fmt.Fprintf(a.stderr, "warning: %v\n", err)
			case ready:
				fmt.Fprintf(a.stdout, "tunnel %s: ready\n", name)
			default:
				fmt.Fprintln(a.stdout, "tunnel: starting")
			}
		}
	}
	return nil
}

// codeStatus is hpctun status's line for the job. It asks the gateway only
// over a connection that is already up, so status stays quick and never
// prompts.
func (a *app) codeStatus(ctx context.Context, cfg *config.Config, connected bool) *codeLine {
	if !connected {
		return &codeLine{NotConnected: true}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	job, err := a.slurm(cfg.Gateway).Ensure(ctx, slurmjob.Params{Mode: slurmjob.ModeFind, Name: codeJobs}, nil, io.Discard)
	switch {
	case errors.Is(err, slurmjob.ErrNone):
		return &codeLine{None: true}
	case err != nil:
		return &codeLine{Err: err}
	}
	return &codeLine{Job: &job}
}

// runCodeStop cancels the job. It also removes the stop marker older
// versions of hpctun wrote; nothing reads it any more.
func runCodeStop(ctx context.Context, a *app, cfg *config.Config) error {
	dir, err := a.stateDir()
	if err != nil {
		return err
	}
	if err := a.slurm(cfg.Gateway).Stop(ctx, codeJob, legacyJob); err != nil {
		return failed(err)
	}
	if err := os.Remove(filepath.Join(dir, "code-stopped")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(a.stderr, "warning: %v\n", err)
	}
	fmt.Fprintf(a.stdout, "cancelled your %s job\n", codeJob)
	return nil
}
