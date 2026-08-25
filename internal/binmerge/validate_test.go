package binmerge

// The guards. The output ones are the reason this file exists: everything else
// here fails a run, but a missed alias overwrites the dump being read, and
// there is no second copy of someone's disc.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBlocksize(t *testing.T) {
	files := func(types ...string) []BinFile {
		f := BinFile{}
		for i, ty := range types {
			f.Tracks = append(f.Tracks, Track{Number: i + 1, Type: ty})
		}
		return []BinFile{f}
	}
	for _, tc := range []struct {
		name  string
		files []BinFile
		want  int
		err   string
	}{
		{name: "all audio", files: files("AUDIO", "AUDIO"), want: 2352},
		{name: "data plus audio, same size", files: files("MODE2/2352", "AUDIO"), want: 2352},
		{name: "cooked data", files: files("MODE1/2048"), want: 2048},
		{name: "cd+g", files: files("CDG"), want: 2448},
		{name: "mixed sizes", files: files("MODE1/2048", "AUDIO"), err: "sector sizes"},
		{name: "unknown type", files: files("MODE2/2324"), err: "Unsupported track type"},
		{name: "no tracks", files: []BinFile{{}}, err: "no tracks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBlocksize(tc.files)
			switch {
			case tc.err != "":
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Errorf("err = %v, want it to contain %q", err, tc.err)
				}
			case err != nil:
				t.Errorf("unexpected error: %v", err)
			case got != tc.want:
				t.Errorf("blocksize = %d, want %d", got, tc.want)
			}
		})
	}

	// The mixed-size message names both modes and their sizes, because "these
	// don't agree" is useless without saying which two.
	_, err := resolveBlocksize(files("MODE1/2048", "AUDIO"))
	if !strings.Contains(err.Error(), "AUDIO=2352") || !strings.Contains(err.Error(), "MODE1/2048=2048") {
		t.Errorf("err = %v, want both modes and sizes named", err)
	}
}

// A bin that isn't a whole number of sectors is truncated or corrupt, and
// every offset computed from its size would land mid-sector.
func TestValidateBinSizes(t *testing.T) {
	files := []BinFile{{Path: "/tmp/game (Track 1).bin", Size: 100 * bs2352}}
	if err := validateBinSizes(files, bs2352); err != nil {
		t.Fatal(err)
	}
	files[0].Size -= 100
	err := validateBinSizes(files, bs2352)
	if err == nil || !strings.Contains(err.Error(), "not a multiple") {
		t.Fatalf("err = %v, want the truncation message", err)
	}
	if !strings.Contains(err.Error(), "game (Track 1).bin") {
		t.Errorf("err = %v, want the offending file named", err)
	}
}

func TestValidateIndexes(t *testing.T) {
	sheet := func(offsets ...int) []BinFile {
		f := BinFile{Path: "/tmp/m.bin", Size: 100 * bs2352}
		for i, o := range offsets {
			f.Tracks = append(f.Tracks, Track{Number: i + 1, Indexes: []Index{{1, o}}})
		}
		return []BinFile{f}
	}
	if err := validateIndexes(sheet(0, 50, 99), bs2352); err != nil {
		t.Fatalf("a valid ascending cue was rejected: %v", err)
	}
	if err := validateIndexes(sheet(0, 60, 50), bs2352); err == nil || !strings.Contains(err.Error(), "backwards") {
		t.Errorf("err = %v, want the backwards-index message", err)
	}
	// 100 sectors means the last addressable one is 99.
	if err := validateIndexes(sheet(0, 100), bs2352); err == nil || !strings.Contains(err.Error(), "beyond the end") {
		t.Errorf("err = %v, want the past-EOF message", err)
	}
	// Offsets are per-file, so a second file starting back at 0 isn't backwards.
	two := append(sheet(0, 50), sheet(0, 50)...)
	if err := validateIndexes(two, bs2352); err != nil {
		t.Errorf("a second file restarting at sector 0 was read as backwards: %v", err)
	}
}

// resolvePath has to work on a path that doesn't exist yet — that's the whole
// case it's for — while still following symlinks as far as the filesystem
// goes, so an output under a symlinked directory is recognised as the file it
// would actually land on.
func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if got, want := resolvePath(filepath.Join(link, "new.bin")), filepath.Join(resolvePath(real), "new.bin"); got != want {
		t.Errorf("resolvePath through a symlink = %q, want %q", got, want)
	}
	// ".." collapses against the real directory, which is what makes a
	// "../NAME" basename land where checkOutputs can see it.
	if got, want := resolvePath(filepath.Join(dir, "missing", "..", "x.bin")), filepath.Join(resolvePath(dir), "x.bin"); got != want {
		t.Errorf("resolvePath through a missing dir = %q, want %q", got, want)
	}
}

// Path strings are not identity. A hardlink, or a case-only difference on the
// case-insensitive filesystem macOS ships by default, names the same inode
// under a name that compares unequal — and writing it truncates the source
// this run is reading. --force must not be able to authorise that.
func TestCheckOutputsRefusesAliasedInputs(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "game (Track 1).bin")
	makeBin(t, input, 2, 1)

	for _, force := range []bool{false, true} {
		if err := checkOutputs([]string{input}, []string{input}, force); err == nil ||
			!strings.Contains(err.Error(), "input") {
			t.Errorf("force=%v: err = %v, want the input-overwrite refusal", force, err)
		}
	}

	alias := filepath.Join(dir, "alias.bin")
	if err := os.Link(input, alias); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputs([]string{alias}, []string{input}, true); err == nil ||
		!strings.Contains(err.Error(), "input") {
		t.Errorf("hardlinked output: err = %v, want the input-overwrite refusal", err)
	}

	upper := filepath.Join(dir, "GAME (TRACK 1).BIN")
	if _, err := os.Stat(upper); err == nil { // only on a case-insensitive filesystem
		if err := checkOutputs([]string{upper}, []string{input}, true); err == nil ||
			!strings.Contains(err.Error(), "input") {
			t.Errorf("case-only output: err = %v, want the input-overwrite refusal", err)
		}
	} else {
		t.Log("case-only alias not testable: filesystem is case-sensitive")
	}
}

// Two outputs on one path is the silent one: trackFilename comes from the
// track number, so a cue repeating one writes the same file twice, the first
// track's sectors are lost, and the run still says it wrote both.
func TestCheckOutputsRefusesDuplicates(t *testing.T) {
	dir := t.TempDir()
	dup := filepath.Join(dir, "dup (Track 1).bin")
	for _, force := range []bool{false, true} {
		err := checkOutputs([]string{filepath.Join(dir, "dup.cue"), dup, dup}, []string{filepath.Join(dir, "m.bin")}, force)
		if err == nil || !strings.Contains(err.Error(), "same path") {
			t.Errorf("force=%v: err = %v, want the duplicate-output refusal", force, err)
		}
	}
}

// Existing outputs are what --force is actually for. A broken symlink counts
// as existing: it's still something a write would clobber, and it's exactly
// what a half-cleaned earlier run leaves behind.
func TestCheckOutputsExisting(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "merged.bin")
	fresh := filepath.Join(dir, "new.bin")
	makeBin(t, out, 1, 1)
	inputs := []string{filepath.Join(dir, "src.bin")}

	if err := checkOutputs([]string{out}, inputs, false); err == nil ||
		!strings.Contains(err.Error(), "Refusing to overwrite") {
		t.Errorf("err = %v, want the clobber refusal", err)
	}
	if err := checkOutputs([]string{out}, inputs, true); err != nil {
		t.Errorf("--force should allow an existing output: %v", err)
	}
	if err := checkOutputs([]string{fresh}, inputs, false); err != nil {
		t.Errorf("a fresh path was refused: %v", err)
	}

	broken := filepath.Join(dir, "broken.bin")
	if err := os.Symlink(filepath.Join(dir, "gone"), broken); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputs([]string{broken}, inputs, false); err == nil {
		t.Error("a broken symlink was treated as a free path")
	}
}
