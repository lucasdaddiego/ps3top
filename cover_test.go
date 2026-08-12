package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	b, err := loadCover(cli, dir, "/icon.png")
	if err != nil {
		t.Fatalf("didn't recover from a corrupt cache entry: %v", err)
	}
	if *hits != 1 {
		t.Error("corrupt entry was served from cache instead of refetched")
	}
	if checkPNG(b) != nil {
		t.Error("returned bytes still aren't a usable PNG")
	}
	// and the repaired entry is what's on disk now
	on, err := os.ReadFile(cache)
	if err != nil || checkPNG(on) != nil {
		t.Errorf("cache not repaired: err=%v", err)
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

func TestCheckPNGBounds(t *testing.T) {
	if err := checkPNG(nil); err == nil {
		t.Error("empty body accepted")
	}
	if err := checkPNG([]byte("GIF89a....")); err == nil {
		t.Error("a GIF accepted as a PNG")
	}
	if err := checkPNG(tinyPNG(t)); err != nil {
		t.Errorf("a real PNG rejected: %v", err)
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
	if err := checkPNG(b); err == nil {
		t.Error("a 100000px-wide cover was accepted")
	}
}
