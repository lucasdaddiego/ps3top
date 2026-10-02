package binmerge

// The copying, and what happens when it doesn't finish. A half-written bin is
// worse than no bin: it's the right length to look plausible and the wrong
// bytes to boot, so every failure path here has to leave nothing behind.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeFilesConcatenates(t *testing.T) {
	dir := t.TempDir()
	var want []byte
	var files []BinFile
	for i, seed := range []int{1, 77, 200} {
		p := filepath.Join(dir, string(rune('a'+i))+".bin")
		makeBin(t, p, 3, seed)
		want = append(want, read(t, p)...)
		files = append(files, BinFile{Path: p, Size: 3 * bs2352})
	}
	out := filepath.Join(dir, "merged.bin")
	if err := mergeFiles(context.Background(), out, files, &logger{w: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, out); !bytes.Equal(got, want) {
		t.Error("merged bytes aren't the inputs in cue order")
	}
}

// A split seeks to each track's first index, so anything before it — the
// pregap sectors a rip leaves at the head of the file — is skipped rather than
// prepended to track 1.
func TestSplitFileHonoursOffsets(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m.bin")
	makeBin(t, src, 100, 9)
	data := read(t, src)

	merged := BinFile{Path: src, Size: 100 * bs2352, Tracks: []Track{
		{Number: 1, Indexes: []Index{{1, 5}}},
		{Number: 2, Indexes: []Index{{1, 50}}},
	}}
	trackLengths(&merged, bs2352)
	outputs := []splitOutput{
		{Track: merged.Tracks[0], Path: filepath.Join(dir, "t1.bin")},
		{Track: merged.Tracks[1], Path: filepath.Join(dir, "t2.bin")},
	}
	if err := splitFile(context.Background(), merged, outputs, bs2352, &logger{w: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, outputs[0].Path); !bytes.Equal(got, data[5*bs2352:50*bs2352]) {
		t.Errorf("track 1 = %d bytes, want sectors 5–49", len(got))
	}
	if got := read(t, outputs[1].Path); !bytes.Equal(got, data[50*bs2352:]) {
		t.Errorf("track 2 = %d bytes, want sectors 50–99", len(got))
	}
}

// Ctrl-C during a merge has to take the partial bin with it. The context is
// already cancelled here, which is the same code path a signal takes and
// doesn't need a race to reproduce.
func TestMergeCleansUpOnCancel(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	makeBin(t, src, 3, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := filepath.Join(dir, "merged.bin")
	err := mergeFiles(ctx, out, []BinFile{{Path: src, Size: 3 * bs2352}}, &logger{w: io.Discard})
	if err == nil {
		t.Fatal("a cancelled merge reported success")
	}
	if exists(out) {
		t.Error("a cancelled merge left a partial bin behind")
	}
}

// Same for a split, where there's more to clean: every track already written
// goes too, not just the one that was in flight.
func TestSplitCleansUpOnCancel(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m.bin")
	makeBin(t, src, 10, 9)
	merged := BinFile{Path: src, Size: 10 * bs2352, Tracks: []Track{
		{Number: 1, Indexes: []Index{{1, 0}}},
		{Number: 2, Indexes: []Index{{1, 5}}},
	}}
	trackLengths(&merged, bs2352)
	outputs := []splitOutput{
		{Track: merged.Tracks[0], Path: filepath.Join(dir, "t1.bin")},
		{Track: merged.Tracks[1], Path: filepath.Join(dir, "t2.bin")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := splitFile(ctx, merged, outputs, bs2352, &logger{w: io.Discard}); err == nil {
		t.Fatal("a cancelled split reported success")
	}
	for _, o := range outputs {
		if exists(o.Path) {
			t.Errorf("%s survived a cancelled split", filepath.Base(o.Path))
		}
	}
}

// A source that ends earlier than the cue says is a corrupt dump, not a short
// track: it stops the run rather than writing a truncated file.
func TestSplitShortSourceFails(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m.bin")
	makeBin(t, src, 4, 9)
	// Sizes claim 10 sectors; the file holds 4.
	merged := BinFile{Path: src, Size: 10 * bs2352, Tracks: []Track{{Number: 1, Sectors: 10, Indexes: []Index{{1, 0}}}}}
	out := filepath.Join(dir, "t1.bin")
	err := splitFile(context.Background(), merged, []splitOutput{{Track: merged.Tracks[0], Path: out}}, bs2352, &logger{w: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "Unexpected end of file") {
		t.Fatalf("err = %v, want the short-source message", err)
	}
	if exists(out) {
		t.Error("the truncated track was left on disk")
	}
}

// copyN is exact: it moves n bytes or fails, and never silently copies fewer.
func TestCopyN(t *testing.T) {
	var buf bytes.Buffer
	if err := copyN(context.Background(), &buf, strings.NewReader("0123456789"), 4, "short"); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "0123" {
		t.Errorf("copied %q, want the first four bytes", buf.String())
	}
	buf.Reset()
	if err := copyN(context.Background(), &buf, strings.NewReader("012"), 8, "short"); err == nil || err.Error() != "short" {
		t.Errorf("err = %v, want the caller's short-read message", err)
	}
}

// A merge whose source disappears mid-run fails and cleans up rather than
// leaving the bins it had already appended.
func TestMergeCleansUpOnMissingSource(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.bin")
	makeBin(t, first, 2, 1)
	out := filepath.Join(dir, "merged.bin")
	files := []BinFile{{Path: first, Size: 2 * bs2352}, {Path: filepath.Join(dir, "gone.bin"), Size: 2 * bs2352}}

	if err := mergeFiles(context.Background(), out, files, &logger{w: io.Discard}); err == nil {
		t.Fatal("a merge over a missing source reported success")
	} else if !os.IsNotExist(err) {
		t.Errorf("err = %v, want a not-exist error", err)
	}
	if exists(out) {
		t.Error("the partially merged bin was left behind")
	}
}

// A split whose os.Create fails (a write-protected file under --force) must
// not delete that file in its cleanup: the run never created or touched it.
func TestSplitCleanupKeepsAFileItNeverCreated(t *testing.T) {
	src := t.TempDir()
	makeBin(t, filepath.Join(src, "m.bin"), 10, 9)
	if err := os.WriteFile(filepath.Join(src, "m.cue"), []byte("FILE \"m.bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"+
		"  TRACK 02 AUDIO\n    INDEX 01 00:00:05\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(src, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(out, "g (Track 2).bin")
	if err := os.WriteFile(keep, []byte("the user's write-protected file"), 0o444); err != nil {
		t.Fatal(err)
	}
	_, _, err := exec(t, Options{CueFile: filepath.Join(src, "m.cue"), Basename: "g", Split: true, OutDir: out, Force: true})
	if err == nil {
		t.Skip("Create on a 0444 file succeeded (running as root?)")
	}
	if !exists(keep) {
		t.Errorf("the run failed with %v, and its cleanup deleted %s, which it never created", err, filepath.Base(keep))
	}
}
