package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	if i, stem := bestFile(getLucky(), files); i != 3 || stem != "Get Lucky (feat. Pharrell Williams & Nile Rodgers)" {
		t.Errorf("Get Lucky -> %d %q", i, stem)
	}
	dance := track("Lose Yourself to Dance", []string{"Daft Punk"}, "Random Access Memories", 353)
	if i, _ := bestFile(dance, files); i != 2 {
		t.Errorf("Lose Yourself to Dance -> %d", i)
	}
	if i, _ := bestFile(track("Contact", []string{"Daft Punk"}, "Random Access Memories", 381), files); i != -1 {
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
	ranked := rankTorrents(results, "Daft Punk", "Random Access Memories", true)
	if len(ranked) != 3 || !strings.Contains(ranked[0].Title, "FLAC") || !strings.Contains(ranked[2].Title, "Disco") {
		t.Errorf("lossless ranking: %+v", ranked)
	}
	if ranked := rankTorrents(results, "Daft Punk", "Random Access Memories", false); !strings.Contains(ranked[0].Title, "MP3") {
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
		prowlarr.URL, jackett.URL + "/", "http://127.0.0.1:9117/api/v2.0/indexers/rutracker/results/torznab/"}})
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
	if _, err := resolveFeeds(context.Background(), TorrentConfig{Key: "wrong", Feeds: []string{prowlarr.URL}}); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("bad key: %v", err)
	}
}

func TestTorrentSettingsAreRemembered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TORZNAB_URL", "")
	cfg, err := torrentSettings([]string{"http://127.0.0.1:9696"}, "secret")
	if err != nil || !cfg.configured() {
		t.Fatal(cfg, err)
	}
	if info, err := os.Stat(torrentConfigPath()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved settings: %v %v", info, err)
	}
	if cfg, _ := torrentSettings(nil, ""); cfg.Key != "secret" || cfg.Feeds[0] != "http://127.0.0.1:9696" {
		t.Errorf("not remembered: %+v", cfg)
	}
	if cfg, _ := torrentSettings([]string{"none"}, ""); cfg.configured() {
		t.Errorf("not forgotten: %+v", cfg)
	}
	if cfg, _ := torrentSettings(nil, ""); cfg.configured() {
		t.Errorf("forgotten settings came back: %+v", cfg)
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
	tools := needFFmpeg(t)
	name := "Daft Punk - Random Access Memories (2013) [MP3]"
	files := []string{"01 - Give Life Back to Music.mp3", "08 - Get Lucky.mp3"}
	tone := toneBytes(t, 4)
	srv := fakeIndexer(t, name, files, [][]byte{tone, tone}, nil)
	defer srv.Close()

	seed := t.TempDir()
	os.MkdirAll(filepath.Join(seed, name), 0o755)
	for _, f := range files {
		os.WriteFile(filepath.Join(seed, name, f), tone, 0o644)
	}
	fake := filepath.Join(t.TempDir(), "aria2c")
	script := `#!/bin/sh
dir=""; sel=""
for a in "$@"; do case "$a" in --dir=*) dir="${a#--dir=}";; --select-file=*) sel="${a#--select-file=}";; esac; done
i=0
for f in "01 - Give Life Back to Music.mp3" "08 - Get Lucky.mp3"; do
  i=$((i+1))
  if [ "$sel" = "$i" ]; then mkdir -p "$dir/` + name + `"; cp "` + seed + `/` + name + `/$f" "$dir/` + name + `/$f"; fi
done
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	feeds, err := resolveFeeds(context.Background(), TorrentConfig{Feeds: []string{srv.URL}, Key: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	src := newTorrentSource(feeds, fake, t.TempDir(), "mp3", time.Minute)
	lucky := getLucky()
	lucky.DurationMs = 4000
	contact := track("Contact", []string{"Daft Punk"}, "Random Access Memories", 4)
	e, state, music := testEngine(t, tools, []Source{src}, nil)
	out := runAll(e, testLibrary(lucky, contact))

	if out[lucky.Key].Status != "downloaded" {
		t.Fatalf("Get Lucky: %+v / %+v", out[lucky.Key], state.get(lucky.Key))
	}
	r := state.get(lucky.Key)
	if r.Source != "torrent" || r.SourceURL != "https://tracker/t/1" || r.SourceAlbum != name || r.Path != "Daft Punk/Random Access Memories/08 - Get Lucky.mp3" {
		t.Errorf("record %+v", r)
	}
	if _, err := os.Stat(filepath.Join(music, filepath.FromSlash(r.Path))); err != nil {
		t.Error(err)
	}
	if rc := state.get(contact.Key); rc.Status != "failed" || !strings.Contains(rc.Reason, "don't contain this track") {
		t.Errorf("Contact: %+v", rc)
	}
	rep := buildReport(testLibrary(lucky, contact), state.all(), music)
	reports, _ := writeReports(rep, filepath.Join(music, "reports"))
	data, _ := os.ReadFile(reports[1])
	if !strings.Contains(string(data), `[Torrent: Daft Punk - Random Access Memories (2013) \[MP3\]](https://tracker/t/1)`) {
		t.Errorf("downloaded.md:\n%s", data)
	}
}

// TestRealAria2cDownload uses the real aria2c (when installed) on a torrent served by a local
// web seed, checking that only the selected file is fetched, correctly.
func TestRealAria2cDownload(t *testing.T) {
	aria2c, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	name := "Test Artist - Test Album"
	files := []string{"01 - First Song.mp3", "02 - Second Song.mp3", "03 - Third Song.mp3"}
	contents := [][]byte{bytes.Repeat([]byte("a"), 40000), bytes.Repeat([]byte("b"), 50000), bytes.Repeat([]byte("c"), 30000)}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, name), 0o755)
	for i, f := range files {
		os.WriteFile(filepath.Join(root, name, f), contents[i], 0o644)
	}
	webseed := httptest.NewServer(http.FileServer(http.Dir(root)))
	defer webseed.Close()
	torrentFile := filepath.Join(t.TempDir(), "album.torrent")
	os.WriteFile(torrentFile, makeTorrent(name, files, contents, webseed.URL+"/"), 0o644)

	src := newTorrentSource(nil, aria2c, t.TempDir(), "mp3", 2*time.Minute)
	workdir := t.TempDir()
	got, err := src.Download(context.Background(), Candidate{Ref: torrentFile + "#1"}, getLucky(), workdir)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(got)
	if !bytes.Equal(data, contents[1]) {
		t.Fatalf("got %d bytes from %s, want the second file", len(data), got)
	}
	for _, other := range []string{files[0], files[2]} {
		if _, err := os.Stat(filepath.Join(workdir, "torrent", name, other)); err == nil {
			t.Errorf("%s was kept although it wasn't selected", other)
		}
	}
}

func TestCheckTorrents(t *testing.T) {
	name := "Daft Punk - Random Access Memories (2013) [MP3]"
	srv := fakeIndexer(t, name, []string{"01 - Give Life Back to Music.mp3", "cover.jpg"}, nil, nil)
	defer srv.Close()
	fake := filepath.Join(t.TempDir(), "aria2c")
	os.WriteFile(fake, []byte("#!/bin/sh\necho 'aria2 version 1.37.0'\n"), 0o755)
	ctx := context.Background()
	if !checkTorrents(ctx, TorrentConfig{Feeds: []string{srv.URL}, Key: "secret"}, fake, "Daft Punk Random Access Memories") {
		t.Error("a working setup should pass")
	}
	if checkTorrents(ctx, TorrentConfig{Feeds: []string{srv.URL}, Key: "wrong"}, fake, "Daft Punk Random Access Memories") {
		t.Error("a wrong API key should fail")
	}
	if checkTorrents(ctx, TorrentConfig{Feeds: []string{srv.URL}, Key: "secret"}, "", "Daft Punk Random Access Memories") {
		t.Error("missing aria2c should fail")
	}
	if checkTorrents(ctx, TorrentConfig{}, fake, "x") {
		t.Error("no configuration should fail")
	}
}
