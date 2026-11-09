package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakySource never has the track and is unreachable when down is set.
type flakySource struct {
	name     string
	down     bool
	searches atomic.Int32
}

func (s *flakySource) Name() string  { return s.name }
func (s *flakySource) Label() string { return s.name }
func (s *flakySource) Search(context.Context, *Track) ([]Candidate, error) {
	s.searches.Add(1)
	if s.down {
		return nil, unavailablef("%s is down", s.name)
	}
	return nil, nil
}
func (s *flakySource) Download(context.Context, Candidate, *Track, string) (string, error) {
	return "", failf("nothing to download")
}

func upgradeJob(t *testing.T, sources ...Source) (*Manager, *Job) {
	t.Helper()
	tr := &Track{Key: "up1", Title: "Song", Artists: []string{"Band"}}
	lib := newLibrary()
	lib.add("Upgrade: below flac", tr)
	m := TestManager(t, &Tools{}, sources, lib, nil)
	j := m.job("up1")
	if j == nil {
		t.Fatal("no job")
	}
	j.mu.Lock()
	j.needBetter, j.minQuality, j.tried = true, Threshold("lossless"), map[string]bool{}
	j.lib = []*LibFile{{Path: "/lib/song.mp3", Q: Quality{Codec: "mp3", Kbps: 128}}}
	j.mu.Unlock()
	return m, j
}

// An upgrade goes through every source once; one being unreachable doesn't send it back in line.
func TestUpgradeIsTriedOnce(t *testing.T) {
	up, down := &flakySource{name: "a"}, &flakySource{name: "b", down: true}
	m, j := upgradeJob(t, up, down)
	task := &Task{Track: j.Track}
	res := m.process(withTask(context.Background(), task), j, task, j.gen)
	if res.state != JobFailed || res.transient {
		t.Fatalf("got state %v transient %v (%s); want not found, no retry", res.state, res.transient, res.reason)
	}
	if !strings.Contains(res.reason, "no better copy") {
		t.Errorf("reason: %s", res.reason)
	}
	if up.searches.Load() != 1 || down.searches.Load() != 1 {
		t.Errorf("searches: %d and %d, want 1 and 1", up.searches.Load(), down.searches.Load())
	}
	m.finish(j, j.gen, res)
	if r := m.state.get("up1"); r == nil || r.Status != "failed" {
		t.Errorf("record: %+v; a transient one would be retried at the next start", r)
	}
}

// A normal track still waits for a source that's down.
func TestOrdinaryTrackStillRetries(t *testing.T) {
	down := &flakySource{name: "b", down: true}
	tr := &Track{Key: "t1", Title: "Song", Artists: []string{"Band"}}
	lib := newLibrary()
	lib.add("Mix", tr)
	m := TestManager(t, &Tools{}, []Source{down}, lib, nil)
	j := m.job("t1")
	j.mu.Lock()
	j.tried = map[string]bool{}
	j.mu.Unlock()
	task := &Task{Track: tr}
	if res := m.process(withTask(context.Background(), task), j, task, j.gen); res.state != JobWaiting || !res.transient {
		t.Fatalf("got %v transient %v", res.state, res.transient)
	}
}

func TestTransliteration(t *testing.T) {
	if got := foldName("Черниковская хата"); got != foldName("Chernikovskaya Khata") {
		t.Errorf("%q vs %q", got, foldName("Chernikovskaya Khata"))
	}
	if got := toLatin("Хата"); got != "Hata" {
		t.Errorf("toLatin: %q", got)
	}
	if got := toCyrillic("Chernikovskaya Hata"); got != "черниковская хата" {
		t.Errorf("toCyrillic: %q", got)
	}
	if got := altScript("artist:Кино year:1988", false); got != "artist:Kino year:1988" {
		t.Errorf("altScript keeps tags: %q", got)
	}
	tr := &Track{Title: "Белая ночь", Artists: []string{"Chernikovskaya Hata"}, DurationMs: 200000}
	m := evaluate(tr, Candidate{Title: "Черниковская хата - Белая ночь", Artist: "Черниковская хата", Duration: 200, ArtistIsCredit: true})
	if m.Reject != "" {
		t.Errorf("same band in the other alphabet was rejected: %s", m.Reject)
	}
	if qs := transliteratedQueries(tr, nil); len(qs) == 0 {
		t.Error("no extra queries for a name that mixes alphabets")
	}
}

func TestMergeAcrossCatalogs(t *testing.T) {
	q := parseMusicQuery("Chernikovskya Hata")
	out := mergeResults([]MusicResult{
		{Kind: "artist", Provider: provDeezer, Ref: "1", Title: "Chernikovskaya Hata", Count: 4},
		{Kind: "artist", Provider: provMB, Ref: "x", Title: "Chernikovskaya Hata", Count: 1},
		{Kind: "artist", Provider: provYandex, Ref: "9", Title: "CHERNIKOVSKAYA HATA", Count: 1},
		{Kind: "artist", Provider: provMB, Ref: "y", Title: "Someone Else Entirely"},
	}, q)
	if len(out) != 1 {
		t.Fatalf("got %d results: %+v", len(out), out)
	}
	if len(out[0].Refs) != 3 || out[0].Count != 4 || out[0].Provider != provDeezer {
		t.Errorf("merged: %+v", out[0])
	}
	if out[0].Score < 80 {
		t.Errorf("a typo should still fit well: %.0f", out[0].Score)
	}
}

func TestSimilarNamesStayApart(t *testing.T) {
	out := mergeResults([]MusicResult{
		{Kind: "artist", Provider: provDeezer, Ref: "1", Title: "Metallica", Count: 40},
		{Kind: "artist", Provider: provITunes, Ref: "2", Title: "Metalica", Count: 1},
	}, parseMusicQuery("Metalica"))
	if len(out) != 2 {
		t.Fatalf("two different bands became one: %+v", out)
	}
}

func TestMergeReleases(t *testing.T) {
	out := mergeReleases([]Release{
		{Ref: CatalogRef{provDeezer, "1"}, Title: "Randevu", Kind: "single", Tracks: 1, Year: "2015"},
		{Ref: CatalogRef{provMB, "a"}, Title: "Randevu", Kind: "album", Year: "2015"},
		{Ref: CatalogRef{provITunes, "2"}, Title: "Randevu (Deluxe Edition)", Kind: "album", Tracks: 9, Year: "2016"},
		{Ref: CatalogRef{provMB, "b"}, Title: "Белая ночь", Kind: "single", Year: "2016"},
	})
	if len(out) != 2 {
		t.Fatalf("got %+v", out)
	}
	if out[0].Title != "Randevu (Deluxe Edition)" || out[0].Kind != "album" || out[0].Tracks != 9 || len(out[0].Alts) != 2 {
		t.Errorf("merged: %+v", out[0])
	}
}

func TestTrackFromUpload(t *testing.T) {
	e := ytdlpLinkInfo{flatEntry: flatEntry{ID: "abc", Title: "Molchat Doma - Sudno (Official Video)", Duration: 200, Channel: "Sacred Bones"}}
	tr := trackFromEntry(e, "", "https://www.youtube.com/playlist?list=x")
	if tr == nil || tr.Title != "Sudno" || tr.Artists[0] != "Molchat Doma" || !strings.Contains(tr.SourceURL, "watch?v=abc") {
		t.Fatalf("got %+v", tr)
	}
	e = ytdlpLinkInfo{flatEntry: flatEntry{Title: "Sudno", Uploader: "Molchat Doma - Topic", WebpageURL: "https://x/y"}}
	if tr = trackFromEntry(e, "", "https://soundcloud.com/a/sets/b"); tr == nil || tr.Artists[0] != "Molchat Doma" {
		t.Fatalf("got %+v", tr)
	}
	if trackFromEntry(ytdlpLinkInfo{flatEntry: flatEntry{Title: "[Deleted video]", Uploader: "x"}}, "", "u") != nil {
		t.Error("a deleted video isn't a song")
	}
}

func TestLinksAreKept(t *testing.T) {
	lib := newLibrary()
	m := TestManager(t, &Tools{}, nil, TestLibrary(&Track{Key: "k", Title: "T", Artists: []string{"A"}}), nil)
	link := "https://example.invalid/playlist/1"
	data := `{"name":"Kept","tracks":[{"key":"z","title":"Song","artists":["Band"]}]}`
	path := m.linkCachePath(link)
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(data), 0o644)
	if err := m.importLink(context.Background(), lib, link, false); err != nil {
		t.Fatal(err)
	}
	if len(lib.Tracks) != 1 || lib.Playlists[0].Name != "Kept" {
		t.Fatalf("got %+v", lib.Playlists)
	}
}

// removalManager has a playlist with songs in the downloads folder and its CSV in an input folder.
func removalManager(t *testing.T) (m *Manager, csv, other string, music []string) {
	t.Helper()
	root := t.TempDir()
	mk := func(p, data string) string {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(data), 0o644)
		return p
	}
	row := "Track Name,Artist Name(s)\nSong,Band\n"
	csv = mk(filepath.Join(root, "lists", "Gone.csv"), row)
	other = mk(filepath.Join(root, "lists", "Stays.csv"), "Track Name,Artist Name(s)\nOther,Group\n")
	downloads := filepath.Join(root, "music")
	library := filepath.Join(root, "library")
	song := mk(filepath.Join(downloads, "Band", "song.mp3"), "x")
	kept := mk(filepath.Join(library, "Band", "old.mp3"), "x")
	m = TestManager(t, &Tools{}, nil, newLibrary(), func(c *Config) {
		c.OutputDir = downloads
		c.LibraryDirs = []string{library}
		c.Inputs = []InputCfg{{Path: filepath.Join(root, "lists"), Enabled: true}}
		c.Playlists = map[string]PlaylistCfg{}
	})
	m.importInputs()
	key := m.Playlists()[0].Name
	if key != "Gone" {
		t.Fatalf("first playlist is %s", key)
	}
	for _, j := range m.jobList() {
		j.mu.Lock()
		if j.Track.Title == "Song" {
			j.state, j.rec = JobDone, &Record{Key: j.Track.Key, Status: "downloaded", Path: song}
			m.state.put(*j.rec)
		}
		j.mu.Unlock()
	}
	return m, csv, other, []string{song, kept}
}

// Whatever is chosen for the playlist's file, the music stays exactly where it was.
func TestRemovePlaylistsNeverTouchesMusic(t *testing.T) {
	for _, how := range []string{"keep", "delete", "move"} {
		t.Run(how, func(t *testing.T) {
			m, _, _, music := removalManager(t)
			if _, err := m.RemovePlaylists([]string{"Gone"}, how, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			for _, p := range music {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("%s was touched: %v", p, err)
				}
			}
			for _, p := range m.Playlists() {
				if p.Name == "Gone" {
					t.Error("the playlist is still there")
				}
			}
			for _, j := range m.jobList() {
				if j.Track.Title == "Song" {
					t.Error("its song is still in the queue")
				}
			}
		})
	}
}

func TestRemovePlaylistsKeepsTheFile(t *testing.T) {
	m, csv, _, _ := removalManager(t)
	res, err := m.RemovePlaylists([]string{"Gone"}, "keep", "")
	if err != nil || res.Deleted != 0 || res.Moved != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(csv); err != nil {
		t.Error(err)
	}
	if !m.Config().Playlists["Gone"].Removed {
		t.Error("it would come back with its file")
	}
}

func TestRemovePlaylistsDeletesTheFile(t *testing.T) {
	m, csv, other, _ := removalManager(t)
	v := m.RemovalPreview([]string{"Gone"})
	if len(v.Files) != 1 || v.Files[0] != csv || v.Shared != 0 {
		t.Fatalf("preview: %+v", v)
	}
	res, err := m.RemovePlaylists([]string{"Gone"}, "delete", "")
	if err != nil || res.Deleted != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(csv); !os.IsNotExist(err) {
		t.Error("the CSV is still there")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error(err)
	}
	if len(m.RemovedPlaylists()) != 0 {
		t.Errorf("nothing is left to bring back: %v", m.RemovedPlaylists())
	}
}

func TestRemovePlaylistsMovesTheFile(t *testing.T) {
	m, csv, _, _ := removalManager(t)
	dest := t.TempDir()
	os.WriteFile(filepath.Join(dest, "Gone.csv"), []byte("already here"), 0o644)
	res, err := m.RemovePlaylists([]string{"Gone"}, "move", dest)
	if err != nil || res.Moved != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(csv); !os.IsNotExist(err) {
		t.Error("the original is still there")
	}
	if b, err := os.ReadFile(filepath.Join(dest, "Gone (2).csv")); err != nil || !strings.Contains(string(b), "Song") {
		t.Errorf("%q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "Gone.csv")); string(b) != "already here" {
		t.Errorf("the file already there was replaced: %q", b)
	}
	if len(m.RemovedPlaylists()) != 0 {
		t.Errorf("nothing is left to bring back: %v", m.RemovedPlaylists())
	}
}

// Moved into a folder the app reads, the playlist would simply be read again.
func TestRemovePlaylistsWontMoveIntoAnInputFolder(t *testing.T) {
	m, csv, _, _ := removalManager(t)
	if _, err := m.RemovePlaylists([]string{"Gone"}, "move", filepath.Join(filepath.Dir(csv), "sub")); err == nil {
		t.Fatal("no error")
	}
	if _, err := os.Stat(csv); err != nil {
		t.Error(err)
	}
	if m.Config().Playlists["Gone"].Removed {
		t.Error("removed although the move was refused")
	}
}

// A file that also holds a playlist staying in the app is left alone.
func TestRemovePlaylistsLeavesFilesHoldingOthers(t *testing.T) {
	dir := t.TempDir()
	both := filepath.Join(dir, "both.csv")
	os.WriteFile(both, []byte("Playlist Name,Track Name,Artist Name(s)\nOne,Song,Band\nTwo,Other,Group\n"), 0o644)
	m := TestManager(t, &Tools{}, nil, newLibrary(), func(c *Config) { c.Inputs = []InputCfg{{Path: dir, Enabled: true}} })
	m.importInputs()
	v := m.RemovalPreview([]string{"One"})
	if len(v.Files) != 0 || v.Shared != 1 {
		t.Fatalf("preview: %+v", v)
	}
	if _, err := m.RemovePlaylists([]string{"One"}, "delete", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(both); err != nil {
		t.Error(err)
	}
	if !m.Config().Playlists["One"].Removed {
		t.Error("it would come back with its file")
	}
	if v := m.RemovalPreview([]string{"One", "Two"}); len(v.Files) != 1 || v.Shared != 0 {
		t.Fatalf("both together: %+v", v)
	}
}

// A file added on its own stops being listed once it's gone.
func TestRemovePlaylistsDropsASingleFileInput(t *testing.T) {
	dir := t.TempDir()
	one := filepath.Join(dir, "One.csv")
	os.WriteFile(one, []byte("Track Name,Artist Name(s)\nSong,Band\n"), 0o644)
	m := TestManager(t, &Tools{}, nil, newLibrary(), func(c *Config) { c.Inputs = []InputCfg{{Path: one, Enabled: true}} })
	m.importInputs()
	if _, err := m.RemovePlaylists([]string{"One"}, "delete", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Config().Inputs); n != 0 {
		t.Errorf("%d inputs left", n)
	}
}

func TestRemovedPlaylistStaysOutAndComesBack(t *testing.T) {
	dir := t.TempDir()
	csv := "Track Name,Artist Name(s)\nSong,Band\n"
	os.WriteFile(filepath.Join(dir, "One.csv"), []byte(csv), 0o644)
	os.WriteFile(filepath.Join(dir, "Two.csv"), []byte("Track Name,Artist Name(s)\nOther,Group\n"), 0o644)
	m := TestManager(t, &Tools{}, nil, newLibrary(), func(c *Config) { c.Inputs = []InputCfg{{Path: dir, Enabled: true}} })
	m.importInputs()
	if len(m.Playlists()) != 2 {
		t.Fatalf("%+v", m.Playlists())
	}
	if _, err := m.RemovePlaylists([]string{"One"}, "keep", ""); err != nil {
		t.Fatal(err)
	}
	m.importInputs()
	if got := m.Playlists(); len(got) != 1 || got[0].Name != "Two" {
		t.Fatalf("%+v", got)
	}
	if m.RestorePlaylists() != 1 {
		t.Fatal("nothing brought back")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(m.Playlists()) != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(m.Playlists()) != 2 {
		t.Fatalf("%+v", m.Playlists())
	}
}

// The ignored list can be edited one playlist at a time, or cleared.
func TestIgnoredListCanBeEdited(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"One", "Two", "Three"} {
		os.WriteFile(filepath.Join(dir, n+".csv"), []byte("Track Name,Artist Name(s)\n"+n+",Band\n"), 0o644)
	}
	m := TestManager(t, &Tools{}, nil, newLibrary(), func(c *Config) { c.Inputs = []InputCfg{{Path: dir, Enabled: true}} })
	m.importInputs()
	if _, err := m.RemovePlaylists([]string{"One", "Two", "Three"}, "keep", ""); err != nil {
		t.Fatal(err)
	}
	if n := m.RestorePlaylists("Two"); n != 1 {
		t.Fatalf("%d came off", n)
	}
	if got := m.RemovedPlaylists(); len(got) != 2 || got[0] != "One" || got[1] != "Three" {
		t.Fatalf("%v", got)
	}
	if n := m.RestorePlaylists("Two"); n != 0 {
		t.Errorf("%d came off a second time", n)
	}
	if n := m.RestorePlaylists(); n != 2 {
		t.Fatalf("%d came off", n)
	}
	if got := m.RemovedPlaylists(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// Deleting a file from the folder is noticed, without pressing anything.
func TestInputSignatureNoticesDeletedFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.csv")
	os.WriteFile(a, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.csv"), []byte("y"), 0o644)
	in := []InputCfg{{Path: dir, Enabled: true}, {Path: "https://example.com/x", Enabled: true}}
	before := inputSignature(in)
	if before != inputSignature(in) {
		t.Fatal("unstable")
	}
	os.Remove(a)
	if before == inputSignature(in) {
		t.Fatal("didn't notice")
	}
}

func TestDeleteSortPlans(t *testing.T) {
	m := TestManager(t, &Tools{}, nil, newLibrary(), nil)
	for _, id := range []string{"20260101-000000", "20260102-000000"} {
		d := filepath.Join(m.sortDir(), id)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "info.json"), []byte(`{"id":"`+id+`"}`), 0o644)
	}
	n, err := m.SortDeletePlans([]string{"20260101-000000", "../../evil", "20269999-000000"})
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if h := m.SortHistory(); len(h) != 1 || h[0].ID != "20260102-000000" {
		t.Fatalf("%+v", h)
	}
}

func TestTrackerCaps(t *testing.T) {
	var trs []string
	for i := range 20 {
		trs = append(trs, "udp://t"+string(rune('a'+i))+".example:80/announce")
	}
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	for _, tr := range trs {
		magnet += "&tr=" + tr
	}
	if got := strings.Count(trimMagnet(magnet), "tr="); got != maxTrackers {
		t.Errorf("%d trackers left", got)
	}
	n := 0
	for _, tier := range capTrackers([][]string{trs[:4], trs[4:]}) {
		n += len(tier)
	}
	if n != maxTrackers {
		t.Errorf("%d trackers left in the tiers", n)
	}
}

func TestRemovesOnlyItsOwnPlaylistFiles(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "Gone.m3u8")
	other := filepath.Join(dir, "Stays.m3u8")
	mine := filepath.Join(dir, "Handmade.m3u8")
	os.WriteFile(own, []byte("#EXTM3U\n#PLAYLIST:Gone\n"), 0o644)
	os.WriteFile(other, []byte("#EXTM3U\n#PLAYLIST:Stays\n"), 0o644)
	os.WriteFile(mine, []byte("#EXTM3U\nsong.mp3\n"), 0o644)
	removePlaylistFiles(dir, map[string]bool{"Gone": true, "Handmade": true})
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Error("the removed playlist's file stayed")
	}
	for _, p := range []string{other, mine} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went", p)
		}
	}
}

// UMD_LIVE=1 go test ./internal/core -run TestCatalogLive -v asks the real catalogs.
func TestCatalogLive(t *testing.T) {
	if os.Getenv("UMD_LIVE") == "" {
		t.Skip("set UMD_LIVE=1 to ask the real catalogs")
	}
	c := newCatalog(newDeezer(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, q := range []string{"Chernikovskaya Hata", "Черниковская хата", "Chernikovskya Hatta", "Metalica", "radiohad - karma police"} {
		start := time.Now()
		rs, err := c.Search(ctx, q)
		t.Logf("=== %q: %d results in %s (err %v)", q, len(rs), time.Since(start).Round(100*time.Millisecond), err)
		for _, r := range firstN(rs, 8) {
			t.Logf("  %-6s %3.0f %-30s %-24s via %s, %d catalogs", r.Kind, r.Score, r.Title, r.Artist, r.Provider, len(r.Refs))
		}
	}
	rs, err := c.Search(ctx, "Chernikovskaya Hata")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Kind != "artist" {
			continue
		}
		rels, err := c.ArtistReleases(ctx, r)
		t.Logf("%s: %d releases (err %v), from %v", r.Title, len(rels), err, r.Refs)
		for _, x := range rels {
			tracks, terr := c.ReleaseTracks(ctx, x)
			t.Logf("  %-6s %s %-32s via %-14s alts %d: %d tracks (%v)", x.Kind, x.Year, x.Title, x.Ref.Provider, len(x.Alts), len(tracks), terr)
		}
		break
	}
}

// UMD_LIVE=1 UMD_YTDLP=<path> go test ./internal/core -run TestLinksLive -v reads real links.
func TestLinksLive(t *testing.T) {
	if os.Getenv("UMD_LIVE") == "" {
		t.Skip("set UMD_LIVE=1")
	}
	m := TestManager(t, &Tools{JSRuntimes: []string{"node"}}, nil, newLibrary(), nil)
	p, err := startLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	m.proxy = p
	m.ytdlpBin = os.Getenv("UMD_YTDLP")
	for _, link := range []string{
		"https://www.deezer.com/album/302127",
		"https://chernikovskayahata.bandcamp.com/album/chernikovskaya-hata-lp",
		"https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://soundcloud.com/mollyjeanmusic/sets/ep",
	} {
		start := time.Now()
		res, err := m.fetchLink(context.Background(), link)
		if err != nil {
			t.Logf("FAIL %s: %v (%s)", link, err, time.Since(start).Round(time.Second))
			continue
		}
		t.Logf("%s -> %q, %d tracks in %s", link, res.Name, len(res.Tracks), time.Since(start).Round(time.Second))
		for _, tr := range firstN(res.Tracks, 3) {
			t.Logf("   %s - %s (%s) src=%s", tr.ArtistStr(), tr.Title, FmtDuration(tr.Duration()), tr.SourceURL)
		}
	}
}

// UMD_LIVE=1 UMD_YTDLP=<path> go test ./internal/core -run TestLinkDownloadLive -v fetches one song by its link.
func TestLinkDownloadLive(t *testing.T) {
	if os.Getenv("UMD_LIVE") == "" {
		t.Skip("set UMD_LIVE=1")
	}
	tools := NeedFFmpeg(t)
	tools.JSRuntimes = []string{"node"}
	m := TestManager(t, tools, nil, newLibrary(), nil)
	p, err := startLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	m.proxy = p
	m.ytdlpBin = os.Getenv("UMD_YTDLP")
	res, err := m.fetchLink(context.Background(), "https://soundcloud.com/forss/sets/soulhack")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%q: %d tracks; first %s src=%s", res.Name, len(res.Tracks), res.Tracks[0], res.Tracks[0].SourceURL)
	m.libMu.Lock()
	m.full.add(res.Name, res.Tracks[0])
	m.libMu.Unlock()
	m.makeJobs()
	j := m.job(res.Tracks[0].Key)
	j.mu.Lock()
	j.tried = map[string]bool{}
	j.mu.Unlock()
	task := &Task{Track: j.Track}
	r := m.process(withTask(context.Background(), task), j, task, j.gen)
	t.Logf("state %v reason %q file %s source %s", r.state, r.reason, r.rec.Path, r.rec.Source)
	if r.state != JobDone {
		t.Fatal("not downloaded")
	}
}
