package main

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote —
// for the plain-text entry points (--once, --stats) that print directly.
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
	small := transmitEscapes(7, []byte("tiny"), 24, 7)
	if !strings.Contains(small, "\x1b_Gf=100,t=d,i=7,q=2,m=0;") {
		t.Errorf("single-chunk header wrong:\n%q", small)
	}
	if !strings.Contains(small, "\x1b_Ga=p,i=7,U=1,p=1,c=24,r=7,q=2\x1b\\") {
		t.Errorf("placement escape missing or wrong shape:\n%q", small)
	}

	// >4096 base64 chars must split into continuation chunks, the last m=0
	big := transmitEscapes(3, make([]byte, 8000), 17, 10)
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

// A placement is shaped to the cover, not to the box: a 320×176 ICON0 takes
// the box's full width and a portion of its height, a 260×300 multiMAN JPEG
// the full height and a portion of its width — in a 24×10 box. Kitty scales
// an image to exactly the cells it's given, so this is the only thing
// keeping a portrait cover portrait.
func TestFitCoverKeepsTheAspect(t *testing.T) {
	for _, c := range []struct {
		w, h       int
		cols, rows int
	}{
		{320, 176, 24, 7},  // landscape ICON0: width-bound
		{260, 300, 17, 10}, // portrait multiMAN cover: height-bound
		{100, 100, 20, 10},
		{0, 0, 24, 10}, // unknown: the whole box
	} {
		cols, rows := fitCover(c.w, c.h, 24, 10, 2)
		if cols != c.cols || rows != c.rows {
			t.Errorf("fitCover(%d×%d) = %d×%d cells, want %d×%d", c.w, c.h, cols, rows, c.cols, c.rows)
		}
		if cols > 24 || rows > 10 || cols < 1 || rows < 1 {
			t.Errorf("fitCover(%d×%d) = %d×%d, outside the box", c.w, c.h, cols, rows)
		}
	}
}

// The placement is shaped at the terminal's real cell aspect: Ghostty's
// cells are nearer 1:2.4 than 1:2, and a rectangle cut for 1:2 leaves a
// letterboxed strip inside the placement that nothing can center.
func TestFitCoverUsesTheCellAspect(t *testing.T) {
	// a 24-wide portrait needs 14 rows of 1:2 cells but only 12 of 1:2.4
	if _, rows := fitCover(260, 300, 24, 20, 2); rows != 14 {
		t.Errorf("1:2 cells: %d rows, want 14", rows)
	}
	if _, rows := fitCover(260, 300, 24, 20, 2.4); rows != 12 {
		t.Errorf("1:2.4 cells: %d rows, want 12", rows)
	}
}

// The box grows with the terminal: wider windows get a wider cover, and the
// box takes the rows the panel's text leaves — but never more than a portrait
// cover at that width can fill, and never past the diacritics table.
func TestArtBoxScalesWithTheTerminal(t *testing.T) {
	const list = 60 // what a typical library's rows need
	for _, c := range []struct {
		width, listH int
		cols, rows   int
	}{
		{84, 20, artMinCols, 14},          // the art threshold: 20 cols left, floored at 24, 14 rows
		{120, 23, 31, 18},                 // 56 cols left, 18 rows, width given back to 31
		{128, 25, 35, 20},                 // a 128×32 window: 64 left, 20 rows, width back to 35
		{200, 40, 61, 35},                 // wide and tall: 136 → 96 cols on offer, 35 rows, width back to 61
		{300, 12, artMinCols, artMinRows}, // very wide but short: rows floored, and the width given back
	} {
		cols, rows := artBox(c.width, c.listH, 2, list)
		if cols != c.cols || rows != c.rows {
			t.Errorf("artBox(%d, %d) = %d×%d, want %d×%d", c.width, c.listH, cols, rows, c.cols, c.rows)
		}
		if cols > len(diacritics) || rows > len(diacritics) {
			t.Errorf("artBox(%d, %d) = %d×%d exceeds the %d-entry diacritics table", c.width, c.listH, cols, rows, len(diacritics))
		}
		// a portrait cover fills the box's width or its height — the rows
		// and columns aren't being handed out for nothing
		if pc, pr := fitCover(260, 300, cols, rows, 2); pc != cols && pr != rows {
			t.Errorf("artBox(%d, %d) = %d×%d: a portrait cover only reaches %d×%d", c.width, c.listH, cols, rows, pc, pr)
		}
	}
	// a taller cell aspect means fewer rows for the same width once the
	// height isn't the limit: 41 cols of portrait is 24 rows at 1:2, 20 at 1:2.4
	if _, rows := artBox(105, 40, 2, list); rows != 24 {
		t.Errorf("1:2 cells: box has %d rows, want 24", rows)
	}
	if _, rows := artBox(105, 40, 2.4, list); rows != 20 {
		t.Errorf("1:2.4 cells: box has %d rows, want 20", rows)
	}
	// a library with long titles keeps its rows whole; the cover takes the
	// rest, down to the floor
	if cols, _ := artBox(128, 25, 2, 100); cols != artMinCols {
		t.Errorf("long titles: box is %d cols, want the %d floor", cols, artMinCols)
	}
}

func TestPlacementRow(t *testing.T) {
	row := placementRow(42, 0, artMinCols)
	if !strings.HasPrefix(row, "\x1b[38;5;42m") {
		t.Errorf("image id isn't carried in the foreground colour: %q", row)
	}
	if !strings.HasSuffix(row, "\x1b[39m") {
		t.Errorf("colour not reset: %q", row)
	}
	if n := strings.Count(row, string(placeholder)); n != artMinCols {
		t.Errorf("%d placeholder cells, want %d", n, artMinCols)
	}
	// the row diacritic identifies which row of the image this is
	if !strings.ContainsRune(row, diacritics[3]) {
		t.Error("row 3 doesn't carry its row diacritic")
	}
}

// cmdsIn unwraps a message that is itself a list of commands — tea.BatchMsg,
// or the unexported sequence message behind tea.Sequence — into its parts.
// Anything else is a leaf and returns nil. Reflection because the sequence
// type isn't exported; both are plain []tea.Cmd underneath.
func cmdsIn(msg tea.Msg) []tea.Cmd {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeOf(tea.Cmd(nil)) {
		return nil
	}
	out := make([]tea.Cmd, v.Len())
	for i := range out {
		out[i] = v.Index(i).Interface().(tea.Cmd)
	}
	return out
}

// leaves runs a command and returns every leaf message it produces, in order,
// descending through batches and sequences without feeding anything back.
func leaves(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if parts := cmdsIn(msg); parts != nil {
		var out []tea.Msg
		for _, c := range parts {
			out = append(out, leaves(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// raw returns the raw escape strings among a command's leaves.
func raw(cmd tea.Cmd) string {
	var sb strings.Builder
	for _, msg := range leaves(cmd) {
		if r, ok := msg.(tea.RawMsg); ok {
			sb.WriteString(fmt.Sprint(r.Msg))
		}
	}
	return sb.String()
}

// placeholderModel is the smallest model that puts a placeholder row on screen
// and quits — the vehicle for checking what the renderer actually emits.
type placeholderModel struct{ row string }

func (placeholderModel) Init() tea.Cmd                         { return tea.Quit }
func (m placeholderModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m placeholderModel) View() tea.View {
	v := tea.NewView(m.row + "\nnext line")
	v.AltScreen = true
	return v
}

// The cover art depends on the renderer passing three things through a cell
// untouched: the U+10EEEE placeholder, the two combining diacritics that name
// the image row/column, and the 256-colour foreground that carries the image
// id. bubbletea's cell renderer re-encodes every cell from its own buffer —
// this pins that what comes out the other end is still a kitty placeholder.
// Headless: the program is given a buffer for a terminal and quits on Init.
func TestRendererPassesPlaceholderCellsThrough(t *testing.T) {
	const id = 7
	var out strings.Builder
	p := tea.NewProgram(placeholderModel{placementRow(id, 3, artMinCols)},
		tea.WithInput(nil),
		tea.WithOutput(&out),
		tea.WithoutSignals(),
		tea.WithWindowSize(80, 24),
		tea.WithEnvironment([]string{"TERM=xterm-ghostty"}),
		tea.WithColorProfile(colorprofile.TrueColor),
	)
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	cell := string(placeholder) + string(diacritics[3]) + string(diacritics[0])
	if !strings.Contains(got, cell) {
		t.Fatalf("first placeholder cell (U+10EEEE + row/col diacritics) not in the output:\n%q", got)
	}
	if n := strings.Count(got, string(placeholder)); n != artMinCols {
		t.Errorf("%d placeholder cells reached the terminal, want %d", n, artMinCols)
	}
	// the image id rides in the foreground colour of those cells
	if !strings.Contains(got, "38;5;7m") && !strings.Contains(got, ";38;5;7m") {
		t.Errorf("image id not carried as a 256-colour foreground:\n%q", got)
	}
	// and the id's colour is set before the first placeholder, not after
	if strings.Index(got, "38;5;7") > strings.Index(got, string(placeholder)) {
		t.Error("foreground set after the placeholder cells it should colour")
	}
}
