package webman

// Folder listings: webMAN's file manager page for one folder, read back as
// rows. The shapes are file_manager.h's (webMAN MOD 1.47.48, folder_listing
// and add_list_entry), and probe P1 checked two facts on the console on
// 2026-10-10: webMAN decodes %XX in a request path, and it cuts a long
// listing itself (the cover pack, about 10,000 files, listed 482 rows: P1
// counted 483 <tr tags, the header row among them).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Entry is one row of a folder listing.
type Entry struct {
	// Name is the file or folder name, decoded from the row's link and
	// sanitized for display like every string off the wire.
	Name string
	// Path is the entry's absolute request path in webMAN's urlenc form (a
	// space is "+", a "+" is "%2B"): Dir takes it as is, and every entry of
	// a folder starts with the same spelling of that folder. webMAN keeps a
	// link raw when spaces are its only change, and Dir folds those spaces
	// to "+". It is "" when webMAN left the link out or cut it (a row past
	// its 512-byte line limit) or the link holds a control byte: such an
	// entry has no path to request.
	Path string
	// Dir is true for a folder.
	Dir bool
	// Size is a file's size in bytes, 0 for a folder. Exact is true when
	// the row carries the exact byte count. Otherwise the row shows only a
	// rounded figure (a .pkg, a text file, a .SFO: "9,241 KB"), and Size is
	// that figure, rounded down to its unit.
	Size  int64
	Exact bool
}

// Listing is one folder as webMAN lists it: every row but "..", in page
// order, and the two counts its footer prints.
type Listing struct {
	Entries []Entry
	// Dirs and Files are the footer's counts. webMAN prints its folder rows
	// minus one for the ".." row, and Dir checks the rows by that same rule.
	Dirs, Files int
}

// ErrCut marks a listing that may not hold every entry of its folder. Dir
// returns it wrapped, together with the rows it read: errors.Is(err,
// ErrCut).
var ErrCut = errors.New("listing cut")

// Dir lists one folder: GET <path>/, every row of webMAN's file manager
// page, and the footer's counts.
//
// path is a request path in webMAN's escaped form: an Entry's Path, or a
// plain one such as /dev_hdd0/PS3ISO. It must lie under /dev_hdd0 or a
// /dev_usbNNN drive, and Dir refuses, before any request, a path webMAN
// would act on rather than list:
//   - another device: webMAN enables /dev_blind and mounts /dev_hdd1 and the
//     NTFS drives when they are listed
//   - '?' (a query, or a flag such as ?restart.ps3), '*' (a wildcard search
//     that writes a file list), ';' (";/" starts a second command) and '#',
//     raw or %XX-escaped
//   - a $USERID$, $RND$, $000$ or $NEW$ tag, raw or %XX-escaped: webMAN
//     replaces one in a path that does not exist, and lists another folder.
//     Any other '$' is part of a name, as in a USB drive's $RECYCLE.BIN
//   - ".ps3" (webMAN's command names, ".ps3$abort" among them), a control
//     byte, an empty, "." or ".." segment, and a '%' that does not start a
//     %XX
//
// A listing that may be incomplete comes back with the rows it holds and an
// error that wraps ErrCut. The main cut is webMAN's own: folder_listing
// reads at most a fixed number of rows, ".." included, set by the memory it
// got for the page (72 rows for 64 KiB, up to 4,885 for 3 MiB) and, in a
// folder whose path ends in /exdata, by its shorter 200-byte rows (241 rows
// for 64 KiB, up to 15,642 for 3 MiB). Then it prints a footer that counts
// only those rows. So a row count equal to one of those limits is reported
// as cut, also for a whole folder with exactly that many rows (71 entries
// and ".." for 64 KiB). A footer that disagrees with the rows (webMAN prints
// no rows at all when the page outgrows its buffer) and a body at the
// client's 4 MiB cap are cut too.
func (c *Client) Dir(ctx context.Context, path string) (Listing, error) {
	folder, err := dirPath(path)
	if err != nil {
		return Listing{}, err
	}
	body, err := c.get(ctx, folder+"/")
	if err != nil {
		return Listing{}, err
	}
	l, err := parseDir(string(body), folder)
	if err != nil {
		return l, fmt.Errorf("%s: %w", folder, err)
	}
	return l, nil
}

// reDirRoot is the start of a path Dir accepts: the internal drive or a USB
// drive, alone or with a folder below it.
var reDirRoot = regexp.MustCompile(`^/dev_(?:hdd0|usb\d{3})(?:/|$)`)

// reDirTag is a tag that webMAN's check_path_tags replaces in a path that
// does not exist (the user number, a random number, the first free number).
// It matches case for case, as webMAN does. webMAN acts on no other '$' in a
// folder path: its other '$' commands follow a ".ps3" name.
var reDirTag = regexp.MustCompile(`\$(?:USERID|RND|000|NEW)\$`)

// dirPath checks a Dir path and returns it without its trailing slash. The
// checks run on the decoded form, because webMAN decodes the whole request
// path before it looks at it.
func dirPath(p string) (string, error) {
	p = strings.TrimSuffix(p, "/")
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("bad folder path %q: %s", p, why)
	}
	dec, err := url.QueryUnescape(p)
	switch {
	case err != nil:
		return bad("a % that does not start a %XX")
	case !reDirRoot.MatchString(dec):
		return bad("Dir lists /dev_hdd0 and /dev_usbNNN only")
	case strings.ContainsAny(dec, "?*;#") || strings.Contains(dec, ".ps3"):
		return bad("webMAN would take it for a command")
	case reDirTag.MatchString(dec):
		return bad("webMAN would replace its $ tag and list another folder")
	case strings.IndexFunc(dec, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return bad("a control byte")
	}
	for _, seg := range strings.Split(dec, "/")[1:] {
		if seg == "" || seg == "." || seg == ".." {
			return bad("an empty, . or .. segment")
		}
	}
	return p, nil
}

// The page folder_listing writes. Every row opens with rowPrefix
// (TABLE_ITEM_PREFIX); the header row that sets the column widths has no
// link, so it is not one:
//
//	<tr><td><a class="d" href="..">..</a><td> <a href="/stat.ps3/dev_hdd0/PS3ISO">&lt;dir></a> &nbsp; <td>21-Aug-2026 17:48</tr>
//	<tr><td><a class="d" href="Old Rips">Old Rips</a><td> <a href="/mount.ps3/dev_hdd0/PS3ISO/Old Rips">&lt;dir></a> &nbsp; <td>02-Sep-2026 11:40</tr>
//	<tr><td><a class="w ps3" href="Ratchet+%26+Clank.iso">Ratchet & Clank.iso</a><td> <a href="/mount.ps3/dev_hdd0/PS3ISO/Ratchet+%26+Clank.iso" title="8,315,076,608 b">7,929 MB</a> &nbsp; <td>16-Jul-2026 04:41</tr>
//
// The class is "d" for a folder and "w ..." for a file (a folder's class
// changes only in the device rows of the root listing, which Dir never
// asks for). The link comes from urlenc, which writes a space as "+" and
// ' " % & + # ? : and every byte above 0x7f as %XX, but which reports a
// change only when the path grew: a path whose only changes are spaces
// keeps its raw link ("Old Rips" above), any other path gets its escaped
// one. The link is relative when that raw or escaped path still starts
// with the folder's decoded path, else absolute, so in a folder with a
// space a name with any other escape gets an absolute link. The text after
// the link is the raw name, not HTML-escaped, so the name comes from the
// link. The footer follows the table:
//
//	<hr><b><a href="/dev_hdd0">/dev_hdd0</a>: 123.4 GB free</b> &nbsp; <font color="#C0C0C0">2 Dir(s) 8 Files 27,305 MB</font>
const rowPrefix = `<tr><td><a class="`

var (
	// reRow takes one row apart, from past rowPrefix to before its </tr>:
	// the class, the link (absent in a row cut down to fit), the text, and,
	// after the last "</a><td>", the size cell and the date. The size cell
	// never holds that sequence, so a name that does cannot end the name
	// cell early.
	reRow = regexp.MustCompile(`(?s)^([a-z])[^"]*"(?: href="([^"]*)")?[^>]*>(.*)</a><td>(.*)$`)
	// reExactSize is the exact count a size cell hangs on its link or label:
	// title="3,735,420,928 b", and a copy link adds " copy to /dev_usb000".
	reExactSize = regexp.MustCompile(`title="([\d,]+) b[" ]`)
	// reShownSize is the figure a size cell shows: exact in bytes below
	// 10,240 B, else rounded down to KB, MB or GB. A row cut down to fit
	// prints it without the thousands commas.
	reShownSize = regexp.MustCompile(`(?:^|>)\s*([\d,]+) (b|KB|MB|GB)\b`)
	// reDirFooter is the footer's two counts. Other languages translate the
	// word for files, but not "Dir(s)".
	reDirFooter = regexp.MustCompile(`</b> &nbsp; <font color="#C0C0C0">([\d,]+) Dir\(s\) ([\d,]+) `)
)

// linkMax is where add_list_entry cuts an escaped path (_MAX_LINE_LEN): a
// link that reaches it may have lost its tail.
const linkMax = 640

var unitBytes = map[string]int64{"b": 1, "KB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30}

// parseDir reads the listing page of the folder at request path folder
// (escaped, no trailing slash, checked by dirPath). A row it cannot read
// fails the whole page; a page that may be cut returns its rows with an
// error that wraps ErrCut.
func parseDir(page, folder string) (Listing, error) {
	dec, err := url.QueryUnescape(folder)
	if err != nil {
		return Listing{}, err
	}
	foot := reDirFooter.FindAllStringSubmatchIndex(page, -1)
	end := len(page)
	if len(foot) > 0 {
		end = foot[len(foot)-1][0]
	}
	var l Listing
	dirRows, fileRows := 0, 0
	for i, piece := range strings.Split(page[:end], rowPrefix)[1:] {
		cut := strings.LastIndex(piece, "</tr>")
		if cut < 0 {
			break // the page stops inside this row
		}
		e, dotdot, err := rowEntry(piece[:cut], dec)
		if err != nil {
			return Listing{}, fmt.Errorf("row %d: %w", i+1, err)
		}
		if e.Dir {
			dirRows++
		} else {
			fileRows++
		}
		if !dotdot {
			l.Entries = append(l.Entries, e)
		}
	}
	rows := dirRows + fileRows
	switch {
	case len(page) >= bodyCap:
		return l, fmt.Errorf("%w: the body reached the client's %d MiB cap", ErrCut, bodyCap>>20)
	case len(foot) == 0 && rows == 0:
		return Listing{}, errors.New("not a folder listing: no rows and no footer")
	case len(foot) == 0:
		return l, fmt.Errorf("%w: the page stops before its footer", ErrCut)
	}
	f := foot[len(foot)-1]
	l.Dirs, l.Files = count(page[f[2]:f[3]]), count(page[f[4]:f[5]])
	if l.Dirs != max(dirRows-1, 0) || l.Files != fileRows {
		return l, fmt.Errorf(`%w: the footer counts %d folders and %d files, the page has %d folder rows (".." included) and %d file rows`,
			ErrCut, l.Dirs, l.Files, dirRows, fileRows)
	}
	if kib, ok := rowLimit(rows, rowBytes(dec)); ok {
		return l, fmt.Errorf(`%w: %d rows (".." included) is where webMAN stops a listing it builds in %d KiB`, ErrCut, rows, kib)
	}
	return l, nil
}

// rowEntry builds the entry of one row; dotdot reports the ".." row, which
// counts for the footer but is no entry. dec is the folder's decoded request
// path. webMAN makes a link relative only when the entry's path, raw or
// escaped, still starts with dec: either urlenc kept the whole path raw (it
// changed nothing but spaces, so dec and the link hold no '+' or '%'), or it
// escaped the name and found nothing to change in dec (not even a space).
// In both cases dec plus the link, each space folded to "+", is webMAN's
// urlenc form of the path, and an absolute link is that form already.
func rowEntry(row, dec string) (e Entry, dotdot bool, err error) {
	m := reRow.FindStringSubmatch(row)
	if m == nil {
		return Entry{}, false, errors.New("not a row webMAN writes")
	}
	e.Dir = m[1] == "d"
	name, ok := linkName(m[2], dec)
	if ok {
		if strings.HasPrefix(m[2], "/") {
			e.Path = m[2]
		} else {
			e.Path = dec + "/" + m[2]
		}
		e.Path = strings.ReplaceAll(e.Path, " ", "+")
	} else {
		name = m[3] // no usable link: the text is the raw name
	}
	if name == ".." {
		return e, true, nil
	}
	if name == "" || name == "." || strings.Contains(name, "/") {
		return Entry{}, false, fmt.Errorf("no name in %q", sanitize(row))
	}
	e.Name = sanitize(name)
	if sanitize(e.Path) != e.Path {
		e.Path = "" // urlenc leaves a control byte raw: no exact path to hand out
	}
	if e.Dir {
		return e, false, nil
	}
	cell := m[4]
	if s := reExactSize.FindStringSubmatch(cell); s != nil {
		e.Size, e.Exact = count64(s[1]), true
	} else if s := reShownSize.FindStringSubmatch(cell); s != nil {
		e.Size, e.Exact = count64(s[1])*unitBytes[s[2]], s[2] == "b"
	}
	if e.Size < 0 || (e.Size == 0 && !e.Exact) {
		return Entry{}, false, fmt.Errorf("no size for %q", e.Name)
	}
	return e, false, nil
}

// linkName decodes the name out of a row's link, or reports false when the
// link is missing, may have been cut at linkMax, or does not lie in the
// folder (decoded: dec).
func linkName(href, dec string) (string, bool) {
	if href == "" {
		return "", false
	}
	slash := strings.LastIndexByte(href, '/')
	if slash >= 0 {
		// an absolute link: webMAN's escaped path of the entry
		if len(href) >= linkMax || !strings.HasPrefix(href, "/") {
			return "", false
		}
		if d, err := url.QueryUnescape(href[:slash]); err != nil || d != dec {
			return "", false
		}
	} else if len(dec)+1+len(href) >= linkMax {
		return "", false // relative: webMAN's path is as long as dec plus the link
	}
	name, err := url.QueryUnescape(href[slash+1:])
	if err != nil {
		return "", false
	}
	return name, true
}

// rowLimit reports whether n rows (".." included) is where folder_listing
// stops reading a folder whose rows it sizes at size bytes (rowBytes): it
// keeps at most (buffer - 16 KiB)/size - 4 rows, and its page buffer is a
// multiple of 64 KiB from 64 KiB to 3 MiB (www_memory.h: 3 MiB or 2 MiB,
// else 320 KiB to 1,280 KiB by the memory setting, lowered 64 KiB at a time
// until the allocation succeeds). P1's cover pack stopped at 482 rows, the
// limit for 320 KiB and 640-byte rows.
func rowLimit(n, size int) (kib int, ok bool) {
	for b := 64 << 10; b <= 3<<20; b += 64 << 10 {
		if (b-(16<<10))/size-4 == n {
			return b >> 10, true
		}
	}
	return 0, false
}

// rowBytes is the row size folder_listing divides its buffer by for the
// folder at decoded path dec: _MAX_LINE_LEN, 640 bytes, or 200 when the path
// ends in "/exdata" (the license folders: HEN's .rap files, a user's .rif
// files), where file_manager.h lowers it. webMAN tests the path decoded and
// without its trailing slash, the form dec has, with extcmp, which compares
// case for case (libc.c: a memcmp of the path's last 7 bytes), so a folder
// that ends in /EXDATA keeps 640.
func rowBytes(dec string) int {
	if strings.HasSuffix(dec, "/exdata") {
		return 200
	}
	return 640
}

// count reads a footer count ("1,234"); -1 if it won't parse.
func count(s string) int {
	n, err := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	if err != nil {
		return -1
	}
	return n
}

// count64 reads a size figure ("10,036,969,472"); -1 if it won't parse.
func count64(s string) int64 {
	n, err := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	if err != nil {
		return -1
	}
	return n
}
