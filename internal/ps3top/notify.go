package ps3top

// Desktop notifications, for the two things worth hearing about when the
// window isn't in front: a sensor crossing the alarm, and the console going
// quiet in the middle of a game. The bell and the red frame stay; this is
// one more sink for the same triggers, never a new one.

import (
	"fmt"
	osexec "os/exec" // the suite has an exec helper of its own
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// notifier is the seam: tests capture what would have been shown.
var notifier = sendNotification

// notifyCmd shows a notification off the event loop; it returns no message.
func notifyCmd(body string) tea.Cmd {
	return func() tea.Msg {
		notifier("ps3top", body)
		return nil
	}
}

// sendNotification uses whatever the platform has: terminal-notifier when
// it's installed (it keeps an icon and a click target), else osascript on
// macOS, notify-send on Linux. Failures are silent — there's nothing to do
// about a missing notifier mid-session, and the bell already rang.
func sendNotification(title, body string) {
	switch runtime.GOOS {
	case "darwin":
		if p, err := osexec.LookPath("terminal-notifier"); err == nil {
			_ = osexec.Command(p, "-title", title, "-message", body).Run()
			return
		}
		script := fmt.Sprintf("display notification %s with title %s", appleString(body), appleString(title))
		_ = osexec.Command("osascript", "-e", script).Run()
	case "linux":
		if p, err := osexec.LookPath("notify-send"); err == nil {
			_ = osexec.Command(p, title, body).Run()
		}
	}
}

// appleString quotes for AppleScript, whose string literals know only the
// \" and \\ escapes — Go's %q would hand it \u escapes it doesn't read.
// Control characters are dropped; a game title is the only untrusted thing
// that reaches here and it was sanitized at the parser already.
func appleString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r < 0x20 || r == 0x7f:
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
