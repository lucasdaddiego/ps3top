package binmerge

// Everything that has to hold before a byte is written. The sector-size and
// index checks catch a cue that doesn't describe its own bins; the output
// guards catch the cases where writing the result would destroy the source.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// resolveBlocksize picks the one sector size the whole sheet uses. Mixing them
// on a disc isn't a thing, so a cue that appears to is either mis-edited or
// describes bins that aren't the ones on disk — either way the offsets it
// produces would be wrong, so it's an error rather than a per-track size.
func resolveBlocksize(files []BinFile) (int, error) {
	seen := map[string]int{}
	for _, f := range files {
		for _, t := range f.Tracks {
			bs, ok := blocksizes[t.Type]
			if !ok {
				return 0, fmt.Errorf("unsupported track type %s (track %d)", t.Type, t.Number)
			}
			seen[t.Type] = bs
		}
	}
	if len(seen) == 0 {
		return 0, fmt.Errorf("cue sheet contains no tracks")
	}
	types := make([]string, 0, len(seen))
	for k := range seen {
		types = append(types, k)
	}
	sort.Strings(types)
	blocksize := seen[types[0]]
	var detail []string
	mixed := false
	for _, k := range types {
		detail = append(detail, fmt.Sprintf("%s=%d", k, seen[k]))
		if seen[k] != blocksize {
			mixed = true
		}
	}
	if mixed {
		return 0, fmt.Errorf("cue mixes track modes with different sector sizes (%s)", strings.Join(detail, ", "))
	}
	return blocksize, nil
}

// validateBinSizes catches a bin that isn't a whole number of sectors — the
// signature of a truncated download or a half-finished copy, and the reason to
// stop before an offset computed from its size lands mid-sector.
func validateBinSizes(files []BinFile, blocksize int) error {
	for _, f := range files {
		if f.Size%int64(blocksize) != 0 {
			return fmt.Errorf("size of %s (%d bytes) is not a multiple of the sector size (%d); the file is truncated or corrupt",
				filepath.Base(f.Path), f.Size, blocksize)
		}
	}
	return nil
}

// validateIndexes requires index offsets to be non-decreasing and to lie
// inside their own bin file. A backwards index would make a split write a
// negative length; one past the end would read off the end of the file.
func validateIndexes(files []BinFile, blocksize int) error {
	for _, f := range files {
		fileSectors := int(f.Size / int64(blocksize))
		prev := 0
		for _, t := range f.Tracks {
			for _, i := range t.Indexes {
				if i.Offset < prev {
					return fmt.Errorf("cue indexes go backwards at track %d INDEX %02d", t.Number, i.Number)
				}
				if i.Offset >= fileSectors {
					return fmt.Errorf("Track %d INDEX %02d points at sector %d, beyond the end of %s (%d sectors)",
						t.Number, i.Number, i.Offset, filepath.Base(f.Path), fileSectors)
				}
				prev = i.Offset
			}
		}
	}
	return nil
}

// resolvePath is Python's Path.resolve() for a path that may not exist yet:
// symlinks are followed as far as the filesystem goes, and what's left is
// appended. The distinction matters because the outputs being checked below
// don't exist — and one of them ("../NAME") only aliases an input once ".." is
// collapsed against the *real* directory.
func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	for cur := abs; ; {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur { // hit the root without an existing ancestor
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// sameFile reports whether both paths exist and name the same file. A missing
// or unstattable path is not the same file as anything.
func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

// checkOutputs is the guard that stands between a run and an unrecoverable
// one. Three separate refusals, in order of how bad they'd be:
//
//   - an output that IS an input. Path strings are not identity: on a
//     case-insensitive filesystem (the macOS APFS default) resolving doesn't
//     canonicalise case, and hardlinks or NFC/NFD-differing names never compare
//     equal either — so inodes are compared as well as paths. Writing such an
//     output truncates the source it aliases, which --force must never allow.
//   - two outputs landing on one path. trackFilename is derived from the track
//     number, so a cue repeating one maps both tracks to the same name: the
//     split writes it twice, the earlier track's sectors are lost, and the run
//     still reports success. --force can't excuse that either.
//   - an output that already exists. This one is what --force is for.
func checkOutputs(outputs, inputs []string, force bool) error {
	resolvedInputs := map[string]bool{}
	for _, in := range inputs {
		resolvedInputs[resolvePath(in)] = true
	}
	var clashes []string
	for _, out := range outputs {
		r := resolvePath(out)
		if resolvedInputs[r] {
			clashes = append(clashes, out)
			continue
		}
		for _, in := range inputs {
			if sameFile(r, in) {
				clashes = append(clashes, out)
				break
			}
		}
	}
	if len(clashes) > 0 {
		return fmt.Errorf("output would overwrite an input file (--force does not allow this):\n  %s",
			strings.Join(clashes, "\n  "))
	}

	count := map[string]int{}
	for _, out := range outputs {
		count[resolvePath(out)]++
	}
	var dupes []string
	for _, out := range outputs {
		if count[resolvePath(out)] > 1 {
			dupes = append(dupes, out)
		}
	}
	if len(dupes) > 0 {
		return fmt.Errorf("two or more outputs would be written to the same path (does the cue repeat a track number?):\n  %s",
			strings.Join(uniqueSorted(dupes), "\n  "))
	}

	if force {
		return nil
	}
	var existing []string
	for _, out := range outputs {
		// Lstat, not Stat: a broken symlink is still something a write would
		// clobber, and it's exactly what a previous half-cleaned run leaves.
		if _, err := os.Lstat(out); err == nil {
			existing = append(existing, out)
		}
	}
	if len(existing) > 0 {
		return fmt.Errorf("refusing to overwrite existing file(s) (use --force to allow):\n  %s",
			strings.Join(existing, "\n  "))
	}
	return nil
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s], out = true, append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
