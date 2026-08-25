package binmerge

// End-to-end runs, plus the fixtures the rest of the suite shares. The shape
// is the Redump one this exists for: three tracks in three bins, a data track
// and two audio, all 2352-byte sectors.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bs2352 = 2352

const gameCue = `FILE "game (Track 1).bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "game (Track 2).bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
FILE "game (Track 3).bin" BINARY
  TRACK 03 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
`

// makeBin writes `sectors` sectors of a seeded byte pattern — distinct per
// track, so a merge that reorders or a split that mis-seeks shows up as
// different bytes rather than a different length.
func makeBin(t *testing.T, path string, sectors, seed int) {
	t.Helper()
	sector := make([]byte, bs2352)
	for i := range sector {
		sector[i] = byte((seed + i) % 256)
	}
	data := bytes.Repeat(sector, sectors)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture lays out the three-track set in a temp dir and returns it.
func fixture(t *testing.T, cue string) string {
	t.Helper()
	dir := t.TempDir()
	makeBin(t, filepath.Join(dir, "game (Track 1).bin"), 100, 1)
	makeBin(t, filepath.Join(dir, "game (Track 2).bin"), 200, 77)
	makeBin(t, filepath.Join(dir, "game (Track 3).bin"), 175, 200)
	if err := os.WriteFile(filepath.Join(dir, "game.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// exec runs with stdout/stderr captured, the way the CLI drives it.
func exec(t *testing.T, opts Options) (stdout, stderr string, err error) {
	t.Helper()
	var out, log bytes.Buffer
	opts.Out, opts.Log = &out, &log
	err = Run(context.Background(), opts)
	return out.String(), log.String(), err
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A merge is a concatenation in cue order, and the new cue carries every INDEX
// pushed out by the sectors now in front of it — 100 sectors is 00:01:25, so
// track 2's INDEX 00 lands there and its INDEX 01 two seconds later. The cue
// goes to disk CRLF, which is the format's convention and what dumps carry.
func TestMerge(t *testing.T) {
	dir := fixture(t, gameCue)
	out := filepath.Join(dir, "out")
	if _, _, err := exec(t, Options{CueFile: filepath.Join(dir, "game.cue"), Basename: "merged", OutDir: out}); err != nil {
		t.Fatal(err)
	}

	var want []byte
	for _, n := range []string{"1", "2", "3"} {
		want = append(want, read(t, filepath.Join(dir, "game (Track "+n+").bin"))...)
	}
	if got := read(t, filepath.Join(out, "merged.bin")); !bytes.Equal(got, want) {
		t.Errorf("merged bin is %d bytes, want the %d-byte concatenation of the inputs", len(got), len(want))
	}

	wantCue := "FILE \"merged.bin\" BINARY\r\n" +
		"  TRACK 01 MODE2/2352\r\n    INDEX 01 00:00:00\r\n" +
		"  TRACK 02 AUDIO\r\n    INDEX 00 00:01:25\r\n    INDEX 01 00:03:25\r\n" +
		"  TRACK 03 AUDIO\r\n    INDEX 00 00:04:00\r\n    INDEX 01 00:06:00\r\n"
	if got := string(read(t, filepath.Join(out, "merged.cue"))); got != wantCue {
		t.Errorf("merged cue =\n%q\nwant\n%q", got, wantCue)
	}
}

// The whole point of --split existing: merge then split has to give back the
// exact bytes and the exact cue that went in. Anything less and the merge is a
// one-way door for someone's only copy of a dump.
func TestSplitRoundTrip(t *testing.T) {
	dir := fixture(t, gameCue)
	out, rt := filepath.Join(dir, "out"), filepath.Join(dir, "rt")
	if _, _, err := exec(t, Options{CueFile: filepath.Join(dir, "game.cue"), Basename: "merged", OutDir: out}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exec(t, Options{CueFile: filepath.Join(out, "merged.cue"), Basename: "game", Split: true, OutDir: rt}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"1", "2", "3"} {
		name := "game (Track " + n + ").bin"
		if !bytes.Equal(read(t, filepath.Join(rt, name)), read(t, filepath.Join(dir, name))) {
			t.Errorf("%s: round-tripped bytes differ from the source", name)
		}
	}
	if got, want := string(read(t, filepath.Join(rt, "game.cue"))), crlf(gameCue); got != want {
		t.Errorf("round-tripped cue =\n%q\nwant\n%q", got, want)
	}
}

// A dry run has to be free: it validates and shows the cue it would write, on
// stdout so it can be piped, and touches nothing — not even the output
// directory, whose absence is the cheapest proof nothing happened.
func TestDryRunWritesNothing(t *testing.T) {
	dir := fixture(t, gameCue)
	out := filepath.Join(dir, "out")
	stdout, stderr, err := exec(t, Options{CueFile: filepath.Join(dir, "game.cue"), Basename: "merged", OutDir: out, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `FILE "merged.bin" BINARY`) {
		t.Errorf("dry run stdout didn't carry the cue: %q", stdout)
	}
	if strings.Contains(stdout, "\r\n") {
		t.Error("dry run printed CRLF; the on-disk cue is CRLF, the printed one shouldn't be")
	}
	if !strings.Contains(stderr, "Dry run") {
		t.Errorf("dry run said nothing on stderr: %q", stderr)
	}
	if exists(out) {
		t.Error("dry run created the output directory")
	}
}

// --force is for outputs and only outputs. An input named as the output is
// refused with it, because the alternative is truncating the source mid-read
// and reporting success.
func TestForceOverwritesOutputsNotInputs(t *testing.T) {
	dir := fixture(t, gameCue)
	out := filepath.Join(dir, "out")
	cue := filepath.Join(dir, "game.cue")
	if _, _, err := exec(t, Options{CueFile: cue, Basename: "merged", OutDir: out}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exec(t, Options{CueFile: cue, Basename: "merged", OutDir: out}); err == nil {
		t.Fatal("second run overwrote an existing output without --force")
	} else if !strings.Contains(err.Error(), "Refusing to overwrite") {
		t.Errorf("error = %v, want a refusal to clobber", err)
	}
	if _, _, err := exec(t, Options{CueFile: cue, Basename: "merged", OutDir: out, Force: true}); err != nil {
		t.Fatalf("--force didn't overwrite the output: %v", err)
	}
	if got, want := len(read(t, filepath.Join(out, "merged.bin"))), (100+200+175)*bs2352; got != want {
		t.Errorf("overwritten bin = %d bytes, want %d", got, want)
	}

	// Now aim the output at an input, with --force set.
	_, _, err := exec(t, Options{CueFile: cue, Basename: "game (Track 1)", OutDir: dir, Force: true})
	if err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("error = %v, want a refusal to overwrite an input", err)
	}
	if got, want := len(read(t, filepath.Join(dir, "game (Track 1).bin"))), 100*bs2352; got != want {
		t.Errorf("input bin = %d bytes, want %d — it was written to", got, want)
	}
}

// Every fatal path, and the message a user greps for. The last field is the
// substring that has to survive a rewording of the sentence around it.
func TestErrorPaths(t *testing.T) {
	tests := []struct {
		name   string
		cue    string       // replaces game.cue when non-empty
		mutate func(string) // mutates the fixture dir before the run
		opts   Options
		want   string
	}{
		{name: "missing bin", mutate: func(d string) { os.Remove(filepath.Join(d, "game (Track 2).bin")) },
			want: "missing"},
		{name: "truncated bin", mutate: func(d string) {
			p := filepath.Join(d, "game (Track 2).bin")
			data, _ := os.ReadFile(p)
			os.WriteFile(p, data[:len(data)-100], 0o644)
		}, want: "not a multiple"},
		{name: "mixed sector sizes", cue: strings.ReplaceAll(gameCue, "MODE2/2352", "MODE1/2048"),
			want: "sector sizes"},
		{name: "unknown track type", cue: strings.ReplaceAll(gameCue, "MODE2/2352", "MODE2/2324"),
			want: "Unsupported track type"},
		{name: "split on a multi-file cue", opts: Options{Split: true},
			want: "single bin file"},
		{name: "non-BINARY file", cue: strings.Replace(gameCue, `FILE "game (Track 3).bin" BINARY`, `FILE "game (Track 3).wav" WAVE`, 1),
			want: "Unsupported file type WAVE"},
		{name: "backwards indexes", cue: strings.Replace(gameCue, "    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n", "    INDEX 00 00:02:00\n    INDEX 01 00:01:00\n", 1),
			want: "backwards"},
		{name: "index past EOF", cue: strings.Replace(gameCue, "INDEX 01 00:02:00", "INDEX 01 10:00:00", 1),
			want: "beyond the end"},
		{name: "invalid timestamp", cue: strings.Replace(gameCue, "INDEX 01 00:02:00", "INDEX 01 00:61:99", 1),
			want: "Invalid cue timestamp"},
		{name: "no cue file", opts: Options{CueFile: "nope.cue"},
			want: "Cue file not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cue := tc.cue
			if cue == "" {
				cue = gameCue
			}
			dir := fixture(t, cue)
			if tc.mutate != nil {
				tc.mutate(dir)
			}
			opts := tc.opts
			if opts.CueFile == "" {
				opts.CueFile = filepath.Join(dir, "game.cue")
			} else {
				opts.CueFile = filepath.Join(dir, opts.CueFile)
			}
			opts.Basename, opts.OutDir = "x", filepath.Join(dir, "out")
			_, _, err := exec(t, opts)
			if err == nil {
				t.Fatalf("run succeeded; want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if exists(filepath.Join(dir, "out", "x.bin")) {
				t.Error("a partial output was left behind")
			}
		})
	}
}

// A single-file cue merges to a plain copy. That's legal and occasionally what
// someone wants, but it's never what they meant to ask for, so it warns.
func TestSingleFileCueWarns(t *testing.T) {
	dir := t.TempDir()
	makeBin(t, filepath.Join(dir, "m.bin"), 20, 3)
	os.WriteFile(filepath.Join(dir, "m.cue"), []byte("FILE \"m.bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"), 0o644)
	_, stderr, err := exec(t, Options{CueFile: filepath.Join(dir, "m.cue"), Basename: "copy", OutDir: filepath.Join(dir, "out")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "plain copy") {
		t.Errorf("stderr = %q, want the single-bin warning", stderr)
	}
}

// --psx is checks, not magic: the merge is byte-identical with and without it.
// What it adds is a refusal (a split set is a multi-file cue, which is the one
// thing the PS1 emulator can't mount), two warnings, and the filenames to copy.
func TestPSXChecks(t *testing.T) {
	dir := fixture(t, gameCue)
	cue := filepath.Join(dir, "game.cue")

	_, stderr, err := exec(t, Options{CueFile: cue, Basename: "Game", OutDir: filepath.Join(dir, "a"), PSX: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "/dev_hdd0/PSXISO/") || !strings.Contains(stderr, "Game.bin") {
		t.Errorf("stderr = %q, want the PSXISO copy hint naming the outputs", stderr)
	}
	plain := fixture(t, gameCue)
	if _, _, err := exec(t, Options{CueFile: filepath.Join(plain, "game.cue"), Basename: "Game", OutDir: filepath.Join(plain, "a")}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, filepath.Join(dir, "a", "Game.bin")), read(t, filepath.Join(plain, "a", "Game.bin"))) ||
		!bytes.Equal(read(t, filepath.Join(dir, "a", "Game.cue")), read(t, filepath.Join(plain, "a", "Game.cue"))) {
		t.Error("--psx changed the output; it's meant to be checks only")
	}

	if _, _, err := exec(t, Options{CueFile: cue, Basename: "Game", OutDir: filepath.Join(dir, "b"), PSX: true, Split: true}); err == nil ||
		!strings.Contains(err.Error(), "can't mount") {
		t.Errorf("--psx --split error = %v, want the multi-file-cue refusal", err)
	}

	// A cooked 2048 rip: valid cue, valid merge, won't boot on the PS1 emulator.
	// Its own fixture, since 2048-byte sectors need bins sized in 2048s.
	cooked := t.TempDir()
	for _, n := range []string{"1", "2"} {
		data := bytes.Repeat([]byte{byte(n[0])}, 10*2048)
		if err := os.WriteFile(filepath.Join(cooked, "c"+n+".bin"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(cooked, "game.cue"), []byte(
		"FILE \"c1.bin\" BINARY\n  TRACK 01 MODE1/2048\n    INDEX 01 00:00:00\n"+
			"FILE \"c2.bin\" BINARY\n  TRACK 02 MODE1/2048\n    INDEX 01 00:00:00\n"), 0o644)
	_, stderr, err = exec(t, Options{CueFile: filepath.Join(cooked, "game.cue"), Basename: "Cooked", OutDir: filepath.Join(cooked, "out"), PSX: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "2352") {
		t.Errorf("stderr = %q, want the cooked-rip warning", stderr)
	}

	_, stderr, err = exec(t, Options{CueFile: cue, Basename: "Game #2", OutDir: filepath.Join(dir, "c"), PSX: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "URL path") {
		t.Errorf("stderr = %q, want the URL-hostile-basename warning", stderr)
	}
}

// The output directory is created on demand, but only once the run is going to
// write something — see TestDryRunWritesNothing for the other half of that.
func TestOutDirCreated(t *testing.T) {
	dir := fixture(t, gameCue)
	nested := filepath.Join(dir, "a", "b", "c")
	if _, _, err := exec(t, Options{CueFile: filepath.Join(dir, "game.cue"), Basename: "m", OutDir: nested}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(nested, "m.bin")) {
		t.Error("nested output directory wasn't created")
	}
}

// With no OutDir the outputs land beside the cue, which is where the bins are.
func TestDefaultOutDirIsCueDir(t *testing.T) {
	dir := fixture(t, gameCue)
	if _, _, err := exec(t, Options{CueFile: filepath.Join(dir, "game.cue"), Basename: "merged"}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, "merged.bin")) || !exists(filepath.Join(dir, "merged.cue")) {
		t.Error("outputs didn't land beside the cue")
	}
}
