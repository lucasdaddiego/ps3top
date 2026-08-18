package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)

	// an unstamped build says so rather than naming a release the source has
	// long since moved past
	version, commit, date = "dev", "none", "unknown"
	if got := versionString(); !strings.Contains(got, "dev") || !strings.Contains(got, "unknown") {
		t.Errorf("dev build prints %q", got)
	}
	version, commit, date = "v1.1.0", "abc1234", "2026-08-12T00:00:00Z"
	got := versionString()
	if !strings.Contains(got, "v1.1.0") || !strings.Contains(got, "abc1234") {
		t.Errorf("stamped build prints %q", got)
	}
	if strings.Contains(got, "2026-08-12") {
		t.Errorf("a tagged build doesn't need the build date: %q", got)
	}
}

// --once is scripting-facing, so an absent field prints ASCII "n/a" rather than
// the TUI's em dash — and 0 stays 0, since webMAN reports it legitimately.
func TestPlainFormatters(t *testing.T) {
	if got := plainInt(unknown, "°C"); got != "n/a" {
		t.Errorf("plainInt(unknown) = %q", got)
	}
	if got := plainInt(0, "%"); got != "0%" {
		t.Errorf("plainInt(0) = %q, want a real zero", got)
	}
	if got := plainInt(58, "°C"); got != "58°C" {
		t.Errorf("plainInt(58) = %q", got)
	}
	if got := plainMem(unknown); got != "n/a" {
		t.Errorf("plainMem(unknown) = %q", got)
	}
	if got := plainMem(1152); got != "1.1M" {
		t.Errorf("plainMem(1152) = %q", got)
	}
	if got := plainHDD(unknown); got != "n/a" {
		t.Errorf("plainHDD(unknown) = %q", got)
	}
	if got := plainHDD(123.4); got != "123.4G" {
		t.Errorf("plainHDD(123.4) = %q", got)
	}
}

// statusPage renders a webMAN status page with the bits each test needs.
func statusPage(extra string) string {
	return `webMAN 1.47.48q MOD` + extra +
		`<a class="s" href="/cpursx.ps3?up">CPU: 58°C <small>[Fan control: SYSCON]</small><br>RSX: 61°C</a>` +
		`<a class="s" href="/games.ps3">MEM: 1,152 KB </a><br><a href="/dev_hdd0">HDD:  123.4 GB free</a>` +
		`<a class="s" href="/cpursx.ps3?mode">FAN SPEED:  26% (0x42)</a>` +
		`<label title="Startup"></label> 01:24:02` +
		`<a class="s" href="/setup.ps3">NOR Firmware: 4.93 CEX PS3HEN 3.5.0<br>` +
		`<H1><img src='/dev_hdd0/tmp/wm_icons/power.png'> 100d 01:02:03 • 1,234 ON • 1,200 OFF (34)</H1>`
}

func TestRunOnce(t *testing.T) {
	const gameBits = `<a href="https://a0.ww.np.dl.playstation.net/tpl/np/MOCK30982/MOCK30982-ver.xml">MOCK30982</a> ` +
		`<a href="http://google.com/search?q=Sample">Sample Game™ 2 01.15</a> ` +
		`<a href="/x"><small>pid=01010200</small></a>` +
		`<label title="Play">&#9737;</label> 01:23:45<br>`

	games, err := os.ReadFile("testdata/mygames.xml")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		status string
		games  string
		want   []string
		absent []string
	}{
		{
			name:   "in game",
			status: statusPage(gameBits),
			games:  string(games),
			want:   []string{"ONLINE", "In game: Sample Game™ 2 [MOCK30982]", "CPU 58°C", "play 01:23:45", "games: 22", "lifetime:"},
		},
		{
			name:   "xmb with a disc mounted",
			status: statusPage(`<a href="/mount.ps3/dev_hdd0/PS3ISO/SampleGame2.iso">x</a>`),
			games:  string(games),
			want:   []string{"XMB — mounted: SampleGame2.iso"},
			absent: []string{"In game:"},
		},
		{
			name:   "xmb, nothing mounted",
			status: statusPage(""),
			games:  string(games),
			want:   []string{"XMB — no disc mounted"},
		},
		{
			name:   "degraded page warns",
			status: `webMAN 1.47.48q MOD<a class="s" href="/setup.ps3">NOR Firmware: 4.93 CEX<br>`,
			games:  string(games),
			want:   []string{"warning:", "fields not found", "n/a"},
		},
		{
			name:   "game list unreachable",
			status: statusPage(""),
			games:  "", // handler 500s on an empty body below
			want:   []string{"games: error:"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "mygames.xml") {
					if c.games == "" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					w.Write([]byte(c.games))
					return
				}
				w.Write([]byte(c.status))
			}))
			defer srv.Close()

			host := strings.TrimPrefix(srv.URL, "http://")
			out := captureStdout(t, func() { runOnce(NewClient(host), host) })
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			for _, no := range c.absent {
				if strings.Contains(out, no) {
					t.Errorf("output unexpectedly contains %q:\n%s", no, out)
				}
			}
		})
	}
}

// main's dispatch paths, driven through the real flag parser. Isolated with a
// temp HOME so --stats reads a fresh data dir rather than the real one, and
// with flag.CommandLine reset per run because main registers its flags on it.
func runMain(t *testing.T, args ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PS3TOP_HOST", "")

	prevArgs, prevFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = prevArgs, prevFlags })
	flag.CommandLine = flag.NewFlagSet("ps3top", flag.ContinueOnError)
	os.Args = append([]string{"ps3top"}, args...)

	return captureStdout(t, main)
}

func TestMainVersion(t *testing.T) {
	if out := runMain(t, "--version"); !strings.Contains(out, "ps3top") {
		t.Errorf("--version printed %q", out)
	}
}

// --help carries the TUI key cheat sheet — the keybar can't show every key at
// every width, and --help is where you look before launching. Rendered via
// usage() directly: driving main through a --help parse would fall through to
// a real LAN sweep under the test flag set's ContinueOnError.
func TestUsageNamesTheTUIKeys(t *testing.T) {
	prev := flag.CommandLine
	t.Cleanup(func() { flag.CommandLine = prev })
	flag.CommandLine = flag.NewFlagSet("ps3top", flag.ContinueOnError)

	var buf bytes.Buffer
	flag.CommandLine.SetOutput(&buf)
	usage()
	out := buf.String()
	for _, want := range []string{
		"r refresh", "g rescan", "t thermal", "u eject", "s sort", "q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--help doesn't name %q:\n%s", want, out)
		}
	}
}

// --stats works with the console off — it reads the local log and nothing else.
func TestMainStats(t *testing.T) {
	if out := runMain(t, "--stats"); !strings.Contains(out, "no play history yet") {
		t.Errorf("--stats on a fresh install printed %q", out)
	}
}

func TestMainOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(statusPage("")))
	}))
	defer srv.Close()

	// --interval below the floor is clamped rather than rejected, and the host
	// is normalized before a single request goes out
	out := runMain(t, "--once", "--interval", "1s", "--host", "http://"+strings.TrimPrefix(srv.URL, "http://"))
	if !strings.Contains(out, "ONLINE") {
		t.Errorf("--once printed %q", out)
	}
	if !strings.Contains(out, "XMB") {
		t.Errorf("--once didn't report the console state: %q", out)
	}
}

// dataDir is where the only copy of your play history lives, so a home
// directory it can't resolve is an error, not a silent fallback to somewhere
// a cache cleaner will eat.
func TestDataDirWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got, err := dataDir(); err == nil {
		t.Errorf("dataDir with no HOME returned %q", got)
	}
}
