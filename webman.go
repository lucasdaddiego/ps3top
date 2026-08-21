package main

// webMAN MOD HTTP client + parsers for the sMAN-skinned build (1.47.48).
// Etiquette: webMAN's web server has ~4 session slots — every request uses a
// fresh connection (no keep-alive), short timeouts, and callers must keep at
// most one status request in flight.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// unknown marks a numeric field the status page didn't carry. It has to be a
// sentinel rather than the zero value: webMAN legitimately reports 0 for some
// of these (FAN SPEED: 0%), so 0 cannot mean "absent". Before this existed a
// markup change made every metric parse as 0 — and 0°C renders as healthy
// green, i.e. a broken parser looked like a perfectly cool console.
const unknown = -1

type Status struct {
	InGame     bool
	GameTitle  string // "Sample Game™ 2" (version suffix stripped)
	GameID     string // "MOCK30982"
	GameVer    string // "01.15"
	CPUTemp    int    // °C, or unknown
	RSXTemp    int    // °C, or unknown
	FanMode    string // "SYSCON" or "manual" ("" if neither marker is present)
	FanPct     int    // %, or unknown
	MaxTemp    int    // dynamic mode only: °C ceiling at which webMAN forces the fan up; else unknown
	MemFreeKB  int    // free available memory (meminfo.avail) — ~1MB in-game is normal; or unknown
	HDDFreeGB  float64
	PlayTime   string // "00:28:34" (empty on XMB)
	PlaySecs   int    // PlayTime in seconds — the authoritative session length
	Uptime     string // "00:28:53"
	MountedISO string // "/dev_hdd0/PS3ISO/SampleGame2.iso", "" if nothing mounted
	Firmware   string // "4.93 CEX PS3HEN 3.5.0"
	WMVersion  string // "1.47.48q"
	LifeDays   int    // 218 — total powered-on days (syscon counter)
	Boots      int    // 2709 — power-on count
	HardOffs   int    // 65 — power-ons minus clean power-offs = unclean shutdowns
}

// statusFields is how many of the page's core fields parseStatus expects to
// find; missing() reports how many it actually lost, which is the signal that
// webMAN's markup moved under us.
const statusFields = 6

func (s Status) missing() int {
	n := 0
	for _, v := range []int{s.CPUTemp, s.RSXTemp, s.FanPct, s.MemFreeKB} {
		if v == unknown {
			n++
		}
	}
	if s.HDDFreeGB == unknown {
		n++
	}
	if s.Firmware == "" {
		n++
	}
	return n
}

type Game struct {
	Title    string // "Sample Game™ 2"
	ID       string // "MOCK30982"
	Ver      string // "01.15" — the installed version, when webMAN lists it
	Path     string // "/dev_hdd0/PS3ISO/SampleGame2.iso"
	MountURL string // "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"
	IconPath string // "/dev_hdd0/tmp/wmtmp/SampleGame2.PNG"
	Category string // "hdd0/PS3ISO"
}

// consoleOrder is chronological — release order, oldest first.
var consoleOrder = []string{"PSX", "PS2", "PSP", "PS3"}

// Console derives the platform from webMAN's source-folder category
// (hdd0/PSXISO, hdd0/PS2ISO, …; anything unrecognized is a PS3 title).
func (g Game) Console() string {
	switch {
	case strings.Contains(g.Category, "PSX"):
		return "PSX"
	case strings.Contains(g.Category, "PS2"):
		return "PS2"
	case strings.Contains(g.Category, "PSP"):
		return "PSP"
	}
	return "PS3"
}

func consoleRank(name string) int {
	for i, c := range consoleOrder {
		if c == name {
			return i
		}
	}
	return len(consoleOrder)
}

type Client struct {
	base string
	http *http.Client
}

// normalizeHost turns a --host / PS3TOP_HOST value into the bare authority the
// client addresses. webMAN is reached as host[:port] and nothing else: a value
// carrying a path silently prefixes every endpoint — status becomes
// /prefix/cpursx.ps3, a mount becomes /prefix/mount_ps3/... — and the failures
// that follow look like the console misbehaving rather than like a typo. A
// leading http:// is tolerated because it's the obvious thing to paste.
func normalizeHost(h string) (string, error) {
	h = strings.TrimSpace(h)
	if s := strings.TrimPrefix(h, "http://"); s != h {
		h = s
	} else if strings.Contains(h, "://") {
		return "", fmt.Errorf("only http is supported: %q", h)
	}
	h = strings.TrimSuffix(h, "/")
	if h == "" {
		return "", fmt.Errorf("empty host")
	}
	if i := strings.IndexAny(h, "/?#@ \t"); i >= 0 {
		return "", fmt.Errorf("expected host or host:port, got %q", h)
	}
	// url.Parse is what enforces the authority shape: brackets around an IPv6
	// literal, a numeric port, no stray delimiters.
	u, err := url.Parse("http://" + h)
	if err != nil {
		return "", fmt.Errorf("bad host %q: %w", h, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("bad host %q", h)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("bad port in %q", h)
		}
	}
	return u.Host, nil
}

func NewClient(host string) *Client {
	return &Client{
		base: "http://" + host,
		http: &http.Client{
			// Hard backstop only — every caller passes a context with the real
			// deadline (status 6s, games/covers 8s, rescan 30s — the long pole,
			// and why this sits at 35s), which also bounds body reads.
			Timeout: 35 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				DialContext:       (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			},
			// webMAN doesn't redirect, so a redirect means we aren't talking to
			// the console we were pointed at. Following one off-host would aim
			// the next state-changing GET — mount, eject, shutdown — at whatever
			// answered, while the UI still displays the address you chose.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("refusing redirect to %s (asked for %s)", req.URL.Host, via[0].URL.Host)
				}
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "close")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// fire hits an action endpoint and discards the response body.
func (c *Client) fire(ctx context.Context, path string) error {
	_, err := c.get(ctx, path)
	return err
}

// errNotWebMAN is what Status returns for a host that answers /cpursx.ps3
// with something other than webMAN's page. It's the discovery signal: the
// address came from a cache or a sweep, and anything else on port 80 of a
// LAN host — a router, a printer — will happily 200 a path it doesn't know.
// Without the check that page parses as a console with every field missing.
var errNotWebMAN = errors.New("not a webMAN console")

func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.statusPage(ctx, "/cpursx.ps3")
}

func (c *Client) statusPage(ctx context.Context, path string) (Status, error) {
	body, err := c.get(ctx, path)
	if err != nil {
		return Status{}, err
	}
	if !bytes.Contains(body, []byte("webMAN")) {
		return Status{}, errNotWebMAN
	}
	return parseStatus(string(body)), nil
}

func (c *Client) Games(ctx context.Context) ([]Game, error) {
	body, err := c.get(ctx, "/dev_hdd0/xmlhost/game_plugin/mygames.xml")
	if err != nil {
		return nil, err
	}
	return parseGames(string(body)), nil
}

func (c *Client) Cover(ctx context.Context, iconPath string) ([]byte, error) {
	return c.get(ctx, iconPath)
}

func (c *Client) Mount(ctx context.Context, g Game) error { return c.fire(ctx, g.MountURL) }
func (c *Client) Eject(ctx context.Context) error         { return c.fire(ctx, "/mount_ps3/unmount") }
func (c *Client) Launch(ctx context.Context) error        { return c.fire(ctx, "/play.ps3") }
func (c *Client) Play(ctx context.Context, g Game) error  { return c.fire(ctx, "/play.ps3"+g.Path) }
func (c *Client) Shutdown(ctx context.Context) error      { return c.fire(ctx, "/shutdown.ps3") }
func (c *Client) Restart(ctx context.Context) error       { return c.fire(ctx, "/restart.ps3") }

// Rescan asks webMAN to re-scan the ISO folders and rebuild the XMB game XML —
// the only way a freshly FTP'd ISO shows up, since mygames.xml is static until
// webMAN regenerates it. ?xmb is what the sMAN skin's own Refresh menu item
// sends; the plain /refresh.ps3 rebuilds the DB but not the XML we read.
func (c *Client) Rescan(ctx context.Context) error { return c.fire(ctx, "/refresh.ps3?xmb") }

func (c *Client) Popup(ctx context.Context, msg string) error {
	return c.fire(ctx, "/popup.ps3/"+url.PathEscape(msg))
}

// Fan commands, taken from the links the status page hangs on its own
// temperature and fan readings: "up"/"dn" step the fan, "mode" advances
// webMAN's fan strategy. Note "mode" is a one-way cycle through webMAN's own
// states, not a SYSCON toggle — every state it reaches has webMAN fan control
// enabled, and only /setup.ps3 hands control back to SYSCON.
//
// These render the whole status page as their response, so the reply IS the
// new state — no follow-up poll, and the caller can report the actual delta
// instead of assuming the step landed.
const (
	fanUp   = "up"
	fanDown = "dn"
	fanMode = "mode"
)

func (c *Client) Fan(ctx context.Context, cmd string) (Status, error) {
	return c.statusPage(ctx, "/cpursx.ps3?"+cmd)
}

// --- parsers (markers pinned from testdata/cpursx_ingame.html) ---

var (
	reCPU     = regexp.MustCompile(`CPU:\s*(\d+)°C`)
	reRSX     = regexp.MustCompile(`RSX:\s*(\d+)°C`)
	reFanMode = regexp.MustCompile(`\[Fan control:\s*([^\]]+)\]`)
	// webMAN doesn't just drop the fan-control label in manual mode — it
	// swaps that slot for the temperature ceiling at which it forces the fan
	// up. °C is required so the Fahrenheit copy of the line ("MAX: 186°F")
	// can't match. Confirmed by a before/after capture across a ?mode toggle.
	reFanMax   = regexp.MustCompile(`\(MAX:\s*(\d+)°C\)`)
	reFanPct   = regexp.MustCompile(`FAN SPEED:\s*(\d+)%`)
	reMem      = regexp.MustCompile(`MEM:\s*([\d,]+)\s*KB`)
	reHDD      = regexp.MustCompile(`HDD:\s*([\d.,]+)\s*GB free`)
	rePlay     = regexp.MustCompile(`(?s)title="Play">.*?</label>\s*([\d:]+)`)
	reUptime   = regexp.MustCompile(`(?s)title="Startup">.*?</label>\s*([\d:]+)`)
	reGameID   = regexp.MustCompile(`/tpl/np/([A-Z0-9]{9})/`)
	reGameName = regexp.MustCompile(`google\.com/search\?q=[^"]*">([^<]+)</a>`)
	rePID      = regexp.MustCompile(`>pid=[0-9A-Fa-fx]+<`)
	reMounted  = regexp.MustCompile(`href="/mount\.ps3(/[^"]+)"`)
	reFW       = regexp.MustCompile(`Firmware:\s*([^<]+?)\s*<`)
	reWM       = regexp.MustCompile(`webMAN\s+([\w.]+)\s+MOD`)
	reLifetime = regexp.MustCompile(`power\.png[^>]*>\s*([^<]+?)\s*</H1>`)
	// the dot is required: a bare trailing digit is part of the title
	// ("MOCK PLANET 2"), only "NN.NN" is an APP_VER suffix
	reGameVer  = regexp.MustCompile(`\s+(\d+\.[\d.]+)$`)
	reLifeBits = regexp.MustCompile(`^(\d+)d [\d:]+ • ([\d,]+) ON • [\d,]+ OFF \((\d+)\)`)
)

func parseStatus(html string) Status {
	s := Status{
		MaxTemp:   unknown,
		PlayTime:  matchStr(rePlay, html),
		Uptime:    matchStr(reUptime, html),
		GameID:    matchStr(reGameID, html),
		Firmware:  matchStr(reFW, html),
		WMVersion: matchStr(reWM, html),
	}
	s.CPUTemp = matchInt(reCPU, html)
	s.RSXTemp = matchInt(reRSX, html)
	s.FanPct = matchInt(reFanPct, html)
	s.MemFreeKB = matchInt(reMem, html)
	s.HDDFreeGB = matchFloat(reHDD, html)
	s.PlaySecs = clockSecs(s.PlayTime)
	s.InGame = rePID.MatchString(html)

	// ?mode cycles three states, and each renders that one slot differently —
	// so the mode is read from which marker is present, including neither:
	//
	//   [Fan control: SYSCON]   console's own control
	//   (MAX: 98°C)             webMAN ramps to hold a target temp
	//   nothing                 manual fixed %
	//
	// Which matters beyond the label: ?up/?dn move the *target* in dynamic
	// mode and the *percentage* in manual, so callers can't assume what a
	// step did. The FanPct guard keeps a page that didn't parse at all from
	// reporting itself as manual.
	if mode := matchStr(reFanMode, html); mode != "" {
		s.FanMode = mode
	} else if mx := matchInt(reFanMax, html); mx != unknown {
		s.FanMode, s.MaxTemp = "dynamic", mx
	} else if s.FanPct != unknown {
		s.FanMode = "manual"
	}
	if m := reGameName.FindStringSubmatch(html); m != nil {
		name := sanitize(m[1])
		if v := reGameVer.FindStringSubmatch(name); v != nil {
			s.GameVer = v[1]
		}
		s.GameTitle = strings.TrimSpace(reGameVer.ReplaceAllString(name, ""))
	}
	if m := reMounted.FindStringSubmatch(html); m != nil {
		s.MountedISO = sanitize(m[1])
	}
	// "218d 07:19:14 • 2,709 ON • 2,644 OFF (65)" — the syscon counters
	if m := reLifeBits.FindStringSubmatch(matchStr(reLifetime, html)); m != nil {
		s.LifeDays, _ = strconv.Atoi(m[1])
		s.Boots, _ = strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		s.HardOffs, _ = strconv.Atoi(m[3])
	}
	return s
}

// sanitize neutralizes a string that came off the wire before it can reach the
// terminal. Every one of these fields — titles, firmware, versions, paths — is
// eventually printed, and webMAN is unauthenticated HTTP discovered by a
// substring match, so the bytes are not trustworthy just because they arrived
// on the LAN. A raw ESC in a game title is a cursor move, a screen rewrite, or
// an OSC 52 clipboard write, depending on the terminal.
//
// Same reasoning as escaping untrusted text on its way into HTML: do it once at
// the boundary, not at each of the dozen render sites, and never rely on a
// style wrapper to have done it. Printable text is untouched, CJK and the ™/®
// real titles carry included. Whitespace controls fold to a space so a wrapped
// field reads as one line; everything else in C0/C1 and the invisible bidi
// overrides is dropped.
func sanitize(s string) string {
	if strings.IndexFunc(s, badRune) < 0 {
		return s // the overwhelming common case — don't allocate for it
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r':
			return ' '
		case badRune(r):
			return -1
		}
		return r
	}, s)
}

func badRune(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f: // C0 and DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 (an 8-bit CSI is one byte, not two)
		return true
	case r == 0x200e, r == 0x200f: // LRM / RLM
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embedding and override
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	}
	return false
}

func matchStr(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(sanitize(m[1]))
	}
	return ""
}

func matchInt(re *regexp.Regexp, s string) int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return unknown
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil {
		return unknown
	}
	return n
}

func matchFloat(re *regexp.Regexp, s string) float64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return unknown
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return unknown
	}
	return f
}

// clockSecs converts webMAN's "HH:MM:SS" to seconds (0 if unparseable).
func clockSecs(c string) int {
	parts := strings.Split(c, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	s, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return h*3600 + mi*60 + s
}

// mygames.xml is XMB-flavored pseudo-XML (`<>value</>` is not a legal tag),
// so encoding/xml can't touch it — regex it is.
var (
	reEntry = regexp.MustCompile(`(?s)<T key="\d+" include="inc">(.*?)</T>`)
	reField = regexp.MustCompile(`<P key="(icon|title|module_action|info)"><>([^<]*)</>`)
	reTitID = regexp.MustCompile(`\s*\[([A-Z0-9]{9})\]\s*$`)
	reID    = regexp.MustCompile(`^[A-Z0-9]{9}$`)
	reVer   = regexp.MustCompile(`^v(\d+\.[\d.]+)$`)
)

// splitInfo takes the info field apart. webMAN has rendered it two ways: the
// bare source folder ("hdd0/PS3ISO", with the title ID tacked onto the title
// as "[BLES00455]"), and — on current builds — "hdd0/PS3ISO | BLES00455 |
// v01.90", the ID and installed version moved out of the title. The first
// segment is the category either way; any segment that looks like a title ID
// or a version is taken for what it is, and the rest is ignored rather than
// guessed at.
func splitInfo(info string) (category, id, ver string) {
	parts := strings.Split(info, "|")
	category = strings.TrimSpace(parts[0])
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		switch {
		case id == "" && reID.MatchString(p):
			id = p
		case ver == "" && reVer.MatchString(p):
			ver = reVer.FindStringSubmatch(p)[1]
		}
	}
	return category, id, ver
}

func parseGames(xml string) []Game {
	var games []Game
	for _, e := range reEntry.FindAllStringSubmatch(xml, -1) {
		var g Game
		for _, f := range reField.FindAllStringSubmatch(e[1], -1) {
			switch f[1] {
			case "icon":
				g.IconPath = sanitize(f[2])
			case "title":
				// titles can contain literal newlines (e.g. "MOCK PLANET\nSPECIAL
				// EDITION"), so collapse whitespace before sanitizing — otherwise
				// the two halves of the name would be glued together
				g.Title = sanitize(strings.Join(strings.Fields(f[2]), " "))
			case "module_action":
				g.MountURL = sanitize(f[2])
			case "info":
				g.Category, g.ID, g.Ver = splitInfo(sanitize(f[2]))
			}
		}
		// older builds carry the ID as a title suffix instead; strip it from
		// the display name either way, and let it supply the ID if info didn't
		if m := reTitID.FindStringSubmatch(g.Title); m != nil {
			if g.ID == "" {
				g.ID = m[1]
			}
			g.Title = strings.TrimSpace(reTitID.ReplaceAllString(g.Title, ""))
		}
		g.Path = strings.TrimPrefix(g.MountURL, "/mount_ps3")
		if g.MountURL != "" {
			games = append(games, g)
		}
	}
	// consoles in chronological order (PSX → PS2 → PSP → PS3); within a
	// console, series-aware alphabetical (originals before numbered sequels).
	// Sorting an index keeps each game paired with the key computed for it.
	keys := gameKeys(games)
	order := make([]int, len(games))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		ri, rj := consoleRank(games[a].Console()), consoleRank(games[b].Console())
		if ri != rj {
			return ri < rj
		}
		return keys[a].less(keys[b])
	})
	out := make([]Game, len(order))
	for i, gi := range order {
		out[i] = games[gi]
	}
	return out
}

var reFranchise = regexp.MustCompile(`^([^:]+):`)

// franchiseKey is the normalized text before a title's first colon
// ("FIXTURE STORM®: APOCALYPSE" → "fixture storm"), or "" for colon-less titles.
func franchiseKey(title string) string {
	t := strings.NewReplacer("™", "", "®", "").Replace(strings.ToLower(title))
	m := reFranchise.FindStringSubmatch(t)
	if m == nil {
		return ""
	}
	return strings.Join(strings.Fields(m[1]), " ")
}

// gameKey is how one game positions itself in the list: a title to collate on,
// and a release-order tiebreak used only inside a franchise block.
type gameKey struct {
	lead string // the title this entry sorts as — shared by a whole franchise
	id   string // title ID, the release-order hint ("" when the hint is off)
}

func (x gameKey) less(y gameKey) bool {
	if lessTitles(x.lead, y.lead) {
		return true
	}
	if lessTitles(y.lead, x.lead) {
		return false
	}
	return x.id < y.id
}

// noPool marks a franchise member carrying no usable title ID.
const noPool = "-"

// gameKeys adds one hint on top of lessTitles: title-ID digits are assigned in
// registration order within a prefix pool, so entries of the SAME
// colon-franchise sharing ONE 4-letter pool sort by ID — release order for
// subtitle-only sequels the words can't date. It stays a hint: pools differ per
// region/publisher block (a franchise can hop BLUS → BLES), and a franchise
// that hops one falls back to the title rules entirely.
//
// The hint cannot be decided per pair, which is what the previous version did.
// sort requires a strict weak ordering and "compare these two by ID because
// they happen to share a pool" is not transitive: A="F: Zulu"/MOCK00001,
// B="F: Alpha"/MOCK00002, C="F: Mike"/DEMO00001 gives A<B by ID, B<C by title,
// C<A by title — a cycle, so the library sorted differently depending on the
// order webMAN happened to list it in.
//
// Deciding it per franchise isn't enough either: ":" isn't a token, so a
// colon-less title ("F Mike") collates among the "F: …" entries and can land
// between two members that are ordering themselves by ID — the same cycle from
// outside. The fix is to make a qualifying franchise occupy one contiguous
// block: every member sorts under the same lead title, and the ID only breaks
// ties inside it.
func gameKeys(games []Game) []gameKey {
	pools := map[string]map[string]bool{}
	lead := map[string]string{}
	for _, g := range games {
		f := franchiseKey(g.Title)
		if f == "" {
			continue
		}
		if pools[f] == nil {
			pools[f] = map[string]bool{}
		}
		if len(g.ID) == 9 {
			pools[f][g.ID[:4]] = true
		} else {
			pools[f][noPool] = true
		}
		// the block sorts where its earliest title would have
		if cur, seen := lead[f]; !seen || lessTitles(g.Title, cur) {
			lead[f] = g.Title
		}
	}
	keys := make([]gameKey, len(games))
	for i, g := range games {
		f := franchiseKey(g.Title)
		if p := pools[f]; f != "" && len(p) == 1 && !p[noPool] {
			keys[i] = gameKey{lead: lead[f], id: g.ID}
			continue
		}
		keys[i] = gameKey{lead: g.Title}
	}
	return keys
}

// lessTitles orders titles for display: token-by-token, numbers compare
// numerically (arabic, roman, and "V2" style), and at the position where one
// title has a word and the other a number, the word wins — so "Sample
// Quest: The Beginning" precedes "Sample Quest 2" and "MOCK PLANET SPECIAL
// EDITION" precedes "MOCK PLANET 2", matching release order for numbered
// series.
func lessTitles(a, b string) bool {
	ta, tb := titleTokens(a), titleTokens(b)
	for i := 0; i < len(ta) && i < len(tb); i++ {
		x, y := ta[i], tb[i]
		switch {
		case x.isNum && y.isNum:
			if x.num != y.num {
				return x.num < y.num
			}
		case x.isNum != y.isNum:
			return y.isNum
		default:
			if x.word != y.word {
				return x.word < y.word
			}
		}
	}
	if len(ta) != len(tb) {
		return len(ta) < len(tb)
	}
	return sortKey(a) < sortKey(b)
}

type ttok struct {
	isNum bool
	num   int
	word  string
}

var (
	reVNum  = regexp.MustCompile(`^v(\d+)$`)
	reRoman = regexp.MustCompile(`^[ivx]{1,7}$`)
	reAlnum = regexp.MustCompile(`[a-z0-9]+`)
	reRuns  = regexp.MustCompile(`[a-z]+|\d+`)
)

func titleTokens(title string) []ttok {
	t := strings.NewReplacer("™", "", "®", "").Replace(strings.ToLower(title))
	var toks []ttok
	for _, w := range reAlnum.FindAllString(t, -1) {
		switch {
		case reVNum.MatchString(w): // "v2" → 2
			n, _ := strconv.Atoi(reVNum.FindStringSubmatch(w)[1])
			toks = append(toks, ttok{isNum: true, num: n})
		case reRoman.MatchString(w): // "iv" → 4
			toks = append(toks, ttok{isNum: true, num: romanVal(w)})
		default: // split embedded digits: "sample2" → sample, 2
			for _, run := range reRuns.FindAllString(w, -1) {
				if n, err := strconv.Atoi(run); err == nil {
					toks = append(toks, ttok{isNum: true, num: n})
				} else {
					toks = append(toks, ttok{word: run})
				}
			}
		}
	}
	return toks
}

func romanVal(s string) int {
	vals := map[byte]int{'i': 1, 'v': 5, 'x': 10}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			total += vals[s[i+1]] - v
			i++
		} else {
			total += v
		}
	}
	return total
}

// sortKey lowercases and drops trademark noise so "FIXTURE STORM®" and
// "Fixture Storm™" collate together.
func sortKey(title string) string {
	t := strings.ToLower(title)
	t = strings.NewReplacer("™", "", "®", "").Replace(t)
	return strings.TrimSpace(t)
}
