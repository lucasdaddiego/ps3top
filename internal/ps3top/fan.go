package ps3top

// The fan-command queue and its delta reports. The thermal screen
// (thermal.go) is the UI that drives this; these are the rules for how
// presses become requests without racing each other or lying about what
// happened.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fanQueueMax bounds the queue so a leaned-on key can't build a backlog of
// requests the console will still be working through a minute later.
const fanQueueMax = 20

// fanReady reports whether the fan controls know what they would be doing.
// ↑↓ move the fan percentage in manual mode and the target temperature in
// dynamic, and f is a one-way door out of SYSCON — none of that can be
// labelled honestly, or confirmed, before a status has said which mode the
// console is in. The keys used to work anyway from the very first frame.
func (m *model) fanReady() bool {
	return m.haveStatus && m.st.FanMode != ""
}

// fanKey applies a fan step, or explains why it can't.
func (m *model) fanKey(cmd string) tea.Cmd {
	if !m.fanReady() {
		return m.fanUnready()
	}
	return m.queueFan(cmd)
}

func (m *model) fanUnready() tea.Cmd {
	m.flash = "fan: waiting for a status read — mode unknown"
	return m.clearFlashLater()
}

// queueFan records a press and starts draining if nothing is in flight. Fan
// requests are serialized deliberately: webMAN's server has ~4 session slots,
// and firing one GET per keypress turns a quick 24%→40% adjustment into a
// pile-up that reads as an unresponsive UI.
//
// The queue is an ordered list, not a net counter plus a mode flag. Those
// commands don't all mean the same thing — ?up moves the percentage in manual
// mode and the target temperature in dynamic — and the old shape always sent a
// queued ?mode FIRST, ahead of presses made before it. So: manual mode, press
// up twice to cool the console, then press f. The mode change went out first,
// and the two ups that were meant to raise the fan raised the temperature
// ceiling instead. Cooling intent, inverted, on the screen whose whole job is
// to not cook the console. Press order is now preserved exactly.
func (m *model) queueFan(cmd string) tea.Cmd {
	// an immediate reversal cancels instead of queueing both — leaning on ↑ and
	// correcting with ↓ shouldn't cost two round trips. Tail only, so it can
	// never reorder anything across a ?mode.
	if n := len(m.fanQueue); n > 0 && m.fanQueue[n-1] == inverseFan(cmd) {
		m.fanQueue = m.fanQueue[:n-1]
		return m.drainFan()
	}
	if len(m.fanQueue) >= fanQueueMax {
		return m.drainFan()
	}
	m.fanQueue = append(m.fanQueue, cmd)
	return m.drainFan()
}

// inverseFan is the command that undoes cmd, or "" for one that can't be undone
// (a mode change is a one-way cycle, never cancelled against a pending step).
func inverseFan(cmd string) string {
	switch cmd {
	case fanUp:
		return fanDown
	case fanDown:
		return fanUp
	}
	return ""
}

func (m *model) drainFan() tea.Cmd {
	if m.fanBusy || len(m.fanQueue) == 0 {
		return nil // the in-flight reply will drain the rest
	}
	cmd := m.fanQueue[0]
	m.fanQueue = m.fanQueue[1:]
	m.fanBusy = true
	cli, prevPct, prevMax, prevMode := m.cli, m.st.FanPct, m.st.MaxTemp, m.st.FanMode
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		st, err := cli.Fan(ctx, cmd)
		return fanMsg{prevPct, prevMax, prevMode, st, err}
	}
}

// fanReport says what the console actually did, by diffing every field a fan
// command can move rather than assuming which one it moved.
//
// That generality is the point: ?up/?dn adjust the *target temperature* in
// dynamic mode and the *fan percentage* in manual, so a report written around
// one of them announces "unchanged" while the other quietly moves — which is
// exactly how this was wrong the first time.
func fanReport(msg fanMsg) string {
	var parts []string
	prevMode, nowMode := strings.ToLower(msg.prevMode), strings.ToLower(msg.st.FanMode)
	switch {
	case nowMode != "" && prevMode == "":
		parts = append(parts, "mode "+nowMode)
	case nowMode != "" && nowMode != prevMode:
		parts = append(parts, "mode "+prevMode+" → "+nowMode)
	}
	if msg.prevPct != unknown && msg.st.FanPct != unknown && msg.prevPct != msg.st.FanPct {
		parts = append(parts, fmt.Sprintf("fan %d%% → %d%%", msg.prevPct, msg.st.FanPct))
	}
	if msg.prevMax > 0 && msg.st.MaxTemp > 0 && msg.prevMax != msg.st.MaxTemp {
		parts = append(parts, fmt.Sprintf("target %d° → %d°", msg.prevMax, msg.st.MaxTemp))
	}
	if len(parts) > 0 {
		return strings.Join(parts, " · ")
	}
	if msg.st.FanPct == unknown {
		return "fan: no reading"
	}
	return fmt.Sprintf("nothing changed (fan %d%%%s)", msg.st.FanPct, modeSuffix(nowMode))
}

func modeSuffix(mode string) string {
	if mode == "" {
		return ""
	}
	return ", " + mode
}
