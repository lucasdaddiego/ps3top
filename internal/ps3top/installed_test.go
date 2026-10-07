package ps3top

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Games a PKG installed to /dev_hdd0/game. The fixture listing is synthetic,
// shaped like the real one: a disc's patch folder, its "_install" and
// "GAMEDATA" side folders, a cover-pack installer, two installed games, a
// folder with no PARAM.SFO, and two homebrew apps under non-Sony IDs.

// sfo builds a PARAM.SFO the way the PS3 lays one out: header, index, key
// table, data table. Fields are key/value pairs, written as NUL-terminated
// strings; an integer field rides along to prove it is skipped.
func sfo(fields ...string) []byte {
	type field struct {
		key    string
		val    []byte
		format uint16
	}
	var fs []field
	for i := 0; i+1 < len(fields); i += 2 {
		fs = append(fs, field{fields[i], append([]byte(fields[i+1]), 0), sfoUTF8})
	}
	fs = append(fs, field{"PARENTAL_LEVEL", []byte{5, 0, 0, 0}, 0x0404})

	le := binary.LittleEndian
	var keys, data bytes.Buffer
	idx := make([]byte, 16*len(fs))
	for i, f := range fs {
		e := idx[i*16:]
		le.PutUint16(e, uint16(keys.Len()))
		le.PutUint16(e[2:], f.format)
		le.PutUint32(e[4:], uint32(len(f.val)))
		le.PutUint32(e[8:], uint32(len(f.val)))
		le.PutUint32(e[12:], uint32(data.Len()))
		keys.WriteString(f.key)
		keys.WriteByte(0)
		data.Write(f.val)
	}
	hdr := make([]byte, 20)
	copy(hdr, "\x00PSF")
	le.PutUint32(hdr[4:], 0x0101)
	le.PutUint32(hdr[8:], uint32(20+len(idx)))
	le.PutUint32(hdr[12:], uint32(20+len(idx)+keys.Len()))
	le.PutUint32(hdr[16:], uint32(len(fs)))
	return slices.Concat(hdr, idx, keys.Bytes(), data.Bytes())
}

func TestParseSFO(t *testing.T) {
	got, err := parseSFO(sfo("CATEGORY", "HG", "TITLE", "Demo Strike®", "TITLE_ID", "DEMO00575", "APP_VER", "01.00"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CATEGORY": "HG", "TITLE": "Demo Strike®", "TITLE_ID": "DEMO00575", "APP_VER": "01.00"}
	if len(got) != len(want) {
		t.Errorf("fields = %v, want %v (the integer field must be skipped)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// The file comes off the network. A short read, a wrong file or a lying
// offset must come back as an error, never as a panic.
func TestParseSFORefusesBrokenFiles(t *testing.T) {
	good := sfo("CATEGORY", "HG", "TITLE", "Demo Strike®")
	for n := range len(good) {
		parseSFO(good[:n]) // no panic at any length is the assertion
	}
	if _, err := parseSFO([]byte("<html>404 Not Found</html>")); err == nil {
		t.Error("an HTML page parsed as a PARAM.SFO")
	}
	lying := bytes.Clone(good)
	binary.LittleEndian.PutUint32(lying[20+12:], 1<<30) // first field's data offset
	if _, err := parseSFO(lying); err == nil {
		t.Error("a data offset past the end was accepted")
	}
	huge := bytes.Clone(good)
	binary.LittleEndian.PutUint32(huge[16:], 1<<31) // entry count
	if _, err := parseSFO(huge); err == nil {
		t.Error("an entry count past the end was accepted")
	}
}

func TestInstalledGame(t *testing.T) {
	const covers = "/dev_hdd0//game/MOCK80608/USRDIR/covers/"
	g, ok := installedGame("DEMO30392", map[string]string{
		"CATEGORY": "HG", "TITLE": "Sample Quest\nOnline", "TITLE_ID": "DEMO30392", "APP_VER": "01.04",
	}, covers)
	if !ok {
		t.Fatal("an HG folder was dropped")
	}
	want := Game{
		Title: "Sample Quest Online", ID: "DEMO30392", Ver: "01.04",
		Path: "/dev_hdd0/game/DEMO30392", MountURL: "/mount_ps3/dev_hdd0/game/DEMO30392",
		IconPath: covers + "DEMO30392.JPG", AltIcon: "/dev_hdd0/game/DEMO30392/ICON0.PNG",
		Category: "hdd0/game",
	}
	if g != want {
		t.Errorf("entry =\n  %+v\nwant\n  %+v", g, want)
	}
	if !g.Installed() || g.Console() != "PS3" {
		t.Errorf("Installed/Console = %v/%s", g.Installed(), g.Console())
	}

	// a patch or data folder is not a game
	for _, cat := range []string{"GD", "SF", ""} {
		if _, ok := installedGame("MOCK01807", map[string]string{"CATEGORY": cat, "TITLE": "x"}, covers); ok {
			t.Errorf("category %q kept", cat)
		}
	}

	// no covers folder: the folder's own icon, and nothing behind it. A
	// missing title or a malformed ID falls back to the folder name.
	g, _ = installedGame("DEMO00575", map[string]string{"CATEGORY": "HG", "TITLE_ID": "\x1b[2J", "APP_VER": "1"}, "")
	if g.Title != "DEMO00575" || g.ID != "DEMO00575" || g.Ver != "" {
		t.Errorf("fallbacks: %+v", g)
	}
	if g.IconPath != "/dev_hdd0/game/DEMO00575/ICON0.PNG" || g.AltIcon != "" {
		t.Errorf("icons without a covers folder: %q / %q", g.IconPath, g.AltIcon)
	}
}

func TestParseGameDirs(t *testing.T) {
	b, err := os.ReadFile("testdata/dir_game.html")
	if err != nil {
		t.Fatal(err)
	}
	got := parseGameDirs(string(b))
	want := []string{"MOCK98106", "MOCK01807", "MOCK80608", "DEMO00404", "DEMO00575", "DEMO30392"}
	if !slices.Equal(got, want) {
		t.Errorf("folders = %v\nwant      %v", got, want)
	}
}

func TestCoversDir(t *testing.T) {
	for _, c := range []struct {
		fixture, want string
	}{
		{"testdata/mygames_info_ids.xml", "/dev_hdd0//game/MOCK80608/USRDIR/covers/"},
		{"testdata/mygames.xml", ""}, // wmtmp PNG icons: webMAN isn't using a covers folder
	} {
		b, err := os.ReadFile(c.fixture)
		if err != nil {
			t.Fatal(err)
		}
		if got := coversDir(parseGames(string(b))); got != c.want {
			t.Errorf("%s: coversDir = %q, want %q", c.fixture, got, c.want)
		}
	}
}

// installedConsole serves the game-folder listing and the PARAM.SFOs behind
// it, plus whatever extra handles, and records every path asked for.
func installedConsole(t *testing.T, extra func(w http.ResponseWriter, r *http.Request) bool) (*Client, func() []string) {
	t.Helper()
	listing, err := os.ReadFile("testdata/dir_game.html")
	if err != nil {
		t.Fatal(err)
	}
	sfos := map[string][]byte{
		"MOCK98106": sfo("CATEGORY", "GD", "TITLE", "Twisted Sample", "TITLE_ID", "MOCK98106"),
		"MOCK01807": sfo("CATEGORY", "GD", "TITLE", "Sample City V", "TITLE_ID", "MOCK01807", "APP_VER", "01.27"),
		"MOCK80608": sfo("CATEGORY", "GD", "TITLE", "Covers Installer", "TITLE_ID", "MOCK80608"),
		"DEMO00575": sfo("CATEGORY", "HG", "TITLE", "Demo Strike®", "TITLE_ID", "DEMO00575", "APP_VER", "01.00"),
		"DEMO30392": sfo("CATEGORY", "HG", "TITLE", "Sample Quest\nOnline", "TITLE_ID", "DEMO30392", "APP_VER", "01.04"),
		"NP0SAMPLE": sfo("CATEGORY", "HG", "TITLE", "Sample Save Tool", "TITLE_ID", "NP0SAMPLE"),
		"PADTESTER": sfo("CATEGORY", "HG", "TITLE", "Pad Tester", "TITLE_ID", "PADTESTER"),
	}
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		if extra != nil && extra(w, r) {
			return
		}
		if r.URL.Path == "/dev_hdd0/game/" {
			w.Write(listing)
			return
		}
		if dir, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/dev_hdd0/game/"), "/PARAM.SFO"); ok {
			if b, ok := sfos[dir]; ok {
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(strings.TrimPrefix(srv.URL, "http://")), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// The library already names MOCK98106 (an ISO), so its folder is that disc's
// patch and is never read. Every other Sony-shaped folder costs one PARAM.SFO
// read, and only the two HG ones come back.
func TestInstalledGamesFromTheConsole(t *testing.T) {
	cli, asked := installedConsole(t, nil)
	lib := []Game{{Title: "Twisted Sample", ID: "MOCK98106", Category: "hdd0/PS3ISO",
		IconPath: "/dev_hdd0//game/MOCK80608/USRDIR/covers/MOCK98106.JPG"}}

	got, _, err := cli.InstalledGames(context.Background(), lib, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	if !slices.Equal(ids, []string{"DEMO00575", "DEMO30392"}) {
		t.Errorf("installed = %v, want [DEMO00575 DEMO30392]", ids)
	}
	if len(got) == 2 && (got[0].Title != "Demo Strike®" || got[0].IconPath != "/dev_hdd0//game/MOCK80608/USRDIR/covers/DEMO00575.JPG") {
		t.Errorf("first entry: %+v", got[0])
	}

	want := []string{
		"/dev_hdd0/game/",
		"/dev_hdd0/game/MOCK01807/PARAM.SFO",
		"/dev_hdd0/game/MOCK80608/PARAM.SFO",
		"/dev_hdd0/game/DEMO00404/PARAM.SFO",
		"/dev_hdd0/game/DEMO00575/PARAM.SFO",
		"/dev_hdd0/game/DEMO30392/PARAM.SFO",
	}
	if a := asked(); !slices.Equal(a, want) {
		t.Errorf("requests =\n  %v\nwant\n  %v", a, want)
	}
}

// A console that runs out the budget part-way keeps what was read before.
func TestInstalledGamesKeepsWhatItReadBeforeTheDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cli, _ := installedConsole(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/dev_hdd0/game/DEMO30392/PARAM.SFO" {
			return false
		}
		cancel()
		<-r.Context().Done() // the client hangs up on its cancelled context
		return true
	})
	got, _, err := cli.InstalledGames(ctx, nil, nil)
	if err == nil {
		t.Error("a cancelled pass reported no error")
	}
	if len(got) != 1 || got[0].ID != "DEMO00575" {
		t.Errorf("kept %+v, want the one game read before the deadline", got)
	}
}

// The installed games join the library, sorted in among the PS3 titles; a
// failed pass leaves the library whole and says why.
func TestFetchGamesAddsInstalledGames(t *testing.T) {
	xml, err := os.ReadFile("testdata/mygames_info_ids.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		listing   bool
		wantTitle []string
		wantFlash string
	}{
		{"listed", true, []string{"Demo Quest", "Demo Strike®", "Fixture Storm™", "Legacy Title", "Sample Game™ 2", "Sample Quest Online"}, ""},
		{"listing fails", false, []string{"Demo Quest", "Fixture Storm™", "Legacy Title", "Sample Game™ 2"}, "installed games: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			cli, _ := installedConsole(t, func(w http.ResponseWriter, r *http.Request) bool {
				switch {
				case strings.HasSuffix(r.URL.Path, "mygames.xml"):
					w.Write(xml)
				case r.URL.Path == "/dev_hdd0/game/" && !c.listing:
					http.Error(w, "boom", http.StatusInternalServerError)
				default:
					return false
				}
				return true
			})
			m := liveModel(t, 120)
			m.cli = cli
			m.flash = ""
			// one step, not exec: the follow-ups include the flash's own
			// clearing timer
			mm, _ := m.Update(m.fetchGames()())
			m = mm.(*model)
			var titles []string
			for _, g := range m.games {
				titles = append(titles, g.Title)
			}
			if !slices.Equal(titles, c.wantTitle) {
				t.Errorf("library =\n  %q\nwant\n  %q", titles, c.wantTitle)
			}
			if c.wantFlash == "" && m.flash != "" || !strings.HasPrefix(m.flash, c.wantFlash) {
				t.Errorf("flash = %q, want prefix %q", m.flash, c.wantFlash)
			}
		})
	}
}

// p is one request on a full build and the mount on Lite, where /play.ps3
// ignores its argument; an installed game starts by its folder name.
func TestPlayKeyByEdition(t *testing.T) {
	iso := Game{Title: "Sample Game™ 2", ID: "MOCK30982", Category: "hdd0/PS3ISO",
		Path: "/dev_hdd0/PS3ISO/SampleGame2.iso", MountURL: "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"}
	inst, _ := installedGame("DEMO00575", map[string]string{"CATEGORY": "HG", "TITLE": "Demo Strike®"}, "")
	for _, c := range []struct {
		name string
		key  string
		lite bool
		game Game
		want string
	}{
		{"full iso", "p", false, iso, "/play.ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{"lite iso", "p", true, iso, "/mount_ps3/dev_hdd0/PS3ISO/SampleGame2.iso"},
		{"full installed", "p", false, inst, "/play.ps3?DEMO00575"},
		{"lite installed", "p", true, inst, "/mount_ps3/dev_hdd0/game/DEMO00575"},
		{"enter installed", "enter", true, inst, "/mount_ps3/dev_hdd0/game/DEMO00575"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/cpursx") {
					got = r.URL.RequestURI()
				}
				w.Write([]byte("webMAN CPU: 58°C"))
			}))
			defer srv.Close()

			m := liveModel(t, 120)
			m.cli = NewClient(strings.TrimPrefix(srv.URL, "http://"))
			m.st.InGame, m.st.Lite = false, c.lite
			m.games[0] = c.game
			m.applyFilter("")
			_, cmd := m.handleKey(key(c.key))
			if cmd == nil {
				t.Fatalf("%s sent nothing", c.key)
			}
			cmd() // the request itself; what follows it is the flash
			if got != c.want {
				t.Errorf("%s hit %q, want %q", c.key, got, c.want)
			}
		})
	}
}

// An installed game's multiMAN cover may not exist; its ICON0 always does.
func TestInstalledCoverFallsBackToICON0(t *testing.T) {
	m := liveModel(t, 120)
	m.artOn = true
	g, _ := installedGame("DEMO00575", map[string]string{"CATEGORY": "HG", "TITLE": "Demo Strike®"},
		"/dev_hdd0//game/MOCK80608/USRDIR/covers/")
	m.games[0] = g
	m.applyFilter("")

	if icon, _ := m.coverWanted(); icon != g.IconPath {
		t.Fatalf("first try = %q, want the multiMAN cover", icon)
	}
	m.coverFailed[g.IconPath] = true
	if icon, _ := m.coverWanted(); icon != g.AltIcon {
		t.Errorf("after a 404 = %q, want %q", icon, g.AltIcon)
	}
	m.coverFailed[g.AltIcon] = true
	if icon, ok := m.coverWanted(); ok {
		t.Errorf("both failed, still asking for %q", icon)
	}
	if !strings.Contains(m.artPanel(), "no cover") {
		t.Error("both failed, and the panel doesn't settle on \"no cover\"")
	}
}

// A folder has no size in its listing, so an installed game costs no request.
func TestFetchSizesSkipsInstalledGames(t *testing.T) {
	cli, asked := installedConsole(t, nil)
	m := liveModel(t, 120)
	m.cli = cli
	inst, _ := installedGame("DEMO00575", map[string]string{"CATEGORY": "HG"}, "")
	m.games = []Game{{Title: "Sample Game™ 2", Path: "/dev_hdd0/PS3ISO/SampleGame2.iso"}, inst}
	exec(t, m, m.fetchSizes())
	if a := asked(); !slices.Equal(a, []string{"/dev_hdd0/PS3ISO/"}) {
		t.Errorf("requests = %v, want only the ISO folder", a)
	}
}

// A reload reads only the folders the session hasn't seen: the first pass
// costs one PARAM.SFO per unknown folder (a 404 counts as read: the console
// said there is no file), the second only the listing, and the installed
// games are still in the list.
func TestInstalledGamesAreCachedAcrossReloads(t *testing.T) {
	xml, err := os.ReadFile("testdata/mygames_info_ids.xml")
	if err != nil {
		t.Fatal(err)
	}
	cli, asked := installedConsole(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "mygames.xml") {
			w.Write(xml)
			return true
		}
		return false
	})
	m := liveModel(t, 120)
	m.cli = cli
	sfoReads := func(from int) (n int) {
		for _, p := range asked()[from:] {
			if strings.HasSuffix(p, "/PARAM.SFO") {
				n++
			}
		}
		return n
	}
	load := func() {
		mm, _ := m.Update(m.fetchGames()())
		m = mm.(*model)
	}
	load()
	first := len(asked())
	if n := sfoReads(0); n == 0 || len(m.sfoCache) != n {
		t.Fatalf("the first pass read %d PARAM.SFOs and remembered %d", n, len(m.sfoCache))
	}
	load()
	if n := sfoReads(first); n != 0 {
		t.Errorf("the second pass re-read %d PARAM.SFOs", n)
	}
	var titles []string
	for _, g := range m.games {
		titles = append(titles, g.Title)
	}
	for _, want := range []string{"Demo Strike®", "Sample Quest Online"} {
		if !slices.Contains(titles, want) {
			t.Errorf("%q missing after the cached reload: %v", want, titles)
		}
	}
}
