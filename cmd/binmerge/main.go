// Command binmerge merges a cue sheet's bin files into a single bin, writing
// a corrected cue; --split reverses it. It ships alongside ps3top because it's
// the prep step for the PSX tab — the PS1 emulator mounts one bin per disc,
// Redump dumps arrive as one bin per track — but it's a plain cue/bin tool and
// stays useful without a console anywhere near it.
//
// A separate binary on purpose: ps3top is a TUI and nothing else, and bolting
// a batch subcommand onto it would be the first crack in that.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lucasdaddiego/ps3top/internal/binmerge"
)

// Set via -ldflags at build time (see the Makefile), same as ps3top's.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// usageText is the flag reference, written out rather than left to
// flag.PrintDefaults: every flag here has a short alias, and PrintDefaults
// lists the two halves as separate entries in alphabetical order, which reads
// as twice as many flags as there are.
const usageText = `Usage: binmerge [flags] CUEFILE BASENAME

Using a cue sheet, merges its bin files into a single bin and writes a new cue
sheet with corrected offsets. Works great with Redump dumps. Supports all
sector modes, but only BINARY track files.

  CUEFILE   path to the cue file (bin files are expected in the same directory)
  BASENAME  name (without extension) for the new bin/cue files

Flags:
  -s, --split        reverse the operation: split a merged bin back into one
                     bin per track
  -o, --outdir DIR   output directory (created if needed); defaults to the cue
                     file's directory
  -f, --force        overwrite existing output files (inputs are never
                     overwritten)
  -n, --dry-run      validate, print the generated cue sheet to stdout, and
                     write nothing
      --psx          check the result against what /dev_hdd0/PSXISO can mount:
                     refuses --split, warns on a cooked rip or a basename
                     webMAN can't mount, and names the files to copy over
  -v, --verbose      print debug messages
  -V, --version      print version
`

func usage() { fmt.Fprint(flag.CommandLine.Output(), usageText) }

func main() {
	// Descriptions live in usageText, which is what --help prints; each long
	// flag gets the short alias the tool has always taken.
	split := flag.Bool("split", false, "")
	outdir := flag.String("outdir", "", "")
	force := flag.Bool("force", false, "")
	dryRun := flag.Bool("dry-run", false, "")
	psx := flag.Bool("psx", false, "")
	verbose := flag.Bool("verbose", false, "")
	ver := flag.Bool("version", false, "")
	flag.BoolVar(split, "s", false, "")
	flag.StringVar(outdir, "o", "", "")
	flag.BoolVar(force, "f", false, "")
	flag.BoolVar(dryRun, "n", false, "")
	flag.BoolVar(verbose, "v", false, "")
	flag.BoolVar(ver, "V", false, "")
	flag.CommandLine.Usage = usage

	// stdlib flag stops at the first positional, but this CLI has always taken
	// `binmerge game.cue merged -o out`. Re-parse what's left after each one.
	flag.Parse()
	var positional []string
	for flag.NArg() > 0 {
		positional = append(positional, flag.Arg(0))
		flag.CommandLine.Parse(flag.Args()[1:])
	}

	if *ver {
		fmt.Printf("binmerge %s (ps3top %s, %s, built %s)\n", binmerge.Version, version, commit, date)
		return
	}
	if len(positional) != 2 {
		usage()
		os.Exit(2)
	}

	// Ctrl-C has to reach Run rather than the process: a merge killed mid-copy
	// leaves a bin that looks like a dump and isn't, so the copy loops watch
	// this context and remove what they'd written. A second Ctrl-C stops being
	// polite (the first one unregisters the handler) and kills it outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); stop() }() // the first signal unregisters: the next one kills

	err := binmerge.Run(ctx, binmerge.Options{
		CueFile:  positional[0],
		Basename: positional[1],
		Split:    *split,
		OutDir:   *outdir,
		Force:    *force,
		DryRun:   *dryRun,
		PSX:      *psx,
		Verbose:  *verbose,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "[ERROR]\tInterrupted\n")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "[ERROR]\t%s\n", err)
		os.Exit(1)
	}
}
