package main

import (
	"bytes"
	"flag"
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

// dataDir is where the only copy of your play history lives, so a home
// directory it can't resolve is an error, not a silent fallback to somewhere
// a cache cleaner will eat.
func TestDataDirWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got, err := dataDir(); err == nil {
		t.Errorf("dataDir with no HOME returned %q", got)
	}
}
