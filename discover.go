package main

// LAN auto-discovery. webMAN has no mDNS/SSDP announcer, so discovery is a
// bounded sweep of the machine's private IPv4 /24s: TCP-dial :80, and for the
// few hosts that answer, GET /cpursx.ps3 and require the webMAN banner in the
// body (any other web server 404s or serves something else). The last good
// host is cached, so later launches probe one address instead of 254 — and a
// stale cache (DHCP moved the console) falls through to a fresh sweep.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	sweepDialTimeout   = 500 * time.Millisecond
	sweepVerifyTimeout = 2 * time.Second
	sweepTimeout       = 15 * time.Second
	sweepWorkers       = 128
)

// sweepPort is webMAN's fixed HTTP port, as a seam: binding 80 needs root, so
// the suite points the sweep at a loopback server on a high port instead. It
// only affects which port is dialled and verified — the address handed back is
// the bare IP either way, so production behaviour is identical.
var sweepPort = "80"

// localNets is a seam for the same reason: a test that called discoverHost
// unguarded would sweep the developer's actual LAN, 254 dials per subnet.
var localNets = localSubnets

// discoverHost finds the console: cached last-good host first, then a sweep.
func discoverHost(cachePath string) (string, error) {
	if b, err := os.ReadFile(cachePath); err == nil {
		// the cache is ours, but it's a file on disk — validate it like any
		// other input rather than concatenating it into a URL on trust
		if host, err := normalizeHost(string(b)); err == nil && isWebMAN(host) {
			return host, nil
		}
	}

	subnets := localNets()
	if len(subnets) == 0 {
		return "", fmt.Errorf("no private IPv4 network found — pass --host")
	}
	fmt.Fprintf(os.Stderr, "ps3top: scanning %s for webMAN…\n", subnetNames(subnets))
	host := sweep(subnets)
	if host == "" {
		return "", fmt.Errorf("no webMAN console on %s — is the PS3 on with HEN active? (--host skips discovery)",
			subnetNames(subnets))
	}

	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err == nil {
		_ = os.WriteFile(cachePath, []byte(host+"\n"), 0o644)
	}
	return host, nil
}

// isWebMAN reports whether host serves webMAN's status page.
func isWebMAN(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), sweepVerifyTimeout)
	defer cancel()
	body, err := NewClient(host).get(ctx, "/cpursx.ps3")
	return err == nil && strings.Contains(string(body), "webMAN")
}

type subnet struct {
	base net.IP // network address of the clamped /24
	self net.IP // this machine's address in it (skipped in the sweep)
}

// localSubnets returns one /24 per up, non-loopback, private IPv4 interface
// address. Wider masks are clamped to the /24 around our own address: the
// console is on the same segment in practice, and sweeping a /16 is 65k
// probes of somebody's network for nothing.
func localSubnets() []subnet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []subnet
	seen := map[string]bool{}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			base := ip4.Mask(net.CIDRMask(24, 32))
			if seen[base.String()] {
				continue
			}
			seen[base.String()] = true
			out = append(out, subnet{base: base, self: ip4})
		}
	}
	return out
}

func subnetNames(subnets []subnet) string {
	names := make([]string, len(subnets))
	for i, sn := range subnets {
		names[i] = sn.base.String() + "/24"
	}
	return strings.Join(names, ", ")
}

// sweep probes every host of every subnet concurrently and returns the first
// confirmed webMAN address ("" if none). First hit cancels the rest — with
// two consoles on the LAN, whichever answers first wins; use --host to pick.
func sweep(subnets []subnet) string {
	// read once, up front: the first hit returns immediately while the rest of
	// the probes are still winding down, so the goroutines outlive this call
	// and must not be reading a variable someone else can still change
	port := sweepPort

	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()

	var wg sync.WaitGroup
	found := make(chan string, 1)
	sem := make(chan struct{}, sweepWorkers)
	for _, sn := range subnets {
		for i := 1; i < 255; i++ {
			ip := fmt.Sprintf("%d.%d.%d.%d", sn.base[0], sn.base[1], sn.base[2], i)
			if ip == sn.self.String() {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				addr := net.JoinHostPort(ip, port)
				d := net.Dialer{Timeout: sweepDialTimeout}
				conn, err := d.DialContext(ctx, "tcp", addr)
				if err != nil {
					return
				}
				conn.Close()
				if isWebMAN(addr) {
					select {
					case found <- ip:
						cancel()
					default:
					}
				}
			}()
		}
	}
	go func() { wg.Wait(); close(found) }()
	return <-found
}
