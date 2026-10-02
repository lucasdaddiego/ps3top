package ps3top

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// coverServer answers /good with a PNG and /bad with an HTML error page under a
// 200, which is what webMAN does when it doesn't like a path.
func coverServer(t *testing.T, body []byte) (*Client, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return NewClient(strings.TrimPrefix(srv.URL, "http://")), &hits
}

func TestCoverCacheHitAvoidsTheConsole(t *testing.T) {
	dir := t.TempDir()
	cli, hits := coverServer(t, tinyPNG(t))

	if _, err := loadCover(cli, dir, "/icon.png"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if *hits != 1 {
		t.Fatalf("first load hit the console %d times", *hits)
	}
	if _, err := loadCover(cli, dir, "/icon.png"); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if *hits != 1 {
		t.Errorf("cached cover still hit the console (%d requests)", *hits)
	}
}

// webMAN answers 200 for pages that aren't covers at all. Those bytes used to
// be written straight to a .png cache path and handed to the terminal's image
// decoder on every selection — permanently, since a non-empty cache file was
// trusted forever. One bad response broke that cover with nothing to show why.
func TestNonPNGResponseIsNeitherCachedNorTransmitted(t *testing.T) {
	dir := t.TempDir()
	cli, _ := coverServer(t, []byte("<html><body>404 not found</body></html>"))

	if _, err := loadCover(cli, dir, "/icon.png"); err == nil {
		t.Fatal("an HTML error page was accepted as a cover")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("cache directory holds %d file(s) after a bad response: %v", len(entries), entries)
	}
}

// A cache entry written before the check existed, or left truncated by an
// interrupted write, must be dropped rather than trusted indefinitely.
func TestCorruptCacheEntryIsReplaced(t *testing.T) {
	dir := t.TempDir()
	cli, hits := coverServer(t, tinyPNG(t))
	cache := filepath.Join(dir, coverKey("/icon.png"))

	// truncated PNG: right magic, nothing decodable behind it
	if err := os.WriteFile(cache, tinyPNG(t)[:12], 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCover(cli, dir, "/icon.png")
	if err != nil {
		t.Fatalf("didn't recover from a corrupt cache entry: %v", err)
	}
	if *hits != 1 {
		t.Error("corrupt entry was served from cache instead of refetched")
	}
	if _, err := decodeCover(c.png); err != nil {
		t.Error("returned bytes still aren't a usable PNG")
	}
	// and the repaired entry is what's on disk now
	on, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCover(on); err != nil {
		t.Errorf("cache not repaired: %v", err)
	}
}

// The write goes through a temp file and a rename, so an interruption leaves
// the old entry or none — never a half-written file that looks cached.
func TestCoverCacheWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	cli, _ := coverServer(t, tinyPNG(t))
	if _, err := loadCover(cli, dir, "/icon.png"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly the cache entry, got %v", entries)
	}
	if name := entries[0].Name(); name != coverKey("/icon.png") {
		t.Errorf("leftover temp file %q", name)
	}
}

// isPNG reports whether b decodes as a PNG — the one format the kitty
// transmission is allowed to carry.
func isPNG(b []byte) bool {
	_, format, err := image.DecodeConfig(bytes.NewReader(b))
	return err == nil && format == "png"
}

// tinyJPEG is a cover-pack-shaped JPEG: portrait, like the 260×300 ones
// webMAN's cover packs ship.
func tinyJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 26, 30))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// webMAN serves covers two ways: the ISO's own ICON0 PNG, or a JPEG from a
// cover pack. The terminal is only ever sent PNG, so a JPEG is converted —
// once, on the way into the cache — and its pixel size survives for the
// placement to be shaped by.
func TestJPEGCoversAreConvertedAndCachedAsPNG(t *testing.T) {
	dir := t.TempDir()
	cli, hits := coverServer(t, tinyJPEG(t))

	c, err := loadCover(cli, dir, "/covers/BLES00455.JPG")
	if err != nil {
		t.Fatalf("a JPEG cover was refused: %v", err)
	}
	if c.w != 26 || c.h != 30 {
		t.Errorf("size = %d×%d, want 26×30", c.w, c.h)
	}
	if !isPNG(c.png) {
		t.Error("the terminal would have been handed something other than a PNG")
	}
	on, err := os.ReadFile(filepath.Join(dir, coverKey("/covers/BLES00455.JPG")))
	if err != nil || !isPNG(on) {
		t.Errorf("cache holds the JPEG, not the converted PNG (err %v)", err)
	}
	// the second load is the cached PNG, not another conversion
	if _, err := loadCover(cli, dir, "/covers/BLES00455.JPG"); err != nil || *hits != 1 {
		t.Errorf("cached JPEG cover refetched (%d hits, %v)", *hits, err)
	}
}

func TestDecodeCoverBounds(t *testing.T) {
	if _, err := decodeCover(nil); err == nil {
		t.Error("empty body accepted")
	}
	if _, err := decodeCover([]byte("GIF89a....")); err == nil {
		t.Error("a GIF accepted as a cover")
	}
	if c, err := decodeCover(tinyPNG(t)); err != nil || c.w != 8 || c.h != 8 {
		t.Errorf("a real PNG rejected or mis-sized: %v, %d×%d", err, c.w, c.h)
	}

	// a header claiming implausible dimensions never reaches the decoder's
	// allocator — real webMAN covers are 320x176 ICON0s
	huge := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, huge); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// patch IHDR width to 100000 and let the CRC be wrong — DecodeConfig should
	// object either way, which is the point: nothing implausible gets through
	b[16], b[17], b[18], b[19] = 0x00, 0x01, 0x86, 0xa0
	if _, err := decodeCover(b); err == nil {
		t.Error("a 100000px-wide cover was accepted")
	}
}

// The ONLINE COVERS source may hand webMAN a URL for the icon; that's not a
// path on the console, and prefixing the console's address to it would make
// a request for nothing. Refused before any request goes out.
func TestNonConsoleIconPathIsRefused(t *testing.T) {
	cli, hits := coverServer(t, tinyPNG(t))
	if _, err := loadCover(cli, t.TempDir(), "http://example.com/cover.jpg"); err == nil {
		t.Error("a URL icon was fetched from the console")
	}
	if *hits != 0 {
		t.Errorf("a URL icon still cost %d request(s)", *hits)
	}
}

// frameOrPanic renders a frame and turns a panic into an error.
func frameOrPanic(m *model) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("View panicked: %v", r)
		}
	}()
	_ = m.frame()
	return nil
}

// A games reload that adds a longer title widens the list and shrinks the art
// box: the shown cover must be re-placed to the new box, or artPanel's left
// pad goes negative and strings.Repeat panics.
func TestCoverRePlacedWhenAReloadWidensTheList(t *testing.T) {
	m := liveModel(t, 120)
	m.height = 40
	m.listH = m.height - headerH - footerH
	m.artOn = true
	lib := []Game{
		{Title: "Sample Game™ 2", ID: "MOCK30982", Category: "hdd0/PS3ISO",
			Path: "/dev_hdd0/PS3ISO/SampleGame2.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso",
			IconPath: "/dev_hdd0/tmp/wmtmp/SampleGame2.PNG"},
		{Title: "Alpha", ID: "MOCK00001", Category: "hdd0/PS3ISO",
			Path: "/dev_hdd0/PS3ISO/Alpha.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/Alpha.iso",
			IconPath: "/dev_hdd0/tmp/wmtmp/Alpha.PNG"},
	}
	m.Update(gamesMsg{games: sortGames(append([]Game(nil), lib...))})
	g, ok := m.selectedGame()
	if !ok {
		t.Fatal("no selection")
	}
	icon := m.icon(g)
	m.startCover(icon)
	m.Update(coverMsg{icon: icon, c: cover{png: tinyPNG(t), w: 320, h: 176}})
	m.Update(coverShownMsg{icon: icon})
	long := Game{Title: "Sample Quest: The Beginning - Game of the Year Edition Remastered", ID: "MOCK00103",
		Category: "hdd0/PS3ISO", Path: "/dev_hdd0/PS3ISO/SQ.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SQ.iso"}
	m.Update(gamesMsg{games: sortGames(append(append([]Game(nil), lib...), long))})
	if sel, _ := m.selectedGame(); m.icon(sel) != icon {
		t.Fatal("the selection moved off the shown cover")
	}
	if box, _ := m.artBox(); m.covers[icon].cols > box {
		t.Errorf("the cover is %d cols in a %d-col box after the reload", m.covers[icon].cols, box)
	}
	if err := frameOrPanic(m); err != nil {
		t.Fatal(err)
	}
}

// A resize that lands between the cover's transmit and its shown mark is
// skipped by replaceCovers (the cover is not shown yet): marking it shown
// must re-place it to the box, or the next frame panics.
func TestCoverRePlacedAfterAResizeDuringTransmit(t *testing.T) {
	m := liveModel(t, 160)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	m.artOn = true
	lib := []Game{{Title: "Sample Game™ 2", ID: "MOCK30982", Category: "hdd0/PS3ISO",
		Path: "/dev_hdd0/PS3ISO/SampleGame2.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso",
		IconPath: "/dev_hdd0/tmp/wmtmp/SampleGame2.PNG"}}
	m.Update(gamesMsg{games: lib})
	g, _ := m.selectedGame()
	icon := m.icon(g)
	m.startCover(icon)
	m.Update(coverMsg{icon: icon, c: cover{png: tinyPNG(t), w: 320, h: 176}})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(coverShownMsg{icon: icon})
	if box, _ := m.artBox(); m.covers[icon].cols > box {
		t.Errorf("the cover is %d cols in a %d-col box after the resize", m.covers[icon].cols, box)
	}
	if err := frameOrPanic(m); err != nil {
		t.Fatal(err)
	}
}
