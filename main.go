// Command ps3top is htop for a HEN'd PS3 — see internal/ps3top for the
// program; this file only carries the build stamp into it.
package main

import (
	"fmt"
	"os"

	"github.com/lucasdaddiego/ps3top/internal/ps3top"
)

// Set via -ldflags at build time (see the Makefile).
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := ps3top.Run(ps3top.Build{Version: version, Commit: commit, Date: date}); err != nil {
		fmt.Fprintln(os.Stderr, "ps3top:", err)
		os.Exit(1)
	}
}
