package webman

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// Every package of the index, the oldest version first, whatever order the
// index lists them in and whatever order each tag carries its attributes
// in; the sha1 lowercased, the entities decoded.
func TestParsePatches(t *testing.T) {
	b, err := os.ReadFile("testdata/ver_MOCK37016.xml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parsePatches(string(b))
	if err != nil {
		t.Fatal(err)
	}
	const base = "http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/"
	want := []Patch{
		{"01.02", base + "MOCK37016_T2/90a1b2c3d4e5f607/UP9000-MOCK37016_00-SAMPLERACERPATCH-A0102-V0100-PE.pkg", 12242464, "89abcdef0123456789abcdef0123456789abcdef", "02.8000"},
		{"01.10", base + "MOCK37016_T10/18293a4b5c6d7e8f/UP9000-MOCK37016_00-SAMPLERACERPATCH-A0110-V0100-PE.pkg", 231762688, "00112233445566778899aabbccddeeff00112233", "04.2000"},
		{"01.13", base + "MOCK37016_T13/1a2b3c4d5e6f7081/UP9000-MOCK37016_00-SAMPLERACERPATCH-A0113-V0100-PE.pkg", 9830304, "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", "04.4000"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("parsePatches =\n  %+v\nwant\n  %+v", got, want)
	}
	// the one-package fixture LatestPatch reads parses the same way
	b, err = os.ReadFile("testdata/ver_MOCK30166.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := parsePatches(string(b)); err != nil || len(got) != 1 || got[0].Version != "01.10" || got[0].SystemVer != "02.4000" {
		t.Errorf("ver_MOCK30166 = %+v, %v", got, err)
	}
	// a title with no updates: Sony answers 200 with an empty body
	if got, err := parsePatches(""); err != nil || len(got) != 0 {
		t.Errorf("empty body = %+v, %v; want no packages", got, err)
	}
	// a package FetchPatch could not check fails the index
	pkg := `<titlepatch><tag><package version="01.01" size="100" sha1sum="00112233445566778899aabbccddeeff00112233" url="http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/x.pkg"/></tag></titlepatch>`
	for name, bad := range map[string]string{
		"not an index": "<html>Service Unavailable</html>",
		"no version":   strings.Replace(pkg, `version="01.01"`, `version="v1"`, 1),
		"no size":      strings.Replace(pkg, `size="100"`, ``, 1),
		"zero size":    strings.Replace(pkg, `size="100"`, `size="0"`, 1),
		"short sha1":   strings.Replace(pkg, `sha1sum="00112233445566778899aabbccddeeff00112233"`, `sha1sum="0011"`, 1),
		"no URL":       strings.Replace(pkg, `url="http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/x.pkg"`, ``, 1),
	} {
		if got, err := parsePatches(bad); err == nil {
			t.Errorf("%s: parsed as %+v", name, got)
		}
	}
	if !lessVersion("01.09", "01.10") || !lessVersion("01.99", "02.00") || lessVersion("01.10", "01.10") {
		t.Error("lessVersion orders wrongly")
	}
}

// The stand-in for Sony's chain: a self-signed RSA root, and leaves it
// signs with SHA256WithRSA that name their host in the Common Name only, as
// Sony's leaf does. The root costs an RSA key, so it is made once.
var testRoot = sync.OnceValues(func() (*x509.Certificate, *rsa.PrivateKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test DNAS Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return root, key
})

func testLeaf(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	root, rootKey := testRoot()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:       big.NewInt(time.Now().UnixNano()),
		Subject:            pkix.Name{CommonName: cn},
		NotBefore:          time.Now().Add(-time.Hour),
		NotAfter:           time.Now().Add(time.Hour),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, root.Raw}, PrivateKey: key}
}

// fakeSony serves h over TLS with a leaf for cn that the test root signed,
// pins the test root in Sony's place, and points every dial of the Sony
// clients at the server. The rest is production: the transport, the hand
// check of the chain against patchHost, the redirect rule. The returned
// func counts the requests that arrived.
func fakeSony(t *testing.T, cn string, h http.HandlerFunc) (hits func() int) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		h(w, r)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{testLeaf(t, cn)}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes are the point
	srv.StartTLS()
	t.Cleanup(srv.Close)

	addr := srv.Listener.Addr().String()
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	transport := func() *http.Transport {
		tr := sonyTransport()
		tr.DialContext = dial
		return tr
	}
	root, _ := testRoot()
	oldRoot, oldPatch, oldFetch := sonyRoot, patchClient, fetchClient
	sonyRoot = root
	patchClient = &http.Client{Timeout: oldPatch.Timeout, Transport: transport(), CheckRedirect: oldPatch.CheckRedirect}
	fetchClient = &http.Client{Transport: transport(), CheckRedirect: oldFetch.CheckRedirect}
	t.Cleanup(func() { sonyRoot, patchClient, fetchClient = oldRoot, oldPatch, oldFetch })
	return func() int { return int(n.Load()) }
}

const sonyCN = "*.ww.np.dl.playstation.net"

// Patches against a stand-in Sony: the index sorted, a 404 that IsNotFound
// knows, an empty 200 as no packages, and a title ID refused before any
// request.
func TestPatchesFromSony(t *testing.T) {
	index, err := os.ReadFile("testdata/ver_MOCK37016.xml")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var asked []string
	hits := fakeSony(t, sonyCN, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Host+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/tpl/np/MOCK37016/MOCK37016-ver.xml":
			w.Write(index)
		case "/tpl/np/MOCK00001/MOCK00001-ver.xml":
			// 200, nothing in it
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	ps, err := Patches(ctx, "MOCK37016")
	if err != nil || len(ps) != 3 || ps[0].Version != "01.02" || ps[2].Version != "01.13" {
		t.Errorf("Patches = %+v, %v", ps, err)
	}
	if ps, err := Patches(ctx, "MOCK00001"); err != nil || len(ps) != 0 {
		t.Errorf("empty index = %+v, %v; want no packages", ps, err)
	}
	if _, err := Patches(ctx, "MOCK00000"); !IsNotFound(err) {
		t.Errorf("unknown title: %v, want a 404", err)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{
		"a0.ww.np.dl.playstation.net/tpl/np/MOCK37016/MOCK37016-ver.xml",
		"a0.ww.np.dl.playstation.net/tpl/np/MOCK00001/MOCK00001-ver.xml",
		"a0.ww.np.dl.playstation.net/tpl/np/MOCK00000/MOCK00000-ver.xml",
	}) {
		t.Errorf("requests = %q", got)
	}
	before := hits()
	for _, id := range []string{"", "mock37016", "MOCK37016/../x", "MOCK3701"} {
		if _, err := Patches(ctx, id); err == nil {
			t.Errorf("Patches(%q) accepted it", id)
		}
	}
	if hits() != before {
		t.Error("a bad title ID reached Sony")
	}
}

// testPkg is a package as Sony serves one: a body, then a 32-byte digest,
// and the index's sha1 over the body alone.
func testPkg() (pkg []byte, sum string) {
	pkg = make([]byte, 64<<10+pkgDigestLen)
	for i := range pkg {
		pkg[i] = byte(i*7 + i>>9)
	}
	head := sha1.Sum(pkg[:len(pkg)-pkgDigestLen])
	copy(pkg[len(pkg)-pkgDigestLen:], head[:])
	return pkg, hex.EncodeToString(head[:])
}

const testPkgPath = "/tppkg/np/MOCK37016/MOCK37016_T2/90a1b2c3d4e5f607/UP9000-MOCK37016_00-SAMPLERACERPATCH-A0102-V0100-PE.pkg"

// leftovers lists what a fetch left in dir besides keep.
func leftovers(t *testing.T, dir string, keep ...string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		if !slices.Contains(keep, de.Name()) {
			out = append(out, de.Name())
		}
	}
	return out
}

// FetchPatch against a stand-in Sony: the package comes from a0 by the
// index's path, lands at dst only when its size and its sha1 (over all but
// the last 32 bytes) match, and a failed fetch leaves no file behind and an
// existing dst as it was.
func TestFetchPatch(t *testing.T) {
	pkg, sum := testPkg()
	var mu sync.Mutex
	var asked []string
	mode := "" // "chunked": no Content-Length, so only the byte count can tell; "extra": and bytes after the package
	setMode := func(m string) { mu.Lock(); mode = m; mu.Unlock() }
	fakeSony(t, sonyCN, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Host+r.URL.Path)
		mode := mode
		mu.Unlock()
		switch {
		case r.URL.Path == "/tppkg/np/MOCK37016/moved.pkg":
			http.Redirect(w, r, "https://a0.ww.np.dl.playstation.net"+testPkgPath, http.StatusFound)
		case r.URL.Path != testPkgPath:
			http.NotFound(w, r)
		case mode != "":
			w.Write(pkg[:1000])
			w.(http.Flusher).Flush()
			w.Write(pkg[1000:])
			if mode == "extra" {
				w.Write([]byte("EXTRA"))
			}
		default: // as Sony does: the length up front
			w.Header().Set("Content-Length", strconv.Itoa(len(pkg)))
			w.Write(pkg)
		}
	})
	ctx := context.Background()
	good := Patch{Version: "01.02", URL: "http://b0.ww.np.dl.playstation.net" + testPkgPath, Size: int64(len(pkg)), SHA1: sum}

	dir := t.TempDir()
	dst := filepath.Join(dir, "patch.pkg")
	if err := FetchPatch(ctx, good, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, pkg) {
		t.Fatalf("dst holds %d bytes (%v), want the package", len(b), err)
	}
	if left := leftovers(t, dir, "patch.pkg"); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{"a0.ww.np.dl.playstation.net" + testPkgPath}) {
		t.Errorf("requests = %q, want a0 with the index's path", got)
	}

	whole := sha1.Sum(pkg)
	for _, c := range []struct {
		name, mode string
		p          Patch
		why        string // which guard must fire
	}{
		{"the whole file's sha1", "", Patch{Version: "01.02", URL: good.URL, Size: good.Size, SHA1: hex.EncodeToString(whole[:])}, "sha1"},
		{"another sha1", "", Patch{Version: "01.02", URL: good.URL, Size: good.Size, SHA1: strings.Repeat("0", 40)}, "sha1"},
		{"a size above the body", "", Patch{Version: "01.02", URL: good.URL, Size: good.Size + 1, SHA1: sum}, "Sony sends"},
		{"a size below the body", "", Patch{Version: "01.02", URL: good.URL, Size: good.Size - 1, SHA1: sum}, "Sony sends"},
		{"a short chunked body", "chunked", Patch{Version: "01.02", URL: good.URL, Size: good.Size + 1, SHA1: sum}, "EOF"},
		{"a long chunked body", "chunked", Patch{Version: "01.02", URL: good.URL, Size: good.Size - 1, SHA1: sum}, "more than"},
		{"bytes after a good package", "extra", good, "more than"},
		{"a redirect", "", Patch{Version: "01.02", URL: "http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/moved.pkg", Size: good.Size, SHA1: sum}, "redirect"},
	} {
		setMode(c.mode)
		if err := os.WriteFile(dst, []byte("the old package"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := FetchPatch(ctx, c.p, dst); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%s: %v, want an error about %q", c.name, err, c.why)
		}
		if b, _ := os.ReadFile(dst); string(b) != "the old package" {
			t.Errorf("%s: dst was replaced", c.name)
		}
		if left := leftovers(t, dir, "patch.pkg"); len(left) != 0 {
			t.Errorf("%s: left behind %q", c.name, left)
		}
	}
	setMode("")

	missing := good
	missing.URL = "http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/gone.pkg"
	if err := FetchPatch(ctx, missing, filepath.Join(dir, "gone.pkg")); !IsNotFound(err) {
		t.Errorf("a package Sony lacks: %v, want a 404", err)
	}
}

// Only a package path on Sony's update service is fetched, and only from
// a0 behind the pinned root: anything else is refused before a request, a
// chain from another root or a leaf for another host after the handshake,
// and neither leaves a file.
func TestFetchPatchHostAndRootRules(t *testing.T) {
	pkg, sum := testPkg()
	hits := fakeSony(t, sonyCN, func(w http.ResponseWriter, r *http.Request) { w.Write(pkg) })
	ctx := context.Background()
	dir := t.TempDir()
	dst := filepath.Join(dir, "patch.pkg")
	p := func(u string) Patch { return Patch{Version: "01.02", URL: u, Size: int64(len(pkg)), SHA1: sum} }

	for _, u := range []string{
		"",
		"http://evil.example.com" + testPkgPath,
		"http://b0.ww.np.dl.playstation.net.evil.example.com" + testPkgPath,
		"http://ww.np.dl.playstation.net" + testPkgPath,
		"http://x.b0.ww.np.dl.playstation.net" + testPkgPath,
		"http://B0.WW.NP.DL.PLAYSTATION.NET" + testPkgPath,
		"http://b0.ww.np.dl.playstation.net:8080" + testPkgPath,
		"http://user@b0.ww.np.dl.playstation.net" + testPkgPath,
		"ftp://b0.ww.np.dl.playstation.net" + testPkgPath,
		"http://b0.ww.np.dl.playstation.net" + testPkgPath + "?x=1",
		"http://b0.ww.np.dl.playstation.net" + testPkgPath + "#x",
		"http://b0.ww.np.dl.playstation.net/tpl/np/MOCK37016/MOCK37016-ver.xml",
		"http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/../../../x.pkg",
		"http://b0.ww.np.dl.playstation.net/tppkg/np/MOCK37016/a%2Fb.pkg",
		"//b0.ww.np.dl.playstation.net" + testPkgPath,
	} {
		if err := FetchPatch(ctx, p(u), dst); err == nil {
			t.Errorf("FetchPatch(%q) accepted it", u)
		}
	}
	{
		bad := p("http://b0.ww.np.dl.playstation.net" + testPkgPath)
		bad.SHA1 = "not hex"
		if err := FetchPatch(ctx, bad, dst); err == nil {
			t.Error("a sha1 that is not hex was accepted")
		}
		bad.SHA1, bad.Size = sum, pkgDigestLen-1
		if err := FetchPatch(ctx, bad, dst); err == nil {
			t.Error("a size below the digest was accepted")
		}
	}
	if hits() != 0 {
		t.Errorf("%d refused fetches reached the server", hits())
	}
	// the https form of the same URL is fine
	if err := FetchPatch(ctx, p("https://b0.ww.np.dl.playstation.net"+testPkgPath), dst); err != nil {
		t.Errorf("https URL: %v", err)
	}
	os.Remove(dst)

	// the real pin: the stand-in's chain is not Sony's
	sonyRoot = sonyRootFromPEM(t)
	if err := FetchPatch(ctx, p("http://b0.ww.np.dl.playstation.net"+testPkgPath), dst); err == nil || !strings.Contains(err.Error(), "pinned root") {
		t.Errorf("a chain from another root: %v", err)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
}

// A leaf the pinned root signed, but for another host, fails the handshake.
func TestFetchPatchRefusesALeafForAnotherHost(t *testing.T) {
	pkg, sum := testPkg()
	fakeSony(t, "evil.example.com", func(w http.ResponseWriter, r *http.Request) { w.Write(pkg) })
	dst := filepath.Join(t.TempDir(), "patch.pkg")
	err := FetchPatch(context.Background(), Patch{Version: "01.02", URL: "http://b0.ww.np.dl.playstation.net" + testPkgPath, Size: int64(len(pkg)), SHA1: sum}, dst)
	if err == nil || !strings.Contains(err.Error(), "not for a0.ww.np.dl.playstation.net") {
		t.Errorf("a leaf for another host: %v", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dst exists after a refused handshake: %v", err)
	}
}

// A fetch whose process dies runs no deferred cleanup, so its temporary
// file stays: a SIGKILL, or ps3sync's second Ctrl-C, which is os.Exit(130).
// Each download first removes what earlier fetches to the same dst left, and
// no other file in the folder.
func TestFetchPatchRemovesWhatADeadFetchLeft(t *testing.T) {
	pkg, sum := testPkg()
	good := Patch{Version: "01.02", URL: "http://b0.ww.np.dl.playstation.net" + testPkgPath, Size: int64(len(pkg)), SHA1: sum}
	if dst := os.Getenv("WEBMAN_DEAD_FETCH_DST"); dst != "" {
		deadFetch(t, good, pkg, dst, os.Getenv("WEBMAN_DEAD_FETCH_MODE"))
		return
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "patch.pkg")
	for _, mode := range []string{"kill", "exit"} {
		before := partFiles(dir)
		var out bytes.Buffer
		cmd := exec.Command(os.Args[0], "-test.run=^TestFetchPatchRemovesWhatADeadFetchLeft$", "-test.timeout=1m")
		cmd.Env = append(os.Environ(), "WEBMAN_DEAD_FETCH_DST="+dst, "WEBMAN_DEAD_FETCH_MODE="+mode)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if mode == "kill" {
			_, ok := waitNewPart(dir, before, 1000, 30*time.Second)
			cmd.Process.Kill()
			if !ok {
				cmd.Wait()
				t.Fatalf("the child's fetch never wrote 1000 bytes:\n%s", out.String())
			}
		}
		err := cmd.Wait()
		var ee *exec.ExitError
		if mode == "exit" && (!errors.As(err, &ee) || ee.ExitCode() != 130) {
			t.Fatalf("the child: %v, want exit 130:\n%s", err, out.String())
		}
		t.Logf("after a fetch that ends by %s: %v", mode, partFiles(dir))
	}
	if n := len(partFiles(dir)); n != 1 {
		t.Errorf("two dead fetches left %d temporary files, want 1: the second removes the first one's", n)
	}

	// none of these is .patch.pkg.<digits>.part as a regular file
	keep := []string{".other.pkg.123.part", "patch.pkg.part", ".patch.pkg.part", ".patch.pkg..part", ".patch.pkg.abc.part", ".patch.pkg.1.2.part"}
	for _, name := range keep {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a fetch to patch.pkg"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".patch.pkg.42.part"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, ".patch.pkg.42.part")
	fakeSony(t, sonyCN, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(pkg)))
		w.Write(pkg)
	})
	if err := FetchPatch(context.Background(), good, dst); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(dst); err != nil || !bytes.Equal(b, pkg) {
		t.Fatalf("dst holds %d bytes (%v), want the package", len(b), err)
	}
	if left := leftovers(t, dir, append(slices.Clone(keep), "patch.pkg")...); len(left) != 0 {
		t.Errorf("a fetch to the same dst kept what dead fetches left: %q", left)
	}
	for _, name := range keep {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the fetch removed %s: %v", name, err)
		}
	}
}

// deadFetch is the child process of TestFetchPatchRemovesWhatADeadFetchLeft.
// Sony sends the first 1000 bytes and then waits, and the process ends in
// the middle of the fetch: by the parent's SIGKILL, or (mode "exit") by its
// own os.Exit(130) once its temporary file holds those bytes.
func deadFetch(t *testing.T, p Patch, pkg []byte, dst, mode string) {
	fakeSony(t, sonyCN, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(pkg)))
		w.Write(pkg[:1000])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	if mode == "exit" {
		before := partFiles(filepath.Dir(dst))
		go func() {
			if _, ok := waitNewPart(filepath.Dir(dst), before, 1000, 30*time.Second); ok {
				os.Exit(130)
			}
		}()
	}
	err := FetchPatch(context.Background(), p, dst)
	t.Fatalf("the fetch returned: %v", err)
}

// partFiles lists the .part files in dir with their sizes.
func partFiles(dir string) map[string]int64 {
	out := map[string]int64{}
	des, _ := os.ReadDir(dir)
	for _, de := range des {
		if info, err := de.Info(); err == nil && info.Mode().IsRegular() && strings.HasSuffix(de.Name(), ".part") {
			out[de.Name()] = info.Size()
		}
	}
	return out
}

// waitNewPart waits for a .part file in dir that is not in before and holds
// at least atLeast bytes.
func waitNewPart(dir string, before map[string]int64, atLeast int64, timeout time.Duration) (string, bool) {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		for name, size := range partFiles(dir) {
			if _, old := before[name]; !old && size >= atLeast {
				return name, true
			}
		}
	}
	return "", false
}

// Two fetches to one dst that overlap: the later one removes the earlier
// one's temporary file, so the earlier one fails at its rename. A fetch may
// report success only when dst holds the whole package.
func TestSavePkgOverlappingFetchesToOneDst(t *testing.T) {
	pkg, sum := testPkg()
	want, _ := hex.DecodeString(sum)
	p := Patch{Version: "01.02", Size: int64(len(pkg)), SHA1: sum}
	dir := t.TempDir()
	dst := filepath.Join(dir, "patch.pkg")
	// start runs savePkg on a pipe and returns once it has read 1000 bytes,
	// so its temporary file exists
	start := func() (*io.PipeWriter, <-chan error) {
		r, w := io.Pipe()
		t.Cleanup(func() { w.CloseWithError(errors.New("test over")) })
		done := make(chan error, 1)
		go func() {
			err := savePkg(r, p, want, dst)
			r.CloseWithError(errors.New("savePkg returned"))
			done <- err
		}()
		if _, err := w.Write(pkg[:1000]); err != nil {
			t.Fatal(err)
		}
		return w, done
	}
	finish := func(w *io.PipeWriter, done <-chan error) error {
		w.Write(pkg[1000:])
		w.Close()
		return <-done
	}
	wa, doneA := start()
	wb, doneB := start()
	errA := finish(wa, doneA)
	if b, _ := os.ReadFile(dst); errA == nil && !bytes.Equal(b, pkg) {
		t.Fatalf("the earlier fetch reports success, but dst holds %d of %d bytes", len(b), len(pkg))
	}
	if err := finish(wb, doneB); err != nil {
		t.Fatalf("the later fetch: %v", err)
	}
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, pkg) {
		t.Fatalf("dst holds %d bytes, want the package", len(b))
	}
	if left := leftovers(t, dir, "patch.pkg"); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
	t.Logf("the earlier fetch: %v", errA)
}

// sonyRootFromPEM parses the pinned PEM afresh: the root a test puts back.
func sonyRootFromPEM(t *testing.T) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(sonyRootPEM))
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The package route, by hand only: PS3TOP_LIVE=1 go test -run TestLiveFetchPatch
// - BLUS30839's index and its 01.02 package (9,463,456 B, probe P5 on
// 2026-10-11), fetched from a0 behind the pinned root and kept only if the
// size and the sha1 match. About 9 MB into a temp folder, removed after.
func TestLiveFetchPatch(t *testing.T) {
	if os.Getenv("PS3TOP_LIVE") == "" {
		t.Skip("set PS3TOP_LIVE=1 to ask Sony for real")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ps, err := Patches(ctx, "BLUS30839")
	if err != nil {
		t.Fatalf("Patches: %v", err)
	}
	i := slices.IndexFunc(ps, func(p Patch) bool { return p.Version == "01.02" })
	if i < 0 {
		t.Fatalf("no 01.02 among %d packages", len(ps))
	}
	dst := filepath.Join(t.TempDir(), "patch.pkg")
	if err := FetchPatch(ctx, ps[i], dst); err != nil {
		t.Fatalf("FetchPatch: %v", err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Size() != ps[i].Size {
		t.Fatalf("dst: %v, %v", st, err)
	}
	t.Logf("BLUS30839 %s: %d B from a0, size and sha1 (without the last 32 B) checked", ps[i].Version, st.Size())
}
