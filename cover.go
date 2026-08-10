package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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

// loadCover returns the PNG for a webMAN icon path, hitting the PS3 only on
// cache miss.
func loadCover(cli *Client, cacheDir, iconPath string) ([]byte, error) {
	cache := filepath.Join(cacheDir, coverKey(iconPath))
	if b, err := os.ReadFile(cache); err == nil && len(b) > 0 {
		return b, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, err := cli.Cover(ctx, iconPath)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(cache, b, 0o644)
	return b, nil
}
