package ps3top

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loopbackWebMAN starts a server on 127.0.0.1 that answers like webMAN and
// points the sweep at its port. Returns the host:port.
func loopbackWebMAN(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// Discovery's whole identity check is "the body says webMAN" — convenient
// rather than cryptographic, and worth pinning so it can't loosen further.
func TestIsWebMAN(t *testing.T) {
	if host := loopbackWebMAN(t, "webMAN 1.47.48q MOD - Simple Web Server"); !isWebMAN(host) {
		t.Error("a webMAN banner wasn't recognised")
	}
	if host := loopbackWebMAN(t, "<html>nginx default page</html>"); isWebMAN(host) {
		t.Error("an unrelated web server passed as a console")
	}
	// nothing listening at all
	if isWebMAN("127.0.0.1:1") {
		t.Error("a dead address reported as webMAN")
	}
}

func TestSubnetNames(t *testing.T) {
	got := subnetNames([]subnet{
		{base: net.IPv4(192, 168, 1, 0).To4()},
		{base: net.IPv4(10, 0, 0, 0).To4()},
	})
	if got != "192.168.1.0/24, 10.0.0.0/24" {
		t.Errorf("subnetNames = %q", got)
	}
	if got := subnetNames(nil); got != "" {
		t.Errorf("subnetNames(nil) = %q, want empty", got)
	}
}

// localSubnets reads this machine's real interfaces, so the assertions are the
// invariants rather than specific addresses: never loopback, never public,
// always clamped to a /24, one entry per network.
func TestLocalSubnets(t *testing.T) {
	seen := map[string]bool{}
	for _, sn := range localSubnets() {
		if !sn.base.IsPrivate() {
			t.Errorf("swept a non-private network: %v", sn.base)
		}
		if sn.base.IsLoopback() {
			t.Errorf("swept loopback: %v", sn.base)
		}
		if sn.base.To4() == nil {
			t.Errorf("non-IPv4 subnet: %v", sn.base)
		}
		if sn.base[3] != 0 {
			t.Errorf("base %v isn't a /24 network address", sn.base)
		}
		if !sn.self.IsPrivate() {
			t.Errorf("self %v isn't in a private range", sn.self)
		}
		if seen[sn.base.String()] {
			t.Errorf("duplicate subnet %v", sn.base)
		}
		seen[sn.base.String()] = true
	}
}

// The sweep runs against 127.0.0.0/24 with one loopback console on it. The
// other 253 addresses refuse instantly, which is also the realistic shape: a
// handful of answers out of a subnet of nothing.
func TestSweepFindsTheConsole(t *testing.T) {
	host := loopbackWebMAN(t, "webMAN 1.47.48q MOD")
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		t.Fatal(err)
	}
	defer func(p string) { sweepPort = p }(sweepPort)
	sweepPort = port

	sn := subnet{base: net.IPv4(127, 0, 0, 0).To4(), self: net.IPv4(127, 0, 0, 99).To4()}
	if got := sweep([]subnet{sn}); got != "127.0.0.1" {
		t.Errorf("sweep = %q, want 127.0.0.1", got)
	}

	// and a subnet with no console on it comes back empty rather than hanging
	sweepPort = "1" // nothing listens here
	if got := sweep([]subnet{sn}); got != "" {
		t.Errorf("sweep of an empty subnet = %q, want none", got)
	}
}

// A cached address is handed back without a request, even one nothing is
// listening on: the TUI's first poll is the verification, and a stale cache
// is re-swept from inside the dashboard. That's what lets ps3top start before
// the console is on. The junk guard still applies — a cache that isn't an
// address is not "a host to try", it's a sweep.
func TestDiscoverHostTrustsTheCache(t *testing.T) {
	loopbackOnly(t)
	defer func(p string) { sweepPort = p }(sweepPort)
	sweepPort = "1" // nothing listens: any sweep finds nothing, fast

	cache := filepath.Join(t.TempDir(), "host")
	if err := os.WriteFile(cache, []byte("127.0.0.1:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := discoverHost(cache)
	if err != nil || got != "127.0.0.1:1" {
		t.Errorf("discoverHost = %q, %v; want the cached address back", got, err)
	}
}

// loopbackOnly points discovery at 127.0.0.0/24 so no test ever sweeps the
// machine's real LAN.
func loopbackOnly(t *testing.T) {
	t.Helper()
	prev := localNets
	t.Cleanup(func() { localNets = prev })
	localNets = func() []subnet {
		return []subnet{{base: net.IPv4(127, 0, 0, 0).To4(), self: net.IPv4(127, 0, 0, 99).To4()}}
	}
}

// A cache file holding something that isn't an address must not be concatenated
// into a URL on trust — it falls through to a sweep like any other stale entry.
func TestDiscoverHostRejectsAJunkCache(t *testing.T) {
	loopbackOnly(t)
	defer func(p string) { sweepPort = p }(sweepPort)
	sweepPort = "1" // nothing listens; the fallback sweep finds nothing, fast

	dir := t.TempDir()
	cache := filepath.Join(dir, "host")
	if err := os.WriteFile(cache, []byte("127.0.0.1:8080/evil?x=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverHost(cache); err == nil {
		t.Errorf("junk cache entry was accepted as %q", got)
	}
}

// A stale cache (the console moved on DHCP) falls through to a sweep, and the
// address that works is written back so the next launch probes one host.
func TestDiscoverHostSweepsAndCaches(t *testing.T) {
	loopbackOnly(t)
	host := loopbackWebMAN(t, "webMAN 1.47.48q MOD")
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		t.Fatal(err)
	}
	defer func(p string) { sweepPort = p }(sweepPort)
	sweepPort = port

	cache := filepath.Join(t.TempDir(), "sub", "host") // parent doesn't exist yet
	got, err := discoverHost(cache)
	if err != nil {
		t.Fatalf("discoverHost: %v", err)
	}
	if got != "127.0.0.1" {
		t.Errorf("discoverHost = %q, want 127.0.0.1", got)
	}
	b, err := os.ReadFile(cache)
	if err != nil {
		t.Fatalf("last-good host wasn't cached: %v", err)
	}
	if strings.TrimSpace(string(b)) != "127.0.0.1" {
		t.Errorf("cached %q", b)
	}
}

func TestDiscoverHostWithNoNetwork(t *testing.T) {
	prev := localNets
	t.Cleanup(func() { localNets = prev })
	localNets = func() []subnet { return nil }

	_, err := discoverHost(filepath.Join(t.TempDir(), "host"))
	if err == nil {
		t.Fatal("no private network reported success")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error doesn't point at the escape hatch: %v", err)
	}
}
