package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bencode is a tiny encoder for building test torrents.
func bencode(v any) string {
	switch x := v.(type) {
	case int:
		return fmt.Sprintf("i%de", x)
	case string:
		return fmt.Sprintf("%d:%s", len(x), x)
	case []any:
		s := "l"
		for _, e := range x {
			s += bencode(e)
		}
		return s + "e"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s := "d"
		for _, k := range keys {
			s += bencode(k) + bencode(x[k])
		}
		return s + "e"
	}
	panic("unsupported")
}

// makeTorrent builds a valid multi-file torrent for the given contents. With webseed set,
// clients can download it over HTTP from webseed/<name>/<path> (BEP 19), no peers needed.
func makeTorrent(name string, files []string, contents [][]byte, webseed string) []byte {
	const pieceLen = 16384
	var all bytes.Buffer
	var list []any
	for i, f := range files {
		var data []byte
		if contents != nil {
			data = contents[i]
		} else {
			data = []byte(f)
		}
		all.Write(data)
		var parts []any
		for _, p := range strings.Split(f, "/") {
			parts = append(parts, p)
		}
		list = append(list, map[string]any{"length": len(data), "path": parts})
	}
	var pieces bytes.Buffer
	for b := all.Bytes(); len(b) > 0; {
		n := min(pieceLen, len(b))
		sum := sha1.Sum(b[:n])
		pieces.Write(sum[:])
		b = b[n:]
	}
	top := map[string]any{"info": map[string]any{"name": name, "piece length": pieceLen, "pieces": pieces.String(), "files": list}}
	if webseed != "" {
		top["url-list"] = webseed
	}
	return []byte(bencode(top))
}

var albumFiles = []string{
	"cover.jpg",
	"CD1/01 - Give Life Back to Music.flac",
	"CD1/07 - Lose Yourself to Dance.flac",
	"CD1/08 - Get Lucky (feat. Pharrell Williams & Nile Rodgers).flac",
	"CD1/08 - Get Lucky (Live).flac",
}

func TestTorrentFilesAndBestFile(t *testing.T) {
	files, err := torrentFiles(makeTorrent("Daft Punk - Random Access Memories (2013) [FLAC]", albumFiles, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if files[3] != "Daft Punk - Random Access Memories (2013) [FLAC]/CD1/08 - Get Lucky (feat. Pharrell Williams & Nile Rodgers).flac" {
		t.Fatalf("files: %q", files)
	}
	if i, stem := bestFile(GetLucky(), files); i != 3 || stem != "Get Lucky (feat. Pharrell Williams & Nile Rodgers)" {
		t.Errorf("Get Lucky -> %d %q", i, stem)
	}
	dance := MakeTrack("Lose Yourself to Dance", []string{"Daft Punk"}, "Random Access Memories", 353)
	if i, _ := bestFile(dance, files); i != 2 {
		t.Errorf("Lose Yourself to Dance -> %d", i)
	}
	if i, _ := bestFile(MakeTrack("Contact", []string{"Daft Punk"}, "Random Access Memories", 381), files); i != -1 {
		t.Errorf("Contact isn't in the torrent, got %d", i)
	}
	if _, err := torrentFiles([]byte("not a torrent")); err == nil {
		t.Error("expected an error")
	}
}

func TestParseAndRankTorznab(t *testing.T) {
	feed := `<?xml version="1.0"?><rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Daft Punk - Random Access Memories (2013) [MP3 320]</title><link>http://x/1.torrent</link><comments>https://tracker/details/1</comments>
 <torznab:attr name="seeders" value="40"/></item>
<item><title>Daft Punk - Random Access Memories (2013) [FLAC]</title><link>http://x/2.torrent</link>
 <torznab:attr name="seeders" value="12"/><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:abc"/></item>
<item><title>Daft Punk - Random Access Memories [dead]</title><link>http://x/3.torrent</link><torznab:attr name="seeders" value="0"/></item>
<item><title>Various Artists - Disco Hits</title><link>magnet:?xt=urn:btih:def</link><torznab:attr name="seeders" value="99"/></item>
</channel></rss>`
	results, err := parseTorznab([]byte(feed))
	if err != nil || len(results) != 4 || results[0].Details != "https://tracker/details/1" || results[1].Magnet == "" || results[3].Magnet == "" {
		t.Fatalf("parsed %+v (%v)", results, err)
	}
	ram := torrentQuery{targets: []string{"Random Access Memories"}}
	ranked := rankTorrents(results, "Daft Punk", ram, true)
	// The dead torrent is dropped, and so is the one that fits neither artist nor album.
	if len(ranked) != 2 || !strings.Contains(ranked[0].Title, "FLAC") || !strings.Contains(ranked[1].Title, "MP3") {
		t.Errorf("lossless ranking: %+v", ranked)
	}
	if ranked := rankTorrents(results, "Daft Punk", ram, false); !strings.Contains(ranked[0].Title, "MP3") {
		t.Errorf("lossy ranking: %+v", ranked)
	}
	if _, err := parseTorznab([]byte(`<error code="100" description="Invalid API Key"/>`)); err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error response: %v", err)
	}
}

func TestResolveFeeds(t *testing.T) {
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != "pkey" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `[{"id":1,"name":"Music Tracker","enable":true,"protocol":"torrent","supportsSearch":true},
			{"id":2,"name":"Disabled","enable":false,"protocol":"torrent"},
			{"id":3,"name":"Some Usenet","enable":true,"protocol":"usenet"}]`)
	}))
	defer prowlarr.Close()
	jackett := httptest.NewServer(http.NotFoundHandler())
	defer jackett.Close()

	feeds, err := resolveFeeds(context.Background(), TorrentConfig{Key: "pkey", Feeds: []string{
		prowlarr.URL, jackett.URL + "/", "http://127.0.0.1:9117/api/v2.0/indexers/rutracker/results/torznab/"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range feeds {
		got = append(got, f.Name+" = "+strings.TrimPrefix(strings.TrimPrefix(f.feed, prowlarr.URL), jackett.URL))
	}
	want := []string{
		"Prowlarr: Music Tracker = /1/api",
		"Jackett (all indexers) = /api/v2.0/indexers/all/results/torznab/api",
		"127.0.0.1:9117/api/v2.0/indexers/rutracker/results/torznab/ = http://127.0.0.1:9117/api/v2.0/indexers/rutracker/results/torznab/api",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("feeds:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(feeds[0].url(nil), "apikey=pkey") {
		t.Errorf("API key not added: %s", feeds[0].url(nil))
	}
	if _, err := resolveFeeds(context.Background(), TorrentConfig{Key: "wrong", Feeds: []string{prowlarr.URL}}, 0); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("bad key: %v", err)
	}
}

func TestConfigMigratesTorrentSettingsAndIsPrivate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveTorrentConfig(TorrentConfig{Feeds: []string{"http://127.0.0.1:9117"}, Key: "secret"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	cfg, err := LoadConfig(path)
	if err != nil || cfg.Torznab.Key != "secret" || !cfg.Source("torrent").Enabled {
		t.Fatalf("migrated: %+v %v", cfg.Torznab, err)
	}
	cfg.Sources = cfg.Sources[:2] // an older config that doesn't know the newer sources
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved settings: %v %v", info, err)
	}
	again, err := LoadConfig(path)
	if err != nil || len(again.Sources) != len(allSources) || again.Source("vk").Route != RouteDirect {
		t.Errorf("reloaded: %+v %v", again.Sources, err)
	}
}

// fakeIndexer serves a Torznab feed with one album torrent; each file's content is a
// short test tone so the download can be converted.
func fakeIndexer(t *testing.T, name string, files []string, contents [][]byte, webseed func() string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/indexer":
			http.NotFound(w, r) // not Prowlarr: treated as Jackett
		case strings.HasSuffix(r.URL.Path, "/torznab/api"):
			if r.URL.Query().Get("apikey") != "secret" {
				fmt.Fprint(w, `<error code="100" description="Invalid API Key"/>`)
				return
			}
			if r.URL.Query().Get("t") == "caps" {
				fmt.Fprint(w, `<caps><searching><search available="yes" supportedParams="q"/></searching></caps>`)
				return
			}
			if !strings.Contains(r.URL.Query().Get("q"), "Random Access Memories") {
				fmt.Fprint(w, `<rss><channel></channel></rss>`)
				return
			}
			fmt.Fprintf(w, `<rss><channel><item><title>%s</title><link>%s/dl/1.torrent</link><guid>https://tracker/t/1</guid>
<attr name="seeders" value="5"/></item></channel></rss>`, name, srv.URL)
		case r.URL.Path == "/dl/1.torrent":
			seed := ""
			if webseed != nil {
				seed = webseed()
			}
			w.Write(makeTorrent(name, files, contents, seed))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func toneBytes(t *testing.T, seconds float64) []byte {
	p := sine(t, filepath.Join(t.TempDir(), "tone.mp3"), seconds)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestTorrentSourceInEngine runs the torrent source through the engine like any other source,
// with a stand-in for aria2c that "downloads" the selected file from a local folder.
func TestTorrentSourceInEngine(t *testing.T) {
	tools := NeedFFmpeg(t)
	name := "Daft Punk - Random Access Memories (2013) [MP3]"
	files := []string{"01 - Give Life Back to Music.mp3", "08 - Get Lucky.mp3"}
	tone := toneBytes(t, 4)
	srv := fakeIndexer(t, name, files, [][]byte{tone, tone}, nil)
	defer srv.Close()

	feeds, err := resolveFeeds(context.Background(), TorrentConfig{Feeds: []string{srv.URL}, Key: "secret"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := newTorrentSource(feeds, t.TempDir(), "mp3", time.Minute)
	fetcher := &fakeFetcher{content: tone}
	src.dl = fetcher
	lucky := GetLucky()
	lucky.DurationMs = 4000
	contact := MakeTrack("Contact", []string{"Daft Punk"}, "Random Access Memories", 4)
	m := TestManager(t, tools, []Source{src}, TestLibrary(lucky, contact), nil)
	state, music := m.state, m.Config().OutputDir
	out := RunManager(t, m)

	if out[lucky.Key].State != JobDone {
		t.Fatalf("Get Lucky: %+v / %+v", out[lucky.Key], state.get(lucky.Key))
	}
	r := state.get(lucky.Key)
	if r.Source != "torrent" || r.SourceURL != "https://tracker/t/1" || r.SourceAlbum != name || r.Path != filepath.Join(music, "Daft Punk/Random Access Memories/08 - Get Lucky.mp3") {
		t.Errorf("record %+v", r)
	}
	if _, err := os.Stat(r.Path); err != nil {
		t.Error(err)
	}
	if fetcher.fetched != 1 {
		t.Errorf("%d downloads, want 1", fetcher.fetched)
	}
	if rc := state.get(contact.Key); rc.Status != "failed" || !strings.Contains(rc.Reason, "don't contain this track") {
		t.Errorf("Contact: %+v", rc)
	}
	views := map[string]JobView{}
	for _, v := range m.Jobs() {
		views[v.Key] = v
	}
	rep := buildReport(TestLibrary(lucky, contact), views)
	reports, _ := writeReports(rep, filepath.Join(music, "reports"))
	data, _ := os.ReadFile(reports[1])
	if !strings.Contains(string(data), `[Torrent: Daft Punk - Random Access Memories (2013) \[MP3\]](https://tracker/t/1)`) {
		t.Errorf("downloaded.md:\n%s", data)
	}
}

// fakeFetcher stands in for aria2c: it "downloads" a file by writing content.
type fakeFetcher struct {
	content []byte
	fail    func(file string) error // the error for a file (path in the torrent), if any
	onFetch func(file string)

	mu       sync.Mutex
	fetched  int
	failures int
	expected []string // files that tracks were said to be on their way for
}

func (f *fakeFetcher) fetch(ctx context.Context, torrentFile string, index int, dest string, task *Task) (string, error) {
	data, err := os.ReadFile(torrentFile)
	if err != nil {
		return "", err
	}
	files, err := torrentFiles(data)
	if err != nil {
		return "", err
	}
	if f.onFetch != nil {
		f.onFetch(files[index])
	}
	if f.fail != nil {
		if err := f.fail(files[index]); err != nil {
			f.mu.Lock()
			f.failures++
			f.mu.Unlock()
			return "", err
		}
	}
	f.mu.Lock()
	f.fetched++
	f.mu.Unlock()
	os.MkdirAll(dest, 0o755)
	p := filepath.Join(dest, path.Base(files[index]))
	return p, os.WriteFile(p, f.content, 0o644)
}

func (f *fakeFetcher) expect(torrentFile string, index int) {
	data, _ := os.ReadFile(torrentFile)
	files, _ := torrentFiles(data)
	f.mu.Lock()
	f.expected = append(f.expected, files[index])
	f.mu.Unlock()
}

func (f *fakeFetcher) unexpect(torrentFile string, index int) {}

func (f *fakeFetcher) close() {}

// --- tracks fetched along with another track's torrent ---------------------------------------------

type testTorrent struct {
	name    string
	files   []string
	seeders int
}

// albumIndexer serves a Torznab feed that answers the searches containing match with
// torrents (every file's content being content), and notes every search.
func albumIndexer(t *testing.T, match string, torrents []testTorrent, content []byte) (*httptest.Server, func() []string) {
	var mu sync.Mutex
	var queries []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/torznab/api") && q.Get("t") == "search":
			mu.Lock()
			queries = append(queries, q.Get("q"))
			mu.Unlock()
			fmt.Fprint(w, "<rss><channel>")
			if strings.Contains(q.Get("q"), match) {
				for i, tt := range torrents {
					fmt.Fprintf(w, `<item><title>%s</title><link>%s/dl/%d.torrent</link><guid>https://tracker/t/%d</guid><attr name="seeders" value="%d"/></item>`,
						tt.name, srv.URL, i, i, tt.seeders)
				}
			}
			fmt.Fprint(w, "</channel></rss>")
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			var i int
			fmt.Sscanf(path.Base(r.URL.Path), "%d.torrent", &i)
			tt := torrents[i]
			contents := make([][]byte, len(tt.files))
			for k := range contents {
				contents[k] = content
			}
			w.Write(makeTorrent(tt.name, tt.files, contents, ""))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

// orderSource finds nothing; it notes the order tracks were searched in, and holds a track's
// search until its hold channel is closed.
type orderSource struct {
	hold  map[string]chan struct{}
	mu    sync.Mutex
	order []string
}

func (o *orderSource) Name() string  { return "first" }
func (o *orderSource) Label() string { return "First" }

func (o *orderSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	o.mu.Lock()
	o.order = append(o.order, tr.Key)
	o.mu.Unlock()
	if ch := o.hold[tr.Key]; ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func (o *orderSource) Download(ctx context.Context, c Candidate, tr *Track, workdir string) (string, error) {
	return "", failf("nothing to download")
}

func (o *orderSource) searched() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.order...)
}

func numbered(title, artist, album string, n int) *Track {
	tr := MakeTrack(title, []string{artist}, album, 4)
	tr.TrackNumber = n
	return tr
}

func torrentSourceFor(t *testing.T, srv *httptest.Server, format string, f torrentFetcher) *TorrentSource {
	feeds, err := resolveFeeds(context.Background(), TorrentConfig{Feeds: []string{srv.URL}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	src := newTorrentSource(feeds, t.TempDir(), format, time.Minute)
	src.dl = f
	return src
}

func TestTracksFoundAlongInATorrent(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, files []string) *torrentMeta {
		p := filepath.Join(dir, fmt.Sprintf("%x.torrent", sha1.Sum([]byte(name))))
		data := makeTorrent(name, files, nil, "")
		os.WriteFile(p, data, 0o644)
		names, sizes, err := torrentFileList(data)
		if err != nil {
			t.Fatal(err)
		}
		return &torrentMeta{file: p, files: names, sizes: sizes}
	}
	ram := "Daft Punk - Random Access Memories (2013) [FLAC]"
	meta := write(ram, albumFiles)
	lucky := GetLucky()
	c := torrentCandidate(lucky, ram, "https://tracker/t/1", meta, 3, "Get Lucky (feat. Pharrell Williams & Nile Rodgers)")
	give := numbered("Give Life Back to Music", "Daft Punk", "Random Access Memories", 1)
	dance := numbered("Lose Yourself to Dance", "Daft Punk", "Random Access Memories", 7)
	contact := numbered("Contact", "Daft Punk", "Random Access Memories", 13)
	cover := numbered("Get Lucky", "Someone Else", "Covers", 8)
	src := newTorrentSource(nil, t.TempDir(), "flac", time.Minute)
	got := src.alongside(c, lucky, []*Track{give, dance, contact, cover})
	if len(got) != 2 || got[give.Key].Ref != meta.file+"#1" || got[dance.Key].Ref != meta.file+"#2" {
		t.Fatalf("found along: %+v", got)
	}
	if g := got[give.Key]; !g.Unsearched || g.Album != ram || g.URL != "https://tracker/t/1" || !g.Quality.Lossless {
		t.Errorf("candidate: %+v", g)
	}

	// From a lossy discography, a lossless search only takes the same album's tracks: the
	// others may find a lossless copy of their own.
	disco := "Daft Punk - Discography (1997-2013) [MP3]"
	meta = write(disco, []string{"Random Access Memories/08 - Get Lucky.mp3", "Random Access Memories/01 - Give Life Back to Music.mp3", "Discovery/01 - One More Time.mp3"})
	c = torrentCandidate(lucky, disco, "", meta, 0, "Get Lucky")
	omt := numbered("One More Time", "Daft Punk", "Discovery", 1)
	if got := src.alongside(c, lucky, []*Track{give, omt}); len(got) != 1 || got[give.Key].Ref != meta.file+"#1" {
		t.Errorf("lossless: %+v", got)
	}
	mp3 := newTorrentSource(nil, t.TempDir(), "mp3", time.Minute)
	if got := mp3.alongside(c, lucky, []*Track{give, omt}); len(got) != 2 || got[omt.Key].Ref != meta.file+"#2" {
		t.Errorf("mp3: %+v", got)
	}

	// In a huge torrent only some tracks are looked for, those of the same album first.
	big := []string{"Random Access Memories/01 - Give Life Back to Music.mp3", "Random Access Memories/08 - Get Lucky.mp3"}
	var others []*Track
	for i := range 4000 {
		big = append(big, fmt.Sprintf("Album %d/%02d - Song %d.mp3", i/20, i%20+1, i))
		if i < 20 {
			others = append(others, numbered(fmt.Sprintf("Song %d", i), "Daft Punk", "Album 0", i+1))
		}
	}
	meta = write("Daft Punk - Everything [MP3]", big)
	c = torrentCandidate(lucky, "Daft Punk - Everything [MP3]", "", meta, 1, "Get Lucky")
	got = mp3.alongside(c, lucky, append(others, give))
	if _, ok := got[give.Key]; !ok || len(got) != alongBudget/len(big) {
		t.Errorf("huge torrent: %d found (Give Life Back to Music: %v), want %d", len(got), ok, alongBudget/len(big))
	}
}

// TestQueuedTracksGoAlong: when a torrent is about to be downloaded for one track, the other
// tracks still to do that it has move to the front of the queue and skip the torrent search,
// so they get there while it downloads. One More Time is only there that way: its own
// searches find nothing on this indexer.
func TestQueuedTracksGoAlong(t *testing.T) {
	tools := NeedFFmpeg(t)
	tone := toneBytes(t, 4)
	name := "Daft Punk - Discography (1997-2013) [MP3]"
	files := []string{"Random Access Memories/07 - Lose Yourself to Dance.mp3", "Random Access Memories/08 - Get Lucky.mp3", "Discovery/01 - One More Time.mp3"}
	srv, queries := albumIndexer(t, "Random Access Memories", []testTorrent{{name, files, 5}}, tone)
	fetcher := &fakeFetcher{content: tone}
	src := torrentSourceFor(t, srv, "mp3", fetcher)
	first := &orderSource{}

	lucky := numbered("Get Lucky", "Daft Punk", "Random Access Memories", 8)
	var fillers []*Track
	for i := 1; i <= 4; i++ {
		fillers = append(fillers, numbered(fmt.Sprintf("Filler %d", i), "Nobody", "Nothing", i))
	}
	contact := numbered("Contact", "Daft Punk", "Random Access Memories", 13)
	dance := numbered("Lose Yourself to Dance", "Daft Punk", "Random Access Memories", 7)
	omt := numbered("One More Time", "Daft Punk", "Discovery", 1)
	lib := TestLibrary(append(append([]*Track{lucky}, fillers...), contact, dance, omt)...)
	m := TestManager(t, tools, []Source{first, src}, lib, func(c *Config) { c.SearchWorkers = 1 })
	state := m.state
	out := RunManager(t, m)

	for _, tr := range []*Track{lucky, dance, omt} {
		if r := state.get(tr.Key); out[tr.Key].State != JobDone || r.Source != "torrent" || r.SourceAlbum != name {
			t.Errorf("%s: %+v / %+v", tr.Title, out[tr.Key], r)
		}
	}
	if out[contact.Key].State != JobFailed {
		t.Errorf("Contact isn't in the torrent: %+v", out[contact.Key])
	}
	for _, q := range queries() {
		if strings.Contains(q, "Discovery") || strings.Contains(q, "One More Time") || strings.Contains(q, "Lose Yourself") {
			t.Errorf("searched for %q", q)
		}
	}
	pos := map[string]int{}
	order := first.searched()
	for i, k := range order {
		pos[k] = i
	}
	last := fillers[len(fillers)-1].Key
	if pos[dance.Key] > pos[last] || pos[omt.Key] > pos[last] {
		t.Errorf("the tracks in the torrent didn't move up the queue: %v", order)
	}
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	sort.Strings(fetcher.expected)
	if want := []string{name + "/" + files[2], name + "/" + files[0]}; fmt.Sprint(fetcher.expected) != fmt.Sprint(want) {
		t.Errorf("the download was told to wait for %q, want %q", fetcher.expected, want)
	}
}

// TestFailedShortcutSearchesProperly: a track handed another track's torrent whose file then
// fails searches the torrent source properly instead of giving up on it.
func TestFailedShortcutSearchesProperly(t *testing.T) {
	tools := NeedFFmpeg(t)
	tone := toneBytes(t, 4)
	files := []string{"01 - Give Life Back to Music.mp3", "08 - Get Lucky.mp3"}
	a := testTorrent{"Daft Punk - Random Access Memories (2013) [MP3]", files, 50}
	b := testTorrent{"Daft Punk - Random Access Memories [MP3 320]", files, 5}
	srv, _ := albumIndexer(t, "Random Access Memories", []testTorrent{a, b}, tone)
	lucky := numbered("Get Lucky", "Daft Punk", "Random Access Memories", 8)
	give := numbered("Give Life Back to Music", "Daft Punk", "Random Access Memories", 1)
	// Give Life Back to Music waits in the first source until Get Lucky's download starts,
	// so it's handed that torrent before it gets to search it.
	release := make(chan struct{})
	var once sync.Once
	fetcher := &fakeFetcher{content: tone,
		onFetch: func(string) { once.Do(func() { close(release) }) },
		fail: func(file string) error {
			if file == a.name+"/"+files[0] {
				return failf("no peer sent any data for 3m (dead torrent?)")
			}
			return nil
		}}
	src := torrentSourceFor(t, srv, "mp3", fetcher)
	first := &orderSource{hold: map[string]chan struct{}{give.Key: release}}
	m := TestManager(t, tools, []Source{first, src}, TestLibrary(lucky, give), nil)
	state := m.state
	out := RunManager(t, m)

	if r := state.get(lucky.Key); r.SourceAlbum != a.name {
		t.Errorf("Get Lucky: %+v", r)
	}
	r := state.get(give.Key)
	if out[give.Key].State != JobDone || r.SourceAlbum != b.name {
		t.Fatalf("Give Life Back to Music: %+v / %+v", out[give.Key], r)
	}
	if reasons := strings.Join(out[give.Key].Reasons, "; "); !strings.Contains(reasons, "dead torrent") {
		t.Errorf("reasons: %s", reasons)
	}
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if fmt.Sprint(fetcher.expected) != fmt.Sprint([]string{a.name + "/" + files[0]}) {
		t.Errorf("Give Life Back to Music wasn't handed Get Lucky's torrent: %q", fetcher.expected)
	}
	if fetcher.failures != 1 {
		t.Errorf("the failed file was tried %d times, want 1", fetcher.failures)
	}
}

// throttled slows a web seed down so a download is still running when more files are added.
type throttled struct {
	http.ResponseWriter
	served *atomic.Int64
}

func (t throttled) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), 8192)
		time.Sleep(50 * time.Millisecond)
		m, err := t.ResponseWriter.Write(p[:k])
		n += m
		t.served.Add(int64(m))
		if err != nil {
			return n, err
		}
		p = p[k:]
	}
	return n, nil
}

// TestTorrentDownloads drives the built-in torrent client on a torrent served by a slow local
// web seed: two files requested together share one download, a third
// requested while it runs joins that download, and the file nobody wants isn't fetched.
func TestTorrentDownloads(t *testing.T) {
	name := "Test Artist - Test Album"
	files := []string{"01 - First Song.mp3", "02 - Second Song.mp3", "03 - Third Song.mp3", "04 - Fourth Song.mp3"}
	var contents [][]byte
	for i := range files {
		contents = append(contents, bytes.Repeat([]byte{byte('a' + i)}, 200_000+i*1000))
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, name), 0o755)
	for i, f := range files {
		os.WriteFile(filepath.Join(root, name, f), contents[i], 0o644)
	}
	served := map[string]*atomic.Int64{}
	for _, f := range files {
		served[f] = &atomic.Int64{}
	}
	webseed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter := served[path.Base(r.URL.Path)]
		if counter == nil {
			counter = &atomic.Int64{}
		}
		http.FileServer(http.Dir(root)).ServeHTTP(throttled{w, counter}, r)
	}))
	defer webseed.Close()
	torrentFile := filepath.Join(t.TempDir(), "album.torrent")
	os.WriteFile(torrentFile, makeTorrent(name, files, contents, webseed.URL+"/"), 0o644)

	downloads := t.TempDir()
	m := newTorrentManager(newBTEngine(), downloads, 2*time.Minute)
	m.gather = 300 * time.Millisecond
	defer m.close()
	type result struct {
		i    int
		path string
		err  error
	}
	results := make(chan result, 3)
	get := func(i int, dest string) {
		p, err := m.fetch(context.Background(), torrentFile, i, dest, nil)
		results <- result{i, p, err}
	}
	go get(0, t.TempDir())
	go get(2, t.TempDir())
	time.Sleep(1500 * time.Millisecond) // the download is running by now
	go get(1, t.TempDir())
	for range 3 {
		r := <-results
		if r.err != nil {
			t.Fatalf("file %d: %v", r.i, r.err)
		}
		data, _ := os.ReadFile(r.path)
		if !bytes.Equal(data, contents[r.i]) {
			t.Errorf("file %d: got %d bytes, want %d", r.i, len(data), len(contents[r.i]))
		}
	}
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if started != 1 {
		t.Errorf("%d downloads for one torrent, want 1", started)
	}
	if n := served[files[3]].Load(); n > 32768 {
		t.Errorf("%d bytes of the unwanted file were downloaded", n)
	}
	time.Sleep(2 * time.Second) // the poller notices the download is complete
	if left, _ := os.ReadDir(downloads); len(left) > 0 {
		t.Errorf("download folder not cleaned up: %v", left)
	}
}

// TestTorrentAddKeepsFinishedFiles adds a file to a running download after another file was
// finished and handed over: that file must be left alone.
func TestTorrentAddKeepsFinishedFiles(t *testing.T) {
	name := "Test Artist - Other Album"
	files := []string{"01 - Short.mp3", "02 - Long.mp3", "03 - Late.mp3"}
	contents := [][]byte{bytes.Repeat([]byte("s"), 40_000), bytes.Repeat([]byte("l"), 700_000), bytes.Repeat([]byte("t"), 60_000)}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, name), 0o755)
	for i, f := range files {
		os.WriteFile(filepath.Join(root, name, f), contents[i], 0o644)
	}
	var served atomic.Int64
	webseed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir(root)).ServeHTTP(throttled{w, &served}, r)
	}))
	defer webseed.Close()
	torrentFile := filepath.Join(t.TempDir(), "album.torrent")
	os.WriteFile(torrentFile, makeTorrent(name, files, contents, webseed.URL+"/"), 0o644)

	m := newTorrentManager(newBTEngine(), t.TempDir(), 2*time.Minute)
	m.gather = 200 * time.Millisecond
	defer m.close()
	ctx := context.Background()
	long := make(chan error, 1)
	go func() {
		p, err := m.fetch(ctx, torrentFile, 1, t.TempDir(), nil)
		if err == nil {
			if data, _ := os.ReadFile(p); !bytes.Equal(data, contents[1]) {
				err = fmt.Errorf("long file: %d bytes", len(data))
			}
		}
		long <- err
	}()
	short, err := m.fetch(ctx, torrentFile, 0, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(short)
	select {
	case err := <-long:
		t.Fatalf("the long file finished before the test could add one (%v); slow the web seed down", err)
	default:
	}
	late, err := m.fetch(ctx, torrentFile, 2, t.TempDir(), nil) // restarts the running download
	if err != nil {
		t.Fatal(err)
	}
	if err := <-long; err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(short)
	data, _ := os.ReadFile(short)
	if !bytes.Equal(data, contents[0]) || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the finished file was touched by the restart (%d bytes, modified %v -> %v)", len(data), before.ModTime(), after.ModTime())
	}
	if data, _ := os.ReadFile(late); !bytes.Equal(data, contents[2]) {
		t.Errorf("late file: %d bytes", len(data))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started != 1 {
		t.Errorf("%d downloads, want 1", m.started)
	}
}

// TestTorrentWaitsForTracksOnTheirWay: a download about to start waits for the files of tracks
// known to be coming (expect), so they go into it instead of restarting it later, and stops
// waiting as soon as the last of them arrives or drops out (unexpect).
func TestTorrentWaitsForTracksOnTheirWay(t *testing.T) {
	name := "Test Artist - Third Album"
	files := []string{"01 - One.mp3", "02 - Two.mp3", "03 - Three.mp3"}
	var contents [][]byte
	for i := range files {
		contents = append(contents, bytes.Repeat([]byte{byte('a' + i)}, 100_000+i*1000))
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, name), 0o755)
	for i, f := range files {
		os.WriteFile(filepath.Join(root, name, f), contents[i], 0o644)
	}
	webseed := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer webseed.Close()
	torrentFile := filepath.Join(t.TempDir(), "album.torrent")
	os.WriteFile(torrentFile, makeTorrent(name, files, contents, webseed.URL+"/"), 0o644)

	m := newTorrentManager(newBTEngine(), t.TempDir(), 2*time.Minute)
	m.gather, m.maxGather, m.along = 200*time.Millisecond, 600*time.Millisecond, 40*time.Second
	defer m.close()
	m.expect(torrentFile, 1)
	m.expect(torrentFile, 2)
	start := time.Now()
	type result struct {
		i   int
		err error
	}
	results := make(chan result, 2)
	get := func(i int) {
		p, err := m.fetch(context.Background(), torrentFile, i, t.TempDir(), nil)
		if err == nil {
			if data, _ := os.ReadFile(p); !bytes.Equal(data, contents[i]) {
				err = fmt.Errorf("got %d bytes, want %d", len(data), len(contents[i]))
			}
		}
		results <- result{i, err}
	}
	go get(0)
	time.Sleep(1500 * time.Millisecond) // well past the usual pause
	m.mu.Lock()
	early := m.started
	m.mu.Unlock()
	go get(1)
	time.Sleep(500 * time.Millisecond)
	m.unexpect(torrentFile, 2) // found elsewhere
	for range 2 {
		if r := <-results; r.err != nil {
			t.Fatalf("file %d: %v", r.i, r.err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if early != 0 || m.started != 1 || m.added != 0 {
		t.Errorf("started %d downloads before the expected track came, %d in all, added files %d times; want 0, 1, 0", early, m.started, m.added)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("took %s: still waited for the track that dropped out", took)
	}
}

func TestCheckTorrents(t *testing.T) {
	name := "Daft Punk - Random Access Memories (2013) [MP3]"
	srv := fakeIndexer(t, name, []string{"01 - Give Life Back to Music.mp3", "cover.jpg"}, nil, nil)
	defer srv.Close()
	ctx := context.Background()
	if !CheckTorrents(ctx, TorrentConfig{Feeds: []string{srv.URL}, Key: "secret"}, "Daft Punk Random Access Memories") {
		t.Error("a working setup should pass")
	}
	if CheckTorrents(ctx, TorrentConfig{Feeds: []string{srv.URL}, Key: "wrong"}, "Daft Punk Random Access Memories") {
		t.Error("a wrong API key should fail")
	}
	if CheckTorrents(ctx, TorrentConfig{}, "x") {
		t.Error("no configuration should fail")
	}
}
