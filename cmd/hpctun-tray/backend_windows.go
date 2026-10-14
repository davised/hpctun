package main

import (
	"fyne.io/systray"

	"hpctun/internal/tray"
)

// The notification area on Windows, which takes ICO icons and has no
// template images.
type menuItem = systray.MenuItem

var (
	trayRun             = systray.Run
	trayQuit            = systray.Quit
	setTemplateIcon     = func(_, regular []byte) { systray.SetIcon(tray.ICO(regular)) }
	setTooltip          = systray.SetTooltip
	addMenuItem         = systray.AddMenuItem
	addMenuItemCheckbox = systray.AddMenuItemCheckbox
	addSeparator        = systray.AddSeparator
)
