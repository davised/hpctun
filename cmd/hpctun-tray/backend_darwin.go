//go:build darwin

package main

import "hpctun/internal/mactray"

// The menu bar on macOS, without cgo (internal/mactray).
type menuItem = mactray.MenuItem

var (
	trayRun             = mactray.Run
	trayQuit            = mactray.Quit
	setTemplateIcon     = mactray.SetTemplateIcon
	setTooltip          = mactray.SetTooltip
	addMenuItem         = mactray.AddMenuItem
	addMenuItemCheckbox = mactray.AddMenuItemCheckbox
	addSeparator        = mactray.AddSeparator
)
