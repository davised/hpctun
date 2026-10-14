package main

import "os"

// execProcess runs argv as a child and waits: Windows cannot replace a
// running process with another program.
func execProcess(argv []string) error { return runChild(argv, os.Stdin, os.Stdout, os.Stderr) }
