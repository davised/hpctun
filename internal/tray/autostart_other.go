//go:build !windows

package tray

import "errors"

var errNotWindows = errors.New("the registry exists only on Windows")

func readRunValue() (string, bool) { return "", false }
func writeRunValue(string) error   { return errNotWindows }
func deleteRunValue() error        { return errNotWindows }
