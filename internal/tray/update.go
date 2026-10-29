package tray

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"hpctun/internal/jsondoc"
)

// now is the wall clock without its monotonic reading, which on macOS
// stops while the machine sleeps; deadlines compared with it are days of
// wall time, not of awake time.
func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().Round(0)
}

// maybeCheck starts an update check if one is due. It runs after every
// status poll, so sleep cannot stretch the schedule.
func (a *App) maybeCheck() {
	a.mu.Lock()
	if a.s.status == nil {
		// A failed poll says nothing about the setting: keep the offer
		// (the menu hides it meanwhile) and start no check.
		a.mu.Unlock()
		return
	}
	if !a.s.status.UpdateCheck {
		hide := a.s.offer != "" && !a.s.updating
		if hide {
			a.s.offer = ""
		}
		a.s.gen++ // a check still running is dropped
		a.mu.Unlock()
		if hide {
			// Not deferred: show takes the lock.
			a.show()
		}
		return
	}
	defer a.mu.Unlock()
	if a.s.updating || a.s.checking {
		return
	}
	now := a.now()
	due := a.s.next
	if due.IsZero() {
		due = a.started.Add(a.CheckDelay)
	}
	if !a.s.lastTry.IsZero() && a.s.lastTry.After(now.Add(time.Hour)) {
		due = now // the clock was set back
	}
	if now.Before(due) {
		return
	}
	a.s.checking, a.s.lastTry = true, now
	gen := a.s.gen
	a.checks.Add(1)
	go a.check(gen)
}

// check runs `hpctun self-update --check --json` and keeps its offer if,
// when it ends, nothing has made it stale.
func (a *App) check(gen int) {
	defer a.checks.Done()
	ctx, cancel := context.WithTimeout(context.Background(), a.StatusTimeout)
	out, errb, exit, err := a.CLI.Run(ctx, "self-update", "--check", "--json")
	cancel()
	if err == nil {
		err = errorOf(out, errb, exit)
	}
	var d jsondoc.Update
	parsed := err == nil && json.Unmarshal(out, &d) == nil
	a.mu.Lock()
	a.s.checking = false
	if gen != a.s.gen || a.s.updating {
		a.mu.Unlock()
		return
	}
	now := a.now()
	if err != nil {
		a.s.next = now.Add(time.Hour) // silent; try again in an hour
		a.mu.Unlock()
		return
	}
	a.s.next = now.Add(24 * time.Hour)
	a.s.offer = ""
	if parsed && a.offerable(d) {
		a.s.offer = d.Latest
	}
	a.mu.Unlock()
	a.show()
}

// offerable reports whether d is an update this tray can offer: a newer
// release, nothing blocking it, and an update that replaces this tray.
func (a *App) offerable(d jsondoc.Update) bool {
	return d.Version == jsondoc.Version && d.Running != "" && d.Latest != "" &&
		d.Action == "update" && d.Blocked == nil && d.Install.Tray != nil && sameFile(*d.Install.Tray, a.Exe)
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// waitCheck waits for a running check; tests use it.
func (a *App) waitCheck() { a.checks.Wait() }
