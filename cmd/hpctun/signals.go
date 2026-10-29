package main

import (
	"os"
	"os/signal"
	"syscall"
)

// holdSignals ignores Ctrl-C and SIGTERM until the returned func runs, so
// an interrupt cannot land between a swap's two renames. Closing a Windows
// console still ends the process; Go cannot hold that off.
func holdSignals() func() {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	signal.Ignore(sigs...)
	return func() { signal.Reset(sigs...) }
}
