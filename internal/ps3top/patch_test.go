package ps3top

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lucasdaddiego/ps3top/webman"
)

// No test may reach Sony: the lookup is stubbed for the whole suite and the
// tests that care swap in their own.
func init() {
	webman.PatchLookup = func(context.Context, string) (string, error) { return "", errors.New("stubbed") }
}

// One question to Sony per running title per session — not per poll, and
// not again after a failure — and the header carries the answer next to the
// version it's about.
func TestPatchCheckOncePerTitle(t *testing.T) {
	asked := 0
	prev := webman.PatchLookup
	t.Cleanup(func() { webman.PatchLookup = prev })
	webman.PatchLookup = func(_ context.Context, id string) (string, error) {
		asked++
		return "01.15", nil
	}

	m := liveModel(t, 160)
	st := m.st // in-game, v01.15 per liveModel
	st.GameVer = "01.10"

	_, cmd := m.Update(statusMsg{st: st})
	var reply tea_patchMsg
	for _, msg := range msgsOf(cmd) {
		if p, ok := msg.(patchMsg); ok {
			reply = tea_patchMsg{p}
		}
	}
	if asked != 1 || reply.patchMsg.latest != "01.15" {
		t.Fatalf("first poll asked %d times, reply %+v", asked, reply)
	}
	m.Update(reply.patchMsg)
	if !strings.Contains(m.header(), "→ 01.15 available") {
		t.Errorf("header doesn't carry the mark:\n%s", m.header())
	}
	// the next poll of the same title asks nothing more
	_, cmd = m.Update(statusMsg{st: st})
	for _, msg := range msgsOf(cmd) {
		if _, ok := msg.(patchMsg); ok {
			t.Error("a routine poll asked Sony again")
		}
	}
	// up to date: no mark
	st.GameVer = "01.15"
	m.Update(statusMsg{st: st})
	if strings.Contains(m.header(), "available") {
		t.Error("an up-to-date game was marked")
	}
	// on the XMB there's nothing to ask about
	m.patches = map[string]string{}
	st.InGame = false
	_, cmd = m.Update(statusMsg{st: st})
	for _, msg := range msgsOf(cmd) {
		if _, ok := msg.(patchMsg); ok {
			t.Error("asked Sony with no game running")
		}
	}
}

// tea_patchMsg just wraps patchMsg so the zero value prints readably.
type tea_patchMsg struct{ patchMsg }

// --no-patch: no title ID leaves the LAN. checkPatch builds no lookup and
// records nothing, whatever game is running.
func TestNoPatchAsksSonyNothing(t *testing.T) {
	prev := webman.PatchLookup
	t.Cleanup(func() { webman.PatchLookup = prev })
	webman.PatchLookup = func(context.Context, string) (string, error) {
		t.Error("Sony was asked")
		return "", nil
	}
	m := liveModel(t, 120)
	m.patchOn = false
	if cmd := m.checkPatch(Status{InGame: true, GameID: "MOCK30982", GameVer: "01.15"}); cmd != nil {
		t.Fatal("--no-patch still built a lookup")
	}
	if len(m.patches) != 0 {
		t.Errorf("patches = %v, want none", m.patches)
	}
}
