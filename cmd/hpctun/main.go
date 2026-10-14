// Command hpctun opens, closes and monitors ssh tunnels through a gateway host.
package main

import (
	"context"
	"os"

	"charm.land/fang/v2"
)

func main() {
	root := newRootCmd(newApp())
	err := fang.Execute(context.Background(), root, fang.WithNotifySignal(os.Interrupt), fang.WithVersion(version()), fang.WithErrorHandler(errorHandler))
	os.Exit(exitCode(err))
}
