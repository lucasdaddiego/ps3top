package ps3top

import (
	"errors"
	"strings"
	"testing"
)

// No test may pop a notification: the sender is stubbed for the whole suite
// and the tests that care swap in a recorder.
func init() {
	notifier = func(string, string) {}
}

func record(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := notifier
	t.Cleanup(func() { notifier = prev })
	notifier = func(_, body string) { got = append(got, body) }
	return &got
}

// A notification goes with the bell on the way into the alarm — once, not
// every poll while it stays hot — and when the console goes quiet in the
// middle of a game; a console going quiet on the XMB is just switched off.
func TestNotificationsFireOnAlarmAndMidGameLoss(t *testing.T) {
	got := record(t)
	m := liveModel(t, 120)
	m.alarm, m.alarming = 70, false

	hot := m.st
	hot.CPUTemp = 82
	_, cmd := m.Update(statusMsg{st: hot})
	leaves(cmd)
	if len(*got) != 1 || !strings.Contains((*got)[0], "82°") || !strings.Contains((*got)[0], "70°") {
		t.Errorf("alarm notification = %v", *got)
	}
	_, cmd = m.Update(statusMsg{st: hot})
	leaves(cmd)
	if len(*got) != 1 {
		t.Errorf("the alarm notified again while already alarming: %v", *got)
	}

	// mid-game loss
	_, cmd = m.Update(statusMsg{err: errors.New("dial tcp: host is down")})
	leaves(cmd)
	if len(*got) != 2 || !strings.Contains((*got)[1], "offline during Sample Game") {
		t.Errorf("mid-game loss notification = %v", *got)
	}
	// still offline on the next poll: nothing more
	_, cmd = m.Update(statusMsg{err: errors.New("still down")})
	leaves(cmd)
	if len(*got) != 2 {
		t.Errorf("a continuing outage notified again: %v", *got)
	}
	// back, on the XMB, then off: switched off, not lost
	xmb := m.st
	xmb.InGame = false
	m.Update(statusMsg{st: xmb})
	_, cmd = m.Update(statusMsg{err: errors.New("host is down")})
	leaves(cmd)
	if len(*got) != 2 {
		t.Errorf("an XMB power-off notified: %v", *got)
	}
}

func TestAppleString(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:              `"plain"`,
		`say "hi" \ there`:   `"say \"hi\" \\ there"`,
		"ctrl\x1bchars\n":    `"ctrlchars"`,
		`Borderlands™ 2 82°`: `"Borderlands™ 2 82°"`,
	} {
		if got := appleString(in); got != want {
			t.Errorf("appleString(%q) = %s, want %s", in, got, want)
		}
	}
}
