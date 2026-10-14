package slurmjob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Exec runs argv with stdin as its input, copies its stderr to stderr as it
// arrives, and returns its stdout and exit status. err is set only when the
// program could not run or ctx ended.
type Exec func(ctx context.Context, argv []string, stdin string, stderr io.Writer) (stdout string, code int, err error)

// Client runs Slurm commands on the gateway.
type Client struct {
	Exec    Exec
	Gateway string // ssh destination: the config's gateway alias
	// Multiplexing: ssh here can share one connection between commands
	// (ControlMaster; not on Windows). A waiting Ensure then opens its own
	// connection: a shared master keeps a session open after its ssh client
	// dies, so the ensure script would never notice VS Code giving up.
	Multiplexing bool
}

// Mode says whether Ensure may submit and wait.
type Mode string

const (
	ModeEnsure Mode = "ensure" // find the job or submit one, then wait until it runs
	ModeFind   Mode = "find"   // report the job as it is; never submit or wait
)

// Params is one Ensure request. Each field reaches the script on its own
// line of stdin, so none may contain a line break.
type Params struct {
	Mode      Mode
	Name      string // job name, or comma-separated names to find; a new job gets the first
	CPUs      int
	Time      string // sbatch --time
	Deadline  string // sbatch --deadline, see Deadline
	Partition string // may be empty
	Node      string // may be empty
	Account   string // sbatch -A; "": the script picks one the partition takes; "-": one is in Extra
	Extra     []string
	Script    string // the batch script (ModeEnsure); see NodeScript
}

// Job is the user's job as squeue reports it.
type Job struct {
	ID, State, Node, Left, Reason string
	Submitted                     bool // this call submitted it
}

// Event is progress while Ensure runs.
type Event struct {
	Kind      string // "reuse", "submitted", "extra", "state", "account" or "new" (the script created the state directory)
	ID        string // reuse, submitted
	Start     string // submitted: expected start from --test-only, e.g. 2026-09-24T13:29:56; may be empty
	Partition string // submitted: from --test-only, may be empty; account: the partition
	Account   string // account: the account the script picked
	State     string // state
	Reason    string // state
	IDs       string // extra: the duplicate jobs cancelled
}

var (
	// ErrNone: in ModeFind, the user has no job.
	ErrNone = errors.New("no job running")
	// ErrNoDir: ResolvePath found no such directory.
	ErrNoDir = errors.New("no such directory on the cluster")
)

// RequestError: Slurm refused the request, or the job can never start as
// submitted. Msg says what happened; Hint, when set, what to do.
type RequestError struct{ Msg, Hint string }

func (e *RequestError) Error() string {
	if e.Hint != "" {
		return e.Msg + ": " + e.Hint
	}
	return e.Msg
}

// GoneError: the job ended before it ran.
type GoneError struct{ ID, State string }

func (e *GoneError) Error() string {
	if e.State == "DEADLINE" {
		return fmt.Sprintf("job %s did not start in time, so Slurm removed it (DEADLINE); try again later or with other options", e.ID)
	}
	return fmt.Sprintf("job %s ended before it started (%s)", e.ID, e.State)
}

var (
	nameRE  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	startRE = regexp.MustCompile(`to start at (\S+) .*in partition (\S+)`)
)

// remote is the ssh command running script on the gateway. args are
// appended after "_" (as $1...); callers pass only validated names.
func (c Client) remote(script string, args ...string) []string {
	return c.remoteWith(nil, script, args...)
}

// ownConnection keeps an ssh command off any shared master. A new
// connection also prints the gateway's login banner, which LogLevel=ERROR
// hides.
var ownConnection = []string{"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "LogLevel=ERROR"}

func (c Client) remoteWith(opts []string, script string, args ...string) []string {
	cmd := "sh -c '" + script + "'"
	if len(args) > 0 {
		cmd += " _ " + strings.Join(args, " ")
	}
	argv := append([]string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}, opts...)
	return append(argv, "--", c.Gateway, cmd)
}

// Ensure runs EnsureScript. progress (may be nil) sees events; other stderr
// lines go to stderr. A pending reason that never clears cancels the job
// and returns a *RequestError. If ctx ends, the returned Job still carries
// the ID and whether this call submitted it, so the caller can cancel it.
func (c Client) Ensure(ctx context.Context, p Params, progress func(Event), stderr io.Writer) (Job, error) {
	lines := append([]string{string(p.Mode), p.Name, strconv.Itoa(p.CPUs), p.Time, p.Deadline, p.Partition, p.Node, p.Account}, p.Extra...)
	for _, l := range lines {
		if strings.ContainsAny(l, "\r\n") {
			return Job{}, fmt.Errorf("%q: job options cannot contain line breaks", l)
		}
	}
	for _, n := range strings.Split(p.Name, ",") {
		if !nameRE.MatchString(n) {
			return Job{}, fmt.Errorf("bad job name %q", n)
		}
	}
	for _, x := range p.Extra {
		if x == "@script" {
			return Job{}, errors.New(`"@script" cannot be an sbatch argument`)
		}
	}
	if p.Mode == ModeEnsure && p.Script == "" {
		return Job{}, errors.New("no batch script")
	}
	input := strings.Join(lines, "\n") + "\n"
	if p.Script != "" {
		input += "@script\n" + p.Script
	}
	if progress == nil {
		progress = func(Event) {}
	}
	run, stop := context.WithCancel(ctx)
	defer stop()
	var job Job
	var never, fail, gone, lost string
	w := &eventWriter{pass: stderr, on: func(kind, rest string) {
		switch kind {
		case "reuse":
			job.ID = rest
			progress(Event{Kind: kind, ID: rest})
		case "submitted":
			id, test, _ := strings.Cut(rest, "|")
			job.ID, job.Submitted = id, true
			e := Event{Kind: kind, ID: id}
			if m := startRE.FindStringSubmatch(test); m != nil {
				e.Start, e.Partition = m[1], m[2]
			}
			progress(e)
		case "new":
			progress(Event{Kind: kind})
		case "account":
			acct, part, _ := strings.Cut(rest, "|")
			progress(Event{Kind: kind, Account: acct, Partition: part})
		case "extra":
			progress(Event{Kind: kind, IDs: strings.TrimSpace(rest)})
		case "state":
			st, reason, _ := strings.Cut(rest, "|")
			if _, ok := NeverClears(reason); ok && never == "" {
				never = reason
				stop()
				return
			}
			progress(Event{Kind: kind, State: st, Reason: reason})
		case "error":
			fail = rest
		case "gone":
			gone = rest
		case "lost":
			lost = rest
		}
	}}
	var opts []string
	if p.Mode == ModeEnsure && c.Multiplexing {
		opts = ownConnection
	}
	out, code, err := c.Exec(run, c.remoteWith(opts, EnsureScript), input, w)
	w.flush()
	if never != "" {
		msg, _ := NeverClears(never)
		e := &RequestError{Msg: fmt.Sprintf("job %s can never start as submitted (%s)", job.ID, never), Hint: msg}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cerr := c.Cancel(cctx, job.ID); cerr != nil {
			e.Hint += fmt.Sprintf("; cancelling it failed (%v), so run `scancel %s`", cerr, job.ID)
		}
		return Job{}, e
	}
	if err != nil {
		return job, err
	}
	switch code {
	case 0:
		return parseJob(out, job.Submitted)
	case 3:
		return Job{}, ErrNone
	case 2:
		return Job{}, requestError(fail)
	}
	if gone != "" {
		return Job{}, &GoneError{ID: job.ID, State: strings.Fields(gone)[0]}
	}
	if lost != "" {
		return Job{}, lostError(c.Gateway, job.ID, strings.TrimSpace(lost))
	}
	return Job{}, fmt.Errorf("running Slurm commands on %s failed (exit status %d)", c.Gateway, code)
}

// lostError: squeue on the gateway did not report the job while it may
// still be queued or running; state is sacct's view, UNKNOWN when it had
// none.
func lostError(gateway, id, state string) error {
	seen := "Slurm's accounting has no state for it"
	if state != "UNKNOWN" && state != "" {
		seen = "Slurm's accounting says it is " + state
	}
	return fmt.Errorf("job %s may still be queued, but squeue on %s did not report it (%s); try again in a minute, or cancel it with `hpctun code --stop`", id, gateway, seen)
}

// lastLine returns the last non-empty line of s, for output that a chatty
// login rc (or CDPATH) may have put lines ahead of.
func lastLine(s string) string {
	line := strings.TrimSpace(s)
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	return line
}

func parseJob(out string, submitted bool) (Job, error) {
	line := lastLine(out)
	f := strings.Split(line, "|")
	if len(f) != 5 || !digits.MatchString(f[0]) {
		return Job{}, fmt.Errorf("unexpected squeue output %q", line)
	}
	return Job{ID: f[0], State: f[1], Node: f[2], Left: f[3], Reason: f[4], Submitted: submitted}, nil
}

func requestError(payload string) error {
	kind, rest, _ := strings.Cut(payload, "|")
	switch kind {
	case "test":
		msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "allocation failure: "))
		return &RequestError{Msg: "Slurm refused the job: " + msg, Hint: Explain(msg)}
	case "dir":
		return &RequestError{Msg: "cannot create " + rest + " on the gateway"}
	case "submit":
		return &RequestError{Msg: "sbatch failed; its message is above"}
	case "nopart":
		return &RequestError{Msg: "you cannot submit to " + rest, Hint: "press Tab after --node to list the nodes you can use"}
	case "noaccount":
		part, allow, _ := strings.Cut(rest, "|")
		return &RequestError{Msg: fmt.Sprintf("partition %s takes only the Slurm accounts %s, and you have none of them", part, strings.ReplaceAll(allow, ",", ", ")),
			Hint: "ask for access to one, or choose another node or partition"}
	case "manypart":
		node, parts, _ := strings.Cut(rest, "|")
		return &RequestError{Msg: fmt.Sprintf("%s is in partitions %s", node, strings.Join(strings.Fields(parts), ", ")), Hint: "choose one with --partition"}
	}
	return &RequestError{Msg: "Slurm refused the job"}
}

// eventWriter splits the script's stderr into lines: "@kind rest" lines
// are events; any other line (ssh's or Slurm's own words) goes to pass.
type eventWriter struct {
	pass io.Writer
	on   func(kind, rest string)
	buf  []byte
}

func (w *eventWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.line(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

func (w *eventWriter) flush() {
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}

func (w *eventWriter) line(l string) {
	if rest, ok := strings.CutPrefix(l, "@"); ok {
		kind, arg, _ := strings.Cut(rest, " ")
		w.on(kind, arg)
		return
	}
	if w.pass != nil {
		fmt.Fprintln(w.pass, l)
	}
}

// Stop cancels the user's jobs with any of names.
func (c Client) Stop(ctx context.Context, names ...string) error {
	var parts []string
	for _, n := range names {
		if !nameRE.MatchString(n) {
			return fmt.Errorf("bad job name %q", n)
		}
		parts = append(parts, `scancel -u "$USER" -n `+n)
	}
	return c.simple(ctx, strings.Join(parts, " && "))
}

// Cancel cancels job id.
func (c Client) Cancel(ctx context.Context, id string) error {
	if !digits.MatchString(id) {
		return fmt.Errorf("bad job id %q", id)
	}
	return c.simple(ctx, "scancel "+id)
}

func (c Client) simple(ctx context.Context, script string) error {
	var errb strings.Builder
	_, code, err := c.Exec(ctx, c.remote(script), "", &errb)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s on %s failed: %s", script, c.Gateway, strings.TrimSpace(errb.String()))
	}
	return nil
}

const pathScript = `IFS= read -r p; cd -- "$p" 2>/dev/null || exit 1; pwd -P`

// ResolvePath returns the absolute form of path on the gateway, whose home
// directory the nodes share; a relative path is relative to that home.
func (c Client) ResolvePath(ctx context.Context, path string) (string, error) {
	if strings.ContainsAny(path, "\r\n") {
		return "", ErrNoDir
	}
	out, code, err := c.Exec(ctx, c.remote(pathScript), path+"\n", io.Discard)
	if err != nil {
		return "", err
	}
	switch p := lastLine(out); {
	case code == 0 && strings.HasPrefix(p, "/"):
		return p, nil
	case code == 1:
		return "", ErrNoDir
	}
	return "", fmt.Errorf("cannot reach %s to find the folder (exit status %d)", c.Gateway, code)
}

// nodePathScript checks a folder inside job $1 on its node: srun --overlap
// runs a step next to the tunnel, so no ssh to the node is needed. The
// path arrives on stdin and reaches the step through the environment
// (srun passes it on), never through a command line. The step prints the
// folder's file system type (stat -f), then its absolute path, and exits 3
// for no such folder; srun's own failures exit otherwise. --mpi=none: with
// the OSU cluster's MpiDefault=pmix the step never launched and srun gave
// up after 52 s; timeout bounds any other stall (exit 124).
const nodePathScript = `IFS= read -r p; HPCTUN_P=$p; export HPCTUN_P; ` +
	`timeout 60 srun --jobid="$1" --overlap --mpi=none -N 1 -n 1 -Q sh -c "cd -- \"\$HPCTUN_P\" 2>/dev/null || exit 3; stat -f -c %T . 2>/dev/null; pwd -P"`

// networkFS are the file system types the nodes share (stat -f -c %T);
// any other, tmpfs included, is the node's own.
var networkFS = regexp.MustCompile(`^(nfs4?|lustre|gpfs|beegfs|cifs|smb2?|ceph|autofs|panfs|wekafs|fuse\..*|fuseblk)$`)

// NodePath returns the absolute form of path on job id's node, for a folder
// only the node may have (node-local /scratch or /tmp), and whether it is
// on the node's own storage rather than one the nodes share. The job must
// be running.
func (c Client) NodePath(ctx context.Context, id, path string) (string, bool, error) {
	if !digits.MatchString(id) {
		return "", false, fmt.Errorf("bad job id %q", id)
	}
	if strings.ContainsAny(path, "\r\n") {
		return "", false, ErrNoDir
	}
	var stderr strings.Builder
	out, code, err := c.Exec(ctx, c.remote(nodePathScript, id), path+"\n", &stderr)
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	switch p := lines[len(lines)-1]; {
	case code == 0 && strings.HasPrefix(p, "/"):
		fs := ""
		if len(lines) > 1 {
			fs = strings.TrimSpace(lines[len(lines)-2])
		}
		return p, !networkFS.MatchString(fs), nil
	case code == 3:
		return "", false, ErrNoDir
	}
	msg := strings.Join(strings.Fields(stderr.String()), " ")
	switch {
	case code == 124:
		msg = "srun did not finish within 60 s"
	case msg == "":
		msg = fmt.Sprintf("exit status %d", code)
	}
	return "", false, fmt.Errorf("cannot check the folder in job %s: %s", id, msg)
}
