package main

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const exportPlaylists = `{"playlists":[
 {"name":"Road Trip","lastModifiedDate":"2024-01-01","items":[
  {"track":{"trackName":"Get Lucky","artistName":"Daft Punk","albumName":"Random Access Memories","trackUri":"spotify:track:69kOkLUCkxIZYexIgSG8rq"},"episode":null,"localTrack":null,"addedDate":"2024-01-01"},
  {"track":null,"episode":{"episodeName":"Ep 1","showName":"Some Show","episodeUri":"spotify:episode:x"},"localTrack":null},
  {"track":null,"episode":null,"localTrack":{"uri":"spotify:local:My+Band:Demos:Garage+Song:215"}}
 ],"description":null,"numberOfFollowers":0},
 {"name":"Discover Weekly","items":[{"track":{"trackName":"Other","artistName":"Someone","albumName":"X","trackUri":"spotify:track:0000000000000000000000"}}]}
]}`

const exportLibrary = `{"tracks":[
 {"artist":"Daft Punk","album":"Random Access Memories","track":"Get Lucky","uri":"spotify:track:69kOkLUCkxIZYexIgSG8rq"},
 {"artist":"Radiohead","album":"Amnesiac","track":"Pyramid Song","uri":"spotify:track:1111111111111111111111"}
],"albums":[],"shows":[]}`

// pavelkomarov/exportify (exportify.net)
const exportifyNew = "Track URI,ISRC,Track Name,Album Name,Artist Name(s),Release Date,Duration (ms),Popularity\n" +
	"spotify:track:69kOkLUCkxIZYexIgSG8rq,USQX91300108,Get Lucky,Random Access Memories,\"Daft Punk,Pharrell Williams\",2013-05-17,248413,80\n"

// watsonbox/exportify
const exportifyOld = "\uFEFFTrack URI,Track Name,Artist URI(s),Artist Name(s),Album URI,Album Name,Album Artist URI(s),Album Artist Name(s),Album Release Date,Album Image URL,Disc Number,Track Number,Track Duration (ms),Track Preview URL,Explicit,Popularity,ISRC,Added By,Added At\n" +
	"spotify:track:2222222222222222222222,Pyramid Song,spotify:artist:4Z8W4fKeB5YxbusRsdQVPb,Radiohead,spotify:album:x,Amnesiac,spotify:artist:4Z8W4fKeB5YxbusRsdQVPb,Radiohead,2001-03-12,https://i.scdn.co/image/abc,1,2,289000,,false,70,GBAYE0100536,me,2020-01-01\n"

const tuneMyMusic = "Track name,Artist name,Album,Playlist name,Type,ISRC,Spotify - id\n" +
	"Get Lucky,Daft Punk,Random Access Memories,Party,Track,USQX91300108,69kOkLUCkxIZYexIgSG8rq\n" +
	"Some Episode,Some Show,,Party,Podcast,,\n"

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func quiet(string, ...any) {}

func TestImportSpotifyDataExportZip(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "my_spotify_data.zip")
	f, _ := os.Create(zp)
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{
		"Spotify Account Data/Playlist1.json":   exportPlaylists,
		"Spotify Account Data/YourLibrary.json": exportLibrary,
		"Spotify Account Data/Userdata.json":    `{"username":"me"}`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	f.Close()

	lib := newLibrary()
	if err := importInput(context.Background(), lib, zp, quiet); err != nil {
		t.Fatal(err)
	}
	if len(lib.Tracks) != 4 {
		t.Fatalf("want 4 unique tracks, got %d", len(lib.Tracks))
	}
	lucky := lib.Tracks["69kOkLUCkxIZYexIgSG8rq"]
	if lucky == nil || lucky.SpotifyURL != "https://open.spotify.com/track/69kOkLUCkxIZYexIgSG8rq" || lucky.Album != "Random Access Memories" {
		t.Fatalf("bad track: %+v", lucky)
	}
	var local *Track
	for _, tr := range lib.Tracks {
		if tr.IsLocal {
			local = tr
		}
	}
	if local == nil || local.Title != "Garage Song" || local.Artists[0] != "My Band" || local.DurationMs != 215000 {
		t.Fatalf("bad local track: %+v", local)
	}
	if len(lib.Skipped) != 1 || lib.Skipped[0].Reason != "podcast episode, not a song" {
		t.Fatalf("skipped = %+v", lib.Skipped)
	}

	view := lib.filtered(nil, []string{"discover weekly"}, false)
	var names []string
	for _, p := range view.Playlists {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Liked Songs,Road Trip" || len(view.Tracks) != 3 || len(lib.Tracks) != 4 {
		t.Fatalf("filtered view: %v, %d tracks (full library %d)", names, len(view.Tracks), len(lib.Tracks))
	}
}

func TestImportCSVVariantsAndText(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"exportify/Liked Songs.csv": exportifyNew,
		"exportify/Chill.csv":       exportifyOld,
		"tunemymusic.csv":           tuneMyMusic,
		"wishlist.txt":              "# my list\nBoards of Canada - Roygbiv\nnot a valid line\n",
	})
	lib := newLibrary()
	for _, src := range []string{filepath.Join(dir, "exportify"), filepath.Join(dir, "tunemymusic.csv"), filepath.Join(dir, "wishlist.txt")} {
		if err := importInput(context.Background(), lib, src, quiet); err != nil {
			t.Fatal(err)
		}
	}
	lucky := lib.Tracks["69kOkLUCkxIZYexIgSG8rq"]
	if lucky.DurationMs != 248413 || lucky.ISRC != "USQX91300108" || len(lucky.Artists) != 2 {
		t.Errorf("exportify.net row: %+v", lucky)
	}
	pyramid := lib.Tracks["2222222222222222222222"]
	if pyramid.TrackNumber != 2 || pyramid.CoverURL == "" || pyramid.ArtistURLs["Radiohead"] != "https://open.spotify.com/artist/4Z8W4fKeB5YxbusRsdQVPb" {
		t.Errorf("watsonbox exportify row: %+v", pyramid)
	}
	party := lib.playlist("Party")
	if len(party.Keys) != 1 || party.Keys[0] != "69kOkLUCkxIZYexIgSG8rq" {
		t.Errorf("TuneMyMusic playlist column not used: %+v", party)
	}
	if len(lib.Skipped) != 1 {
		t.Errorf("podcast row not skipped: %+v", lib.Skipped)
	}
	wish := lib.playlist("wishlist")
	if len(wish.Keys) != 1 || lib.Tracks[wish.Keys[0]].Title != "Roygbiv" {
		t.Errorf("text import: %+v", wish)
	}
}

// Real playlists list songs twice: the same row again, or the album and a re-release under
// another Spotify ID (sometimes with a translated title, or with ISRCs swapped between songs).
const repeatsCSV = "Track URI,ISRC,Track Name,Album Name,Artist Name(s),Duration (ms)\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa1,AEA0D1846144,Тоска,Этажи,Molchat Doma,189048\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa2,AEA0D1847461,Люди надоели,С крыш наших домов,Molchat Doma,178860\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa3,AEA0D1847466,Дома молчат,С крыш наших домов,Molchat Doma,176979\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa4,QZES81838053,Groza (Thunderstorm),1,Leto V Gorode,234370\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa5,USHM91797375,War Is Peace,Leviathan,Diary of My Misanthropy,197333\n" +
	"spotify:track:aaaaaaaaaaaaaaaaaaaaa1,AEA0D1846144,Тоска,Этажи,Molchat Doma,189048\n" + // the same row again
	"spotify:track:bbbbbbbbbbbbbbbbbbbbb1,AEA0D1846144,ТОСКА,Этажи,Molchat Doma,189003\n" + // re-release
	"spotify:track:bbbbbbbbbbbbbbbbbbbbb2,AEA0D1847461,Дома Молчат,С крыш наших домов,Molchat Doma,176979\n" + // swapped ISRC
	"spotify:track:bbbbbbbbbbbbbbbbbbbbb3,AEA0D1847463,Люди Надоели,С крыш наших домов,Molchat Doma,178860\n" +
	"spotify:track:bbbbbbbbbbbbbbbbbbbbb4,QZES81838053,Groza,1,Leto V Gorode,234370\n" + // translated title
	"spotify:track:bbbbbbbbbbbbbbbbbbbbb5,USHM21864543,War Is Peace,Theatre of the Absurd,Diary of My Misanthropy,168000\n" // another version

func TestImportMergesRepeatedSongs(t *testing.T) {
	lib := newLibrary()
	if err := importCSV(lib, "Sovietwave", strings.NewReader(repeatsCSV)); err != nil {
		t.Fatal(err)
	}
	// A third listing in another file, without an ID or length.
	if err := importText(lib, "Wishlist", strings.NewReader("Molchat Doma - Тоска\n"), quiet); err != nil {
		t.Fatal(err)
	}
	keys := lib.playlist("Sovietwave").Keys
	want := "aaaaaaaaaaaaaaaaaaaaa1 aaaaaaaaaaaaaaaaaaaaa2 aaaaaaaaaaaaaaaaaaaaa3 aaaaaaaaaaaaaaaaaaaaa4 " +
		"aaaaaaaaaaaaaaaaaaaaa5 bbbbbbbbbbbbbbbbbbbbb5"
	if got := strings.Join(keys, " "); got != want || len(lib.Tracks) != 6 || len(lib.Order) != 6 {
		t.Fatalf("playlist = %s (%d tracks)\nwant %s", got, len(lib.Tracks), want)
	}
	if w := lib.playlist("Wishlist").Keys; len(w) != 1 || w[0] != "aaaaaaaaaaaaaaaaaaaaa1" {
		t.Errorf("text listing not merged: %v", w)
	}
	for key, also := range map[string]string{
		"aaaaaaaaaaaaaaaaaaaaa1": "bbbbbbbbbbbbbbbbbbbbb1 " + trackKey("", "Molchat Doma", "Тоска"),
		"aaaaaaaaaaaaaaaaaaaaa2": "bbbbbbbbbbbbbbbbbbbbb3", // by title, not by the swapped ISRC
		"aaaaaaaaaaaaaaaaaaaaa3": "bbbbbbbbbbbbbbbbbbbbb2",
		"aaaaaaaaaaaaaaaaaaaaa4": "bbbbbbbbbbbbbbbbbbbbb4",
		"aaaaaaaaaaaaaaaaaaaaa5": "",
	} {
		if got := strings.Join(lib.Tracks[key].Also, " "); got != also {
			t.Errorf("%s merged %q, want %q", lib.Tracks[key], got, also)
		}
	}
	if lib.merged != 5 {
		t.Errorf("merged = %d, want 5", lib.merged)
	}

	// A download made under a key since merged away still counts for the song.
	file := filepath.Join(t.TempDir(), "Тоска.flac")
	os.WriteFile(file, []byte("x"), 0o644)
	recs := map[string]Record{
		"aaaaaaaaaaaaaaaaaaaaa1": {Key: "aaaaaaaaaaaaaaaaaaaaa1", Status: "failed"},
		"bbbbbbbbbbbbbbbbbbbbb1": {Key: "bbbbbbbbbbbbbbbbbbbbb1", Status: "downloaded", Path: file},
	}
	if rec := recordFor(recs, lib.Tracks["aaaaaaaaaaaaaaaaaaaaa1"]); rec.Path != file {
		t.Errorf("record = %+v", rec)
	}
	os.Remove(file)
	if rec := recordFor(recs, lib.Tracks["aaaaaaaaaaaaaaaaaaaaa1"]); rec.Status != "failed" {
		t.Errorf("record with the file gone = %+v", rec)
	}

	// Saved and loaded again, the merges are remembered.
	p := filepath.Join(t.TempDir(), "library.json")
	if err := lib.save(p); err != nil {
		t.Fatal(err)
	}
	again, err := loadLibrary(p)
	if err != nil {
		t.Fatal(err)
	}
	again.add("Sovietwave", &Track{Key: "bbbbbbbbbbbbbbbbbbbbb1", Title: "Тоска", Artists: []string{"Molchat Doma"}})
	if len(again.Tracks) != 6 || len(again.playlist("Sovietwave").Keys) != 6 {
		t.Errorf("after reload: %d tracks, playlist %v", len(again.Tracks), again.playlist("Sovietwave").Keys)
	}

	// A listing without a length joins one version, and doesn't bridge it to another
	// version of a different length, whichever order they come in.
	long := &Track{Key: "ccccccccccccccccccccc1", Title: "Farewell", Artists: []string{"Boris"}, Album: "Pink", DurationMs: 453400}
	short := &Track{Key: "ccccccccccccccccccccc2", Title: "Farewell", Artists: []string{"Boris"}, Album: "Split", DurationMs: 302199}
	nolen := &Track{Key: trackKey("", "Boris", "Farewell"), Title: "Farewell", Artists: []string{"Boris"}}
	for _, order := range [][]*Track{{long, nolen, short}, {nolen, long, short}} {
		lib := newLibrary()
		for _, tr := range order {
			cp := *tr
			lib.add("Boris", &cp)
		}
		if len(lib.Tracks) != 2 || lib.merged != 1 {
			t.Errorf("%s first: %d tracks, %d merged", order[0].Key, len(lib.Tracks), lib.merged)
		}
	}
}

func TestParseDurations(t *testing.T) {
	for in, want := range map[string]int{"3:25": 205000, "1:02:03": 3723000, "205": 205000, "205000": 205000, "": 0} {
		if got := parseDurationMs(in); got != want {
			t.Errorf("parseDurationMs(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRelativePath(t *testing.T) {
	tr := getLucky()
	tr.TrackNumber = 0
	if got := relativePath(tr, defaultTemplate, ".mp3"); got != "Daft Punk/Random Access Memories/Get Lucky.mp3" {
		t.Errorf("got %q", got)
	}
	tr2 := track("AC/DC: Live?", []string{"AC/DC"}, "Live", 100)
	tr2.DiscNumber, tr2.TrackNumber = 2, 4
	if got := relativePath(tr2, defaultTemplate, ".flac"); got != "AC_DC/Live/2-04 - AC_DC_ Live_.flac" {
		t.Errorf("got %q", got)
	}
}

func TestParseYouTubeMusicResults(t *testing.T) {
	resp := map[string]any{"contents": []any{map[string]any{"musicResponsiveListItemRenderer": map[string]any{
		"playlistItemData": map[string]any{"videoId": "abc"},
		"flexColumns": []any{
			map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{map[string]any{"text": "Get Lucky"}}}}},
			map[string]any{"musicResponsiveListItemFlexColumnRenderer": map[string]any{"text": map[string]any{"runs": []any{
				map[string]any{"text": "Daft Punk", "navigationEndpoint": map[string]any{"browseEndpoint": map[string]any{"browseId": "UC1",
					"browseEndpointContextSupportedConfigs": map[string]any{"browseEndpointContextMusicConfig": map[string]any{"pageType": "MUSIC_PAGE_TYPE_ARTIST"}}}}},
				map[string]any{"text": " & "},
				map[string]any{"text": "Uncredited Guest"},
				map[string]any{"text": " • "},
				map[string]any{"text": "Random Access Memories", "navigationEndpoint": map[string]any{"browseEndpoint": map[string]any{"browseId": "MPRE1",
					"browseEndpointContextSupportedConfigs": map[string]any{"browseEndpointContextMusicConfig": map[string]any{"pageType": "MUSIC_PAGE_TYPE_ALBUM"}}}}},
				map[string]any{"text": " • "},
				map[string]any{"text": "6:10"},
			}}}},
		},
	}}}}
	got := parseYTMusicResults(resp)
	if len(got) != 1 {
		t.Fatalf("got %d results", len(got))
	}
	c := got[0]
	if c.Title != "Get Lucky" || c.Artist != "Daft Punk, Uncredited Guest" || c.Album != "Random Access Memories" ||
		c.Duration != 370 || c.ArtistURL != "https://music.youtube.com/channel/UC1" || c.URL != "https://music.youtube.com/watch?v=abc" {
		t.Errorf("parsed %+v", c)
	}
}
