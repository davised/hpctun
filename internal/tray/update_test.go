package tray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hpctun/internal/jsondoc"
)

// clock is a wall clock the test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// updateCLI answers status with *st and self-update --check with *upd (or
// fails it when fail is set), counting checks.
type updateCLI struct {
	t      *testing.T
	mu     sync.Mutex
	st     *jsondoc.Status
	upd    *jsondoc.Update
	fail   bool
	stFail bool
	checks int
	*fakeCLI
}

func newUpdateCLI(t *testing.T, st *jsondoc.Status, upd *jsondoc.Update) *updateCLI {
	u := &updateCLI{t: t, st: st, upd: upd}
	u.fakeCLI = &fakeCLI{handler: func(args []string) (string, string, int) {
		u.mu.Lock()
		defer u.mu.Unlock()
		a := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(a, "status --json"):
			if u.stFail {
				return "", "ERROR  cannot reach hpctun", 1
			}
			return doc(t, u.st), "", 0
		case a == "self-update --check --json":
			u.checks++
			if u.fail {
				return doc(t, jsondoc.Error{Version: 1, Error: jsondoc.ErrorBody{Message: "no network", Exit: 1}}), "", 1
			}
			return doc(t, u.upd), "", 0
		}
		return "", "unexpected", 2
	}}
	return u
}

func (u *updateCLI) count() int { u.mu.Lock(); defer u.mu.Unlock(); return u.checks }

// trayExe makes a file for the running tray and returns its path.
func trayExe(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "hpctun-tray")
	os.WriteFile(p, []byte("tray"), 0o755)
	return p
}

func offerDoc(tray string) *jsondoc.Update {
	return &jsondoc.Update{Version: 1, Running: "v0.20261111-0a1b2c3", Latest: "v0.20261202-4d5e6f7", Action: "update",
		Install: jsondoc.UpdateInstall{Kind: "plain_with_tray", Target: "/x/hpctun", Tray: &tray}}
}

func checkingStatus(on bool) *jsondoc.Status {
	st := status(true, dashDown)
	st.UpdateCheck = on
	return st
}

// startApp runs an App with a hand-moved clock; polls happen on Refresh.
func startApp(t *testing.T, cli CLI, exe string, c *clock) (*App, *recorder) {
	view := &recorder{}
	a := &App{CLI: cli, View: view, Poll: time.Hour, Exe: exe, Now: c.now}
	runApp(t, a)
	// The startup poll ends, maybeCheck included, when polls reaches 1;
	// waiting for its header would leave it racing the test's clock moves.
	eventually(t, "startup poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.s.polls >= 1 })
	return a, view
}

// poll asks for a poll and waits until it, and any check it started, end.
func poll(t *testing.T, a *App) {
	t.Helper()
	a.mu.Lock()
	n := a.s.polls
	a.mu.Unlock()
	a.Refresh()
	eventually(t, "a poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.s.polls > n })
	a.waitCheck()
}

func TestCheckSchedule(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, cli, exe, c)
	if cli.count() != 0 {
		t.Fatal("checked before a minute passed")
	}
	c.add(61 * time.Second)
	poll(t, a)
	if cli.count() != 1 || view.last().Update != "Update to v0.20261202-4d5e6f7" {
		t.Fatalf("checks %d update %q", cli.count(), view.last().Update)
	}
	if !strings.HasSuffix(view.last().Tooltip, " (update available)") {
		t.Fatalf("tooltip %q", view.last().Tooltip)
	}
	c.add(23 * time.Hour)
	poll(t, a)
	if cli.count() != 1 {
		t.Fatal("checked again within a day")
	}
	c.add(time.Hour + time.Second)
	poll(t, a)
	if cli.count() != 2 {
		t.Fatal("no check after a day")
	}
}

// A laptop asleep all day: the monotonic clock barely moves, the wall clock
// does. The deadline is wall time, so the check comes at the next poll.
func TestCheckUsesWallClock(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, _ := startApp(t, cli, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	c.add(25 * time.Hour) // asleep; no polls in between
	poll(t, a)
	if cli.count() != 2 {
		t.Fatalf("checks %d", cli.count())
	}
}

func TestCheckFailureRetriesInAnHour(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	cli.fail = true
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, cli, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	if view.last().Update != "" || view.last().LastError != "" {
		t.Fatalf("a failed check showed something: %+v", view.last())
	}
	c.add(59 * time.Minute)
	poll(t, a)
	if cli.count() != 1 {
		t.Fatal("retried too soon")
	}
	c.add(2 * time.Minute)
	poll(t, a)
	if cli.count() != 2 {
		t.Fatal("no retry after an hour")
	}
}

func TestCheckClockSetBack(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, _ := startApp(t, cli, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	c.add(-3 * time.Hour)
	poll(t, a)
	if cli.count() != 2 {
		t.Fatalf("checks %d after the clock went back", cli.count())
	}
}

func TestNoCheckWhenOff(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(false), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, _ := startApp(t, cli, exe, c)
	c.add(48 * time.Hour)
	poll(t, a)
	if cli.count() != 0 {
		t.Fatal("checked with update_check = false")
	}
}

func TestOfferHiddenWhenTurnedOff(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, cli, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	cli.mu.Lock()
	cli.st.UpdateCheck = false
	cli.mu.Unlock()
	poll(t, a)
	if view.last().Update != "" {
		t.Fatal("offer still shown after update_check = false")
	}
}

func TestOfferOnlyForThisTray(t *testing.T) {
	exe := trayExe(t)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(exe, link)
	other := trayExe(t)
	for _, tc := range []struct {
		name  string
		edit  func(d *jsondoc.Update)
		offer bool
	}{
		{"this tray through a symlink", func(d *jsondoc.Update) { d.Install.Tray = &link }, true},
		{"another install's tray", func(d *jsondoc.Update) { d.Install.Tray = &other }, false},
		{"no tray", func(d *jsondoc.Update) { d.Install.Tray = nil }, false},
		{"blocked", func(d *jsondoc.Update) { s := "dev build"; d.Blocked = &s }, false},
		{"downgrade", func(d *jsondoc.Update) { d.Action = "downgrade" }, false},
		{"up to date", func(d *jsondoc.Update) { d.Action = "up_to_date" }, false},
		{"unknown version", func(d *jsondoc.Update) { d.Version = 2 }, false},
		{"no latest", func(d *jsondoc.Update) { d.Latest = "" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := offerDoc(exe)
			tc.edit(d)
			cli := newUpdateCLI(t, checkingStatus(true), d)
			c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
			a, view := startApp(t, cli, exe, c)
			c.add(2 * time.Minute)
			poll(t, a)
			if got := view.last().Update != ""; got != tc.offer {
				t.Fatalf("offer shown %v, want %v", got, tc.offer)
			}
		})
	}
}

// A check still running when checks are turned off is dropped on arrival.
func TestLateCheckDropped(t *testing.T) {
	exe := trayExe(t)
	release := make(chan struct{})
	u := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	inner := u.fakeCLI.handler
	u.fakeCLI.handler = func(args []string) (string, string, int) {
		if strings.Join(args, " ") == "self-update --check --json" {
			<-release
		}
		return inner(args)
	}
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, u, exe, c)
	a.mu.Lock()
	n0 := a.s.polls
	a.mu.Unlock()
	c.add(2 * time.Minute)
	a.Refresh()
	eventually(t, "check started", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.s.checking && a.s.polls > n0
	})
	u.mu.Lock()
	u.st.UpdateCheck = false
	u.mu.Unlock()
	a.mu.Lock()
	n := a.s.polls
	a.mu.Unlock()
	a.Refresh()
	eventually(t, "a poll", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.s.polls > n })
	close(release)
	a.waitCheck()
	if view.last().Update != "" {
		t.Fatal("a late check's offer was shown")
	}
}

// A failed status poll hides the offer without dropping it or checking
// again; the next good poll brings it back.
func TestOfferSurvivesFailedStatus(t *testing.T) {
	exe := trayExe(t)
	cli := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, cli, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	if view.last().Update == "" {
		t.Fatal("no offer to begin with")
	}
	cli.mu.Lock()
	cli.stFail = true
	cli.mu.Unlock()
	poll(t, a)
	if view.last().Update != "" {
		t.Fatal("offer shown while status fails")
	}
	cli.mu.Lock()
	cli.stFail = false
	cli.mu.Unlock()
	poll(t, a)
	if view.last().Update == "" || cli.count() != 1 {
		t.Fatalf("update %q, checks %d", view.last().Update, cli.count())
	}
}

// offered starts an App with an offer showing, using handler for the rest.
func offered(t *testing.T, handler func(args []string) (string, string, int)) (*App, *recorder, *updateCLI) {
	exe := trayExe(t)
	u := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	inner := u.fakeCLI.handler
	u.fakeCLI.handler = func(args []string) (string, string, int) {
		if args[0] == "self-update" && args[1] != "--check" {
			return handler(args)
		}
		return inner(args)
	}
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, u, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	if view.last().Update == "" {
		t.Fatal("no offer")
	}
	return a, view, u
}

func updatedDoc(t *testing.T, updated bool) string {
	d := offerDoc("/x/hpctun-tray")
	d.Updated = updated
	if !updated {
		d.Action = "up_to_date"
	}
	return doc(t, d)
}

func TestUpdateRelaunches(t *testing.T) {
	for _, updated := range []bool{true, false} {
		var relaunched, quit int
		a, _, u := offered(t, func(args []string) (string, string, int) { return updatedDoc(t, updated), "", 0 })
		a.Relaunch = func() error { relaunched++; return nil }
		a.Quit = func() { quit++ }
		a.Update()
		a.Wait()
		if got := u.called("self-update --json --expect v0.20261202-4d5e6f7"); len(got) != 1 {
			t.Fatalf("calls %q", u.calls)
		}
		if relaunched != 1 || quit != 1 {
			t.Fatalf("updated=%v: relaunched %d quit %d", updated, relaunched, quit)
		}
	}
}

func TestUpdateFailureShowsError(t *testing.T) {
	a, view, _ := offered(t, func(args []string) (string, string, int) {
		return doc(t, jsondoc.Error{Version: 1, Error: jsondoc.ErrorBody{Message: "release v…: signature does not verify; nothing was changed", Exit: 1}}), "", 1
	})
	a.Relaunch = func() error { t.Error("relaunched after a failure"); return nil }
	a.Quit = func() { t.Error("quit after a failure") }
	a.Update()
	a.Wait()
	m := view.last()
	if !strings.Contains(m.LastError, "signature does not verify") || m.Update != "Update to v0.20261202-4d5e6f7" || m.UpdateDisabled {
		t.Fatalf("model %+v", m)
	}
}

func TestRelaunchFailure(t *testing.T) {
	a, view, _ := offered(t, func(args []string) (string, string, int) { return updatedDoc(t, true), "", 0 })
	a.Relaunch = func() error { return errors.New("no open") }
	a.Quit = func() { t.Error("quit although the relaunch failed") }
	a.Update()
	a.Wait()
	m := view.last()
	if m.LastError != "Updated to v0.20261202-4d5e6f7; quit and reopen hpctun-tray" || m.Update != "" {
		t.Fatalf("model %+v", m)
	}
}

func TestQuitDuringUpdateWaitsAndDoesNotRelaunch(t *testing.T) {
	release := make(chan struct{})
	a, view, _ := offered(t, func(args []string) (string, string, int) { <-release; return updatedDoc(t, true), "", 0 })
	var quit int
	a.Relaunch = func() error { t.Error("relaunched after Quit"); return nil }
	a.Quit = func() { quit++ }
	a.Update()
	eventually(t, "updating", func() bool { return strings.HasPrefix(view.last().Update, "Updating to ") })
	a.RequestQuit()
	if quit != 0 {
		t.Fatal("quit while the update ran")
	}
	close(release)
	a.Wait()
	if quit != 1 {
		t.Fatalf("quit %d", quit)
	}
}

func TestGate(t *testing.T) {
	release := make(chan struct{})
	a, view, u := offered(t, func(args []string) (string, string, int) { <-release; return updatedDoc(t, true), "", 0 })
	a.Relaunch = func() error { return nil }
	a.Quit = func() {}
	a.Update()
	eventually(t, "updating", func() bool { return strings.HasPrefix(view.last().Update, "Updating to ") })
	a.Toggle(context.Background(), 8099)
	a.Login(context.Background())
	a.Browse(context.Background(), 8099)
	a.OpenAll(context.Background())
	m := view.last()
	if m.CanLogin || m.CanLogout || m.CanOpenAll || (len(m.Tunnels) > 0 && !m.Tunnels[0].Disabled) {
		t.Fatalf("actions offered during the update: %+v", m)
	}
	close(release)
	a.Wait()
	for _, p := range []string{"--askpass up", "--askpass login", "--askpass browse"} {
		if got := u.called(p); len(got) != 0 {
			t.Fatalf("%s ran during the update", p)
		}
	}
}

// The other way round: an update is not admitted while an action runs.
func TestGateRefusesUpdateDuringAction(t *testing.T) {
	release := make(chan struct{})
	exe := trayExe(t)
	u := newUpdateCLI(t, checkingStatus(true), offerDoc(exe))
	inner := u.fakeCLI.handler
	u.fakeCLI.handler = func(args []string) (string, string, int) {
		if strings.HasPrefix(strings.Join(args, " "), "--askpass login") {
			<-release
			return "", "", 0
		}
		if args[0] == "self-update" && args[1] != "--check" {
			t.Error("update ran during login")
		}
		return inner(args)
	}
	c := &clock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	a, view := startApp(t, u, exe, c)
	c.add(2 * time.Minute)
	poll(t, a)
	a.Login(context.Background())
	if !view.last().UpdateDisabled {
		t.Fatal("update item enabled during login")
	}
	a.Update()
	close(release)
	a.Wait()
}

// hpctun replaces its own binary during an update, so a status poll may
// fail meanwhile; the item still shows, disabled, for the whole update.
func TestUpdatingStaysVisibleWhenStatusFails(t *testing.T) {
	release := make(chan struct{})
	a, view, u := offered(t, func(args []string) (string, string, int) { <-release; return updatedDoc(t, true), "", 0 })
	a.Relaunch = func() error { return nil }
	a.Quit = func() {}
	a.Update()
	eventually(t, "updating", func() bool { return strings.HasPrefix(view.last().Update, "Updating to ") })
	u.mu.Lock()
	u.stFail = true
	u.mu.Unlock()
	poll(t, a)
	m := view.last()
	if m.Update != "Updating to v0.20261202-4d5e6f7…" || !m.UpdateDisabled {
		t.Fatalf("model %+v", m)
	}
	close(release)
	a.Wait()
}
