package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// coverKey names the cache file from the FULL icon path — basenames can
// collide when entries reference disc ICON0.PNGs instead of wmtmp mirrors,
// and a basename key would then show the wrong cover forever.
func coverKey(iconPath string) string {
	sum := sha1.Sum([]byte(iconPath))
	return hex.EncodeToString(sum[:8]) + ".png"
}

// coverMaxPx bounds what we'll hand the terminal to decode. webMAN covers are
// 320×176 ICON0s or 260×300 multiMAN JPEGs; anything near this is not one.
const coverMaxPx = 4096

// cover is an image ready for the terminal: PNG bytes (the one format the
// kitty transmission sends) plus the pixel size the placement is shaped to.
type cover struct {
	png  []byte
	w, h int
}

// decodeCover accepts the two things webMAN serves as a game icon, which is
// the covers source on /setup.ps3: ICON0.PNG is the ISO's own 320×176 icon,
// MM COVERS is multiMAN's folder of 260×300 JPEGs (/dev_hdd0/game/BLES80608/
// USRDIR/covers/<ID>.JPG, with whatever cover pack was dropped in there). It
// returns a PNG either way, since that's what goes down the wire. Anything
// else is refused: webMAN answers 200 for pages that aren't covers at all
// (an error page, a redirect body), and without this those were written to
// the cache and re-fed to the terminal's decoder on every selection, forever.
func decodeCover(b []byte) (cover, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return cover{}, fmt.Errorf("not an image (%d bytes)", len(b))
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > coverMaxPx || cfg.Height > coverMaxPx {
		return cover{}, fmt.Errorf("implausible cover size %dx%d", cfg.Width, cfg.Height)
	}
	switch format {
	case "png":
		return cover{png: b, w: cfg.Width, h: cfg.Height}, nil
	case "jpeg":
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			return cover{}, fmt.Errorf("undecodable JPEG: %w", err)
		}
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			return cover{}, err
		}
		return cover{png: out.Bytes(), w: cfg.Width, h: cfg.Height}, nil
	}
	return cover{}, fmt.Errorf("unsupported image format %q", format)
}

// coverCached reports whether a cover is already on disk — the cheap check
// that decides whether a selection can show its art at once or has to wait
// out the debounce before asking the console.
func coverCached(cacheDir, iconPath string) bool {
	fi, err := os.Stat(filepath.Join(cacheDir, coverKey(iconPath)))
	return err == nil && fi.Size() > 0
}

// loadCover returns the cover for a webMAN icon path, hitting the PS3 only
// on cache miss. The cache always holds the PNG, so a JPEG is converted once.
//
// Only console paths are fetched. The ONLINE COVERS source on /setup.ps3 has
// webMAN pull covers from the web itself, and what it then writes into the
// icon field hasn't been captured — if it's a URL, gluing it onto the
// console's address would request nonsense, so it's refused outright.
func loadCover(cli *Client, cacheDir, iconPath string) (cover, error) {
	if !strings.HasPrefix(iconPath, "/") {
		return cover{}, fmt.Errorf("cover %q: not a console path", iconPath)
	}
	cache := filepath.Join(cacheDir, coverKey(iconPath))
	if b, err := os.ReadFile(cache); err == nil && len(b) > 0 {
		if c, err := decodeCover(b); err == nil {
			return c, nil
		}
		// a cache entry written before this check existed, or a truncated
		// write — drop it and re-fetch rather than trusting it forever
		os.Remove(cache)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, err := cli.Cover(ctx, iconPath)
	if err != nil {
		return cover{}, err
	}
	c, err := decodeCover(b)
	if err != nil {
		return cover{}, fmt.Errorf("cover %s: %w", filepath.Base(iconPath), err)
	}
	writeCache(cache, c.png)
	return c, nil
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
