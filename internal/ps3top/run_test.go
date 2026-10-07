package ps3top

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	// an unstamped build says so rather than naming a release the source has
	// long since moved past
	if got := (Build{"dev", "none", "unknown"}).String(); !strings.Contains(got, "dev") || !strings.Contains(got, "unknown") {
		t.Errorf("dev build prints %q", got)
	}
	got := Build{"v1.1.0", "abc1234", "2026-08-12T00:00:00Z"}.String()
	if !strings.Contains(got, "v1.1.0") || !strings.Contains(got, "abc1234") {
		t.Errorf("stamped build prints %q", got)
	}
	if strings.Contains(got, "2026-08-12") {
		t.Errorf("a tagged build doesn't need the build date: %q", got)
	}
}

// main's dispatch paths, driven through the real flag parser, with
// flag.CommandLine reset per run because main registers its flags on it.
func runMain(t *testing.T, args ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PS3TOP_HOST", "")

	prevArgs, prevFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = prevArgs, prevFlags })
	flag.CommandLine = flag.NewFlagSet("ps3top", flag.ContinueOnError)
	os.Args = append([]string{"ps3top"}, args...)

	return captureStdout(t, func() {
		if err := Run(Build{"v1.2.3", "abc1234", "2026-08-21T00:00:00Z"}); err != nil {
			t.Errorf("Run: %v", err)
		}
	})
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
	// every key the table knows is on a line of its own, key then meaning
	for _, s := range helpSections {
		for _, k := range s.keys {
			found := false
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), k.keys) && strings.Contains(line, k.what) {
					found = true
				}
			}
			if !found {
				t.Errorf("--help doesn't name %q %q:\n%s", k.keys, k.what, out)
			}
		}
	}
	for _, want := range []string{"rescan", "thermals", "eject", "sort", "quit", "this screen"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help doesn't mention %q", want)
		}
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

// --alarm is a temperature, not a free integer: 0 made the first poll alarm
// (bell, notification, red frame for the whole session), and 121 could never
// fire. Outside 1..120 is refused before anything is sent.
func TestAlarmIsValidated(t *testing.T) {
	for _, v := range []string{"0", "-5", "121"} {
		prevArgs, prevFlags := os.Args, flag.CommandLine
		flag.CommandLine = flag.NewFlagSet("ps3top", flag.ContinueOnError)
		os.Args = []string{"ps3top", "--host", "127.0.0.1:9", "--alarm", v}
		err := Run(Build{"v1.2.3", "abc1234", "2026-08-21T00:00:00Z"})
		os.Args, flag.CommandLine = prevArgs, prevFlags
		if err == nil || !strings.Contains(err.Error(), "--alarm") {
			t.Errorf("--alarm %s: %v, want a refusal", v, err)
		}
	}
}
