package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain re-execs this test binary as the real main() when asked to.
func TestMain(m *testing.M) {
	if os.Getenv("BINMERGE_AS_MAIN") == "1" {
		os.Args = append([]string{"binmerge"}, strings.Split(os.Getenv("BINMERGE_ARGS"), "\n")...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runMain(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "BINMERGE_AS_MAIN=1", "BINMERGE_ARGS="+strings.Join(args, "\n"))
	return cmd
}

// writeSet writes a two-bin cue set of sectors per bin and returns the cue.
func writeSet(t *testing.T, dir string, sectors int) string {
	t.Helper()
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, n+".bin"), bytes.Repeat([]byte{n[0]}, sectors*2352), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cue := filepath.Join(dir, "g.cue")
	if err := os.WriteFile(cue, []byte("FILE \"a.bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"+
		"FILE \"b.bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 01 00:00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cue
}

// The first Ctrl-C cancels the copy politely; the second must kill a run that
// is stuck in I/O (a FIFO as the output stands in for a hung share).
func TestSecondCtrlCKills(t *testing.T) {
	dir := t.TempDir()
	cue := writeSet(t, dir, 200)
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(out, "m.bin"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := runMain("-f", "-o", out, cue, "m")
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	time.Sleep(1500 * time.Millisecond) // it reaches the blocked open
	select {
	case err := <-done:
		t.Fatalf("the process exited before the signals (%v): %s", err, stderr.String())
	default:
	}
	cmd.Process.Signal(os.Interrupt)
	time.Sleep(300 * time.Millisecond)
	cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Errorf("still running 3s after a second Ctrl-C; stderr:\n%s", stderr.String())
	}
}

// After "--" every argument is positional, not only the first one:
// `binmerge -n -- -g.cue -m` must not read -m as an unknown flag.
func TestDoubleDashProtectsEveryPositional(t *testing.T) {
	dir := t.TempDir()
	cue := writeSet(t, dir, 2)
	if err := os.Rename(cue, filepath.Join(dir, "-g.cue")); err != nil {
		t.Fatal(err)
	}
	cmd := runMain("-n", "--", "-g.cue", "-m")
	cmd.Dir = dir
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("`binmerge -n -- -g.cue -m` failed (%v):\n%s", err, outb)
	}
	// the interleaved form keeps working: flags after each positional
	cmd = runMain(filepath.Join(dir, "-g.cue"), "m", "-n", "-o", filepath.Join(dir, "out"))
	if outb, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("interleaved `cue m -n -o out` failed (%v):\n%s", err, outb)
	}
}
