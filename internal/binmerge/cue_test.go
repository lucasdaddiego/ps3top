package binmerge

// The parser and the two generators. The passthrough cases are the ones worth
// having: a cue is the only copy of FLAGS, PREGAP, ISRC and disc metadata, and
// a merge that quietly drops them produces a file that still mounts and no
// longer describes the disc.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mm:ss:ff at 75 sectors per second, both ways, plus the two shapes that
// aren't a timestamp even though they look like one.
func TestStamps(t *testing.T) {
	for _, tc := range []struct {
		stamp   string
		sectors int
	}{
		{"00:00:00", 0},
		{"00:00:74", 74},
		{"00:01:25", 100},
		{"00:02:00", 150},
		{"01:00:00", 4500},
		{"74:00:00", 333000}, // a full CD
	} {
		got, err := stampToSectors(tc.stamp)
		if err != nil || got != tc.sectors {
			t.Errorf("stampToSectors(%q) = %d, %v; want %d", tc.stamp, got, err, tc.sectors)
		}
		if back := sectorsToStamp(tc.sectors); back != tc.stamp {
			t.Errorf("sectorsToStamp(%d) = %q, want %q", tc.sectors, back, tc.stamp)
		}
	}
	for _, bad := range []string{"00:61:00", "00:00:75", "00:60:00", "1:2", "aa:bb:cc"} {
		if _, err := stampToSectors(bad); err == nil {
			t.Errorf("stampToSectors(%q) accepted an impossible stamp", bad)
		}
	}
}

// UTF-8 first (BOM stripped), then cp1252, then latin-1. The middle one is the
// one that matters: 0x92 is a right single quote in a Windows-made cue, and
// decoding it as latin-1 bakes a C1 control character into the UTF-8 output.
func TestDecodeCue(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"plain ascii", []byte("TITLE \"Game\""), "TITLE \"Game\""},
		{"utf-8 with BOM", []byte("\xef\xbb\xbfTITLE \"Café\""), "TITLE \"Café\""},
		{"cp1252 punctuation", []byte("TITLE \"Rock\x92n\x92 Roll\""), "TITLE \"Rock’n’ Roll\""},
		{"cp1252 dash and quotes", []byte("REM \x93a\x94 \x97 b"), "REM “a” — b"},
		{"latin-1 fallback (0x81 is undefined in cp1252)", []byte("REM caf\xe9 \x81"), "REM caf\u00e9 \u0081"},
		{"high latin-1 only", []byte("REM caf\xe9"), "REM café"},
	} {
		if got := decodeCue(tc.in); got != tc.want {
			t.Errorf("%s: decodeCue = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Lines are stripped and blanks dropped, and a cue written on any of the three
// line-ending conventions parses the same.
func TestLines(t *testing.T) {
	for _, in := range []string{"a\nb\n\nc\n", "a\r\nb\r\n\r\nc\r\n", "a\rb\r\rc\r", "  a  \n\tb\n\nc"} {
		got := lines(in)
		if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Errorf("lines(%q) = %q", in, got)
		}
	}
}

// The parse the whole tool rests on: three files, one track each, offsets in
// sectors relative to their own file.
func TestParseCue(t *testing.T) {
	dir := fixture(t, gameCue)
	sheet, err := ParseCue(filepath.Join(dir, "game.cue"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheet.Files) != 3 {
		t.Fatalf("%d files, want 3", len(sheet.Files))
	}
	if got := sheet.Files[0].Size; got != 100*bs2352 {
		t.Errorf("first bin size = %d, want %d — the cue's bins are resolved beside it", got, 100*bs2352)
	}
	tr := sheet.Files[1].Tracks
	if len(tr) != 1 || tr[0].Number != 2 || tr[0].Type != "AUDIO" {
		t.Fatalf("second file's tracks = %+v", tr)
	}
	if len(tr[0].Indexes) != 2 || tr[0].Indexes[1].Offset != 150 {
		t.Errorf("indexes = %+v, want INDEX 01 at sector 150", tr[0].Indexes)
	}
	if tr[0].Start() != 0 {
		t.Errorf("Start() = %d, want the first index", tr[0].Start())
	}
}

// Anchored regexes, matched against stripped lines: a cue keyword inside a REM
// is a comment, and parsing it as a command would invent tracks that don't
// exist and shift every offset after them.
func TestREMKeywordsAreNotCommands(t *testing.T) {
	dir := fixture(t, "REM TRACK 99 AUDIO\nREM INDEX 01 99:00:00\n"+gameCue)
	sheet, err := ParseCue(filepath.Join(dir, "game.cue"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, f := range sheet.Files {
		total += len(f.Tracks)
	}
	if total != 3 {
		t.Errorf("%d tracks parsed, want 3 — a REM was read as a command", total)
	}
	if len(sheet.Header) != 2 {
		t.Errorf("header = %q, want both REM lines carried as disc-level metadata", sheet.Header)
	}
}

// FILE names come quoted, but not always. An unquoted name runs to the last
// field on the line, spaces and all.
func TestUnquotedFileName(t *testing.T) {
	dir := fixture(t, strings.Replace(gameCue, `"game (Track 1).bin"`, `game (Track 1).bin`, 1))
	sheet, err := ParseCue(filepath.Join(dir, "game.cue"))
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(sheet.Files[0].Path); got != "game (Track 1).bin" {
		t.Errorf("unquoted FILE name parsed as %q", got)
	}
}

// Every line that isn't FILE/TRACK/INDEX is carried through where it was
// found: disc-level lines above the first FILE, track lines before or after
// that track's indexes depending on which side they arrived on.
func TestMetadataPassthrough(t *testing.T) {
	cue := "REM COMMENT \"ripped long ago\"\nCATALOG 1234567890123\nTITLE \"Some Game\"\n" +
		strings.Replace(gameCue, "  TRACK 02 AUDIO\n",
			"  TRACK 02 AUDIO\n    FLAGS DCP PRE\n    PREGAP 00:02:00\n    ISRC ABCDE1234567\n", 1)
	cue = strings.Replace(cue, "    INDEX 01 00:02:00\nFILE \"game (Track 3).bin\"",
		"    INDEX 01 00:02:00\n    POSTGAP 00:01:00\nFILE \"game (Track 3).bin\"", 1)
	dir := fixture(t, cue)
	sheet, err := ParseCue(filepath.Join(dir, "game.cue"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheet.Header) != 3 {
		t.Errorf("header = %q, want the three disc-level lines", sheet.Header)
	}
	tr := sheet.Files[1].Tracks[0]
	if len(tr.Pre) != 3 || tr.Pre[0] != "FLAGS DCP PRE" || tr.Pre[2] != "ISRC ABCDE1234567" {
		t.Errorf("pre-index lines = %q", tr.Pre)
	}
	if len(tr.Post) != 1 || tr.Post[0] != "POSTGAP 00:01:00" {
		t.Errorf("post-index lines = %q", tr.Post)
	}

	out := mergedCue("merged", sheet, bs2352)
	flags, pregap, index := strings.Index(out, "FLAGS"), strings.Index(out, "PREGAP"), strings.Index(out, "INDEX 00 00:01:25")
	if !(flags < pregap && pregap < index) {
		t.Errorf("FLAGS/PREGAP didn't stay ahead of the indexes:\n%s", out)
	}
	if !strings.HasPrefix(out, "REM COMMENT \"ripped long ago\"\nCATALOG 1234567890123\nTITLE \"Some Game\"\nFILE \"merged.bin\" BINARY\n") {
		t.Errorf("disc-level lines didn't stay above the FILE line:\n%s", out)
	}
	if !strings.Contains(out, "    POSTGAP 00:01:00\n  TRACK 03") {
		t.Errorf("POSTGAP didn't stay after its track's indexes:\n%s", out)
	}
}

// Redump's naming: bare for a single track, and zero-padded once a disc has
// ten or more, so the files sort the way the tracks play.
func TestTrackFilename(t *testing.T) {
	for _, tc := range []struct {
		num, count int
		want       string
	}{
		{1, 1, "Foo.bin"},
		{1, 3, "Foo (Track 1).bin"},
		{9, 9, "Foo (Track 9).bin"},
		{1, 10, "Foo (Track 01).bin"},
		{10, 10, "Foo (Track 10).bin"},
	} {
		if got := trackFilename("Foo", tc.num, tc.count); got != tc.want {
			t.Errorf("trackFilename(Foo, %d, %d) = %q, want %q", tc.num, tc.count, got, tc.want)
		}
	}
}

// trackLengths is what a split cuts by: each track runs to the start of the
// next, the last to the end of the bin. Only a single-file cue can know this.
func TestTrackLengths(t *testing.T) {
	f := &BinFile{
		Size: 100 * bs2352,
		Tracks: []Track{
			{Number: 1, Indexes: []Index{{1, 0}}},
			{Number: 2, Indexes: []Index{{0, 20}, {1, 25}}},
			{Number: 3, Indexes: []Index{{1, 60}}},
		},
	}
	trackLengths(f, bs2352)
	for i, want := range []int{20, 40, 40} {
		if got := f.Tracks[i].Sectors; got != want {
			t.Errorf("track %d = %d sectors, want %d", i+1, got, want)
		}
	}
}

// A split cue's stamps go back to being relative to each track's own file, so
// the first index of every track lands at 00:00:00 (or its own pregap).
func TestSplitCueRebasesStamps(t *testing.T) {
	sheet := &Sheet{Files: []BinFile{{
		Size: 100 * bs2352,
		Tracks: []Track{
			{Number: 1, Type: "MODE2/2352", Indexes: []Index{{1, 0}}},
			{Number: 2, Type: "AUDIO", Indexes: []Index{{0, 100}, {1, 250}}},
		},
	}}}
	got := splitCue("game", sheet)
	want := "FILE \"game (Track 1).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n" +
		"FILE \"game (Track 2).bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n"
	if got != want {
		t.Errorf("splitCue =\n%q\nwant\n%q", got, want)
	}
}

// A cue that describes nothing usable fails at parse rather than at write.
func TestParseCueRejects(t *testing.T) {
	for _, tc := range []struct{ name, cue, want string }{
		{"no files", "REM nothing here\n", "No bin files found"},
		{"track before file", "  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n", "TRACK line before any FILE"},
		{"index outside a track", "FILE \"game (Track 1).bin\" BINARY\n    INDEX 01 00:00:00\n", "INDEX line outside"},
		{"track with no index", "FILE \"game (Track 1).bin\" BINARY\n  TRACK 01 AUDIO\n", "no INDEX lines"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture(t, tc.cue)
			_, err := ParseCue(filepath.Join(dir, "game.cue"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if _, err := ParseCue(filepath.Join(t.TempDir(), "nope.cue")); !os.IsNotExist(err) {
		t.Errorf("missing cue: err = %v, want a not-exist error", err)
	}
}
