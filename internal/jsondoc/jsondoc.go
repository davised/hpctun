// Package jsondoc holds the documents hpctun prints with --json.
// Nullable fields are pointers without omitempty, so a field that does not
// apply is null rather than missing.
package jsondoc

// Version is the version of every document. Adding a field keeps it;
// removing, renaming or changing one bumps it.
const Version = 1

// Status is `hpctun status --json`.
type Status struct {
	Version  int      `json:"version"`
	Config   string   `json:"config"`
	Gateway  *Gateway `json:"gateway"`
	Login    *Conn    `json:"login"`
	Transfer *Conn    `json:"transfer"`
	Code     *Code    `json:"code"`
	Tunnels  []Row    `json:"tunnels"`
}

// Gateway is the connection tunnels ride on; nil when none is configured.
type Gateway struct {
	Dest        string   `json:"dest"`
	TunnelsOnly bool     `json:"tunnels_only"`
	Mode        *string  `json:"mode"`
	Running     bool     `json:"running"`
	PID         *int     `json:"pid"`
	Stale       bool     `json:"stale"`
	Error       *string  `json:"error"`
	Hints       []string `json:"hints"`
}

// Conn is another shared connection: the gateway when tunnels have their
// own, or the transfer host.
type Conn struct {
	Alias     string  `json:"alias"`
	Mode      *string `json:"mode"`
	Shared    bool    `json:"shared"`
	Connected bool    `json:"connected"`
	PID       *int    `json:"pid"`
	Error     *string `json:"error"`
}

// Code is the hpctun code job; nil without [code] or with --no-code.
type Code struct {
	State string  `json:"state"` // running, none, unknown or error
	Job   *Job    `json:"job"`
	Error *string `json:"error"`
}

// Job is Slurm's view of the code job.
type Job struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Node     string `json:"node"`
	TimeLeft string `json:"time_left"`
	Reason   string `json:"reason"`
}

// Row is one line of the status table.
type Row struct {
	Kind    string  `json:"kind"`
	Name    *string `json:"name"`
	Port    int     `json:"port"`
	Route   *string `json:"route"`
	Target  *string `json:"target"`
	State   string  `json:"state"`
	Conns   *int    `json:"conns"`
	Owner   *string `json:"owner"`
	Warning *string `json:"warning"`
	URL     *string `json:"url"`
	// Browse is false for a tunnel with browse = false (no page to open).
	// A pointer so a document from an older hpctun, without it, reads as
	// browsable.
	Browse *bool `json:"browse"`
}

// Results is `hpctun up --json` and `hpctun down --json`.
type Results struct {
	Version   int      `json:"version"`
	Results   []Result `json:"results"`
	Saved     *Saved   `json:"saved"`
	SaveError *string  `json:"save_error"` // why --save did not save
}

// Browse is `hpctun browse --json`.
type Browse struct {
	Version int     `json:"version"`
	Result  Result  `json:"result"`
	Browser *string `json:"browser"` // null when no browser started
	URL     *string `json:"url"`     // null for a blank window
	Warning *string `json:"warning"`
}

// Result is what happened to one tunnel.
type Result struct {
	Kind       string  `json:"kind"`
	Name       *string `json:"name"`
	Port       int     `json:"port"`
	Route      string  `json:"route"`
	Host       *string `json:"host"`
	RemotePort *int    `json:"remote_port"`
	Outcome    string  `json:"outcome"`
	Detail     *string `json:"detail"`
}

// Saved names the tunnel `up --save` saved, and the config it went to.
type Saved struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

// Error is printed instead when a --json command fails before it has a
// document.
type Error struct {
	Version int       `json:"version"`
	Error   ErrorBody `json:"error"`
}

// ErrorBody is the message and the exit status hpctun exits with.
type ErrorBody struct {
	Message string `json:"message"`
	Exit    int    `json:"exit"`
}
