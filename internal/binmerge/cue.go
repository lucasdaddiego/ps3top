package binmerge

// The cue sheet: reading it, and writing the rewritten one back out.
//
// A cue is a line-oriented list of FILE / TRACK / INDEX commands with metadata
// hung off them. Merging rewrites the FILE lines into one and shifts every
// INDEX stamp by the sectors that now precede it; splitting does the reverse.
// Everything else is carried through verbatim at the position it was found —
// a cue is the only place FLAGS, PREGAP, ISRC and disc TITLE live, and a merge
// that drops them silently degrades the dump.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const sectorsPerSecond = 75

// blocksizes is bytes per sector for each cue TRACK mode. Sector sizes can't
// be mixed on one disc, so every track in a sheet must resolve to one value.
var blocksizes = map[string]int{
	"AUDIO":      2352, // audio/music
	"CDG":        2448, // karaoke CD+G
	"MODE1/2048": 2048, // CD-ROM Mode 1 data (cooked)
	"MODE1/2352": 2352, // CD-ROM Mode 1 data (raw)
	"MODE2/2336": 2336, // CD-ROM XA Mode 2 data
	"MODE2/2352": 2352, // CD-ROM XA Mode 2 data
	"CDI/2336":   2336, // CDI Mode 2 data
	"CDI/2352":   2352, // CDI Mode 2 data
}

// Anchored at the start, like Python's re.match, and matched against stripped
// lines — so a keyword inside a comment (`REM TRACK 02 AUDIO`) can never parse
// as a command.
var (
	fileRE  = regexp.MustCompile(`(?i)^FILE\s+(?:"([^"]*)"|(.+?))\s+(\S+)\s*$`)
	trackRE = regexp.MustCompile(`(?i)^TRACK\s+(\d+)\s+(\S+)`)
	indexRE = regexp.MustCompile(`(?i)^INDEX\s+(\d+)\s+(\d+:\d+:\d+)`)
)

// Index is one INDEX line: a sector offset relative to the start of the FILE
// it sits under, which is why merging has to rewrite them.
type Index struct {
	Number int
	Offset int
}

// Track is one TRACK block. The verbatim lines are split around the indexes so
// a rewrite puts them back on the right side of them: FLAGS and PREGAP belong
// before INDEX 00, POSTGAP after the last one.
type Track struct {
	Number  int
	Type    string
	Indexes []Index
	Pre     []string // between TRACK and the first INDEX (FLAGS, TITLE, PREGAP, REM, …)
	Post    []string // after the indexes (POSTGAP, REM, …)
	Sectors int      // track length; only known for a single-file cue (see trackLengths)
}

// Start is the track's first index — where a split cuts.
func (t Track) Start() int { return t.Indexes[0].Offset }

// BinFile is one FILE line and the tracks under it.
type BinFile struct {
	Path   string
	Size   int64
	Tracks []Track
	Pre    []string // between FILE and its first TRACK
}

// Sheet is a parsed cue: the disc-level preamble, then the files.
type Sheet struct {
	Header []string // before the first FILE (CATALOG, TITLE, REM, …)
	Files  []BinFile
}

// stampToSectors reads mm:ss:ff at 75 frames (sectors) per second.
func stampToSectors(stamp string) (int, error) {
	bad := fmt.Errorf("Invalid cue timestamp: %s", stamp)
	parts := strings.Split(stamp, ":")
	if len(parts) != 3 {
		return 0, bad
	}
	var n [3]int
	for i, p := range parts {
		// Atoi, not ParseInt(64): a stamp wide enough to overflow an int is
		// nonsense in a cue, and the error path already says so.
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return 0, bad
		}
		n[i] = v
	}
	if n[0] > 9999 { // (mm*60+ss)*75 must not wrap: a CD is 80 minutes, a cue never days
		return 0, bad
	}
	if n[1] > 59 || n[2] >= sectorsPerSecond {
		return 0, fmt.Errorf("Invalid cue timestamp (mm:ss:ff, ss<60, ff<75): %s", stamp)
	}
	return (n[0]*60+n[1])*sectorsPerSecond + n[2], nil
}

func sectorsToStamp(sectors int) string {
	minutes, rem := sectors/(60*sectorsPerSecond), sectors%(60*sectorsPerSecond)
	return fmt.Sprintf("%02d:%02d:%02d", minutes, rem/sectorsPerSecond, rem%sectorsPerSecond)
}

// cp1252High is 0x80–0x9F under cp1252; 0 marks the five bytes it leaves
// undefined, which are what send decodeCue on to latin-1.
var cp1252High = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}

// decodeCue turns cue bytes into text. Cue sheets in the wild are occasionally
// cp1252/latin-1, so UTF-8 (BOM stripped) is tried first, then cp1252: it
// agrees with latin-1 everywhere except 0x80–0x9F, where latin-1 yields C1
// control characters and cp1252 yields the punctuation (curly quotes, dashes)
// such bytes actually carry — and the rewritten cue goes back out as UTF-8, so
// a wrong decode here is baked into the output. cp1252 leaves five bytes
// undefined, so latin-1 (every byte to U+00XX) stays as the never-fails
// fallback.
func decodeCue(data []byte) string {
	data = trimBOM(data)
	if utf8.Valid(data) {
		return string(data)
	}
	var b strings.Builder
	b.Grow(len(data))
	for _, c := range data {
		switch {
		case c < 0x80:
			b.WriteByte(c)
		case c < 0xA0:
			r := cp1252High[c-0x80]
			if r == 0 {
				return latin1(data) // undefined in cp1252; the whole sheet decodes as latin-1
			}
			b.WriteRune(r)
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

func trimBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}

func latin1(data []byte) string {
	var b strings.Builder
	b.Grow(len(data))
	for _, c := range data {
		b.WriteRune(rune(c))
	}
	return b.String()
}

// lines splits decoded cue text the way a cue is written: LF or CRLF, with a
// bare CR (classic Mac) tolerated, and every line stripped.
func lines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	out := make([]string, 0, 32)
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ParseCue reads the sheet and stats every bin it references. Bin paths are
// resolved against the cue's own directory, which is where a Redump set keeps
// them; a name that escapes it still works, it's just not what dumps do.
func ParseCue(cuePath string) (*Sheet, error) {
	data, err := os.ReadFile(cuePath)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(cuePath)

	sheet := &Sheet{}
	var missing []string
	// Indexes into sheet.Files / the current file's Tracks: the slices grow, so
	// holding pointers into them across an append would dangle.
	curFile, curTrack := -1, -1

	for _, line := range lines(decodeCue(data)) {
		if m := fileRE.FindStringSubmatch(line); m != nil {
			name, fileType := m[1], strings.ToUpper(m[3])
			if m[1] == "" && m[2] != "" {
				name = m[2] // unquoted; spaces and all
			}
			if fileType != "BINARY" {
				return nil, fmt.Errorf("Unsupported file type %s for %s; only BINARY is supported", fileType, name)
			}
			binPath := filepath.Join(dir, name)
			var size int64
			if st, err := os.Stat(binPath); err != nil {
				missing = append(missing, binPath)
			} else {
				size = st.Size()
			}
			sheet.Files = append(sheet.Files, BinFile{Path: binPath, Size: size})
			curFile, curTrack = len(sheet.Files)-1, -1
			continue
		}
		if m := trackRE.FindStringSubmatch(line); m != nil {
			if curFile < 0 {
				return nil, fmt.Errorf("TRACK line before any FILE line: %s", line)
			}
			num, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("Invalid track number: %s", line)
			}
			f := &sheet.Files[curFile]
			f.Tracks = append(f.Tracks, Track{Number: num, Type: strings.ToUpper(m[2])})
			curTrack = len(f.Tracks) - 1
			continue
		}
		if m := indexRE.FindStringSubmatch(line); m != nil {
			if curTrack < 0 {
				return nil, fmt.Errorf("INDEX line outside a TRACK block: %s", line)
			}
			num, err := strconv.Atoi(m[1])
			if err != nil {
				return nil, fmt.Errorf("Invalid index number: %s", line)
			}
			offset, err := stampToSectors(m[2])
			if err != nil {
				return nil, err
			}
			t := &sheet.Files[curFile].Tracks[curTrack]
			t.Indexes = append(t.Indexes, Index{Number: num, Offset: offset})
			continue
		}
		// Everything else (FLAGS, PREGAP, TITLE, PERFORMER, ISRC, REM, …) is
		// carried through verbatim at the position it was found.
		switch {
		case curTrack >= 0:
			t := &sheet.Files[curFile].Tracks[curTrack]
			if len(t.Indexes) > 0 {
				t.Post = append(t.Post, line)
			} else {
				t.Pre = append(t.Pre, line)
			}
		case curFile >= 0:
			sheet.Files[curFile].Pre = append(sheet.Files[curFile].Pre, line)
		default:
			sheet.Header = append(sheet.Header, line)
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("Bin file(s) referenced by the cue are missing or unreadable:\n  %s",
			strings.Join(missing, "\n  "))
	}
	if len(sheet.Files) == 0 {
		return nil, fmt.Errorf("No bin files found in the cue sheet. Is it a valid cue?")
	}
	for _, f := range sheet.Files {
		for _, t := range f.Tracks {
			if len(t.Indexes) == 0 {
				return nil, fmt.Errorf("Track %d has no INDEX lines; cue is invalid", t.Number)
			}
		}
	}
	return sheet, nil
}

// trackLengths fills in Track.Sectors for a single-file (already merged) cue:
// each track runs to the start of the next, the last to the end of the bin.
func trackLengths(f *BinFile, blocksize int) {
	end := int(f.Size / int64(blocksize))
	for i := len(f.Tracks) - 1; i >= 0; i-- {
		t := &f.Tracks[i]
		t.Sectors = end - t.Start()
		end = t.Start()
	}
}

// trackFilename follows the Redump naming convention:
//
//	1 track:    "Foo.bin"
//	<10 tracks: "Foo (Track N).bin"
//	>=10:       "Foo (Track NN).bin"
func trackFilename(prefix string, trackNum, trackCount int) string {
	switch {
	case trackCount == 1:
		return prefix + ".bin"
	case trackCount > 9:
		return fmt.Sprintf("%s (Track %02d).bin", prefix, trackNum)
	default:
		return fmt.Sprintf("%s (Track %d).bin", prefix, trackNum)
	}
}

// trackCueLines renders one track, with its INDEX stamps shifted by base.
func trackCueLines(t Track, base int) []string {
	out := []string{fmt.Sprintf("  TRACK %02d %s", t.Number, t.Type)}
	for _, extra := range t.Pre {
		out = append(out, "    "+extra)
	}
	for _, i := range t.Indexes {
		out = append(out, fmt.Sprintf("    INDEX %02d %s", i.Number, sectorsToStamp(base+i.Offset)))
	}
	for _, extra := range t.Post {
		out = append(out, "    "+extra)
	}
	return out
}

// mergedCue is the one-FILE sheet: every track's stamps pushed out by the
// sectors of the files that now precede it.
func mergedCue(basename string, sheet *Sheet, blocksize int) string {
	out := append([]string{}, sheet.Header...)
	out = append(out, fmt.Sprintf("FILE \"%s.bin\" BINARY", basename))
	pos := 0
	for _, f := range sheet.Files {
		out = append(out, f.Pre...)
		for _, t := range f.Tracks {
			out = append(out, trackCueLines(t, pos)...)
		}
		pos += int(f.Size / int64(blocksize))
	}
	return strings.Join(out, "\n") + "\n"
}

// splitCue is the one-FILE-per-track sheet: stamps go back to being relative
// to the track's own file, so each is shifted down by where the track started.
func splitCue(basename string, sheet *Sheet) string {
	merged := sheet.Files[0]
	out := append([]string{}, sheet.Header...)
	out = append(out, merged.Pre...)
	for _, t := range merged.Tracks {
		out = append(out, fmt.Sprintf("FILE \"%s\" BINARY", trackFilename(basename, t.Number, len(merged.Tracks))))
		out = append(out, trackCueLines(t, -t.Start())...)
	}
	return strings.Join(out, "\n") + "\n"
}

// crlf is what the cue goes to disk as — the format's convention, and what
// every dump in the wild carries.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
