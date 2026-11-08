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
