package main

import (
	"encoding/json"
	"io"
	"strings"

	"hpctun/internal/config"
	"hpctun/internal/gateway"
	"hpctun/internal/jsondoc"
	"hpctun/internal/tunnel"
)

const jsonFlagUsage = "print one JSON document on stdout instead of text"

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func errStr(err error) *string {
	if err == nil {
		return nil
	}
	return strOrNil(err.Error())
}

func pidOrNil(s gateway.Status) *int {
	if !s.Running {
		return nil
	}
	return &s.PID
}

func statusDoc(h header, rows []tunnel.Row) jsondoc.Status {
	doc := jsondoc.Status{Version: jsondoc.Version, Config: h.Config, Login: connDoc(h.Login), Transfer: connDoc(h.Transfer), Code: codeDoc(h.Code), Tunnels: []jsondoc.Row{}}
	if h.Dest != "" {
		doc.Gateway = &jsondoc.Gateway{
			Dest: h.Dest, TunnelsOnly: h.Tunnels, Mode: strOrNil(string(h.Mode)),
			Running: h.Status.Running, PID: pidOrNil(h.Status), Stale: h.Status.Stale,
			Error: errStr(h.Err), Hints: append([]string{}, h.Hints...),
		}
	}
	for _, r := range rows {
		doc.Tunnels = append(doc.Tunnels, rowDoc(r))
	}
	return doc
}

func connDoc(c *conn) *jsondoc.Conn {
	if c == nil {
		return nil
	}
	return &jsondoc.Conn{
		Alias: c.Alias, Mode: strOrNil(string(c.Mode)),
		// A transfer host without ControlMaster, and every connection on
		// Windows, shares no login, and is not checked.
		Shared:    !(c.Role == "transfer" && c.Mode == gateway.ModeManaged) && c.Mode != gateway.ModeStandalone,
		Connected: c.Status.Running, PID: pidOrNil(c.Status), Error: errStr(c.Err),
	}
}

// codeDoc follows codeLine.String's order.
func codeDoc(c *codeLine) *jsondoc.Code {
	switch {
	case c == nil:
		return nil
	case c.NotConnected:
		return &jsondoc.Code{State: "unknown"}
	case c.Err != nil:
		return &jsondoc.Code{State: "error", Error: errStr(c.Err)}
	case c.None || c.Job == nil:
		return &jsondoc.Code{State: "none"}
	}
	j := c.Job
	return &jsondoc.Code{State: "running", Job: &jsondoc.Job{ID: j.ID, State: j.State, Node: j.Node, TimeLeft: j.Left, Reason: j.Reason}}
}

func rowDoc(r tunnel.Row) jsondoc.Row {
	doc := jsondoc.Row{Kind: string(r.Kind), Port: r.Port, Route: &r.Route, Target: &r.Target, State: string(r.State),
		Owner: strOrNil(r.Owner), Warning: strOrNil(r.Warning), URL: strOrNil(r.URL)}
	browse := !r.NoBrowse
	doc.Browse = &browse
	if r.Kind == tunnel.KindNamed {
		doc.Name = &r.Name
	}
	if r.Kind == tunnel.KindExternal {
		doc.Route = nil
	}
	if r.Route == string(config.RouteSocks) {
		doc.Target = nil
	}
	if r.Conns >= 0 {
		doc.Conns = &r.Conns
	}
	return doc
}

func resultsDoc(results []tunnel.Result, saved *jsondoc.Saved, saveErr *string) jsondoc.Results {
	doc := jsondoc.Results{Version: jsondoc.Version, Results: []jsondoc.Result{}, Saved: saved, SaveError: saveErr}
	for _, r := range results {
		doc.Results = append(doc.Results, resultDoc(r))
	}
	return doc
}

func resultDoc(r tunnel.Result) jsondoc.Result {
	s := r.Spec
	doc := jsondoc.Result{Kind: string(s.Kind()), Name: strOrNil(s.Name), Port: s.Port, Route: string(s.Route),
		Outcome: strings.ReplaceAll(string(r.Outcome), " ", "_"), Detail: strOrNil(r.Detail)}
	if s.Route != config.RouteSocks {
		doc.Host, doc.RemotePort = &s.Host, &s.RemotePort
	}
	return doc
}

// writeJSON writes v as one line of JSON. HTML escaping is off: targets and
// messages are shown to people as they are.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// jsonErrors runs a --json command. When it fails before writing its
// document, the error becomes the document, so stdout always carries one;
// the error is returned unchanged for fang's message and the exit status.
func jsonErrors(a *app, asJSON bool, run func() error) error {
	if !asJSON {
		return run()
	}
	out := a.stdout
	w := &countWriter{w: out}
	a.stdout = w
	err := run()
	a.stdout = out
	if err != nil && w.n == 0 {
		writeErrorJSON(out, err)
	}
	return err
}

func writeErrorJSON(w io.Writer, err error) {
	writeJSON(w, jsondoc.Error{Version: jsondoc.Version, Error: jsondoc.ErrorBody{Message: err.Error(), Exit: exitCode(err)}})
}

// countWriter counts the bytes written through it.
type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}
