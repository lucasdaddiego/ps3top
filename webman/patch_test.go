package webman

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// No test may reach Sony: the lookup is stubbed for the whole suite and the
// tests that care swap in their own.
func init() {
	PatchLookup = func(context.Context, string) (string, error) { return "", errors.New("stubbed") }
}

// sonyChain is the chain a0.ww.np.dl.playstation.net served on 2026-08-21:
// the leaf, then the root. Public certificates, nothing of the console's.
func sonyChain(t *testing.T) (leaf, root []byte) {
	t.Helper()
	b, err := os.ReadFile("testdata/sony_chain.crt")
	if err != nil {
		t.Fatal(err)
	}
	var ders [][]byte
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		ders = append(ders, block.Bytes)
	}
	if len(ders) != 2 {
		t.Fatalf("chain has %d certificates, want leaf + root", len(ders))
	}
	return ders[0], ders[1]
}

// Go's verifier refuses the SHA-1 signature on Sony's leaf, so the chain is
// checked by hand against the pinned root. This is that check against the
// real chain: it passes as served, and fails on each thing it's meant to
// catch — wrong host, outside the validity window, a tampered leaf, a
// different root.
func TestVerifyLeafAgainstSonyRoot(t *testing.T) {
	leafDER, rootDER := sonyChain(t)
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	// the served root IS the pinned one
	if !root.Equal(sonyRoot) {
		t.Fatal("the pinned root isn't the one in the served chain")
	}
	within := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

	if err := verifyLeaf([][]byte{leafDER}, sonyRoot, patchHost, within); err != nil {
		t.Fatalf("the real chain was refused: %v", err)
	}
	if err := verifyLeaf([][]byte{leafDER}, sonyRoot, "example.com", within); err == nil {
		t.Error("a leaf for another host was accepted")
	}
	if err := verifyLeaf([][]byte{leafDER}, sonyRoot, patchHost, within.AddDate(10, 0, 0)); err == nil {
		t.Error("an expired leaf was accepted")
	}
	if err := verifyLeaf(nil, sonyRoot, patchHost, within); err == nil {
		t.Error("an empty chain was accepted")
	}
	// flip a byte inside the signed part: the signature no longer verifies
	tampered := append([]byte(nil), leafDER...)
	leaf, _ := x509.ParseCertificate(leafDER)
	off := strings.Index(string(tampered), string(leaf.RawSubject)) + 10
	tampered[off] ^= 0x01
	if err := verifyLeaf([][]byte{tampered}, sonyRoot, patchHost, within); err == nil {
		t.Error("a tampered leaf was accepted")
	}
	// a self-signed stand-in for the root: the leaf isn't its
	if err := verifyLeaf([][]byte{leafDER}, leaf, patchHost, within); err == nil {
		t.Error("a leaf was accepted against a root that didn't issue it")
	}
}

func TestNewestVersion(t *testing.T) {
	b, err := os.ReadFile("testdata/ver_MOCK30166.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got := newestVersion(string(b)); got != "01.10" {
		t.Errorf("newestVersion(fixture) = %q, want 01.10", got)
	}
	// several packages: the highest wins, whatever the order
	multi := `<titlepatch><tag><package version="01.03"/><package version="01.15"/><package version="01.07"/></tag></titlepatch>`
	if got := newestVersion(multi); got != "01.15" {
		t.Errorf("newestVersion(multi) = %q, want 01.15", got)
	}
	if got := newestVersion("<html>nope</html>"); got != "" {
		t.Errorf("newestVersion(html) = %q, want empty", got)
	}
	if !PatchBehind("01.10", "01.15") || PatchBehind("01.15", "01.15") || PatchBehind("", "01.15") || PatchBehind("01.15", "") {
		t.Error("PatchBehind compares wrongly")
	}
}

func TestNameMatchesCommonNameWildcard(t *testing.T) {
	leafDER, _ := sonyChain(t)
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.DNSNames) != 0 {
		t.Skip("Sony's leaf grew SANs; the CN path is moot")
	}
	for host, want := range map[string]bool{
		"a0.ww.np.dl.playstation.net":    true,
		"B0.WW.NP.DL.PLAYSTATION.NET":    true,
		"ww.np.dl.playstation.net":       false, // the wildcard needs a label
		"x.a0.ww.np.dl.playstation.net":  false, // and covers exactly one
		"a0.ww.np.dl.playstation.net.ev": false,
		"example.com":                    false,
	} {
		if got := nameMatches(leaf, host); got != want {
			t.Errorf("nameMatches(%q) = %v, want %v", host, got, want)
		}
	}
}

// The real thing, by hand only: PS3TOP_LIVE=1 go test -run TestLivePatchLookup
// — the pinned root, the hand verifier and Sony's index, end to end.
func TestLivePatchLookup(t *testing.T) {
	if os.Getenv("PS3TOP_LIVE") == "" {
		t.Skip("set PS3TOP_LIVE=1 to ask Sony for real")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got, err := LatestPatch(ctx, "BLUS30166") // BioShock: 01.10 as of 2026-08-21
	if err != nil {
		t.Fatalf("LatestPatch: %v", err)
	}
	if got == "" {
		t.Error("no version came back")
	}
	t.Logf("BLUS30166 newest patch: %s", got)
	// a title with no patches is "", not an error
	if got, err := LatestPatch(ctx, "MOCK00000"); err != nil || got != "" {
		t.Errorf("unknown title = %q, %v; want empty, nil", got, err)
	}
}
