package main

import (
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// The escape writers go straight to the fd rather than through bubbletea, which
// is the whole point of them — so this is the only way to see their output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()

	fn()
	os.Stdout = prev
	w.Close()
	out := <-done
	r.Close()
	return out
}

// WezTerm speaks the kitty graphics protocol but NOT Unicode-placeholder mode,
// so it must not be detected — placeholder cells would render as garbage.
func TestArtSupported(t *testing.T) {
	cases := []struct {
		term, prog string
		want       bool
	}{
		{"xterm-kitty", "", true},
		{"xterm-ghostty", "", true},
		{"xterm-256color", "ghostty", true},
		{"xterm-256color", "kitty", true},
		{"xterm-256color", "", false},
		{"", "", false},
		{"wezterm", "WezTerm", false},
		{"screen-256color", "iTerm.app", false},
	}
	for _, c := range cases {
		t.Setenv("TERM", c.term)
		t.Setenv("TERM_PROGRAM", c.prog)
		if got := artSupported(); got != c.want {
			t.Errorf("TERM=%q TERM_PROGRAM=%q: artSupported = %v, want %v", c.term, c.prog, got, c.want)
		}
	}
}

func TestTransmitEscapes(t *testing.T) {
	// small image: one chunk, so m=0 on the first (and only) transmission
	small := transmitEscapes(7, []byte("tiny"))
	if !strings.Contains(small, "\x1b_Gf=100,t=d,i=7,q=2,m=0;") {
		t.Errorf("single-chunk header wrong:\n%q", small)
	}
	if !strings.Contains(small, "\x1b_Ga=p,i=7,U=1,p=1,c=24,r=7,q=2\x1b\\") {
		t.Errorf("placement escape missing or wrong shape:\n%q", small)
	}

	// >4096 base64 chars must split into continuation chunks, the last m=0
	big := transmitEscapes(3, make([]byte, 8000))
	if n := strings.Count(big, "\x1b_G"); n < 3 {
		t.Errorf("8000 bytes produced %d escapes, want a chunked transmission", n)
	}
	if !strings.Contains(big, "\x1b_Gm=1;") {
		t.Error("no continuation chunk marked m=1")
	}
	if !strings.Contains(big, "\x1b_Gm=0;") {
		t.Error("final continuation chunk not marked m=0")
	}
	if strings.Count(big, "\x1b_Gf=100") != 1 {
		t.Error("more than one transmission header")
	}
}

func TestPlacementRow(t *testing.T) {
	row := placementRow(42, 0)
	if !strings.HasPrefix(row, "\x1b[38;5;42m") {
		t.Errorf("image id isn't carried in the foreground colour: %q", row)
	}
	if !strings.HasSuffix(row, "\x1b[39m") {
		t.Errorf("colour not reset: %q", row)
	}
	if n := strings.Count(row, string(placeholder)); n != artCols {
		t.Errorf("%d placeholder cells, want %d", n, artCols)
	}
	// the row diacritic identifies which row of the image this is
	if !strings.ContainsRune(row, diacritics[3]) {
		t.Error("row 3 doesn't carry its row diacritic")
	}
}

// A cover download finishing after quit must not re-add an image to the
// terminal's store: it would sit there until the window closed.
func TestRawWriteIsGatedAfterQuit(t *testing.T) {
	defer quitting.Store(false) // global, and the suite is shuffled
	quitting.Store(false)

	if got := captureStdout(t, func() { rawWrite("hello") }); got != "hello" {
		t.Errorf("rawWrite wrote %q", got)
	}
	got := captureStdout(t, func() {
		clearImages()
		rawWrite("late cover")
	})
	if !strings.Contains(got, "\x1b_Ga=d,d=A,q=2\x1b\\") {
		t.Errorf("clearImages didn't send the delete-all escape: %q", got)
	}
	if strings.Contains(got, "late cover") {
		t.Error("a transmission after quit still reached the terminal")
	}
}
