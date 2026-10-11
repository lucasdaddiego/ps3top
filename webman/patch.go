package webman

// Patch-available check for the running game. Sony still serves the PS3
// title-update index: https://a0.ww.np.dl.playstation.net/tpl/np/<ID>/<ID>-ver.xml
// lists every patch package for a title with its version. One GET to Sony
// (not the console) per distinct running title per session, and the header
// says "v01.10 → 01.15 available" when the installed version is behind.
//
// The server's certificate is signed by Sony's private CA ("SCEI DNAS Root
// 05", 2004–2037) with SHA-1, which Go's verifier refuses outright, so the
// standard chain check can't be made to pass by pinning anything. Instead the
// root is pinned here and the leaf is checked by hand: issued by that root,
// signature verified against the root's key, inside its validity window,
// for this host. A rotated root, or any other failure, just means no mark.
//
// Patches and FetchPatch serve ps3sync's patch command over the same pinned
// chain: every package of a title's index, and one package downloaded to a
// file with its size and sha1 checked.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const patchHost = "a0.ww.np.dl.playstation.net"

// sonyRootPEM is "SCEI DNAS Root 05" as served in the chain, captured
// 2026-08-21. sonyRootSPKI pins its public key so an edited PEM can't
// silently become a different trust anchor.
const sonyRootPEM = `-----BEGIN CERTIFICATE-----
MIID0jCCArqgAwIBAgIBADANBgkqhkiG9w0BAQUFADBUMQswCQYDVQQGEwJKUDEp
MCcGA1UEChMgU29ueSBDb21wdXRlciBFbnRlcnRhaW5tZW50IEluYy4xGjAYBgNV
BAMTEVNDRUkgRE5BUyBSb290IDA1MB4XDTA0MDcxMjA5MDExOVoXDTM3MTIwNjA5
MDExOVowVDELMAkGA1UEBhMCSlAxKTAnBgNVBAoTIFNvbnkgQ29tcHV0ZXIgRW50
ZXJ0YWlubWVudCBJbmMuMRowGAYDVQQDExFTQ0VJIEROQVMgUm9vdCAwNTCCASIw
DQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBANmPeza8PwCqlI7esOGIkoSESnIN
g72ZD3Ut63jy7SdothPIvGBqVZWYkIpqJYJd1I4Nh//IpXQCQL0PnJLrh9BBeowq
Muf5NNq3Us80Ihiu9CvNEAEO18g3OFV1TYdSwQ5zUsk33OUeI7h4aBPDVcZXYeHt
dbPLqe4K8igian5prrAD5S6h28t8aAm+qMWRo+bW25B/841XwDGBP7/IxZv8Yoio
rCo80CVYe6lGoU08eeqQiaHI5zAF281DWZSoVfLjJUEWmEnxqr8aOhszRGePi+Ei
7UQjHDuZX9rLhDI1zAND+BA259tn/iwOqVXe20OccJllHJcG4Ecmd98f5qMCAwEA
AaOBrjCBqzAdBgNVHQ4EFgQUxlahM1tPzoN3YgVEhm0gV7Wv2twwfAYDVR0jBHUw
c4AUxlahM1tPzoN3YgVEhm0gV7Wv2tyhWKRWMFQxCzAJBgNVBAYTAkpQMSkwJwYD
VQQKEyBTb255IENvbXB1dGVyIEVudGVydGFpbm1lbnQgSW5jLjEaMBgGA1UEAxMR
U0NFSSBETkFTIFJvb3QgMDWCAQAwDAYDVR0TBAUwAwEB/zANBgkqhkiG9w0BAQUF
AAOCAQEACZPihjwXA27wJ03tEKcHAeFLi8aBw2ysH4GwuH1dWb3UpuznWOB0iQT1
wQocnEFYCJx5XFEnj4aLWpSHLEq/sSO+my+aPoTEsy20ajF+YLYZm0bZxH50CJYh
rkET4C2aC0XvhGp9k1JQ1o0W6+cFT5LTlXapsq8Btt31t+XDPX7RqGV4WGekt3hM
T7xRc7JWXdAQijIrbYi8mtbM07KEGnPU6IT8C47+0mSurpwLOoWL1tPgo6ePpLNi
c4quUMgh9RXVjeTyXOMmyYdeUm2gt7qErvQONli+6Epmhm0A2khpIMHSpQjTE8gV
rZp42a6+zg1iYy2vFBOmiQ17GRUl0A==
-----END CERTIFICATE-----`

const sonyRootSPKI = "55a914dd950aeea522337e2a19764ac9786d0ddbd085e90c14d8ab5335ea6d25"

var sonyRoot = func() *x509.Certificate {
	block, _ := pem.Decode([]byte(sonyRootPEM))
	if block == nil {
		panic("sonyRootPEM: not PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		panic("sonyRootPEM: " + err.Error())
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	if hex.EncodeToString(sum[:]) != sonyRootSPKI {
		panic("sonyRootPEM: public key doesn't match the pin")
	}
	return c
}()

// verifySonyChain is the TLS VerifyPeerCertificate callback: the leaf must
// be issued by the pinned root, carry a signature that root's key verifies,
// be current, and name the host. SHA-1 is accepted here and only here —
// it's what Sony signs with, the trust comes from the pin, and a forged
// SHA-1 collision against a 2004 RSA root is not the threat model of a
// version number in a dashboard.
func verifySonyChain(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return verifyLeaf(rawCerts, sonyRoot, patchHost, time.Now())
}

func verifyLeaf(rawCerts [][]byte, root *x509.Certificate, host string, now time.Time) error {
	if len(rawCerts) == 0 {
		return errors.New("no certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	if !bytes.Equal(leaf.RawIssuer, root.RawSubject) {
		return errors.New("not issued by the pinned root")
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("certificate not valid at %s", now.Format(time.RFC3339))
	}
	if !nameMatches(leaf, host) {
		return fmt.Errorf("certificate is not for %s", host)
	}
	pub, ok := root.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("pinned root is not RSA")
	}
	switch leaf.SignatureAlgorithm {
	case x509.SHA1WithRSA:
		sum := sha1.Sum(leaf.RawTBSCertificate)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA1, sum[:], leaf.Signature)
	case x509.SHA256WithRSA:
		sum := sha256.Sum256(leaf.RawTBSCertificate)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], leaf.Signature)
	}
	return fmt.Errorf("unexpected signature algorithm %s", leaf.SignatureAlgorithm)
}

// nameMatches is hostname verification for a certificate that may predate
// SANs: Sony's leaf names the host only in its Common Name, which Go's
// VerifyHostname refuses outright. SANs win when present; otherwise the CN
// is matched the way a SAN would be, a "*." wildcard covering exactly one
// label.
func nameMatches(leaf *x509.Certificate, host string) bool {
	if len(leaf.DNSNames) > 0 {
		return leaf.VerifyHostname(host) == nil
	}
	pattern := strings.ToLower(leaf.Subject.CommonName)
	host = strings.ToLower(host)
	if rest, ok := strings.CutPrefix(pattern, "*."); ok {
		label, parent, found := strings.Cut(host, ".")
		return found && label != "" && parent == rest
	}
	return pattern != "" && pattern == host
}

var patchClient = &http.Client{
	Timeout:       10 * time.Second,
	Transport:     sonyTransport(),
	CheckRedirect: refuseRedirect,
}

// fetchClient is patchClient without its 10 s limit: a package runs to
// gigabytes, so FetchPatch leaves the deadline to its context.
var fetchClient = &http.Client{
	Transport:     sonyTransport(),
	CheckRedirect: refuseRedirect,
}

// sonyTransport is the transport to Sony's update service: a fresh
// connection per request, and the pinned chain check in place of the
// standard one.
func sonyTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives:     true,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSClientConfig: &tls.Config{
			// the chain can't pass the standard check (SHA-1), so it's
			// replaced wholesale by verifySonyChain, not skipped
			InsecureSkipVerify:    true, //nolint:gosec
			VerifyPeerCertificate: verifySonyChain,
			MinVersion:            tls.VersionTLS12,
		},
	}
}

func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("unexpected redirect")
}

// PatchLookup is the lookup the TUI calls through. It is a test hook: tests
// stub it so no suite run ever reaches Sony.
var PatchLookup = LatestPatch

// LatestPatch asks Sony for the newest patch version of a title ("" when
// the title has no patches — the index 404s for those).
func LatestPatch(ctx context.Context, id string) (string, error) {
	if !reID.MatchString(id) {
		return "", fmt.Errorf("not a title ID: %q", id)
	}
	url := fmt.Sprintf("https://%s/tpl/np/%s/%s-ver.xml", patchHost, id, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := patchClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return newestVersion(string(body)), nil
}

var rePkgVer = regexp.MustCompile(`<package[^>]*\sversion="(\d+\.\d+)"`)

// newestVersion is the highest package version in a -ver.xml. Versions are
// zero-padded "NN.NN", so the string order is the numeric order.
func newestVersion(xml string) string {
	best := ""
	for _, m := range rePkgVer.FindAllStringSubmatch(xml, -1) {
		if m[1] > best {
			best = m[1]
		}
	}
	return best
}

// PatchBehind says whether installed trails latest, both "NN.NN".
func PatchBehind(installed, latest string) bool {
	return installed != "" && latest != "" && strings.Compare(installed, latest) < 0
}

// Patch is one package of a title's update index.
type Patch struct {
	Version   string // "01.02"
	URL       string // as the index gives it: http://b0.ww.np.dl.playstation.net/tppkg/np/<ID>/.../<name>.pkg
	Size      int64  // bytes
	SHA1      string // 40 lowercase hex digits, over the package without its last 32 bytes
	SystemVer string // the firmware it needs, as the index writes it ("04.2000" is 4.20); "" if the index gives none
}

// indexCap is the most of an update index Patches reads. The longest seen
// (BCJS37016, 21 packages) is far below it.
const indexCap = 1 << 20

// Patches lists every package of a title's update index, the oldest version
// first: one GET to Sony (never to the console), behind the pinned root. A
// title Sony has no index for answers 404, and IsNotFound(err) is true then.
// Some titles with no updates answer 200 with an empty body instead (probe
// P5, 2026-10-11), which gives an empty list. A package that lacks its
// version, size, sha1 or URL fails the call, because FetchPatch could not
// check it.
func Patches(ctx context.Context, id string) ([]Patch, error) {
	if !reID.MatchString(id) {
		return nil, fmt.Errorf("not a title ID: %q", id)
	}
	index := fmt.Sprintf("https://%s/tpl/np/%s/%s-ver.xml", patchHost, id, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, index, nil)
	if err != nil {
		return nil, err
	}
	resp, err := patchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %w", id, httpError(resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, indexCap))
	if err != nil {
		return nil, err
	}
	if len(body) >= indexCap {
		return nil, fmt.Errorf("%s: the update index runs past %d KiB", id, indexCap>>10)
	}
	ps, err := parsePatches(string(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return ps, nil
}

var (
	rePkgTag  = regexp.MustCompile(`<package\s[^>]*>`)
	rePkgAttr = regexp.MustCompile(`\s([a-z0-9_]+)=(?:"([^"]*)"|'([^']*)')`)
	reVerNum  = regexp.MustCompile(`^\d+\.\d+$`)
	reSHA1Hex = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// parsePatches reads a -ver.xml into its packages, the oldest version first:
//
//	<titlepatch status="alive" titleid="..."><tag name="..._T2" ...><package version="01.10"
//	  size="231762688" sha1sum="..." url="http://b0.ww.np.dl.playstation.net/tppkg/np/..."
//	  ps3_system_ver="02.4000"><paramsfo>...</paramsfo></package></tag></titlepatch>
//
// The attributes come in any order. An empty body is an index with no
// packages.
func parsePatches(xml string) ([]Patch, error) {
	if strings.TrimSpace(xml) == "" {
		return nil, nil
	}
	if !strings.Contains(xml, "<titlepatch") {
		return nil, errors.New("not an update index")
	}
	var out []Patch
	for i, tag := range rePkgTag.FindAllString(xml, -1) {
		a := map[string]string{}
		for _, m := range rePkgAttr.FindAllStringSubmatch(tag, -1) {
			a[m[1]] = sanitize(unescape(m[2] + m[3]))
		}
		size, err := strconv.ParseInt(a["size"], 10, 64)
		p := Patch{
			Version:   a["version"],
			URL:       a["url"],
			Size:      size,
			SHA1:      strings.ToLower(a["sha1sum"]),
			SystemVer: a["ps3_system_ver"],
		}
		switch {
		case !reVerNum.MatchString(p.Version):
			return nil, fmt.Errorf("package %d: no version", i+1)
		case err != nil || size <= 0:
			return nil, fmt.Errorf("package %s: no size", p.Version)
		case !reSHA1Hex.MatchString(p.SHA1):
			return nil, fmt.Errorf("package %s: no sha1", p.Version)
		case p.URL == "":
			return nil, fmt.Errorf("package %s: no URL", p.Version)
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return lessVersion(out[i].Version, out[j].Version) })
	return out, nil
}

// lessVersion orders two "NN.NN" versions by their numbers.
func lessVersion(a, b string) bool {
	am, an, _ := strings.Cut(a, ".")
	bm, bn, _ := strings.Cut(b, ".")
	x, _ := strconv.Atoi(am)
	y, _ := strconv.Atoi(bm)
	if x != y {
		return x < y
	}
	x, _ = strconv.Atoi(an)
	y, _ = strconv.Atoi(bn)
	return x < y
}

// pkgDigestLen is the digest a PS3 package ends with. The update index's
// sha1 covers the package without it (probe P5, 2026-10-11: BLUS30839
// 01.02, the whole file's sha1 differs).
const pkgDigestLen = 32

var (
	// rePkgHost is a host of Sony's update service, and rePkgPath a
	// package's path on it: /tppkg/np/<ID>/<ID>_T<n>/<hash>/<name>.pkg.
	rePkgHost = regexp.MustCompile(`^[a-z0-9]+\.ww\.np\.dl\.playstation\.net$`)
	rePkgPath = regexp.MustCompile(`^/tppkg/np/[A-Z0-9]{9}/[A-Za-z0-9_./-]+\.pkg$`)
)

// FetchPatch downloads one package that Patches listed to the file dst, and
// keeps it only if it checks out: exactly p.Size bytes, and p.SHA1 over the
// file without its last 32 bytes, the form Sony's index uses (probe P5,
// 2026-10-11).
//
// p.URL must name a package on Sony's update service. The index gives it as
// plain http on b0.ww.np.dl.playstation.net, whose TLS certificate belongs
// to a CDN and names other hosts, so the bytes come over HTTPS from
// a0.ww.np.dl.playstation.net, which serves the same path behind the pinned
// root (P5 again). No redirect is followed.
//
// The download goes to a temporary file beside dst, .<name>.<digits>.part
// for a dst named <name>, renamed to dst only after both checks: a failed
// fetch leaves no file behind and an existing dst untouched. A process that
// dies during a fetch (a SIGKILL, an os.Exit) runs no cleanup and leaves
// that file, so a download first removes the ones that earlier fetches to
// the same dst left. Two fetches to one dst must not overlap: the later one
// removes the earlier one's file, and the earlier one then fails. A 404
// gives an error for which IsNotFound is true. ctx bounds the whole
// download.
func FetchPatch(ctx context.Context, p Patch, dst string) error {
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !rePkgHost.MatchString(u.Host) ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		!rePkgPath.MatchString(u.Path) || strings.Contains(u.Path, "..") {
		return fmt.Errorf("patch %s: not a package on Sony's update service: %q", p.Version, p.URL)
	}
	want, err := hex.DecodeString(p.SHA1)
	if err != nil || len(want) != sha1.Size {
		return fmt.Errorf("patch %s: bad sha1 %q", p.Version, p.SHA1)
	}
	if p.Size < pkgDigestLen {
		return fmt.Errorf("patch %s: %d B is less than a package's %d-byte digest", p.Version, p.Size, pkgDigestLen)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+patchHost+u.Path, nil)
	if err != nil {
		return err
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		return fmt.Errorf("patch %s: %w", p.Version, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("patch %s: %w", p.Version, httpError(resp.StatusCode))
	}
	if resp.ContentLength >= 0 && resp.ContentLength != p.Size {
		return fmt.Errorf("patch %s: Sony sends %d B, the index says %d B", p.Version, resp.ContentLength, p.Size)
	}
	return savePkg(resp.Body, p, want, dst)
}

// savePkg writes a package body to dst through a temporary file in the same
// folder, and renames it only when the size and the sha1 match.
func savePkg(body io.Reader, p Patch, want []byte, dst string) (err error) {
	dir, prefix, suffix := filepath.Dir(dst), "."+filepath.Base(dst)+".", ".part"
	removeLeftParts(dir, prefix, suffix)
	f, err := os.CreateTemp(dir, prefix+"*"+suffix)
	if err != nil {
		return fmt.Errorf("patch %s: %w", p.Version, err)
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	h := sha1.New()
	head := p.Size - pkgDigestLen
	if n, err := io.CopyN(io.MultiWriter(f, h), body, head); err != nil {
		return fmt.Errorf("patch %s: %d of %d B: %w", p.Version, n, p.Size, err)
	}
	if n, err := io.CopyN(f, body, pkgDigestLen); err != nil {
		return fmt.Errorf("patch %s: %d of %d B: %w", p.Version, head+n, p.Size, err)
	}
	if n, _ := io.CopyN(io.Discard, body, 1); n > 0 {
		return fmt.Errorf("patch %s: Sony sends more than the index's %d B", p.Version, p.Size)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("patch %s: %w", p.Version, err)
	}
	if got := h.Sum(nil); !bytes.Equal(got, want) {
		return fmt.Errorf("patch %s: sha1 %x without the last %d B, the index says %x", p.Version, got, pkgDigestLen, want)
	}
	if err := os.Rename(f.Name(), dst); err != nil {
		return fmt.Errorf("patch %s: %w", p.Version, err)
	}
	return nil
}

// removeLeftParts removes the temporary files that earlier savePkg calls
// for the same dst left in dir: prefix, a decimal number (the random part
// os.CreateTemp puts in place of the star), then suffix. A call leaves one
// when its process dies, because a dead process runs no deferred cleanup.
// Only regular files with exactly that form go: the number has no dot, so
// the temporary file of another dst never matches. Errors are ignored,
// because the fetch works without this cleanup.
func removeLeftParts(dir, prefix, suffix string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range des {
		mid, ok := strings.CutPrefix(de.Name(), prefix)
		if !ok || !de.Type().IsRegular() {
			continue
		}
		if mid, ok = strings.CutSuffix(mid, suffix); ok && mid != "" && strings.Trim(mid, "0123456789") == "" {
			os.Remove(filepath.Join(dir, de.Name()))
		}
	}
}
