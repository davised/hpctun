// Command hpctun-tray shows hpctun's tunnels in the macOS menu bar, the
// Linux system tray or the Windows notification area, and opens and closes
// them with a click. It runs the hpctun CLI for everything.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"hpctun/internal/tray"
)

func main() {
	cliPath := flag.String("hpctun", "", "the hpctun program to run (default: next to hpctun-tray, else on PATH)")
	flag.Parse()
	exe, _ := os.Executable()
	path := *cliPath
	if path == "" {
		var err error
		if path, err = tray.FindCLI(exe, exec.LookPath); err != nil {
			fmt.Fprintln(os.Stderr, "hpctun-tray:", err)
			os.Exit(1)
		}
	}
	home, _ := os.UserHomeDir()
	auto := tray.Autostart{GOOS: runtime.GOOS, Home: home, ConfigHome: os.Getenv("XDG_CONFIG_HOME"), Exe: exe}
	if err := run(tray.ExecCLI{Path: path}, auto); err != nil {
		fmt.Fprintln(os.Stderr, "hpctun-tray:", err)
		os.Exit(1)
	}
}
