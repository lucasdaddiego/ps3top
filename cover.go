package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"time"
)

// coverKey names the cache file from the FULL icon path — basenames can
// collide when entries reference disc ICON0.PNGs instead of wmtmp mirrors,
// and a basename key would then show the wrong cover forever.
func coverKey(iconPath string) string {
	sum := sha1.Sum([]byte(iconPath))
	return hex.EncodeToString(sum[:8]) + ".png"
}

// coverMaxPx bounds what we'll hand the terminal to decode. Real webMAN covers
// are 320×176 ICON0s; anything near this is not one.
const coverMaxPx = 4096

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// checkPNG rejects bytes that aren't a PNG the terminal should be asked to
// decode. webMAN answers 200 for pages that aren't covers at all (an error
// page, a redirect body), and without this those get written to a .png cache
// path and re-fed to the terminal's image decoder on every selection, forever —
// one bad response permanently breaks that one cover with no way to notice why.
func checkPNG(b []byte) error {
	if !bytes.HasPrefix(b, pngMagic) {
		return fmt.Errorf("not a PNG (%d bytes)", len(b))
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("undecodable PNG: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > coverMaxPx || cfg.Height > coverMaxPx {
		return fmt.Errorf("implausible cover size %dx%d", cfg.Width, cfg.Height)
	}
	return nil
}

// coverCached reports whether a cover is already on disk — the cheap check
// that decides whether a selection can show its art at once or has to wait
// out the debounce before asking the console.
func coverCached(cacheDir, iconPath string) bool {
	fi, err := os.Stat(filepath.Join(cacheDir, coverKey(iconPath)))
	return err == nil && fi.Size() > 0
}

// loadCover returns the PNG for a webMAN icon path, hitting the PS3 only on
// cache miss.
func loadCover(cli *Client, cacheDir, iconPath string) ([]byte, error) {
	cache := filepath.Join(cacheDir, coverKey(iconPath))
	if b, err := os.ReadFile(cache); err == nil && len(b) > 0 {
		if checkPNG(b) == nil {
			return b, nil
		}
		// a cache entry written before this check existed, or a truncated
		// write — drop it and re-fetch rather than trusting it forever
		os.Remove(cache)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, err := cli.Cover(ctx, iconPath)
	if err != nil {
		return nil, err
	}
	if err := checkPNG(b); err != nil {
		return nil, fmt.Errorf("cover %s: %w", filepath.Base(iconPath), err)
	}
	writeCache(cache, b)
	return b, nil
}

// writeCache writes through a temp file in the same directory: an interrupted
// write then leaves the old entry (or none) rather than a truncated file that
// looks cached and decodes to nothing. Failures are silent by design — a
// missing cache costs one refetch, and there's nothing the user would do about
// it mid-session.
func writeCache(path string, b []byte) {
	f, err := os.CreateTemp(filepath.Dir(path), ".cover-*")
	if err != nil {
		return
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
}
