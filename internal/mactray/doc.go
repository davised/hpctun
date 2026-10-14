// Package mactray is the macOS menu-bar item hpctun-tray shows, without
// cgo: an NSStatusItem and its NSMenu reached through purego's objc
// package. Its calls match the parts of fyne.io/systray that hpctun-tray
// uses, so the tray picks one or the other by platform.
// It is empty except on macOS.
package mactray
