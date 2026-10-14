package main

import (
	"fmt"
	"io"
	"strings"

	"hpctun/internal/config"
	"hpctun/internal/tunnel"
)

func describe(s tunnel.Spec) string {
	if s.Route == config.RouteSocks {
		return fmt.Sprintf("%s (SOCKS %d)", s.Label(), s.Port)
	}
	return fmt.Sprintf("%s (%d → %s, %s)", s.Label(), s.Port, s.Target(), s.Route)
}

// report prints one line per result, with indented detail, and returns how
// many failed.
func report(w io.Writer, results []tunnel.Result) int {
	for _, r := range results {
		fmt.Fprintf(w, "%s: %s\n", describe(r.Spec), r.Outcome)
		if r.Detail != "" {
			for line := range strings.SplitSeq(r.Detail, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
	return countFailed(results)
}

func countFailed(results []tunnel.Result) int {
	n := 0
	for _, r := range results {
		if r.Outcome == tunnel.Failed {
			n++
		}
	}
	return n
}
