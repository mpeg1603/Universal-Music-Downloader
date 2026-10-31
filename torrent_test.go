package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

func makeTorrent(name string, files []string) []byte {
	var list []any
	for _, f := range files {
		var parts []any
		for _, p := range strings.Split(f, "/") {
			parts = append(parts, p)
		}
		list = append(list, map[string]any{"length": 1, "path": parts})
	}
	return []byte(bencode(map[string]any{"announce": "http://tracker.invalid/announce",
		"info": map[string]any{"name": name, "piece length": 16384, "pieces": "", "files": list}}))
}

var albumFiles = []string{
	"cover.jpg",
	"CD1/01 - Give Life Back to Music.flac",
	"CD1/07 - Lose Yourself to Dance.flac",
	"CD1/08 - Get Lucky (feat. Pharrell Williams & Nile Rodgers).flac",
	"CD1/08 - Get Lucky (Live).flac",
}

func TestTorrentFilesAndMatching(t *testing.T) {
	files, err := torrentFiles(makeTorrent("Daft Punk - Random Access Memories (2013) [FLAC]", albumFiles))
	if err != nil {
		t.Fatal(err)
	}
	if files[3] != "Daft Punk - Random Access Memories (2013) [FLAC]/CD1/08 - Get Lucky (feat. Pharrell Williams & Nile Rodgers).flac" {
		t.Fatalf("files: %q", files)
	}
	lucky := getLucky()
	dance := track("Lose Yourself to Dance", []string{"Daft Punk"}, "Random Access Memories", 353)
	dance.TrackNumber = 7
	absent := track("Contact", []string{"Daft Punk"}, "Random Access Memories", 381)
	m := matchTorrentFiles([]*Track{lucky, dance, absent}, files)
	if m[lucky.Key] != 3 || m[dance.Key] != 2 {
		t.Errorf("mapping %v", m)
	}
	if _, ok := m[absent.Key]; ok {
		t.Errorf("Contact isn't in the torrent: %v", m)
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
<item><title>Various Artists - Disco Hits</title><link>http://x/4.torrent</link><torznab:attr name="seeders" value="99"/></item>
</channel></rss>`
	results, err := parseTorznab([]byte(feed))
	if err != nil || len(results) != 4 || results[0].Details != "https://tracker/details/1" || results[1].Magnet == "" {
		t.Fatalf("parsed %+v (%v)", results, err)
	}
	ranked := rankTorrents(results, "Daft Punk", "Random Access Memories", true)
	if len(ranked) != 2 || !strings.Contains(ranked[0].Title, "FLAC") {
		t.Errorf("lossless ranking: %+v", ranked)
	}
	if ranked := rankTorrents(results, "Daft Punk", "Random Access Memories", false); !strings.Contains(ranked[0].Title, "MP3") {
		t.Errorf("lossy ranking: %+v", ranked)
	}
	if _, err := parseTorznab([]byte(`<error code="100" description="Invalid API Key"/>`)); err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error response: %v", err)
	}
}

// TestTorrentPass runs the torrent fallback end to end against a fake Torznab feed and a
// stand-in for aria2c that "downloads" the selected files from a local folder.
func TestTorrentPass(t *testing.T) {
	tools := needFFmpeg(t)
	name := "Daft Punk - Random Access Memories (2013) [MP3]"
	files := []string{"01 - Give Life Back to Music.mp3", "08 - Get Lucky.mp3"}
	torrent := makeTorrent(name, files)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			if r.URL.Query().Get("apikey") != "secret" || !strings.Contains(r.URL.Query().Get("q"), "Random Access Memories") {
				fmt.Fprint(w, `<error code="100" description="bad request"/>`)
				return
			}
			fmt.Fprintf(w, `<rss><channel><item><title>%s</title><link>http://%s/dl/1.torrent</link><guid>https://tracker/t/1</guid>
<attr name="seeders" value="5"/></item></channel></rss>`, name, r.Host)
		case "/dl/1.torrent":
			w.Write(torrent)
		}
	}))
	defer srv.Close()

	seed := t.TempDir()
	os.MkdirAll(filepath.Join(seed, name), 0o755)
	for _, f := range files {
		sine(t, filepath.Join(seed, name, f), 4)
	}
	fake := filepath.Join(t.TempDir(), "aria2c")
	script := `#!/bin/sh
dir=""; sel=""
for a in "$@"; do case "$a" in --dir=*) dir="${a#--dir=}";; --select-file=*) sel="${a#--select-file=}";; esac; done
i=0
for f in "01 - Give Life Back to Music.mp3" "08 - Get Lucky.mp3"; do
  i=$((i+1))
  case ",$sel," in *",$i,"*) mkdir -p "$dir/` + name + `"; cp "` + seed + `/` + name + `/$f" "$dir/` + name + `/$f";; esac
done
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools.Aria2c = fake

	lucky := getLucky()
	lucky.DurationMs = 4000
	missing := track("Contact", []string{"Daft Punk"}, "Random Access Memories", 4)
	e, state, music := testEngine(t, tools, nil, nil)
	fetcher := newTorrentFetcher(srv.URL, "secret", tools, time.Minute)
	got := map[string]Outcome{}
	e.runTorrents(context.Background(), make(chan struct{}), fetcher, []*Track{lucky, missing}, func(o Outcome) { got[o.Track.Key] = o })

	if got[lucky.Key].Status != "downloaded" {
		t.Fatalf("outcomes: %+v / %+v", got[lucky.Key], state.get(lucky.Key))
	}
	r := state.get(lucky.Key)
	if r.Source != "torrent" || r.SourceURL != "https://tracker/t/1" || r.SourceTitle != name {
		t.Errorf("record %+v", r)
	}
	if _, err := os.Stat(filepath.Join(music, filepath.FromSlash(r.Path))); err != nil {
		t.Error(err)
	}
	if got[missing.Key].Status != "failed" || !strings.Contains(state.get(missing.Key).Reason, "Torrent: not in") {
		t.Errorf("missing track: %+v %+v", got[missing.Key], state.get(missing.Key))
	}
}
