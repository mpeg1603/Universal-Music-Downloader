package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- YouTube cookies --------------------------------------------------------------------------

// fakeYtDlp writes a stand-in for yt-dlp that behaves like it where cookies are concerned:
// it exports a browser's cookies into the --cookies file, and otherwise rewrites the
// --cookies file when it exits (after saving what it was given to $FAKE_SEEN).
func fakeYtDlp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
cookies=""; browser=""
while [ $# -gt 0 ]; do
  case "$1" in
    --cookies) cookies="$2"; shift;;
    --cookies-from-browser) browser="$2"; shift;;
  esac
  shift
done
if [ -n "$browser" ]; then
  printf '# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tlogin\n.youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\tsap\n.example.com\tTRUE\t/\tTRUE\t0\tBANK\tsecret\n' > "$cookies"
  echo "Usage: yt-dlp [OPTIONS] URL" >&2
  exit 2
fi
if [ -n "$cookies" ]; then
  cp "$cookies" "$FAKE_SEEN"
  printf '# Netscape HTTP Cookie File\n# rotated away\n' > "$cookies"
fi
echo '{"entries": []}'
`
	p := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const loginCookies = "# Netscape HTTP Cookie File\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1900000000\tLOGIN_INFO\tabc\n" +
	"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1900000000\t__Secure-3PAPISID\tdef\n" +
	".github.com\tTRUE\t/\tTRUE\t1900000000\tlogged_in\tyes\n"

func TestYtDlpNeverWritesYourCookiesFile(t *testing.T) {
	bin := fakeYtDlp(t)
	dir := t.TempDir()
	mine := filepath.Join(dir, "cookies.txt")
	os.WriteFile(mine, []byte(loginCookies), 0o644)
	seen := filepath.Join(dir, "seen.txt")
	t.Setenv("FAKE_SEEN", seen)
	private := filepath.Join(dir, "private")
	y := newYtDlp(&Tools{YtDlp: bin}, newYtCookies(mine, "", bin, private), "")
	for i := 0; i < 3; i++ {
		if _, err := y.run(context.Background(), "--flat-playlist", "-J", "ytsearch1:x"); err != nil {
			t.Fatal(err)
		}
	}
	if data, _ := os.ReadFile(mine); string(data) != loginCookies {
		t.Errorf("your cookies file was changed:\n%s", data)
	}
	got, _ := os.ReadFile(seen)
	if !hasYouTubeLogin(got) || strings.Contains(string(got), "github") {
		t.Errorf("yt-dlp got:\n%s", got)
	}
	if left, _ := filepath.Glob(filepath.Join(private, "run-*")); len(left) > 0 {
		t.Errorf("copies left behind: %v", left)
	}
}

func TestBrowserCookiesSnapshot(t *testing.T) {
	bin := fakeYtDlp(t)
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.txt")
	t.Setenv("FAKE_SEEN", seen)
	c := newYtCookies("/does/not/matter", "chromium", bin, filepath.Join(dir, "private"))
	if problem := c.check(context.Background()); problem != "" {
		t.Fatalf("check: %s", problem)
	}
	y := newYtDlp(&Tools{YtDlp: bin}, c, "")
	if _, err := y.run(context.Background(), "-J", "x"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(seen)
	if !hasYouTubeLogin(got) || strings.Contains(string(got), "BANK") {
		t.Errorf("yt-dlp got:\n%s", got)
	}
	if !strings.Contains(c.describe(), "chromium") {
		t.Error(c.describe())
	}
}

func TestCookiesWithoutLoginAreReported(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "cookies.txt")
	// What the user's file looked like after yt-dlp had rewritten it for a while.
	os.WriteFile(stale, []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\t__Secure-3PAPISID\tx\n"+
		".youtube.com\tTRUE\t/\tTRUE\t0\tPREF\ty\n"), 0o644)
	c := newYtCookies(stale, "", "", filepath.Join(dir, "p"))
	if p := c.check(context.Background()); !strings.Contains(p, "no YouTube login") {
		t.Errorf("check: %q", p)
	}
	if a := c.botCheckAdvice(context.Background()); !strings.Contains(a, "choose your browser") {
		t.Errorf("advice: %q", a)
	}
	var none *ytCookies
	if a := none.botCheckAdvice(context.Background()); !strings.Contains(a, "browser where you're logged in") {
		t.Errorf("advice without cookies: %q", a)
	}
	if newYtCookies("", "", "", dir) != nil {
		t.Error("no cookies set should give none")
	}
}

// --- search queries ----------------------------------------------------------------------------

func TestSongQueries(t *testing.T) {
	for _, c := range []struct {
		tr   *Track
		want []string
	}{
		{&Track{Title: "Missing Out - The Midnight Remix", Artists: []string{"The Ivy", "The Midnight"}, Album: "Missing Out (The Midnight Remix)"},
			[]string{"The Ivy Missing Out The Midnight Remix", "The Ivy Missing Out", "Missing Out The Midnight"}},
		{&Track{Title: "Out of My Mind (feat. Sam Sparro)", Artists: []string{"Droid Bishop", "Sam Sparro"}, Album: "Bad Things", AlbumArtist: "Droid Bishop"},
			[]string{"Droid Bishop Out of My Mind", "Out of My Mind Sam Sparro", "Out of My Mind Bad Things"}},
		{&Track{Title: "Nightcall", Artists: []string{"Kavinsky"}, Album: "Drive (Original Motion Picture Soundtrack)", AlbumArtist: "Various Artists"},
			[]string{"Kavinsky Nightcall", "Nightcall Drive"}},
		{&Track{Title: "Here Comes the Sun - Remastered 2009", Artists: []string{"The Beatles"}, Album: "Abbey Road", AlbumArtist: "The Beatles"},
			[]string{"The Beatles Here Comes the Sun", "Here Comes the Sun Abbey Road"}},
		{&Track{Title: "Sing to Me Candy", Artists: []string{"Papercuts"}, Album: "Can't Go Back", AlbumArtist: "Jason Quever"},
			[]string{"Papercuts Sing to Me Candy", "Jason Quever Sing to Me Candy", "Sing to Me Candy Can't Go Back"}},
	} {
		got := songQueries(c.tr, 5)
		if strings.Join(got, " | ") != strings.Join(c.want, " | ") {
			t.Errorf("%s:\n got %q\nwant %q", c.tr, got, c.want)
		}
	}
	if got := songQueries(&Track{Title: "A", Artists: []string{"B"}, Album: "C", AlbumArtist: "D"}, 2); len(got) != 2 {
		t.Errorf("max: %q", got)
	}
}

func TestSearchUntilFound(t *testing.T) {
	tr := GetLucky()
	var asked []string
	search := func(q string) ([]Candidate, error) {
		asked = append(asked, q)
		switch q {
		case "first":
			return []Candidate{cand("Something Else", "Daft Punk", 248)}, nil
		case "second":
			return []Candidate{cand("Get Lucky", "Daft Punk", 248)}, nil
		}
		return nil, errors.New("not reached")
	}
	got, err := searchUntilFound(context.Background(), tr, []string{"first", "second", "third"}, search)
	if err != nil || len(got) != 2 || strings.Join(asked, ",") != "first,second" {
		t.Errorf("got %v %v, asked %v", got, err, asked)
	}
	// An error on the first search is the answer; on a later one, what was found before is.
	_, err = searchUntilFound(context.Background(), tr, []string{"x"}, func(string) ([]Candidate, error) { return nil, unavailablef("down") })
	if !isUnavailable(err) {
		t.Errorf("err %v", err)
	}
	got, err = searchUntilFound(context.Background(), tr, []string{"first", "x"}, func(q string) ([]Candidate, error) {
		if q == "x" {
			return nil, unavailablef("down")
		}
		return search(q)
	})
	if err != nil || len(got) != 1 {
		t.Errorf("later error: %v %v", got, err)
	}
}

func TestTorrentQueriesUseTheAlbumArtist(t *testing.T) {
	for _, c := range []struct {
		track *Track
		want  []string
	}{
		{&Track{Title: "Nightcall", Artists: []string{"Kavinsky"}, Album: "Drive (Original Motion Picture Soundtrack)", AlbumArtist: "Various Artists"},
			[]string{"Drive", "Kavinsky Nightcall", "Kavinsky"}},
		{&Track{Title: "Missing Out - The Midnight Remix", Artists: []string{"The Ivy", "The Midnight"}, Album: "Remixes, Vol. 1", AlbumArtist: "The Midnight"},
			[]string{"The Midnight Remixes Vol. 1", "The Ivy Missing Out", "The Midnight", "The Ivy"}},
	} {
		var got []string
		for _, q := range torrentQueries(c.track) {
			got = append(got, q.text)
		}
		if strings.Join(got, " | ") != strings.Join(c.want, " | ") {
			t.Errorf("%s:\n got %q\nwant %q", c.track, got, c.want)
		}
	}
	// A discography of the album artist counts for the track of another credited artist.
	tr := &Track{Title: "Missing Out - The Midnight Remix", Artists: []string{"The Ivy"}, Album: "Remixes, Vol. 1", AlbumArtist: "The Midnight"}
	src := &TorrentSource{lossless: true}
	if !src.considers(tr, "The Midnight - Discography (2012-2024) [FLAC]") {
		t.Error("the album artist's discography should be looked into")
	}
}

// --- Bandcamp pacing ---------------------------------------------------------------------------

func TestAdaptiveLimiter(t *testing.T) {
	r := newAdaptiveLimiter(10*time.Millisecond, 4*time.Second)
	r.slowDown()
	r.slowDown() // answer to a request that was already under way
	if r.interval != time.Second {
		t.Fatalf("after a 429: %s", r.interval)
	}
	for i := 0; i < 3; i++ {
		r.slowedAt = time.Time{} // an interval later
		r.slowDown()
	}
	if r.interval != 4*time.Second {
		t.Fatalf("capped: %s", r.interval)
	}
	for i := 0; i < 20; i++ {
		r.ok()
	}
	if r.interval != 3200*time.Millisecond {
		t.Fatalf("after 20 answers: %s", r.interval)
	}
	for i := 0; i < 20*40; i++ {
		r.ok()
	}
	if r.interval != 10*time.Millisecond {
		t.Fatalf("back to base: %s", r.interval)
	}
	fixed := &rateLimiter{interval: time.Millisecond}
	fixed.slowDown()
	if fixed.interval != time.Millisecond {
		t.Error("a fixed limiter changed")
	}
	// The backlog grows with requests waiting for their turn.
	r = newAdaptiveLimiter(time.Second, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3; i++ {
		go r.wait(ctx)
	}
	WaitFor(t, func() bool { return r.backlog() > 2*time.Second })
}

func TestHTTPSlowsDownOn429(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	h := newHTTP("Test", 0)
	h.limiter = newAdaptiveLimiter(0, 50*time.Millisecond)
	var out map[string]bool
	if err := h.getJSON(context.Background(), srv.URL, &out); err != nil || !out["ok"] {
		t.Fatalf("%v %v", out, err)
	}
	if h.limiter.interval != 50*time.Millisecond {
		t.Errorf("interval %s", h.limiter.interval)
	}
}

func TestTTLCache(t *testing.T) {
	c := &ttlCache[int]{ttl: time.Hour, max: 2}
	calls := 0
	f := func() (int, error) { calls++; return calls, nil }
	c.get("a", f)
	if v, _ := c.get("a", f); v != 1 || calls != 1 {
		t.Errorf("cached: %d %d", v, calls)
	}
	if _, err := c.get("e", func() (int, error) { return 0, errors.New("x") }); err == nil {
		t.Error("error lost")
	}
	if _, err := c.get("e", f); err != nil || calls != 2 {
		t.Error("errors aren't kept")
	}
	c.get("b", f) // over max: starts over
	if len(c.entries) > 2 {
		t.Errorf("%d entries", len(c.entries))
	}
}

func TestDeadTorrentsAreRemembered(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(404)
	}))
	defer srv.Close()
	s := newTorrentSource(nil, t.TempDir(), "flac", time.Minute)
	r := torrentResult{Title: "Dead Album", Link: srv.URL + "/dl/1", Details: "https://tracker/1"}
	if _, err := s.meta(context.Background(), r); err == nil {
		t.Fatal("no error")
	}
	// A new source (as after a restart) doesn't ask again.
	s2 := newTorrentSource(nil, filepath.Dir(s.dir), "flac", time.Minute)
	_, err := s2.meta(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "tried") || hits.Load() != 1 {
		t.Errorf("%v, %d requests", err, hits.Load())
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\n\n[#1 0B]\n\nerror: x\n", 2); got != "[#1 0B] / error: x" {
		t.Error(got)
	}
	if got := lastLines("", 2); got != "" {
		t.Error(got)
	}
}

// The library check's shortcut has to decide exactly what the full comparison decides.
func TestLibraryCheckAgreesWithEvaluate(t *testing.T) {
	track := func(title, artist, album string, ms int) *Track {
		return &Track{Key: title, Title: title, Artists: []string{artist}, Album: album, DurationMs: ms}
	}
	tracks := []*Track{track("Roygbiv", "Boards of Canada", "Music Has the Right to Children", 150000),
		track("Song (feat. Someone)", "An Artist", "Album", 200000), track("Song - Remastered 2011", "An Artist", "Album", 200000),
		track("Live Forever", "Oasis", "Definitely Maybe", 276000), track("Живи", "Кино", "Группа крови", 240000),
		track("One", "U2", "Achtung Baby", 276000), track("One", "Metallica", "...And Justice for All", 446000)}
	file := func(title, artist, albumArtist string, d float64) *LibFile {
		return &LibFile{Title: title, Artist: artist, AlbumArtist: albumArtist, Album: "Album", Duration: d}
	}
	files := []*LibFile{file("Roygbiv", "Boards of Canada", "", 150), file("ROYGBIV (Remastered)", "boards of canada", "", 151),
		file("Roygbiv (Live)", "Boards of Canada", "", 150), file("Roygbiv", "Boards Of Canada", "Various Artists", 300),
		file("Boards of Canada - Roygbiv", "Unknown", "Boards of Canada", 150), file("Song", "An Artist", "", 200), file("Song (Remix)", "An Artist", "", 200),
		file("Song", "Another Artist", "An Artist", 200), file("Live Forever", "Oasis", "", 276), file("Live Forever (Live)", "Oasis", "", 277),
		file("Живи", "Кино", "", 240), file("One", "U2", "", 276), file("One", "Metallica", "", 446), file("One", "", "U2", 0)}
	old := func(tr *Track, f *LibFile) bool {
		c := Candidate{Title: f.Title, Artist: FirstNonEmpty(f.Artist, f.AlbumArtist), Album: f.Album, Duration: f.Duration, ArtistIsCredit: true}
		m := evaluate(tr, c)
		if m.Reject != "" && f.AlbumArtist != "" && f.Artist != "" && strings.HasPrefix(m.Reject, "different artist") {
			c.Artist = f.AlbumArtist
			m = evaluate(tr, c)
		}
		return m.Reject == ""
	}
	accepted := 0
	for _, tr := range tracks {
		for _, f := range files {
			want := old(tr, f)
			if want {
				accepted++
			}
			if got := libMatches(tr, f, ""); got != want {
				t.Errorf("%q / %q: got %v, want %v", tr.Title, f.Title, got, want)
			}
		}
	}
	if accepted == 0 {
		t.Error("nothing matched at all")
	}
}

// The app's own files are kept in bounds: old ones go, then the oldest until the rest fit, and
// nothing that may be in use.
func TestPruneDir(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	put := func(name string, size int, age time.Duration) {
		p := filepath.Join(dir, name)
		os.WriteFile(p, make([]byte, size), 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	put("ancient", 10, 100*time.Hour)
	put("old", 40, 5*time.Hour)
	put("middle", 40, 3*time.Hour)
	put("recent", 40, time.Hour)
	put("in-use", 40, time.Minute)
	files, freed := pruneDir(pruneRule{dir: dir, maxAge: 72 * time.Hour, maxBytes: 120}, now)
	if files != 2 || freed != 50 {
		t.Errorf("%d files, %d bytes", files, freed)
	}
	for name, want := range map[string]bool{"ancient": false, "old": false, "middle": true, "recent": true, "in-use": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: present %v, want %v", name, err == nil, want)
		}
	}
	// Over the limit with only files in use: they all stay.
	put("a", 500, time.Minute)
	if files, _ := pruneDir(pruneRule{dir: dir, maxAge: 72 * time.Hour, maxBytes: 120}, now); files != 2 {
		t.Errorf("%d removed, want the 2 not in use", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err != nil {
		t.Error("removed a file in use")
	}
	if n, _ := pruneDir(pruneRule{dir: filepath.Join(dir, "missing"), maxAge: time.Hour, maxBytes: 1}, now); n != 0 {
		t.Error("a missing folder")
	}
}

// Clearing by hand: a kind or everything, never what may be in use, nor Sort & merge's while it runs.
func TestClearCache(t *testing.T) {
	m := &Manager{dataDir: t.TempDir()}
	old := time.Now().Add(-time.Hour)
	put := func(rel string, age time.Time) {
		p := filepath.Join(m.dataDir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("12345"), 0o644)
		os.Chtimes(p, age, age)
	}
	put("cache/bandcamp/a.zip", old)
	put("cache/bandcamp/in-use.zip", time.Now())
	put("cache/torrents/b.torrent", old)
	put("logs/1.log", old)
	put("logs/2.log", old.Add(time.Minute))
	put("sort-tags.jsonl", old)
	usage := map[string]CacheInfo{}
	for _, c := range m.CacheUsage() {
		usage[c.Name] = c
	}
	if c := usage["albums"]; c.Files != 2 || c.Bytes != 10 || c.Label == "" {
		t.Errorf("albums: %+v", c)
	}
	if files, freed, err := m.ClearCache("albums"); err != nil || files != 1 || freed != 5 {
		t.Errorf("albums cleared: %d, %d, %v", files, freed, err)
	}
	if !fileExists(filepath.Join(m.dataDir, "cache/bandcamp/in-use.zip")) || !fileExists(filepath.Join(m.dataDir, "cache/torrents/b.torrent")) {
		t.Error("removed what wasn't asked for, or something in use")
	}
	if _, _, err := m.ClearCache("nonsense"); err == nil {
		t.Error("an unknown kind was accepted")
	}
	m.sort.view.Busy = true
	if _, _, err := m.ClearCache("sort-tags"); err == nil || !fileExists(filepath.Join(m.dataDir, "sort-tags.jsonl")) {
		t.Errorf("cleared Sort & merge's files while it ran: %v", err)
	}
	m.sort.view.Busy = false
	if files, _, err := m.ClearCache(""); err != nil || files != 2 { // torrent, tags (the latest few logs stay)
		t.Errorf("everything: %d, %v", files, err)
	}
	if !fileExists(filepath.Join(m.dataDir, "logs/2.log")) {
		t.Error("removed the current log")
	}
}

func TestPlaylistFinished(t *testing.T) {
	for _, c := range []struct {
		s    PlaylistStatus
		want bool
	}{
		{PlaylistStatus{Enabled: true, Tracks: 5, Done: 3, InLib: 2}, true},
		{PlaylistStatus{Enabled: true, Tracks: 5, Done: 3, Failed: 2}, true},
		{PlaylistStatus{Enabled: true, Tracks: 5, Done: 3, Todo: 2}, false},
		{PlaylistStatus{Enabled: true, Tracks: 5, Failed: 5}, false},
		{PlaylistStatus{Enabled: false, Tracks: 5}, false}, // switched off: its songs have no jobs
		{PlaylistStatus{Enabled: true}, false},
	} {
		if got := c.s.Finished(); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}
