package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func needFFmpeg(t *testing.T) *Tools {
	t.Helper()
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg not installed")
	}
	return &Tools{FFmpeg: ff, FFprobe: fp}
}

// sine writes a short test tone; the codec follows the extension.
func sine(t *testing.T, path string, seconds float64) string {
	t.Helper()
	codec := map[string][]string{".webm": {"-c:a", "libopus"}, ".m4a": {"-c:a", "aac"}, ".mp3": {"-c:a", "libmp3lame"},
		".flac": {"-c:a", "flac"}, ".ogg": {"-c:a", "libvorbis"}}[filepath.Ext(path)]
	args := append([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%g", seconds)}, codec...)
	if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

type fakeSource struct {
	name        string
	results     map[string][]Candidate
	searchErr   error
	downloadErr error
	ext         string
	length      float64 // >0: downloaded file length regardless of the candidate
	t           *testing.T
	searched    atomic.Int32
}

func (f *fakeSource) Name() string  { return f.name }
func (f *fakeSource) Label() string { return f.name }

func (f *fakeSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	f.searched.Add(1)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.results[tr.Key], nil
}

func (f *fakeSource) Download(ctx context.Context, c Candidate, tr *Track, workdir string) (string, error) {
	if f.downloadErr != nil {
		return "", f.downloadErr
	}
	length := c.Duration
	if f.length > 0 {
		length = f.length
	}
	ext := f.ext
	if ext == "" {
		ext = ".webm"
	}
	return sine(f.t, filepath.Join(workdir, "raw"+ext), length), nil
}

func found(src, title, artist string, seconds float64) []Candidate {
	return []Candidate{{Source: src, URL: "https://example.com/" + strings.ReplaceAll(title, " ", "-"), Title: title,
		Artist: artist, Duration: seconds, ArtistURL: "https://example.com/" + artist}}
}

func testLibrary(tracks ...*Track) *Library {
	lib := newLibrary()
	for _, tr := range tracks {
		lib.add(likedSongs, tr)
	}
	lib.add("Road/Trip", tracks[0])
	lib.Skipped = append(lib.Skipped, Skipped{"Some Podcast", "Show", "podcast episode, not a song", "Road/Trip"})
	return lib
}

// testManager makes a manager working through lib with the given sources (no network, no
// setup), its downloads folder in a temporary directory.
func testManager(t *testing.T, tools *Tools, sources []Source, lib *Library, mutate func(*Config)) *Manager {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.OutputDir = filepath.Join(dir, "music")
	cfg.Format, cfg.Enrich, cfg.SearchWorkers = "mp3", false, 2
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	m := newManager(filepath.Join(dir, "config.json"), data, cfg)
	os.MkdirAll(filepath.Join(data, "tmp"), 0o755)
	st, err := openState(filepath.Join(data, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	m.state, m.tools = st, tools
	m.index = newLibIndex(filepath.Join(data, "library-index.json"), tools)
	if len(cfg.LibraryDirs) > 0 {
		if err := m.index.Scan(context.Background(), cfg.LibraryDirs); err != nil {
			t.Fatal(err)
		}
	}
	m.full = lib
	m.sources = sources
	m.rebuildJobs()
	t.Cleanup(func() {
		m.cancel()
		m.wg.Wait()
		st.close()
	})
	return m
}

// runManager works until nothing is left to do and returns every job.
func runManager(t *testing.T, m *Manager) map[string]JobView {
	t.Helper()
	m.ready.Store(true)
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.dispatchLoop() }()
	go func() { defer m.wg.Done(); m.governor() }()
	m.wake()
	waitIdle(t, m)
	out := map[string]JobView{}
	for _, v := range m.Jobs() {
		out[v.Key] = v
	}
	return out
}

func waitIdle(t *testing.T, m *Manager) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	for deadline := time.Now().Add(90 * time.Second); !m.idle(); time.Sleep(30 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still busy: %s", m.summaryLine())
		}
	}
}

func readCSV(t *testing.T, path string) []map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\uFEFF")))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func ffprobeTags(t *testing.T, path string) map[string]string {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags:stream_tags:stream=codec_type", "-of", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Format  struct{ Tags map[string]string } `json:"format"`
		Streams []struct {
			CodecType string            `json:"codec_type"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
	}
	json.Unmarshal(out, &data)
	tags := map[string]string{}
	for _, s := range data.Streams {
		if s.CodecType == "video" {
			tags["_cover"] = "yes"
			continue // the cover's own tags ("title=Cover")
		}
		for k, v := range s.Tags {
			tags[strings.ToLower(k)] = v
		}
	}
	for k, v := range data.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	return tags
}

func TestFullFlow(t *testing.T) {
	tools := needFFmpeg(t)
	hit := getLucky()
	hit.Key, hit.DurationMs = "t1", 4000
	miss := track("Nonexistent Song", []string{"Nobody"}, "", 4)
	miss.Key = "t2"
	bandcamp := &fakeSource{name: "Bandcamp", searchErr: failf("on Bandcamp but not a free download; you can buy it at https://x.bandcamp.com/track/y"), t: t}
	ytm := &fakeSource{name: "YouTube Music", t: t, results: map[string][]Candidate{
		"t1": found("youtube_music", "Get Lucky", "Daft Punk", 4), "t2": found("youtube_music", "Other Song", "Nobody", 4)}}
	lib := testLibrary(hit, miss)
	m := testManager(t, tools, []Source{bandcamp, ytm}, lib, nil)
	music := m.Config().OutputDir

	out := runManager(t, m)
	if out["t1"].State != JobDone || out["t2"].State != JobFailed {
		t.Fatalf("outcomes: %v / %v (%s)", out["t1"].State, out["t2"].State, out["t2"].Reason)
	}
	rec := m.state.get("t1")
	if want := filepath.Join(music, "Daft Punk/Random Access Memories/08 - Get Lucky.mp3"); rec.Path != want {
		t.Fatalf("path %q", rec.Path)
	}
	if !rec.Quality.known() || rec.Quality.Codec != "mp3" {
		t.Errorf("quality %+v", rec.Quality)
	}
	tags := ffprobeTags(t, rec.Path)
	if tags["title"] != "Get Lucky" || tags["album"] != "Random Access Memories" || tags["track"] != "8/13" || tags["comment"] == "" {
		t.Errorf("tags: %v", tags)
	}
	reason := m.state.get("t2").Reason
	if !strings.Contains(reason, "you can buy it at https://x.bandcamp.com") || !strings.Contains(reason, "closest was “Other Song”") {
		t.Errorf("reason: %s", reason)
	}

	// A new manager over the same state knows what's done; failed tracks stay failed until
	// retried.
	if n := m.RetryFailed(false); n != 1 {
		t.Errorf("retry failed: %d", n)
	}
	m.Cancel("t2")
	if v, _ := m.JobView("t2"); v.State != JobCancelled {
		t.Errorf("cancel: %v", v.State)
	}

	m.writeReports()
	artists := readCSV(t, filepath.Join(music, "reports", "artists.csv"))
	if len(artists) != 1 || artists[0]["artist"] != "Daft Punk" || !strings.Contains(artists[0]["links"], "https://example.com/Daft Punk") {
		t.Errorf("artists.csv: %v", artists)
	}
	failed := readCSV(t, filepath.Join(music, "reports", "failed.csv"))
	cats := map[string]string{}
	for _, f := range failed {
		cats[f["title"]] = f["category"]
	}
	if cats["Nonexistent Song"] != "cancelled" || cats["Some Podcast"] != "skipped" || len(failed) != 2 {
		t.Errorf("failed.csv: %v", failed)
	}
	md, _ := os.ReadFile(filepath.Join(music, "reports", "downloaded.md"))
	if !strings.Contains(string(md), "[Get Lucky](https://open.spotify.com/track/xyz)") {
		t.Errorf("downloaded.md:\n%s", md)
	}
	m3u, _ := os.ReadFile(filepath.Join(music, "Playlists", "Road_Trip.m3u8"))
	if !strings.Contains(string(m3u), "../Daft Punk/Random Access Memories/08 - Get Lucky.mp3") {
		t.Errorf("playlist:\n%s", m3u)
	}
}

func TestBlockedSourcePausesAndTracksWait(t *testing.T) {
	tools := needFFmpeg(t)
	var tracks []*Track
	for i := 0; i < 6; i++ {
		tr := track(fmt.Sprintf("Song %d", i), []string{"Band"}, "", 4)
		tr.Key = fmt.Sprintf("t%d", i)
		tracks = append(tracks, tr)
	}
	yt := &fakeSource{name: "youtube", searchErr: blockedf("YouTube asked to sign in (bot check)"), t: t}
	m := testManager(t, tools, []Source{yt}, testLibrary(tracks...), func(c *Config) { c.SearchWorkers = 1 })
	out := runManager(t, m)
	if n := yt.searched.Load(); n != 1 {
		t.Errorf("searched %d times; a blocked source should pause after the first", n)
	}
	for k, v := range out {
		if v.State != JobWaiting || v.RetryAt.IsZero() {
			t.Errorf("%s: %v %s", k, v.State, v.Reason)
		}
	}
	st := m.SourceStatus()
	var ytStatus SourceStatus
	for _, s := range st {
		if s.Name == "youtube" {
			ytStatus = s
		}
	}
	if ytStatus.Until.IsZero() || !strings.Contains(ytStatus.Reason, "bot check") {
		t.Errorf("status: %+v", ytStatus)
	}
	// Resuming the source and retrying: they're searched again.
	m.PauseSource("youtube", false)
	yt.searchErr = nil
	m.RetryFailed(false)
	waitIdle(t, m)
	if n := yt.searched.Load(); n != 7 {
		t.Errorf("searched %d times after resuming", n)
	}
}

func TestUnreachableSourcePausesAfterFive(t *testing.T) {
	tools := needFFmpeg(t)
	var tracks []*Track
	for i := 0; i < 8; i++ {
		tr := track(fmt.Sprintf("Song %d", i), []string{"Band"}, "", 4)
		tr.Key = fmt.Sprintf("t%d", i)
		tracks = append(tracks, tr)
	}
	down := &fakeSource{name: "soundcloud", searchErr: unavailablef("SoundCloud unreachable (timed out)"), t: t}
	m := testManager(t, tools, []Source{down}, testLibrary(tracks...), func(c *Config) { c.SearchWorkers = 1 })
	out := runManager(t, m)
	if n := down.searched.Load(); n != 5 {
		t.Errorf("searched %d times, want 5 before pausing the source", n)
	}
	if v := out["t7"]; v.State != JobWaiting || !strings.Contains(v.Reason, "paused") {
		t.Errorf("t7: %v %s", v.State, v.Reason)
	}
	m.writeReports()
	failed := readCSV(t, filepath.Join(m.Config().OutputDir, "reports", "failed.csv"))
	if failed[0]["category"] != "source_error" {
		t.Errorf("category %s", failed[0]["category"])
	}
}

func TestFallsThroughAndChecksRealLength(t *testing.T) {
	tools := needFFmpeg(t)
	a := getLucky()
	a.Key, a.DurationMs = "a", 4000
	b := track("Preview Song", []string{"Band"}, "", 60)
	b.Key = "b"
	broken := &fakeSource{name: "soundcloud", downloadErr: failf("gone"), t: t, results: map[string][]Candidate{"a": found("soundcloud", "Get Lucky", "Daft Punk", 4)}}
	preview := &fakeSource{name: "previews", length: 5, t: t, results: map[string][]Candidate{"b": found("x", "Preview Song", "Band", 60)}}
	good := &fakeSource{name: "youtube", ext: ".m4a", t: t, results: map[string][]Candidate{"a": found("youtube", "Get Lucky", "Daft Punk", 4)}}
	m := testManager(t, tools, []Source{broken, preview, good}, testLibrary(a, b), nil)
	runManager(t, m)
	if r := m.state.get("a"); r.Source != "youtube" {
		t.Errorf("a: %+v", r)
	}
	if r := m.state.get("b"); r.Status != "failed" || !strings.Contains(r.Reason, "preview") {
		t.Errorf("b: %+v", r)
	}
}

func TestSamePathGetsNumberedAndNothingIsOverwritten(t *testing.T) {
	tools := needFFmpeg(t)
	a, b := getLucky(), getLucky()
	a.Key, b.Key, a.DurationMs, b.DurationMs = "a", "b", 4000, 4000
	src := &fakeSource{name: "youtube", t: t, results: map[string][]Candidate{
		"a": found("youtube", "Get Lucky", "Daft Punk", 4), "b": found("youtube", "Get Lucky", "Daft Punk", 4)}}
	m := testManager(t, tools, []Source{src}, testLibrary(a, b), func(c *Config) { c.SearchWorkers = 1 })
	music := m.Config().OutputDir
	// A file that was already there (not ours) is never replaced.
	existing := filepath.Join(music, "Daft Punk/Random Access Memories/08 - Get Lucky.mp3")
	os.MkdirAll(filepath.Dir(existing), 0o755)
	os.WriteFile(existing, []byte("mine"), 0o644)
	runManager(t, m)
	got := []string{m.state.get("a").Path, m.state.get("b").Path}
	sort.Strings(got)
	want := []string{filepath.Join(music, "Daft Punk/Random Access Memories/08 - Get Lucky (2).mp3"),
		filepath.Join(music, "Daft Punk/Random Access Memories/08 - Get Lucky (3).mp3")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("paths %v", got)
	}
	if data, _ := os.ReadFile(existing); string(data) != "mine" {
		t.Error("an existing file was overwritten")
	}
}

// The library is checked but never written to: good copies are skipped, worse ones are only
// replaced by a better download that goes to the downloads folder.
func TestLibraryCheck(t *testing.T) {
	tools := needFFmpeg(t)
	libDir := t.TempDir()
	tagged := func(name, title, artist string, extra ...string) string {
		p := filepath.Join(libDir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		args := []string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=4",
			"-metadata", "title=" + title, "-metadata", "artist=" + artist}
		args = append(append(args, extra...), p)
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return p
	}
	flac := tagged("Daft Punk/RAM/08 - Get Lucky.flac", "Get Lucky", "Daft Punk", "-c:a", "flac")
	low := tagged("Interpol/TOTBL/NYC.mp3", "NYC", "Interpol", "-c:a", "libmp3lame", "-b:a", "96k")
	untagged := filepath.Join(libDir, "Burial", "Untrue", "02 - Archangel.mp3")
	os.MkdirAll(filepath.Dir(untagged), 0o755)
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=4", "-c:a", "libmp3lame", "-b:a", "320k", untagged).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	before := snapshotDir(t, libDir)

	lucky := getLucky()
	lucky.Key, lucky.DurationMs = "lucky", 4000
	nyc := track("NYC", []string{"Interpol"}, "Turn On The Bright Lights", 4)
	nyc.Key = "nyc"
	arch := track("Archangel", []string{"Burial"}, "Untrue", 4)
	arch.Key = "arch"
	other := track("Stella Was a Diver", []string{"Interpol"}, "", 4)
	other.Key = "stella"
	src := &fakeSource{name: "youtube", ext: ".webm", t: t, results: map[string][]Candidate{
		"lucky": found("youtube", "Get Lucky", "Daft Punk", 4), "nyc": found("youtube", "NYC", "Interpol", 4),
		"arch": found("youtube", "Archangel", "Burial", 4), "stella": found("youtube", "Stella Was a Diver", "Interpol", 4)}}
	for _, cs := range src.results {
		cs[0].Quality = youtubeQuality
	}
	m := testManager(t, tools, []Source{src}, testLibrary(lucky, nyc, arch, other), func(c *Config) {
		c.LibraryDirs = []string{libDir}
		c.Format = "original"
		c.Threshold = "320"
	})
	out := runManager(t, m)
	if v := out["lucky"]; v.State != JobInLibrary || v.Lib == nil || v.Lib.Path != flac {
		t.Errorf("lucky: %v %+v", v.State, v.Lib)
	}
	if v := out["arch"]; v.State != JobInLibrary { // matched by file name; 320 kbps is good enough
		t.Errorf("archangel: %v %s", v.State, v.Reason)
	}
	v := out["nyc"]
	if v.State != JobDone || v.Rec.LibPath != low || !better(v.Rec.Quality, v.Rec.LibQuality) {
		t.Errorf("nyc: %v %+v (%s)", v.State, v.Rec, v.Reason)
	}
	if isWithin(v.Rec.Path, libDir) || !isWithin(v.Rec.Path, m.Config().OutputDir) {
		t.Errorf("downloaded into %s", v.Rec.Path)
	}
	if out["stella"].State != JobDone {
		t.Errorf("stella: %v", out["stella"].State)
	}
	if n := src.searched.Load(); n != 2 {
		t.Errorf("searched %d tracks, want 2", n)
	}
	if after := snapshotDir(t, libDir); after != before {
		t.Errorf("the library changed:\nbefore %s\nafter  %s", before, after)
	}

	// A library copy that's below the threshold and no better offer: not downloaded.
	m2 := testManager(t, tools, []Source{src}, testLibrary(nyc), func(c *Config) {
		c.LibraryDirs, c.Threshold = []string{libDir}, "lossless"
	})
	src.results["nyc"][0].Quality = Quality{Codec: "mp3", Kbps: 96}
	if v := runManager(t, m2)["nyc"]; v.State != JobFailed || !strings.Contains(v.Reason, "not better") {
		t.Errorf("nyc with an equal offer: %v %s", v.State, v.Reason)
	}
	// Restarting a track that's in the library downloads it anyway.
	m3 := testManager(t, tools, []Source{src}, testLibrary(lucky), func(c *Config) { c.LibraryDirs = []string{libDir} })
	runManager(t, m3)
	m3.Restart("lucky")
	waitIdle(t, m3)
	if v, _ := m3.JobView("lucky"); v.State != JobDone {
		t.Errorf("forced download: %v %s", v.State, v.Reason)
	}
}

func snapshotDir(t *testing.T, dir string) string {
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := d.Info()
		data, _ := os.ReadFile(p)
		fmt.Fprintf(&b, "%s:%d:%d:%x;", p, info.Size(), info.ModTime().UnixNano(), sha1.Sum(data))
		return nil
	})
	return b.String()
}

func TestConfigKeepsLibraryAndDownloadsApart(t *testing.T) {
	lib := t.TempDir()
	for _, out := range []string{lib, filepath.Join(lib, "Downloads")} {
		c := defaultConfig()
		c.OutputDir, c.LibraryDirs = out, []string{lib}
		if err := c.validate(); err == nil {
			t.Errorf("downloads in %s accepted", out)
		}
	}
	c := defaultConfig()
	c.OutputDir, c.LibraryDirs = lib, []string{filepath.Join(lib, "Music")}
	if err := c.validate(); err == nil {
		t.Error("library inside the downloads folder accepted")
	}
	c.OutputDir = filepath.Join(filepath.Dir(lib), "elsewhere")
	if err := c.validate(); err != nil {
		t.Error(err)
	}
}

func TestCancelAndRestartWhileSearching(t *testing.T) {
	tools := needFFmpeg(t)
	a := getLucky()
	a.Key, a.DurationMs = "a", 4000
	slow := &slowSource{fakeSource: fakeSource{name: "youtube", t: t, results: map[string][]Candidate{"a": found("youtube", "Get Lucky", "Daft Punk", 4)}},
		release: make(chan struct{})}
	m := testManager(t, tools, []Source{slow}, testLibrary(a), nil)
	m.ready.Store(true)
	m.wg.Add(1)
	go func() { defer m.wg.Done(); m.dispatchLoop() }()
	m.wake()
	waitFor(t, func() bool { v, _ := m.JobView("a"); return v.State == JobSearching })
	m.Cancel("a")
	waitFor(t, func() bool { v, _ := m.JobView("a"); return v.State == JobCancelled })
	if r := m.state.get("a"); r.Status != "cancelled" {
		t.Errorf("record %+v", r)
	}
	close(slow.release)
	m.Restart("a")
	waitFor(t, func() bool { v, _ := m.JobView("a"); return v.State == JobDone })
}

type slowSource struct {
	fakeSource
	release chan struct{}
}

func (s *slowSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.fakeSource.Search(ctx, tr)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}

func TestGatePriorityAndResize(t *testing.T) {
	ctx := context.Background()
	g := newGate(1)
	g.acquire(ctx, 0)
	order := make(chan int64, 3)
	for _, p := range []int64{1, 5, 3} {
		go func() { g.acquire(ctx, p); order <- p; g.release() }()
		time.Sleep(20 * time.Millisecond)
	}
	g.release()
	got := []int64{<-order, <-order, <-order}
	if fmt.Sprint(got) != "[5 3 1]" {
		t.Errorf("order %v", got)
	}
	g.acquire(ctx, 0)
	done := make(chan bool)
	go func() { g.acquire(ctx, 0); done <- true }()
	time.Sleep(20 * time.Millisecond)
	g.setLimit(2)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("raising the limit didn't let the waiter through")
	}
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := g.acquire(cctx, 0); err == nil {
		t.Error("acquired past the limit")
	}
	if used, limit, waiting := g.stats(); used != 2 || limit != 2 || waiting != 0 {
		t.Errorf("stats %d %d %d", used, limit, waiting)
	}
}

func TestFormatsConvertAndTag(t *testing.T) {
	tools := needFFmpeg(t)
	cases := []struct{ format, raw, want string }{
		{"mp3", ".webm", ".mp3"}, {"opus", ".webm", ".opus"}, {"m4a", ".m4a", ".m4a"},
		{"flac", ".mp3", ".flac"}, {"original", ".webm", ".opus"}, {"original", ".flac", ".flac"},
	}
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "color=red:s=32x32", "-frames:v", "1", cover).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, c := range cases {
		t.Run(c.format+c.raw, func(t *testing.T) {
			tr := track("Ünïcödé / Title?", []string{"Daft Punk", "Pharrell Williams"}, "Random Access Memories", 4)
			tr.Key = "t1"
			tr.CoverURL = "file-cover"
			src := &fakeSource{name: "youtube", ext: c.raw, t: t, results: map[string][]Candidate{"t1": found("youtube", "Ünïcödé / Title", "Daft Punk", 4)}}
			m := testManager(t, tools, []Source{src}, testLibrary(tr), func(cfg *Config) { cfg.Format = c.format })
			cc := coverCache(filepath.Join(m.dataDir, "tmp", "covers"))
			cc.entries["file-cover"] = &coverEntry{path: cover}
			cc.entries["file-cover"].once.Do(func() {})
			runManager(t, m)
			r := m.state.get("t1")
			if r.Status != "downloaded" {
				t.Fatalf("not downloaded: %s", r.Reason)
			}
			if want := filepath.Join(m.Config().OutputDir, "Daft Punk/Random Access Memories/08 - Ünïcödé _ Title_"+c.want); r.Path != want {
				t.Fatalf("path %q, want %q", r.Path, want)
			}
			tags := ffprobeTags(t, r.Path)
			if tags["title"] != "Ünïcödé / Title?" || tags["artist"] != "Daft Punk; Pharrell Williams" || tags["_cover"] != "yes" ||
				tags["comment"] != "https://open.spotify.com/track/xyz" || tags["album"] != "Random Access Memories" {
				t.Errorf("tags: %v", tags)
			}
		})
	}
}

func TestBandcampZipExtractionAndStat(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"Artist - Album - 01 First Song.mp3": "one", "Artist - Album - 02 Second Song.mp3": "two", "cover.jpg": "img"} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	dir := t.TempDir()
	for _, c := range []struct {
		num   int
		title string
		want  string
	}{{2, "Second Song", "two"}, {0, "First Song", "one"}} {
		p, err := extractTrack(zr, dir, c.num, c.title)
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(p); string(data) != c.want {
			t.Errorf("track %d: got %q", c.num, data)
		}
	}
	if _, err := extractTrack(zr, dir, 0, "Completely Different"); err == nil {
		t.Error("expected no match")
	}
	if s := parseStatResponse([]byte(`cb({"result":"ok","download_url":"https://p4.bcbits.com/x"})`)); s.DownloadURL != "https://p4.bcbits.com/x" {
		t.Errorf("stat: %+v", s)
	}
}
