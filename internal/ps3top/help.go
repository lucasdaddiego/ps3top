package ps3top

// The key reference: one table behind both the in-app help screen (?) and
// the --help text, so the two can't drift. The footer used to carry a keybar
// that never fit every key at any width and so always hid the ones worth
// discovering; now it carries "? help" and this screen carries everything.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type keyHelp struct{ keys, what string }

type helpSection struct {
	title string
	keys  []keyHelp
}

var helpSections = []helpSection{
	{"list", []keyHelp{
		{"↑↓ j k", "move"},
		{"pgup pgdn ^u ^d", "page"},
		{"home end", "first / last"},
		{"⇥ ← → h l", "console tab"},
		{"/", "fuzzy filter (esc clears)"},
		{"s", "sort: alpha → recent → largest"},
	}},
	{"game", []keyHelp{
		{"⏎", "mount (already mounted → launch)"},
		{"p", "play: mount + launch (Lite: via Auto-Play)"},
		{"u", "eject"},
		{"x", "quit the running game to the XMB"},
		{"X", "restart the running game"},
	}},
	{"console", []keyHelp{
		{"m", "popup message on the TV"},
		{"r", "refresh status + game list"},
		{"g", "rescan library (webMAN re-reads the ISO folders)"},
		{"S", "shutdown"},
		{"R", "restart"},
	}},
	{"thermals (t)", []keyHelp{
		{"↑↓ + −", "fan step: speed (manual) / target (dynamic)"},
		{"f", "fan mode (leaving SYSCON is one-way; asks first)"},
		{"h", "long-term history page (and back)"},
		{"r", "refresh"},
		{"esc q t", "back"},
	}},
	{"", []keyHelp{
		{"?", "this screen"},
		{"q", "quit"},
	}},
}

// helpText is the reference as plain text, for --help.
func helpText() string {
	var sb strings.Builder
	sb.WriteString("\nKeys:\n")
	for _, s := range helpSections {
		if s.title != "" {
			fmt.Fprintf(&sb, "  %s\n", s.title)
		}
		for _, k := range s.keys {
			fmt.Fprintf(&sb, "    %-26s %s\n", k.keys, k.what)
		}
	}
	return sb.String()
}

// helpView is the reference as a screen: sections as dim headings, keys in
// the accent colour, one line each. Two columns when the width allows —
// the whole table is taller than a 30-row terminal's body in one — and one
// column otherwise, clipped to the width like every frame.
func (m *model) helpView() string {
	blocks := make([]string, len(helpSections))
	for i, s := range helpSections {
		blocks[i] = helpBlock(s)
	}
	left := strings.Join(blocks[:2], "\n\n")  // list, game
	right := strings.Join(blocks[2:], "\n\n") // console, thermals, other
	if lipgloss.Width(left)+4+lipgloss.Width(right) <= m.width {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, "    ", right)
	}
	return left + "\n\n" + right
}

func helpBlock(s helpSection) string {
	keyW := 0
	for _, k := range s.keys {
		keyW = max(keyW, lipgloss.Width(k.keys))
	}
	var out []string
	if s.title != "" {
		out = append(out, "  "+dimSt.Render(s.title))
	}
	for _, k := range s.keys {
		out = append(out, "    "+accentSt.Render(pad(k.keys, keyW))+"  "+k.what)
	}
	return strings.Join(out, "\n")
}
