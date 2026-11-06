package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Tenth Anniversary Edition tracks of "Turn On The Bright Lights" weren't found: the album
// query carried the edition name, which no torrent title matches word for word.
func TestTorrentQueries(t *testing.T) {
	anniversary := &Track{Title: "Obstacle 1", Artists: []string{"Interpol"},
		Album: "Turn On The Bright Lights: The Tenth Anniversary Edition (Remastered)"}
	single := &Track{Title: "The Heinrich Maneuver - Radio Edit", Artists: []string{"Interpol"}, Album: "The Heinrich Maneuver"}
	slash := &Track{Title: "Get The Girls/Song 5 (First Demo)", Artists: []string{"TR/ST"}, Album: "Joyland"}
	for _, c := range []struct {
		track *Track
		want  []string
	}{
		{anniversary, []string{"Interpol Turn On The Bright Lights", "Interpol Obstacle 1", "Interpol"}},
		{single, []string{"Interpol The Heinrich Maneuver", "Interpol"}},
		{slash, []string{"TR ST Joyland", "TR ST Get The Girls Song 5", "TR ST"}},
	} {
		var got []string
		for _, q := range torrentQueries(c.track) {
			got = append(got, q.text)
		}
		if strings.Join(got, " | ") != strings.Join(c.want, " | ") {
			t.Errorf("%s:\n got %q\nwant %q", c.track, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"Random Access Memories (10th Anniversary Edition)": "Random Access Memories",
		"Hot Fuss - Deluxe Edition":                         "Hot Fuss",
		"Antics":                                            "Antics",
		"(Untitled)":                                        "(Untitled)",
	} {
		if got := baseAlbum(in); got != want {
			t.Errorf("baseAlbum(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRankTorrentsPrefersEditionAndTrackFiles(t *testing.T) {
	results := []torrentResult{
		{Title: "Interpol - Turn On The Bright Lights (2002) [FLAC (image+.cue)]", Link: "l1", Seeders: 30},
		{Title: "(Indie Rock) [LP] [24/96] Interpol - Turn on the Bright Lights - 2002, FLAC (tracks), lossless", Link: "l2", Seeders: 12},
		{Title: "Interpol - Turn On The Bright Lights - 2002, FLAC (tracks), lossless", Link: "l3", Seeders: 10},
		{Title: "Interpol - Turn On The Bright Lights (Tenth Anniversary Edition) 2012, FLAC (tracks)", Link: "l4", Seeders: 5},
		{Title: "Владислав Бобков - Мир Воителей 1, Целитель чудовищ (2026) MP3", Link: "l5", Seeders: 50},
		{Title: "Interpol - Discography (2002-2022) FLAC", Link: "l6", Seeders: 20},
		{Title: "Interpol - Turn On The Bright Lights [dead]", Link: "l7", Seeders: 0},
	}
	tr := &Track{Title: "PDA (First Demo)", Artists: []string{"Interpol"},
		Album: "Turn On The Bright Lights: The Tenth Anniversary Edition (Remastered)"}
	var got []string
	for _, r := range rankTorrents(results, "Interpol", torrentQueries(tr)[0], true) {
		got = append(got, r.Link)
	}
	if want := "l4 l3 l2 l1 l6"; strings.Join(got, " ") != want {
		t.Errorf("ranking %q, want %q", got, want)
	}
}

func TestParseTorznabDropsAudiobooks(t *testing.T) {
	feed := `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>Some Author - Some Book (2026) MP3</title><link>http://x/1</link><category>3030</category></item>
<item><title>Band - Album (2020) FLAC</title><link>http://x/2</link><torznab:attr name="category" value="3040"/></item>
<item><title>Band - Album (2020) MP3</title><link>http://x/3</link><category>3000</category><category>3030</category></item>
<item><title>Band - Album (2020) [tracker category]</title><link>http://x/4</link><category>100423</category></item>
</channel></rss>`
	results, err := parseTorznab([]byte(feed))
	if err != nil {
		t.Fatal(err)
	}
	var links []string
	for _, r := range results {
		links = append(links, r.Link)
	}
	if strings.Join(links, " ") != "http://x/2 http://x/3 http://x/4" {
		t.Errorf("kept %q", links)
	}
}

func TestBestFileVinylPositionsAndAlbumFolders(t *testing.T) {
	files := []string{
		"Interpol - Discography/2002 - Turn On The Bright Lights [24-96 Vinyl]/A1. Untitled.flac",
		"Interpol - Discography/2004 - Antics/01 - Next Exit.flac",
		"Interpol - Discography/2012 - Turn On The Bright Lights (Tenth Anniversary)/CD2/07 - Untitled (Third Demo).flac",
		"Interpol - Discography/2022 - The Other Side of Make-Believe/02 - Untitled.flac",
		"Iron Maiden - The Number of the Beast/22 Acacia Avenue.flac",
	}
	untitled := &Track{Title: "Untitled", Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights"}
	if i, stem := bestFile(untitled, files); i != 0 || stem != "Untitled" {
		t.Errorf("Untitled -> %d %q", i, stem)
	}
	demo := &Track{Title: "Untitled (Third Demo)", Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights: The Tenth Anniversary Edition"}
	if i, _ := bestFile(demo, files); i != 2 {
		t.Errorf("Untitled (Third Demo) -> %d", i)
	}
	acacia := &Track{Title: "22 Acacia Avenue", Artists: []string{"Iron Maiden"}, Album: "The Number of the Beast"}
	if i, _ := bestFile(acacia, files); i != 4 {
		t.Errorf("22 Acacia Avenue -> %d", i)
	}
}

// Jackett's all-indexers feed returns nothing at all when an indexer fails; searching each
// indexer's own feed shows the error instead (and the track is retried next run).
func TestJackettIndexerFeedsReportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/api/v2.0/indexers/all/results/torznab/api" && q.Get("t") == "indexers":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><indexers>
<indexer id="rutracker" configured="true"><title>RuTracker.org</title><caps><categories>
 <category id="3000" name="Audio"><subcat id="3040" name="Audio/Lossless"/></category></categories></caps></indexer>
<indexer id="moviesonly" configured="true"><title>Movies Only</title><caps><categories>
 <category id="2000" name="Movies"/></categories></caps></indexer>
</indexers>`)
		case r.URL.Path == "/api/v2.0/indexers/rutracker/results/torznab/api" && q.Get("t") == "search":
			fmt.Fprint(w, `<error code="900" description="RuTracker.org: login failed"/>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	feeds, err := resolveFeeds(context.Background(), TorrentConfig{Feeds: []string{srv.URL}, Key: "k"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(feeds) != 1 || feeds[0].Name != "Jackett: RuTracker.org" || !strings.HasSuffix(feeds[0].feed, "/indexers/rutracker/results/torznab/api") {
		t.Fatalf("feeds: %+v", feeds)
	}
	src := newTorrentSource(feeds, "", t.TempDir(), "flac", time.Minute)
	_, err = src.Search(context.Background(), &Track{Title: "NYC", Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights"})
	if err == nil || !isUnavailable(err) || !strings.Contains(err.Error(), "login failed") {
		t.Errorf("search error: %v", err)
	}
}

func TestProgressLines(t *testing.T) {
	task := &Task{}
	var out bytes.Buffer
	w := &progressLines{out: &out, task: task}
	w.Write([]byte("@sdl-progress 1048576 NA 4194304.5\n/tmp/x/ytd"))
	w.Write([]byte("lp.webm\n"))
	v := task.view()
	if v.Done != 1048576 || v.Total != 4194304 || out.String() != "/tmp/x/ytdlp.webm\n" {
		t.Errorf("progress %d/%d, output %q", v.Done, v.Total, out.String())
	}
}

func TestDisplayHelpers(t *testing.T) {
	if got := fitWidth("Интерпол - Лейф", 8); got != "Интерпо…" {
		t.Errorf("fitWidth: %q", got)
	}
	colored := "\x1b[32m✓\x1b[0m done"
	if displayWidth(colored) != 6 || fitWidth(colored, 10) != colored {
		t.Errorf("colors counted: %d", displayWidth(colored))
	}
	if fmtBytes(1536) != "1.5 KiB" || fmtBytes(83*1024*1024) != "83.0 MiB" {
		t.Errorf("fmtBytes: %s %s", fmtBytes(1536), fmtBytes(83*1024*1024))
	}
}

// File names and titles as they are in the torrents RuTracker has for this album.
func TestBestFileShortenedNamesAndBSides(t *testing.T) {
	vinyl := []string{
		"Interpol - 2002 - Turn on the Bright Lights [24-96 Vinyl]/08 - Stella Was A Diver.flac",
		"Interpol - 2002 - Turn on the Bright Lights [24-96 Vinyl]/05 - Say Hello.flac",
		"Interpol - 2002 - Turn on the Bright Lights [24-96 Vinyl]/01 - Untitled.flac",
		"Interpol - 2002 - Turn on the Bright Lights [24-96 Vinyl]/totbl.jpg",
	}
	album := "Turn On The Bright Lights"
	for title, want := range map[string]int{
		"Say Hello to the Angels":                    1,
		"Stella was a diver and she was always down": 0,
		"Stella (Third Demo)":                        -1,
		"Untitled (Third Demo)":                      -1,
	} {
		tr := &Track{Title: title, Artists: []string{"Interpol"}, Album: album, DurationMs: 268000}
		i, name := bestFile(tr, vinyl)
		if i != want {
			t.Errorf("%s -> %d %q, want %d", title, i, name, want)
			continue
		}
		if i >= 0 { // the engine must accept what bestFile picked
			if m := evaluate(tr, Candidate{Title: name, Artist: "Interpol - Turn on the Bright Lights"}); m.Reject != "" {
				t.Errorf("%s: %q rejected: %s", title, name, m.Reject)
			}
		}
	}
	bside := &Track{Title: "Interlude (B-Side)", Artists: []string{"Interpol"}, Album: album}
	if m := evaluate(bside, Candidate{Title: "Interlude", Artist: "Interpol - Discography"}); m.Reject != "" {
		t.Errorf("Interlude (B-Side) vs Interlude: %s", m.Reject)
	}
}

func TestTorrentIDIgnoresChangingLinks(t *testing.T) {
	a := torrentResult{Title: "x", Link: "http://127.0.0.1:9117/dl/rutracker/?path=AAA", Details: "https://rutracker.org/forum/viewtopic.php?t=1"}
	b := torrentResult{Title: "x", Link: "http://127.0.0.1:9117/dl/rutracker/?path=BBB", Details: "https://rutracker.org/forum/viewtopic.php?t=1"}
	c := torrentResult{Title: "y", Magnet: "magnet:?xt=urn:btih:ABCDEF0123&dn=y"}
	feed := `<rss><channel><item><title>y</title><link>magnet:?xt=urn:btih:ABCDEF0123&amp;dn=y</link></item></channel></rss>`
	parsed, _ := parseTorznab([]byte(feed))
	if a.id() != b.id() || len(parsed) != 1 || parsed[0].id() != "btih:abcdef0123" || c.id() == parsed[0].id() {
		t.Errorf("ids: %q %q %q %+v", a.id(), b.id(), c.id(), parsed)
	}
}

func TestShortenedNamesDontMatchOtherVersions(t *testing.T) {
	vinyl := []string{"TOTBL [24-96 Vinyl]/11 - Leif Erikson.flac", "TOTBL [24-96 Vinyl]/03 - NYC.flac"}
	for _, title := range []string{"Leif Erikson (Third Demo)", "NYC (Peel Session)"} {
		tr := &Track{Title: title, Artists: []string{"Interpol"}, Album: "Turn On The Bright Lights"}
		if i, name := bestFile(tr, vinyl); i >= 0 {
			t.Errorf("%s matched %q", title, name)
		}
	}
}

// Names as RuTracker has them: the artist-only search must put Interpol's own releases first
// and not be swayed by other artists' releases, videos or another album's anniversary edition.
func TestArtistSearchRanking(t *testing.T) {
	titles := []string{
		"Later with Jools Holland - Even Louder (Interpol, Muse, PJ Harvey, Nick Cave, Metallica) [2005, Hard Rock / Post-Punk / Alternative, DVDRip]",
		"(Industrial) Nine Inch Nails - Every Day Is Exactly The Same (Interpol Remixes) - 2006, FLAC (tracks+.cue), lossless",
		"(Melodic House / Techno) [WEB] Kristian Heikkila - Interpol [Upon You Records - UY138] - 2018, FLAC (tracks), lossless",
		"Interpol • The Paris Ghost Session (Studio 130, Paris, France) [2018, Alternative rock, HDTV, 720p]",
		"(Post-Punk Revival / Indie Rock) [WEB] Interpol - Antics (The Twentieth Anniversary Edition) - 2024, FLAC (tracks), lossless",
		"(Indie rock, post-punk revival) [CD`11|WEB`1] Interpol - / Discography - 2001-2019, ALAC (tracks+.cue), lossless",
		"(Alternative Rock, Indie) [CD] Interpol - Antics - 2005 (Enhanced), FLAC (tracks+.cue), lossless",
	}
	var results []torrentResult
	for i, title := range titles {
		results = append(results, torrentResult{Title: title, Link: fmt.Sprint(i), Seeders: 10})
	}
	tr := &Track{Title: "NYC (Third Demo)", Artists: []string{"Interpol"},
		Album: "Turn On The Bright Lights: The Tenth Anniversary Edition (Remastered)"}
	qs := torrentQueries(tr)
	ranked := rankTorrents(results, "Interpol", qs[len(qs)-1], true)
	var got []string
	for _, r := range ranked {
		got = append(got, r.Link)
	}
	// The discography first; the three releases by other artists dropped; the video last.
	if strings.Join(got, " ") != "5 4 6 3" {
		t.Errorf("ranking %q", got)
	}
	for title, want := range map[string]string{
		"(Indie rock) [CD] Interpol - / Discography - 2001-2019": "Interpol",
		"Nine Inch Nails - Every Day (Interpol Remixes)":         "Nine Inch Nails",
		"Interpol • The Paris Ghost Session":                     "Interpol • The Paris Ghost Session",
	} {
		if got := artistPart(title); got != want {
			t.Errorf("artistPart(%q) = %q", title, got)
		}
	}
}
