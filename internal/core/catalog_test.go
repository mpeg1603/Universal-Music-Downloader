package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDeezer answers like Deezer's API for a tiny catalog: Daft Punk, one album and a few
// other releases.
func fakeDeezer(t *testing.T) *httptest.Server {
	artist := map[string]any{"id": 27, "name": "Daft Punk", "link": "https://www.deezer.com/artist/27"}
	ram := map[string]any{"id": 10, "title": "Random Access Memories", "nb_tracks": 13, "release_date": "2013-05-17", "record_type": "album", "artist": artist, "cover_xl": "http://x/cover.jpg"}
	tracks := map[int][]map[string]any{
		10: {{"id": 101, "title": "Give Life Back to Music", "duration": 274, "track_position": 1, "disk_number": 1, "isrc": "USQX91300101", "artist": artist},
			{"id": 108, "title": "Get Lucky", "duration": 369, "track_position": 8, "disk_number": 1, "isrc": "USQX91300108", "artist": artist},
			{"id": 113, "title": "Contact", "duration": 381, "track_position": 13, "disk_number": 1, "artist": artist}},
		11: {{"id": 111, "title": "Get Lucky (Radio Edit)", "duration": 248, "track_position": 1, "disk_number": 1, "artist": artist}},
		12: {{"id": 121, "title": "Get Lucky", "duration": 369, "track_position": 8, "disk_number": 1, "artist": artist},
			{"id": 122, "title": "Infinity Repeating", "duration": 237, "track_position": 22, "disk_number": 2, "artist": artist}},
		13: {{"id": 131, "title": "Someone Else's Song", "duration": 200, "track_position": 1, "disk_number": 1,
			"artist": map[string]any{"id": 5, "name": "Somebody"}}},
	}
	albums := map[int]map[string]any{10: ram,
		11: {"id": 11, "title": "Get Lucky (Radio Edit)", "nb_tracks": 1, "release_date": "2013-04-19", "record_type": "single", "artist": artist},
		12: {"id": 12, "title": "Random Access Memories (10th Anniversary Edition)", "nb_tracks": 22, "release_date": "2023-05-12", "record_type": "album", "artist": artist},
		13: {"id": 13, "title": "Various Hits", "nb_tracks": 1, "release_date": "2014-01-01", "record_type": "compile", "artist": artist},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		out := map[string]any{"data": []any{}}
		var id int
		p := r.URL.Path
		if strings.HasPrefix(p, "/album/") {
			fmt.Sscanf(strings.TrimPrefix(p, "/album/"), "%d", &id)
		}
		switch {
		case p == "/search":
			if strings.Contains(q, "lucky") && strings.Contains(q, "daft") {
				out["data"] = []any{map[string]any{"id": 108, "title": "Get Lucky", "duration": 369, "artist": artist, "album": ram}}
			}
		case p == "/search/album":
			if strings.Contains(q, "random access") {
				out["data"] = []any{ram, albums[12]}
			} else if strings.Contains(q, "lucky") {
				out["data"] = []any{albums[11]}
			}
		case p == "/search/artist":
			if strings.Contains(q, "daft punk") {
				out["data"] = []any{map[string]any{"id": 27, "name": "Daft Punk", "nb_album": 4}}
			}
		case p == "/track/108":
			tr := tracks[10][1]
			out = map[string]any{}
			for k, v := range tr {
				out[k] = v
			}
			out["album"], out["release_date"] = ram, "2013-05-17"
		case p == "/artist/27/albums":
			out["data"] = []any{albums[11], albums[12], ram, albums[13]}
		case id > 0 && strings.HasSuffix(p, "/tracks"):
			var data []any
			for _, x := range tracks[id] {
				data = append(data, x)
			}
			out["data"] = data
		case id > 0:
			out = albums[id]
		default:
			t.Errorf("unexpected %s", r.URL)
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testDeezer(t *testing.T) *Deezer {
	d := newDeezer()
	d.base = fakeDeezer(t).URL
	d.http.limiter.interval = 0
	return d
}

func TestParseMusicQuery(t *testing.T) {
	for in, want := range map[string]musicQuery{
		"Daft Punk - Get Lucky":     {"", "Daft Punk", "Get Lucky"},
		"album: Daft Punk – RAM":    {"album", "Daft Punk", "RAM"},
		"Daft Punk":                 {"", "Daft Punk", ""},
		"artist: Simon - Garfunkel": {"artist", "Simon - Garfunkel", ""},
		"Song: Hey-Ya - OutKast":    {"track", "Hey-Ya", "OutKast"},
		"  Discography:Daft Punk ":  {"artist", "Daft Punk", ""},
	} {
		if got := parseMusicQuery(in); got != want {
			t.Errorf("%q: %+v, want %+v", in, got, want)
		}
	}
}

func TestSearchMusic(t *testing.T) {
	d := testDeezer(t)
	ctx := context.Background()
	first := func(q string) MusicResult {
		t.Helper()
		res, err := d.SearchMusic(ctx, q)
		if err != nil || len(res) == 0 {
			t.Fatalf("%q: %v %v", q, res, err)
		}
		return res[0]
	}
	if r := first("Daft Punk - Get Lucky"); r.Kind != "track" || r.Title != "Get Lucky" {
		t.Errorf("artist - title: %+v", r)
	}
	if r := first("Get Lucky - Daft Punk"); r.Kind != "track" || r.Title != "Get Lucky" {
		t.Errorf("title - artist: %+v", r)
	}
	if r := first("daft punk - random access memories"); r.Kind != "album" || r.ID != 10 {
		t.Errorf("artist - album: %+v", r)
	}
	if r := first("Daft Punk"); r.Kind != "artist" || r.ID != 27 {
		t.Errorf("artist: %+v", r)
	}

	tracks, name, err := d.Expand(ctx, MusicResult{Kind: "album", ID: 10}, nil)
	if err != nil || name != "Daft Punk - Random Access Memories" || len(tracks) != 3 || tracks[1].TrackNumber != 8 ||
		tracks[1].ISRC != "USQX91300108" || tracks[1].Key != "deezer:108" || tracks[1].AlbumArtist != "Daft Punk" {
		t.Fatalf("album: %q %v %+v", name, err, tracks)
	}
	var steps []string
	tracks, _, err = d.Expand(ctx, MusicResult{Kind: "artist", ID: 27, Title: "Daft Punk"}, func(s string) { steps = append(steps, s) })
	var titles []string
	for _, tr := range tracks {
		titles = append(titles, tr.Title+" ["+tr.Album+"]")
	}
	// The album's version of a song wins over the single's and the reissue's; compilations
	// and other artists' tracks are left out; each song once.
	want := "Give Life Back to Music [Random Access Memories]|Get Lucky [Random Access Memories]|Contact [Random Access Memories]|" +
		"Infinity Repeating [Random Access Memories (10th Anniversary Edition)]"
	if err != nil || strings.Join(titles, "|") != want || len(steps) != 3 {
		t.Errorf("discography: %v\n%s\n%v", err, strings.Join(titles, "|"), steps)
	}
}

func TestTextListImport(t *testing.T) {
	res := newResolver(testDeezer(t), "")
	lib := newLibrary()
	list := "# my list\nDaft Punk - Get Lucky\nalbum: Daft Punk - Random Access Memories\n\nNobody Known At All\nSome Band - Some Song\n"
	if err := importText(context.Background(), lib, "list", strings.NewReader(list), res, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range lib.Playlists[0].Keys {
		got = append(got, lib.Tracks[k].String())
	}
	// Get Lucky once (the album has it too), the album's other tracks, and the unknown line as
	// Artist - Title.
	want := "Daft Punk - Get Lucky|Daft Punk - Give Life Back to Music|Daft Punk - Contact|Some Band - Some Song"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
	// Without the catalog, lines are "Artist - Title".
	lib = newLibrary()
	importText(context.Background(), lib, "list", strings.NewReader(list), nil, func(string, ...any) {})
	if n := len(lib.Playlists[0].Keys); n != 2 {
		t.Errorf("offline: %d tracks", n)
	}
}

func TestAddMusicFromSearch(t *testing.T) {
	tools := NeedFFmpeg(t)
	a := GetLucky()
	a.Key = "a"
	m := TestManager(t, tools, []Source{&FakeSource{SrcName: "youtube", T: t, Results: map[string][]Candidate{}}}, TestLibrary(a), nil)
	m.deezer = testDeezer(t)
	name, n, err := m.AddMusic(context.Background(), MusicResult{Kind: "album", ID: 10}, nil)
	if err != nil || n != 3 || name != "Search: Daft Punk - Random Access Memories" {
		t.Fatalf("%q %d %v", name, n, err)
	}
	q := m.Queue(QueueQuery{Filter: "search"})
	if len(q) != 3 {
		t.Fatalf("%d tracks from the search", len(q))
	}
	for _, v := range q {
		if !v.Bumped && v.Track.Title != "Get Lucky" {
			t.Errorf("%s isn't first in line", v.Track)
		}
	}
	if len(m.Additions()) != 1 {
		t.Error("additions")
	}
	if !m.RemoveAddition(name) {
		t.Fatal("not removed")
	}
	WaitFor(t, func() bool { return len(m.Queue(QueueQuery{Filter: "search"})) == 0 })
}

func TestRouteSpec(t *testing.T) {
	for _, s := range []string{"system", "direct", "socks5://127.0.0.1:10808", "http://user:p%40ss@proxy.example:3128"} {
		r := ParseRoute(s)
		if r.String() != s || r.Validate() != nil {
			t.Errorf("%q: %+v -> %q (%v)", s, r, r.String(), r.Validate())
		}
	}
	if r := ParseRoute("http://user:p%40ss@proxy.example:3128"); r.Pass != "p@ss" || r.User != "user" || r.Port != 3128 {
		t.Errorf("%+v", r)
	}
	if err := (RouteSpec{Kind: "socks5", Host: "x"}).Validate(); err == nil {
		t.Error("a proxy needs a port")
	}
	if ParseRoute("").Kind != RouteSystem || ParseRoute("garbage").Kind != RouteSystem {
		t.Error("defaults")
	}
}

func TestQueueQuery(t *testing.T) {
	tools := NeedFFmpeg(t)
	a, b := GetLucky(), MakeTrack("Aerodynamic", []string{"Daft Punk"}, "Discovery", 212)
	a.Key, b.Key = "a", "b"
	src := &FakeSource{SrcName: "youtube", T: t, Results: map[string][]Candidate{"a": Found("youtube", "Get Lucky", "Daft Punk", 248)}}
	m := TestManager(t, tools, []Source{src}, TestLibrary(a, b), nil)
	RunManager(t, m)
	keys := func(q QueueQuery) string {
		var out []string
		for _, v := range m.Queue(q) {
			out = append(out, v.Key)
		}
		return strings.Join(out, ",")
	}
	if got := keys(QueueQuery{Filter: "done"}); got != "a" {
		t.Errorf("done: %s", got)
	}
	if got := keys(QueueQuery{Filter: "failed"}); got != "b" {
		t.Errorf("failed: %s", got)
	}
	if got := keys(QueueQuery{Filter: "done", Source: "youtube"}); got != "a" {
		t.Errorf("from youtube: %s", got)
	}
	if got := keys(QueueQuery{Sort: "title"}); got != "b,a" {
		t.Errorf("by title: %s", got)
	}
	if got := keys(QueueQuery{Sort: "title", Reverse: true}); got != "a,b" {
		t.Errorf("by title, reversed: %s", got)
	}
	if got := keys(QueueQuery{Text: "aero"}); got != "b" {
		t.Errorf("text: %s", got)
	}
	if c := m.QueueCounts(); c["done"] != 1 || c["failed"] != 1 || c["all"] != 2 {
		t.Errorf("%v", c)
	}
}
