package binmerge

// Options and Run: the whole program bar the flags, so cmd/binmerge stays as
// thin as the module root's main.go. Everything user-facing goes through the
// logger's writer (stderr, in the CLI) except the dry-run cue itself, which is
// data and goes to Out — that split is what lets `-n` be piped somewhere.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Options is one invocation. Zero values are the plain merge: no split, no
// force, no dry run, output beside the cue.
type Options struct {
	CueFile  string // the cue to read; its bins are expected beside it
	Basename string // name, without extension, for the new bin/cue
	Split    bool   // reverse the operation: one bin per track
	OutDir   string // "" → the cue file's own directory
	Force    bool   // overwrite existing outputs (never inputs)
	DryRun   bool   // validate, print the cue to Out, write nothing
	PSX      bool   // check the result against what /dev_hdd0/PSXISO can mount
	Verbose  bool   // debug messages

	Out io.Writer // dry-run cue sheet; nil → os.Stdout
	Log io.Writer // progress and errors; nil → os.Stderr
}

// logger keeps the Python original's "[LEVEL]\tmessage" lines, which is what
// makes a failed run greppable in a shell history.
type logger struct {
	w       io.Writer
	verbose bool
}

func (l *logger) line(level, format string, a ...any) {
	fmt.Fprintf(l.w, "[%s]\t%s\n", level, fmt.Sprintf(format, a...))
}
func (l *logger) infof(f string, a ...any) { l.line("INFO", f, a...) }
func (l *logger) warnf(f string, a ...any) { l.line("WARNING", f, a...) }
func (l *logger) debugf(f string, a ...any) {
	if l.verbose {
		l.line("DEBUG", f, a...)
	}
}

// Run does the job described by opts. Every error it returns is meant for a
// user to read: cmd/binmerge prints it and exits 1, no stack, no wrapping.
func Run(ctx context.Context, opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	log := &logger{w: opts.Log, verbose: opts.Verbose}
	if log.w == nil {
		log.w = os.Stderr
	}

	// A flag contradiction, not a data problem: caught before the cue is even
	// read, so the answer doesn't depend on what happens to be in it.
	if opts.PSX && opts.Split {
		return fmt.Errorf("--psx and --split are contradictory: a split set is a multi-file cue, " +
			"which is exactly what the PS1 emulator can't mount. Merge instead")
	}
	// The cue names each bin in double quotes, with no escape for one: a basename
	// holding '"' would write a cue that nothing, this tool included, can read.
	if strings.ContainsRune(opts.Basename, '"') {
		return fmt.Errorf("basename %s contains a double quote, which a cue's FILE line cannot hold", opts.Basename)
	}

	cuePath := resolvePath(opts.CueFile)
	if st, err := os.Stat(cuePath); err != nil || !st.Mode().IsRegular() {
		return fmt.Errorf("cue file not found: %s", cuePath)
	}

	outDir := filepath.Dir(cuePath)
	if opts.OutDir != "" {
		outDir = resolvePath(opts.OutDir)
	}

	log.infof("Reading cue: %s", cuePath)
	sheet, err := ParseCue(cuePath)
	if err != nil {
		return err
	}
	blocksize, err := resolveBlocksize(sheet.Files)
	if err != nil {
		return err
	}
	log.debugf("Sector size resolved to %d bytes", blocksize)
	if err := validateBinSizes(sheet.Files, blocksize); err != nil {
		return err
	}
	if err := validateIndexes(sheet.Files, blocksize); err != nil {
		return err
	}
	for _, f := range sheet.Files {
		var tracks []string
		for _, t := range f.Tracks {
			tracks = append(tracks, fmt.Sprintf("track %d %s", t.Number, t.Type))
		}
		log.debugf("File %s (%d bytes): %s", filepath.Base(f.Path), f.Size, strings.Join(tracks, ", "))
	}

	newCue := filepath.Join(outDir, opts.Basename+".cue")
	inputs := []string{cuePath}
	for _, f := range sheet.Files {
		inputs = append(inputs, f.Path)
	}

	var (
		cuesheet string
		outPaths []string
		outputs  []splitOutput
		newBin   string
	)
	if opts.Split {
		if len(sheet.Files) != 1 {
			return fmt.Errorf("--split needs a cue with a single bin file; this one references %d", len(sheet.Files))
		}
		merged := &sheet.Files[0]
		trackLengths(merged, blocksize)
		for _, t := range merged.Tracks {
			if t.Sectors == 0 { // an empty bin: no cue can index it, so the set never merges back
				return fmt.Errorf("Track %d has no sectors: it starts where the next track starts", t.Number)
			}
			outputs = append(outputs, splitOutput{
				Track: t,
				Path:  filepath.Join(outDir, trackFilename(opts.Basename, t.Number, len(merged.Tracks))),
			})
		}
		cuesheet = splitCue(opts.Basename, sheet)
		outPaths = []string{newCue}
		for _, o := range outputs {
			outPaths = append(outPaths, o.Path)
		}
	} else {
		if len(sheet.Files) == 1 {
			log.warnf("Cue only references one bin file; output will be a plain copy")
		}
		newBin = filepath.Join(outDir, opts.Basename+".bin")
		cuesheet = mergedCue(opts.Basename, sheet, blocksize)
		outPaths = []string{newCue, newBin}
	}

	if opts.PSX {
		psxWarnings(opts, blocksize, log)
	}

	if err := checkOutputs(outPaths, inputs, opts.Force); err != nil {
		return err
	}

	if opts.DryRun {
		log.infof("Dry run — would write:\n  %s", strings.Join(outPaths, "\n  "))
		if _, err := io.WriteString(out, cuesheet); err != nil {
			return err
		}
		return nil
	}

	log.infof("Output directory: %s", outDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("could not create output directory: %w", err)
	}

	if opts.Split {
		log.infof("Splitting into %d bin files...", len(outputs))
		if err := splitFile(ctx, sheet.Files[0], outputs, blocksize, log); err != nil {
			return err
		}
		log.infof("Wrote %d bin files", len(outputs))
	} else {
		log.infof("Merging %d bin files...", len(sheet.Files))
		if err := mergeFiles(ctx, newBin, sheet.Files, log); err != nil {
			return err
		}
		log.infof("Wrote %s", newBin)
	}

	if err := os.WriteFile(newCue, []byte(crlf(cuesheet)), 0o644); err != nil {
		// nothing partial survives: the new bins are useless without their cue,
		// and wrong beside an old one whose offsets no longer match
		for _, p := range outPaths[1:] {
			os.Remove(p)
		}
		return err
	}
	log.infof("Wrote new cue: %s", newCue)

	if opts.PSX {
		log.infof("Copy to /dev_hdd0/PSXISO/ on the console:\n  %s\n  %s",
			filepath.Base(newBin), filepath.Base(newCue))
	}
	return nil
}

// psxWarnings is the rest of --psx: checks, not magic. The merge itself is
// unchanged — a cue is a cue — but these are the ways a technically valid
// result still won't mount from /dev_hdd0/PSXISO. Warnings, not errors: both
// are judgements about a console this can't see, and being wrong about one
// shouldn't cost someone their merge.
func psxWarnings(opts Options, blocksize int, log *logger) {
	if blocksize != 2352 {
		log.warnf("Sector size is %d, not 2352: this is a cooked rip, and the PS3's PS1 "+
			"emulator wants raw sectors. Expect it not to boot", blocksize)
	}
	// webMAN mounts by URL path (/mount.ps3/dev_hdd0/PSXISO/<name>), so a name
	// carrying URL syntax gets truncated or mis-decoded on the way through.
	var bad []string
	for _, r := range "#?%" {
		if strings.ContainsRune(opts.Basename, r) {
			bad = append(bad, string(r))
		}
	}
	if len(bad) > 0 {
		log.warnf("Basename contains %s: webMAN mounts by URL path, so these break the mount. "+
			"Rename before copying to the console", strings.Join(bad, " "))
	}
}
