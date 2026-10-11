package webman

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The listing fixtures are synthetic, shaped after file_manager.h (webMAN
// MOD 1.47.48) and the dir_game.html capture. dir_listing.html holds a
// folder with a space, ISOs whose names carry an apostrophe, an '&' and a
// '+', a split ISO part and a .dat behind a label, an image whose copy link
// carries the exact size, and a text file and a pkg that show only a
// rounded figure. The folder and the split part have spaces as their only
// escape, so webMAN links them raw, and their paths show Dir's fold to '+'.
func TestParseDir(t *testing.T) {
	b, err := os.ReadFile("testdata/dir_listing.html")
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseDir(string(b), "/dev_hdd0/PS3ISO")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Name: "Extras", Path: "/dev_hdd0/PS3ISO/Extras", Dir: true},
		{Name: "Old Rips", Path: "/dev_hdd0/PS3ISO/Old+Rips", Dir: true},
		{Name: "Assassin's Sample.iso", Path: "/dev_hdd0/PS3ISO/Assassin%27s+Sample.iso", Size: 7516192768, Exact: true},
		{Name: "Big Sample.iso.1", Path: "/dev_hdd0/PS3ISO/Big+Sample.iso.1", Size: 4294967296, Exact: true},
		{Name: "Legacy.iso", Path: "/dev_hdd0/PS3ISO/Legacy.iso", Size: 3735420928, Exact: true},
		{Name: "Ratchet & Clank.iso", Path: "/dev_hdd0/PS3ISO/Ratchet+%26+Clank.iso", Size: 8315076608, Exact: true},
		{Name: "Sample Game+ 2.iso", Path: "/dev_hdd0/PS3ISO/Sample+Game%2B+2.iso", Size: 10036969472, Exact: true},
		{Name: "cover.png", Path: "/dev_hdd0/PS3ISO/cover.png", Size: 35912, Exact: true},
		{Name: "notes.txt", Path: "/dev_hdd0/PS3ISO/notes.txt", Size: 1104, Exact: true},
		{Name: "save.dat", Path: "/dev_hdd0/PS3ISO/save.dat", Size: 20480, Exact: true},
		{Name: "update.pkg", Path: "/dev_hdd0/PS3ISO/update.pkg", Size: 9241 << 10},
	}
	if !slices.Equal(l.Entries, want) {
		t.Errorf("entries:\n%s\nwant:\n%s", entries(l.Entries), entries(want))
	}
	if l.Dirs != 2 || l.Files != 9 {
		t.Errorf("footer = %d dirs, %d files; want 2, 9", l.Dirs, l.Files)
	}
}

func entries(es []Entry) string {
	var b strings.Builder
	for _, e := range es {
		fmt.Fprintf(&b, "  %+v\n", e)
	}
	return b.String()
}

// The /dev_hdd0/game capture: ten folders, no files, and a footer that
// counts them without "..".
func TestParseDirGameFolders(t *testing.T) {
	b, err := os.ReadFile("testdata/dir_game.html")
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseDir(string(b), "/dev_hdd0/game")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 10 || l.Dirs != 10 || l.Files != 0 {
		t.Fatalf("%d entries, footer %d/%d; want 10, 10/0", len(l.Entries), l.Dirs, l.Files)
	}
	for _, e := range l.Entries {
		if !e.Dir || e.Path != "/dev_hdd0/game/"+e.Name || e.Size != 0 {
			t.Errorf("entry %+v", e)
		}
	}
}

// page builds a listing page with the given rows and footer counts.
func page(rows []string, dirs, files int) string {
	return `<html><body><hr><a class="f" href="/dev_hdd0/">/dev_hdd0</a>:<br><table id="files" class="propfont">` +
		`<tr><td colspan=3><col width="220"><col width="98">` + strings.Join(rows, "\n") + `</table>` +
		fmt.Sprintf(`<hr><b><a href="/dev_hdd0">/dev_hdd0</a>: 1.0 GB free</b> &nbsp; <font color="#C0C0C0">%d Dir(s) %d Files 1 KB</font></body></html>`, dirs, files)
}

const dotdotRow = `<tr><td><a class="d" href="..">..</a><td> <a href="/stat.ps3/dev_hdd0/x">&lt;dir></a> &nbsp; <td>01-Oct-2026 10:00</tr>`

func fileRow(href, text, size string) string {
	return `<tr><td><a class="w" href="` + href + `">` + text + `</a><td> ` + size + ` &nbsp; <td>01-Oct-2026 10:00</tr>`
}

// In a folder with a space, a name with any other escape (here a '+') gets
// an absolute link, because its escaped path no longer starts with the raw
// folder path: the link is the entry's path as it stands. A link that does
// not lie in the folder, a row with no link (webMAN drops it from a row
// past 512 bytes, and prints the size there without commas) and a link
// that reaches webMAN's 640-byte cut all fall back to the text, with no
// path.
func TestParseDirLinks(t *testing.T) {
	long := strings.Repeat("x", linkMax)
	rows := []string{
		dotdotRow,
		fileRow("/dev_hdd0/Old+Rips/Sample+Game%2B+2.iso", "Sample Game+ 2.iso", `<a href="/mount.ps3/dev_hdd0/Old+Rips/Sample+Game%2B+2.iso" title="1,104 b">1,104 b</a>`),
		fileRow("/dev_hdd0/Other/Elsewhere.iso", "Elsewhere.iso", `<label title="20,480 b"> 20 KB</label>`),
		`<tr><td><a class="w ps3">A Very Long Name.iso</a><td> 9572 MB &nbsp; <td>01-Oct-2026 10:00</tr>`,
		fileRow("/dev_hdd0/Old+Rips/"+long, "Cut Link.iso", "12 b"),
	}
	l, err := parseDir(page(rows, 0, 4), "/dev_hdd0/Old+Rips")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Name: "Sample Game+ 2.iso", Path: "/dev_hdd0/Old+Rips/Sample+Game%2B+2.iso", Size: 1104, Exact: true},
		{Name: "Elsewhere.iso", Size: 20480, Exact: true},
		{Name: "A Very Long Name.iso", Size: 9572 << 20},
		{Name: "Cut Link.iso", Size: 12, Exact: true},
	}
	if !slices.Equal(l.Entries, want) {
		t.Errorf("entries:\n%s\nwant:\n%s", entries(l.Entries), entries(want))
	}
	// the same folder asked for with %20: webMAN's link still decodes to it
	if _, err := parseDir(page(rows[:2], 0, 1), "/dev_hdd0/Old%20Rips"); err != nil {
		t.Errorf("%%20 spelling: %v", err)
	}
}

// One folder, one spelling. In a folder with a space, webMAN links a name
// whose only escapes are spaces raw and relative, and a name with any other
// escape escaped and absolute; every path comes back in urlenc form under
// the same folder prefix, whichever way the folder was asked for, and a
// subfolder's path is the request Dir sends for it. A folder with a '+'
// gets absolute links only (urlenc grows its path), kept as webMAN wrote
// them.
func TestParseDirOneSpellingPerFolder(t *testing.T) {
	rows := []string{
		`<tr><td><a class="d" href="..">..</a><td> <a href="/stat.ps3/dev_hdd0/Old Rips">&lt;dir></a> &nbsp; <td>01-Oct-2026 10:00</tr>`,
		`<tr><td><a class="d" href="Sub Dir">Sub Dir</a><td> <a href="/mount.ps3/dev_hdd0/Old Rips/Sub Dir">&lt;dir></a> &nbsp; <td>01-Oct-2026 10:00</tr>`,
		fileRow("c d.iso", "c d.iso", `<a href="/mount.ps3/dev_hdd0/Old Rips/c d.iso" title="1,104 b">1,104 b</a>`),
		fileRow("/dev_hdd0/Old+Rips/b%27s.iso", "b's.iso", `<a href="/mount.ps3/dev_hdd0/Old+Rips/b%27s.iso" title="1,104 b">1,104 b</a>`),
	}
	want := []Entry{
		{Name: "Sub Dir", Path: "/dev_hdd0/Old+Rips/Sub+Dir", Dir: true},
		{Name: "c d.iso", Path: "/dev_hdd0/Old+Rips/c+d.iso", Size: 1104, Exact: true},
		{Name: "b's.iso", Path: "/dev_hdd0/Old+Rips/b%27s.iso", Size: 1104, Exact: true},
	}
	for _, folder := range []string{"/dev_hdd0/Old+Rips", "/dev_hdd0/Old%20Rips", "/dev_hdd0/Old Rips"} {
		l, err := parseDir(page(rows, 1, 2), folder)
		if err != nil {
			t.Fatalf("%s: %v", folder, err)
		}
		if !slices.Equal(l.Entries, want) {
			t.Errorf("%s: entries:\n%s\nwant:\n%s", folder, entries(l.Entries), entries(want))
		}
	}

	cli, asked := listingConsole(t, map[string]string{
		"/dev_hdd0/Old+Rips/":         page(rows, 1, 2),
		"/dev_hdd0/Old+Rips/Sub+Dir/": page([]string{dotdotRow}, 0, 0),
	})
	ctx := context.Background()
	l, err := cli.Dir(ctx, "/dev_hdd0/Old+Rips")
	if err != nil || len(l.Entries) != 3 {
		t.Fatalf("Dir = %d entries, %v", len(l.Entries), err)
	}
	if _, err := cli.Dir(ctx, l.Entries[0].Path); err != nil {
		t.Errorf("Dir(%q): %v", l.Entries[0].Path, err)
	}
	if a := asked(); !slices.Equal(a, []string{"/dev_hdd0/Old+Rips/", "/dev_hdd0/Old+Rips/Sub+Dir/"}) {
		t.Errorf("requests = %q", a)
	}

	plus := []string{dotdotRow, fileRow("/dev_hdd0/a%2Bb/x+y.iso", "x y.iso", "12 b")}
	l, err = parseDir(page(plus, 0, 1), "/dev_hdd0/a%2Bb")
	if err != nil {
		t.Fatal(err)
	}
	if e := l.Entries[0]; e.Name != "x y.iso" || e.Path != "/dev_hdd0/a%2Bb/x+y.iso" {
		t.Errorf("a folder with a '+': %+v", e)
	}
}

// The name comes from the link, never from the text with its entities
// decoded: for a local folder webMAN writes the text raw, so a file named
// "A&amp;B.iso" is that, not "A&B.iso". And it reaches the caller
// sanitized: a raw ESC, which urlenc leaves alone, is dropped from the name,
// and the path, which would no longer be exact, is left empty.
func TestParseDirNamesComeFromTheLink(t *testing.T) {
	rows := []string{
		dotdotRow,
		fileRow("A%26amp;B.iso", "A&amp;B.iso", "12 b"),
		fileRow("Evil\x1b[2J.iso", "Evil\x1b[2J.iso", "12 b"),
		`<tr><td><a class="w">C&amp;D.iso</a><td> 12 b &nbsp; <td>01-Oct-2026 10:00</tr>`, // no link: the raw text
	}
	l, err := parseDir(page(rows, 0, 3), "/dev_hdd0/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 3 {
		t.Fatalf("%d entries", len(l.Entries))
	}
	if e := l.Entries[0]; e.Name != "A&amp;B.iso" || e.Path != "/dev_hdd0/x/A%26amp;B.iso" {
		t.Errorf("entities decoded: %+v", e)
	}
	if e := l.Entries[1]; e.Name != "Evil[2J.iso" || e.Path != "" {
		t.Errorf("control byte: %+v", e)
	}
	if e := l.Entries[2]; e.Name != "C&amp;D.iso" || e.Path != "" {
		t.Errorf("entities decoded in a row with no link: %+v", e)
	}
}

// A row the parser does not understand fails the page rather than vanishing
// from the totals: a file with no size, a row with no name.
func TestParseDirRefusesUnreadableRows(t *testing.T) {
	for name, row := range map[string]string{
		"no size":   fileRow("x.bin", "x.bin", "&lt;?>"),
		"no name":   fileRow("", "", "12 b"),
		"slash":     `<tr><td><a class="w">a/b</a><td> 12 b &nbsp; <td>01-Oct-2026 10:00</tr>`,
		"bad shape": `<tr><td><a class="w" href="x">x</tr>`,
	} {
		_, err := parseDir(page([]string{dotdotRow, row}, 0, 1), "/dev_hdd0/x")
		if err == nil || errors.Is(err, ErrCut) {
			t.Errorf("%s: err = %v, want a plain error", name, err)
		}
	}
}

// webMAN's own cut: folder_listing stops at (buffer - 16 KiB)/640 - 4 rows,
// ".." included, and its footer counts only those. With the smallest buffer,
// 64 KiB, that is 72 rows; P1's cover pack stopped at 482 (320 KiB). One row
// more or less is a whole folder.
func TestDirCutAtWebMANsRowLimit(t *testing.T) {
	for _, c := range []struct {
		rows int
		cut  bool
	}{{71, false}, {72, true}, {73, false}, {481, false}, {482, true}, {483, false}, {4885, true}} {
		rows := []string{dotdotRow}
		for i := 1; i < c.rows; i++ {
			rows = append(rows, fileRow(fmt.Sprintf("MOCK%05d.JPG", i), fmt.Sprintf("MOCK%05d.JPG", i), "12 b"))
		}
		l, err := parseDir(page(rows, 0, c.rows-1), "/dev_hdd0/x")
		if got := errors.Is(err, ErrCut); got != c.cut {
			t.Errorf("%d rows: cut = %v (%v), want %v", c.rows, got, err, c.cut)
		}
		if len(l.Entries) != c.rows-1 {
			t.Errorf("%d rows: %d entries came back, want every row but ..", c.rows, len(l.Entries))
		}
	}
	for _, n := range []int{72, 175, 277, 380, 482, 4885} {
		if _, ok := rowLimit(n, 640); !ok {
			t.Errorf("rowLimit(%d, 640) = false", n)
		}
	}
	if kib, ok := rowLimit(482, 640); !ok || kib != 320 {
		t.Errorf("rowLimit(482, 640) = %d KiB, %v; want 320", kib, ok)
	}
}

// In a folder whose path ends in /exdata webMAN sizes its rows at 200 bytes,
// not 640, so it stops at (buffer - 16 KiB)/200 - 4 rows: 241 for 64 KiB,
// 1,552 for 320 KiB, 15,642 for 3 MiB. There the 640-byte limits do not
// apply, and nowhere else do the 200-byte ones. webMAN tests the decoded
// path case for case, so an escaped spelling counts and EXDATA does not.
// HEN's .rap folder with 3,000 files and a 320 KiB buffer lists 1,551 of
// them under a footer that agrees.
func TestDirCutAtWebMANsExdataRowLimit(t *testing.T) {
	raps := func(n int) []string {
		rows := []string{dotdotRow}
		for i := 1; i < n; i++ {
			name := fmt.Sprintf("EP0001-NPEB%05d_00-0000000000000001.rap", i)
			rows = append(rows, fileRow(name, name, "16 b"))
		}
		return rows
	}
	for _, c := range []struct {
		folder string
		rows   int
		cut    bool
	}{
		{"/dev_hdd0/exdata", 241, true},
		{"/dev_hdd0/exdata", 1552, true},
		{"/dev_hdd0/exdata", 15642, true},
		{"/dev_hdd0/exdata", 240, false},
		{"/dev_hdd0/exdata", 1551, false},
		{"/dev_hdd0/exdata", 1553, false},
		{"/dev_hdd0/exdata", 72, false},
		{"/dev_hdd0/exdata", 482, false},
		{"/dev_hdd0/home/00000001/exdata", 1552, true},
		{"/dev_usb000/exdata", 1552, true},
		{"/dev_hdd0/exdat%61", 1552, true},
		{"/dev_hdd0/x", 241, false},
		{"/dev_hdd0/x", 1552, false},
		{"/dev_hdd0/myexdata", 1552, false},
		{"/dev_usb000/EXDATA", 1552, false},
		{"/dev_hdd0/exdata/old", 1552, false},
		{"/dev_hdd0/exdata/old", 482, true},
	} {
		l, err := parseDir(page(raps(c.rows), 0, c.rows-1), c.folder)
		if got := errors.Is(err, ErrCut); got != c.cut {
			t.Errorf("%s, %d rows: cut = %v (%v), want %v", c.folder, c.rows, got, err, c.cut)
		}
		if len(l.Entries) != c.rows-1 {
			t.Errorf("%s, %d rows: %d entries came back, want every row but ..", c.folder, c.rows, len(l.Entries))
		}
	}
	if kib, ok := rowLimit(1552, 200); !ok || kib != 320 {
		t.Errorf("rowLimit(1552, 200) = %d KiB, %v; want 320", kib, ok)
	}
	cli, _ := listingConsole(t, map[string]string{"/dev_hdd0/exdata/": page(raps(1552), 0, 1551)})
	l, err := cli.Dir(context.Background(), "/dev_hdd0/exdata")
	if !errors.Is(err, ErrCut) || len(l.Entries) != 1551 || l.Files != 1551 {
		t.Errorf("Dir(/dev_hdd0/exdata) = %d entries, footer %d files, %v; want 1551, 1551 and a cut", len(l.Entries), l.Files, err)
	}
}

// A footer that disagrees with the rows is a cut. dir_cut.html is the page
// webMAN builds when the rows outgrow its buffer: no rows, the footer counts
// them all. A folder row that webMAN counted but dropped is caught too. A
// page with no ".." row still matches, because webMAN subtracts one from
// its folder rows whatever they are.
func TestDirCutWhenTheFooterDisagrees(t *testing.T) {
	b, err := os.ReadFile("testdata/dir_cut.html")
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseDir(string(b), "/dev_hdd0/game/MOCK80608/USRDIR/covers")
	if !errors.Is(err, ErrCut) || len(l.Entries) != 0 || l.Files != 2301 {
		t.Errorf("byte cut: %d entries, %d files, %v", len(l.Entries), l.Files, err)
	}

	dirRow := func(name string) string {
		return `<tr><td><a class="d" href="` + name + `">` + name + `</a><td> <a href="/mount.ps3/dev_hdd0/x/` + name + `">&lt;dir></a> &nbsp; <td>01-Oct-2026 10:00</tr>`
	}
	if _, err := parseDir(page([]string{dotdotRow, dirRow("A")}, 2, 0), "/dev_hdd0/x"); !errors.Is(err, ErrCut) {
		t.Errorf("a dropped folder row: %v", err)
	}
	if _, err := parseDir(page([]string{dirRow("A"), dirRow("B")}, 1, 0), "/dev_hdd0/x"); err != nil {
		t.Errorf("no .. row: %v", err)
	}
}

// A page with rows but no footer stops early; a page with neither is not a
// listing at all (a file's body, say), which is not a cut.
func TestDirWithoutAFooter(t *testing.T) {
	rows := `<table id="files">` + dotdotRow + fileRow("a.iso", "a.iso", "12 b") + `</table>`
	if _, err := parseDir(rows, "/dev_hdd0/x"); !errors.Is(err, ErrCut) {
		t.Errorf("rows and no footer: %v", err)
	}
	if _, err := parseDir("PSF\x01\x01 not a page", "/dev_hdd0/x"); err == nil || errors.Is(err, ErrCut) {
		t.Errorf("no rows and no footer: %v, want a plain error", err)
	}
}

// listingConsole serves pages by request URI, as sent, and records every
// URI asked for.
func listingConsole(t *testing.T, pages map[string]string) (*Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.RequestURI)
		mu.Unlock()
		if p, ok := pages[r.RequestURI]; ok {
			w.Write([]byte(p))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(strings.TrimPrefix(srv.URL, "http://")), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// Dir asks for the folder with a trailing slash, sends an Entry's path as
// it stands (the '+' and the %2B go out as they are), and a 404 is one
// IsNotFound knows, in the error text ps3kit reads today.
func TestDirFromTheConsole(t *testing.T) {
	listing, err := os.ReadFile("testdata/dir_listing.html")
	if err != nil {
		t.Fatal(err)
	}
	sub := page([]string{dotdotRow}, 0, 0)
	cli, asked := listingConsole(t, map[string]string{
		"/dev_hdd0/PS3ISO/":                    string(listing),
		"/dev_hdd0/PS3ISO/Old+Rips/":           sub,
		"/dev_hdd0/PS3ISO/Sample+Game%2B+2.x/": sub,
	})
	ctx := context.Background()

	l, err := cli.Dir(ctx, "/dev_hdd0/PS3ISO")
	if err != nil || len(l.Entries) != 11 {
		t.Fatalf("Dir = %d entries, %v", len(l.Entries), err)
	}
	if _, err := cli.Dir(ctx, "/dev_hdd0/PS3ISO/"); err != nil {
		t.Errorf("trailing slash: %v", err)
	}
	if _, err := cli.Dir(ctx, l.Entries[1].Path); err != nil { // Old+Rips
		t.Errorf("Dir(%q): %v", l.Entries[1].Path, err)
	}
	if _, err := cli.Dir(ctx, "/dev_hdd0/PS3ISO/Sample+Game%2B+2.x"); err != nil {
		t.Errorf("an escaped plus: %v", err)
	}
	_, err = cli.Dir(ctx, "/dev_hdd0/nothing")
	if !IsNotFound(err) || !strings.HasSuffix(err.Error(), ": HTTP 404") {
		t.Errorf("missing folder: %v, want a 404", err)
	}
	want := []string{
		"/dev_hdd0/PS3ISO/",
		"/dev_hdd0/PS3ISO/",
		"/dev_hdd0/PS3ISO/Old+Rips/",
		"/dev_hdd0/PS3ISO/Sample+Game%2B+2.x/",
		"/dev_hdd0/nothing/",
	}
	if a := asked(); !slices.Equal(a, want) {
		t.Errorf("requests =\n  %q\nwant\n  %q", a, want)
	}
}

// A USB drive that Windows formatted holds $RECYCLE.BIN. webMAN lists it
// (folder_listing skips a '$' name only at an NTFS root) and urlenc keeps
// the '$', so the entry's path holds one: Dir takes it back as is.
func TestDirListsADollarFolder(t *testing.T) {
	dirRow := func(name string) string {
		return `<tr><td><a class="d" href="` + name + `">` + name + `</a><td> <a href="/mount.ps3/dev_usb000/` + name + `">&lt;dir></a> &nbsp; <td>01-Oct-2026 10:00</tr>`
	}
	cli, asked := listingConsole(t, map[string]string{
		"/dev_usb000/":              page([]string{dotdotRow, dirRow("$RECYCLE.BIN"), dirRow("PS3ISO")}, 2, 0),
		"/dev_usb000/$RECYCLE.BIN/": page([]string{dotdotRow}, 0, 0),
	})
	ctx := context.Background()
	l, err := cli.Dir(ctx, "/dev_usb000")
	if err != nil || len(l.Entries) != 2 {
		t.Fatalf("Dir = %d entries, %v", len(l.Entries), err)
	}
	e := l.Entries[0]
	if e.Name != "$RECYCLE.BIN" || e.Path != "/dev_usb000/$RECYCLE.BIN" || !e.Dir {
		t.Fatalf("entry %+v", e)
	}
	if _, err := cli.Dir(ctx, e.Path); err != nil {
		t.Errorf("Dir(%q): %v", e.Path, err)
	}
	if a, want := asked(), []string{"/dev_usb000/", "/dev_usb000/$RECYCLE.BIN/"}; !slices.Equal(a, want) {
		t.Errorf("requests = %q, want %q", a, want)
	}
}

// A cut that only the client sees: a body at the 4 MiB cap. The client
// reads no further, so a page that ends there with its footer whole is
// still taken for cut: nothing tells it from a longer one.
func TestDirCutAtTheBodyCap(t *testing.T) {
	small := page([]string{dotdotRow, fileRow("a.iso", "a.iso", "12 b")}, 0, 1)
	long := strings.Replace(small, "</table>", "</table>"+strings.Repeat("<br>", bodyCap/4), 1)
	exact := strings.Replace(small, "<html>", "<html>"+strings.Repeat(" ", bodyCap-len(small)), 1)
	if len(exact) != bodyCap {
		t.Fatalf("the exact page is %d bytes", len(exact))
	}
	cli, _ := listingConsole(t, map[string]string{"/dev_hdd0/long/": long, "/dev_hdd0/exact/": exact})
	for _, p := range []string{"/dev_hdd0/long", "/dev_hdd0/exact"} {
		l, err := cli.Dir(context.Background(), p)
		if !errors.Is(err, ErrCut) || !strings.Contains(err.Error(), "cap") || len(l.Entries) != 1 {
			t.Errorf("%s: %d entries, %v; want the row and a cut at the cap", p, len(l.Entries), err)
		}
	}
}

// A path webMAN would act on rather than list never leaves the client: other
// devices (listing /dev_blind enables it), queries and flags, wildcards,
// a second command, command names, the $ tags webMAN replaces, dot segments,
// control bytes, a bare %. The escaped forms count: webMAN decodes before it
// looks.
func TestDirRefusesPaths(t *testing.T) {
	cli, asked := listingConsole(t, nil)
	ctx := context.Background()
	for _, p := range []string{
		"", "/", "dev_hdd0/x", "//dev_hdd0", "/dev_flash/vsh", "/dev_blind", "/dev_hdd1/caches",
		"/dev_hdd0x", "/dev_usb0/x", "/dev_ntfs0:/x", "/app_home",
		"/dev_hdd0/a?b", "/dev_hdd0/a%3Fb", "/dev_hdd0/x?restart.ps3",
		"/dev_hdd0/*", "/dev_hdd0/a%2A",
		"/dev_hdd0/a;/shutdown.ps3", "/dev_hdd0/a%3B",
		"/dev_hdd0/a#b", "/dev_hdd0/a%23b", "/dev_hdd0/x.ps3$abort", "/dev_hdd0/x.ps3%24abort", "/dev_hdd0/sman.ps3",
		"/dev_hdd0/home/$USERID$", "/dev_hdd0/home/%24USERID%24", "/dev_usb000/a$RND$", "/dev_hdd0/a%24000$", "/dev_hdd0/x/$NEW$/y",
		"/dev_hdd0/../dev_flash", "/dev_hdd0/%2E%2E/x", "/dev_hdd0/./x", "/dev_hdd0//x",
		"/dev_hdd0/a\x1b[2J", "/dev_hdd0/a%1B", "/dev_hdd0/a%", "/dev_hdd0/a%zz",
	} {
		if _, err := cli.Dir(ctx, p); err == nil || IsNotFound(err) {
			t.Errorf("Dir(%q) = %v, want a refusal", p, err)
		}
	}
	if a := asked(); len(a) != 0 {
		t.Errorf("refused paths reached the console: %q", a)
	}
	// and the shapes it takes: a drive alone, a USB drive, webMAN's escapes,
	// a '$' that is part of a name
	for _, p := range []string{"/dev_hdd0", "/dev_hdd0/", "/dev_usb000/PS3ISO", "/dev_hdd0/Old+Rips", "/dev_hdd0/a%2Bb", "/dev_hdd0/Ratchet+%26+Clank",
		"/dev_usb000/$RECYCLE.BIN", "/dev_hdd0/a%24b", "/dev_hdd0/$USERID"} {
		if _, err := dirPath(p); err != nil {
			t.Errorf("dirPath(%q): %v", p, err)
		}
	}
}
