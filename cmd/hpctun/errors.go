package main

import (
	"errors"
	"io"
	"unicode"

	"charm.land/fang/v2"
)

// exitError carries the process exit status for an error returned by a command.
type exitError struct {
	code  int
	err   error
	quiet bool // print nothing
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// failed marks err as a failed action: hpctun exits 1.
func failed(err error) error { return &exitError{code: 1, err: err} }

// usage marks err as bad usage or configuration: hpctun exits 2.
func usage(err error) error { return &exitError{code: 2, err: err} }

// quiet exits with code after the command has said what it needs to (or
// nothing, as askpass when its dialog is cancelled): no error box.
func quiet(code int, err error) error { return &exitError{code: code, err: err, quiet: true} }

// exitCode maps a command error to the process exit status. Untyped errors,
// including cobra's own flag and argument errors, exit 2.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 2
}

// errorHandler is fang's error box, except that it capitalizes the first
// word only when it is a plain word. fang title-cases whatever comes first,
// which mangles a path (/private/tmp/run.toml became /Private/Tmp/Run.toml)
// or a flag.
func errorHandler(w io.Writer, styles fang.Styles, err error) {
	var ee *exitError
	if errors.As(err, &ee) && ee.quiet {
		return
	}
	styles.ErrorText = styles.ErrorText.Transform(capitalizeWord)
	fang.DefaultErrorHandler(w, styles, err)
}

// capitalizeWord upper-cases the first letter of s when its first word is
// made of letters only.
func capitalizeWord(s string) string {
	r := []rune(s)
	i := 0
	for i < len(r) && unicode.IsSpace(r[i]) {
		i++
	}
	j := i
	for j < len(r) && !unicode.IsSpace(r[j]) {
		if !unicode.IsLetter(r[j]) {
			return s
		}
		j++
	}
	if i == j {
		return s
	}
	r[i] = unicode.ToUpper(r[i])
	return string(r)
}
