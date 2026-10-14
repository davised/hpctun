// Command hello is a tiny program for scripts/macapp's tests.
package main

import (
	"fmt"
	"runtime"
)

func main() { fmt.Println("hello from", runtime.GOARCH) }
