//go:build !darwin && !windows

package main

import "fyne.io/systray"

// The system tray elsewhere; fyne.io/systray needs cgo only on macOS.
type menuItem = systray.MenuItem

var (
	trayRun             = systray.Run
	trayQuit            = systray.Quit
	setTemplateIcon     = systray.SetTemplateIcon
	setTooltip          = systray.SetTooltip
	addMenuItem         = systray.AddMenuItem
	addMenuItemCheckbox = systray.AddMenuItemCheckbox
	addSeparator        = systray.AddSeparator
)
