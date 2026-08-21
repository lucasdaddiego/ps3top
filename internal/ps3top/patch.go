package ps3top

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
	"net/http"
	"regexp"
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
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			// the chain can't pass the standard check (SHA-1), so it's
			// replaced wholesale by verifySonyChain — not skipped
			InsecureSkipVerify:    true, //nolint:gosec
			VerifyPeerCertificate: verifySonyChain,
			MinVersion:            tls.VersionTLS12,
		},
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("unexpected redirect")
	},
}

// patchLookup is the seam the model calls through; tests stub it so no
// suite run ever reaches Sony.
var patchLookup = latestPatch

// latestPatch asks Sony for the newest patch version of a title ("" when
// the title has no patches — the index 404s for those).
func latestPatch(ctx context.Context, id string) (string, error) {
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

// patchBehind says whether installed trails latest, both "NN.NN".
func patchBehind(installed, latest string) bool {
	return installed != "" && latest != "" && strings.Compare(installed, latest) < 0
}
