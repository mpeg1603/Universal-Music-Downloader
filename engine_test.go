package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
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

func testEngine(t *testing.T, tools *Tools, sources []Source, mutate func(*Settings)) (*Engine, *State, string) {
	dir := t.TempDir()
	state, err := openState(filepath.Join(dir, "state", "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.close)
	set := Settings{MusicDir: filepath.Join(dir, "music"), Format: "mp3", Template: defaultTemplate, Workers: 2, TriesPerSource: 2, MaxOutages: 5}
	if mutate != nil {
		mutate(&set)
	}
	return newEngine(set, sources, state, tools, filepath.Join(dir, "state", "tmp")), state, set.MusicDir
}

func runAll(e *Engine, lib *Library) map[string]Outcome {
	out := map[string]Outcome{}
	e.run(context.Background(), make(chan struct{}), e.pending(lib), func(o Outcome) { out[o.Track.Key] = o })
	return out
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
	e, state, music := testEngine(t, tools, []Source{bandcamp, ytm}, nil)
	lib := testLibrary(hit, miss)

	out := runAll(e, lib)
	if out["t1"].Status != "downloaded" || out["t2"].Status != "failed" {
		t.Fatalf("outcomes: %+v", out)
	}
	rec := state.get("t1")
	if rec.Path != "Daft Punk/Random Access Memories/08 - Get Lucky.mp3" {
		t.Fatalf("path %q", rec.Path)
	}
	tags := ffprobeTags(t, filepath.Join(music, rec.Path))
	if tags["title"] != "Get Lucky" || tags["album"] != "Random Access Memories" || tags["track"] != "8/13" || tags["comment"] == "" {
		t.Errorf("tags: %v", tags)
	}
	reason := state.get("t2").Reason
	if !strings.Contains(reason, "you can buy it at https://x.bandcamp.com") || !strings.Contains(reason, "closest was “Other Song”") {
		t.Errorf("reason: %s", reason)
	}

	// Nothing left to do; failed tracks only come back on request.
	if p := e.pending(lib); len(p) != 0 {
		t.Errorf("pending after run: %d", len(p))
	}
	e.set.RetryFailed = true
	if p := e.pending(lib); len(p) != 1 || p[0].Key != "t2" {
		t.Errorf("retry-failed pending: %v", p)
	}

	recs := state.all()
	rep := buildReport(lib, recs, music)
	files, err := writeReports(rep, filepath.Join(music, "reports"))
	if err != nil || len(files) != 3 {
		t.Fatal(err)
	}
	artists := readCSV(t, filepath.Join(music, "reports", "artists.csv"))
	if len(artists) != 1 || artists[0]["artist"] != "Daft Punk" || !strings.Contains(artists[0]["links"], "https://example.com/Daft Punk") {
		t.Errorf("artists.csv: %v", artists)
	}
	failed := readCSV(t, filepath.Join(music, "reports", "failed.csv"))
	cats := map[string]string{}
	for _, f := range failed {
		cats[f["title"]] = f["category"]
	}
	if cats["Nonexistent Song"] != "not_found" || cats["Some Podcast"] != "skipped" || len(failed) != 2 {
		t.Errorf("failed.csv: %v", failed)
	}
	md, _ := os.ReadFile(filepath.Join(music, "reports", "downloaded.md"))
	if !strings.Contains(string(md), "[Get Lucky](https://open.spotify.com/track/xyz)") {
		t.Errorf("downloaded.md:\n%s", md)
	}
	if _, err := writePlaylists(lib, recs, music); err != nil {
		t.Fatal(err)
	}
	m3u, _ := os.ReadFile(filepath.Join(music, "Playlists", "Road_Trip.m3u8"))
	if !strings.Contains(string(m3u), "../Daft Punk/Random Access Memories/08 - Get Lucky.mp3") {
		t.Errorf("playlist:\n%s", m3u)
	}
}

func TestUnreachableSourceRetriesAndCircuitBreaker(t *testing.T) {
	tools := needFFmpeg(t)
	var tracks []*Track
	for i := 0; i < 8; i++ {
		tr := track(fmt.Sprintf("Song %d", i), []string{"Band"}, "", 4)
		tr.Key = fmt.Sprintf("t%d", i)
		tracks = append(tracks, tr)
	}
	down := &fakeSource{name: "YouTube", searchErr: unavailablef("YouTube unreachable (timed out)"), t: t}
	e, state, music := testEngine(t, tools, []Source{down}, func(s *Settings) { s.Workers = 1 })
	lib := testLibrary(tracks...)
	runAll(e, lib)
	if down.searched.Load() != 5 {
		t.Errorf("searched %d times, want 5 before giving up on the source", down.searched.Load())
	}
	r := state.get("t7")
	if r.Status != "error" || !strings.Contains(r.Reason, "skipped") {
		t.Errorf("t7: %+v", r)
	}
	if len(e.pending(lib)) != 8 {
		t.Errorf("unreachable tracks should be retried next run")
	}
	if rep := buildReport(lib, state.all(), music); rep.Failed[0].Category != "source_error" {
		t.Errorf("category %s", rep.Failed[0].Category)
	}
}

func TestFallsThroughAndChecksRealLength(t *testing.T) {
	tools := needFFmpeg(t)
	a := getLucky()
	a.Key, a.DurationMs = "a", 4000
	b := track("Preview Song", []string{"Band"}, "", 60)
	b.Key = "b"
	broken := &fakeSource{name: "SoundCloud", downloadErr: failf("gone"), t: t, results: map[string][]Candidate{"a": found("soundcloud", "Get Lucky", "Daft Punk", 4)}}
	preview := &fakeSource{name: "Previews", length: 5, t: t, results: map[string][]Candidate{"b": found("x", "Preview Song", "Band", 60)}}
	good := &fakeSource{name: "YouTube", ext: ".m4a", t: t, results: map[string][]Candidate{"a": found("youtube", "Get Lucky", "Daft Punk", 4)}}
	e, state, _ := testEngine(t, tools, []Source{broken, preview, good}, nil)
	runAll(e, testLibrary(a, b))
	if r := state.get("a"); r.Source != "youtube" {
		t.Errorf("a: %+v", r)
	}
	if r := state.get("b"); r.Status != "failed" || !strings.Contains(r.Reason, "preview") {
		t.Errorf("b: %+v", r)
	}
}

func TestSamePathGetsNumbered(t *testing.T) {
	tools := needFFmpeg(t)
	a, b := getLucky(), getLucky()
	a.Key, b.Key, a.DurationMs, b.DurationMs = "a", "b", 4000, 4000
	src := &fakeSource{name: "YouTube", t: t, results: map[string][]Candidate{
		"a": found("youtube", "Get Lucky", "Daft Punk", 4), "b": found("youtube", "Get Lucky", "Daft Punk", 4)}}
	e, state, _ := testEngine(t, tools, []Source{src}, func(s *Settings) { s.Workers = 1 })
	runAll(e, testLibrary(a, b))
	got := state.get("a").Path + "|" + state.get("b").Path
	if got != "Daft Punk/Random Access Memories/08 - Get Lucky.mp3|Daft Punk/Random Access Memories/08 - Get Lucky (2).mp3" {
		t.Errorf("paths %s", got)
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
			src := &fakeSource{name: "YouTube", ext: c.raw, t: t, results: map[string][]Candidate{"t1": found("youtube", "Ünïcödé / Title", "Daft Punk", 4)}}
			e, state, music := testEngine(t, tools, []Source{src}, func(s *Settings) { s.Format = c.format })
			e.covers.entries[""] = nil
			tr.CoverURL = "file-cover"
			e.covers.entries["file-cover"] = &coverEntry{path: cover}
			e.covers.entries["file-cover"].once.Do(func() {})
			runAll(e, testLibrary(tr))
			r := state.get("t1")
			if r.Status != "downloaded" {
				t.Fatalf("not downloaded: %s", r.Reason)
			}
			if want := "Daft Punk/Random Access Memories/08 - Ünïcödé _ Title_" + c.want; r.Path != want {
				t.Fatalf("path %q, want %q", r.Path, want)
			}
			tags := ffprobeTags(t, filepath.Join(music, r.Path))
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
