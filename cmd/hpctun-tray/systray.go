package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"

	"hpctun/internal/tray"
)

// slots is how many tunnels the menu has room for; more are summed up in
// one line. Menu items are made once and shown or hidden, which every
// platform's tray handles, rather than added and removed.
const slots = 24

func run(cli tray.CLI, auto tray.Autostart) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := &view{auto: auto, icon: -1}
	app := &tray.App{CLI: cli, View: v}
	v.app = app
	trayRun(func() { v.build(ctx); go app.Run(ctx) }, cancel)
	return nil
}

// view is the tray menu.
type view struct {
	app  *tray.App
	auto tray.Autostart

	header, login, logout *menuItem
	conns                 [2]*menuItem // login gateway, transfer host
	tunnels               [slots]*menuItem
	more                  *menuItem
	pages                 [slots]*menuItem
	openAll, closeAll     *menuItem
	code                  *menuItem
	lastErr               *menuItem
	openConfig, startUp   *menuItem
	quit                  *menuItem

	mu        sync.Mutex
	ports     [slots]int // the port each tunnel slot shows
	pagePorts [slots]int // the port each "Open in browser" slot opens
	config    string
	icon      tray.Icon
}

func (v *view) build(ctx context.Context) {
	tpl, reg := tray.Icons(tray.IconOff)
	setTemplateIcon(tpl, reg)
	setTooltip("hpctun")
	v.header = addMenuItem("hpctun: checking…", "")
	v.header.Disable()
	for i := range v.conns {
		v.conns[i] = addMenuItem("", "")
		v.conns[i].Disable()
		v.conns[i].Hide()
	}
	v.login = addMenuItem("Log in", "Open the connection to the gateway; ssh asks for your password in a window")
	v.logout = addMenuItem("Log out", "Close the gateway connection and every tunnel on it")
	addSeparator()
	for i := range v.tunnels {
		v.tunnels[i] = addMenuItemCheckbox("", "Click to open or close", false)
		v.tunnels[i].Hide()
	}
	v.more = addMenuItem("", "")
	v.more.Disable()
	v.more.Hide()
	v.openAll = addMenuItem("Open all", "Open every tunnel (hpctun up --all)")
	v.closeAll = addMenuItem("Close all", "Close every tunnel (hpctun down --all)")
	addSeparator()
	for i := range v.pages {
		v.pages[i] = addMenuItem("", "Open in a browser, opening the tunnel first if needed")
		v.pages[i].Hide()
	}
	addSeparator()
	v.code = addMenuItem("", "")
	v.code.Disable()
	v.code.Hide()
	v.lastErr = addMenuItem("", "Click to dismiss")
	v.lastErr.Hide()
	v.openConfig = addMenuItem("Open config", "Edit hpctun's config file")
	v.startUp = addMenuItemCheckbox("Start at login", "Start hpctun-tray when you log in", v.auto.Enabled())
	v.quit = addMenuItem("Quit", "Quit hpctun-tray; tunnels stay open")

	on := func(item *menuItem, f func()) {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-item.ClickedCh:
					f()
				}
			}
		}()
	}
	on(v.login, func() { v.app.Login(ctx) })
	on(v.logout, func() { v.app.Logout(ctx) })
	for i, item := range v.tunnels {
		on(item, func() {
			v.mu.Lock()
			port := v.ports[i]
			v.mu.Unlock()
			if port != 0 {
				v.app.Toggle(ctx, port)
			}
		})
	}
	for i, item := range v.pages {
		on(item, func() {
			v.mu.Lock()
			port := v.pagePorts[i]
			v.mu.Unlock()
			if port != 0 {
				v.app.Browse(ctx, port)
			}
		})
	}
	on(v.openAll, func() { v.app.OpenAll(ctx) })
	on(v.closeAll, func() { v.app.CloseAll(ctx) })
	on(v.lastErr, v.app.ClearError)
	on(v.openConfig, func() {
		v.mu.Lock()
		path := v.config
		v.mu.Unlock()
		if path != "" {
			argv := tray.OpenFileCommand(runtime.GOOS, path)
			exec.Command(argv[0], argv[1:]...).Start()
		}
	})
	on(v.startUp, func() {
		want := !v.startUp.Checked()
		if err := v.auto.Set(want); err != nil {
			v.app.ShowError("Start at login: " + err.Error())
			return
		}
		if want {
			v.startUp.Check()
		} else {
			v.startUp.Uncheck()
		}
	})
	on(v.quit, trayQuit)
}

// Render shows m. It only touches items whose content changed.
func (v *view) Render(m tray.Model) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if m.Icon != v.icon {
		tpl, reg := tray.Icons(m.Icon)
		setTemplateIcon(tpl, reg)
		v.icon = m.Icon
	}
	setTooltip(m.Tooltip)
	v.header.SetTitle(m.Header)
	for i, item := range v.conns {
		line := ""
		if i < len(m.Conns) {
			line = m.Conns[i]
		}
		setLine(item, line)
	}
	showIf(v.login, m.CanLogin)
	showIf(v.logout, m.CanLogout)
	for i, item := range v.tunnels {
		if i >= len(m.Tunnels) {
			v.ports[i] = 0
			item.Hide()
			continue
		}
		t := m.Tunnels[i]
		v.ports[i] = t.Port
		item.SetTitle(t.Label)
		if t.Checked {
			item.Check()
		} else {
			item.Uncheck()
		}
		if t.Disabled {
			item.Disable()
		} else {
			item.Enable()
		}
		item.Show()
	}
	if extra := len(m.Tunnels) - slots; extra > 0 {
		v.more.SetTitle(fmt.Sprintf("%d more: see hpctun status", extra))
		v.more.Show()
	} else {
		v.more.Hide()
	}
	for i, item := range v.pages {
		if i >= len(m.Pages) {
			v.pagePorts[i] = 0
			item.Hide()
			continue
		}
		p := m.Pages[i]
		v.pagePorts[i] = p.Port
		item.SetTitle(p.Label)
		enableIf(item, !p.Disabled)
		item.Show()
	}
	enableIf(v.openAll, m.CanOpenAll)
	enableIf(v.closeAll, m.CanOpenAll)
	setLine(v.code, m.Code)
	setLine(v.lastErr, m.LastError)
	v.config = m.Config
	enableIf(v.openConfig, m.Config != "")
}

func showIf(item *menuItem, show bool) {
	if show {
		item.Show()
	} else {
		item.Hide()
	}
}

func enableIf(item *menuItem, on bool) {
	if on {
		item.Enable()
	} else {
		item.Disable()
	}
}

// setLine shows text in item, or hides it when there is none.
func setLine(item *menuItem, text string) {
	if text == "" {
		item.Hide()
		return
	}
	item.SetTitle(text)
	item.Show()
}
