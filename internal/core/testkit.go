package core

// Helpers for tests, here and in the app's frontends (which test against a real backend):
// a manager working through a small library with fake sources, test tones, sample tracks.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func MakeTrack(title string, artists []string, album string, seconds float64) *Track {
	return &Track{Key: "k-" + title, Title: title, Artists: artists, Album: album, DurationMs: int(seconds * 1000),
		TrackNumber: 8, TotalTracks: 13, ReleaseDate: "2013-05-17", SpotifyURL: "https://open.spotify.com/track/xyz"}
}

func GetLucky() *Track {
	return MakeTrack("Get Lucky", []string{"Daft Punk"}, "Random Access Memories", 248)
}

func NeedFFmpeg(t testing.TB) *Tools {
	t.Helper()
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg not installed")
	}
	return &Tools{FFmpeg: ff, FFprobe: fp}
}

// sine writes a short test tone; the codec follows the extension.
func sine(t testing.TB, path string, seconds float64) string {
	t.Helper()
	codec := map[string][]string{".webm": {"-c:a", "libopus"}, ".m4a": {"-c:a", "aac"}, ".mp3": {"-c:a", "libmp3lame"},
		".flac": {"-c:a", "flac"}, ".ogg": {"-c:a", "libvorbis"}}[filepath.Ext(path)]
	args := append([]string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%g", seconds)}, codec...)
	if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

type FakeSource struct {
	SrcName     string
	Results     map[string][]Candidate
	searchErr   error
	downloadErr error
	ext         string
	length      float64 // >0: downloaded file length regardless of the candidate
	T           testing.TB
	searched    atomic.Int32
}

func (f *FakeSource) Name() string  { return f.SrcName }
func (f *FakeSource) Label() string { return f.SrcName }

func (f *FakeSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	f.searched.Add(1)
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.Results[tr.Key], nil
}

func (f *FakeSource) Download(ctx context.Context, c Candidate, tr *Track, workdir string) (string, error) {
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
	return sine(f.T, filepath.Join(workdir, "raw"+ext), length), nil
}

func Found(src, title, artist string, seconds float64) []Candidate {
	return []Candidate{{Source: src, URL: "https://example.com/" + strings.ReplaceAll(title, " ", "-"), Title: title,
		Artist: artist, Duration: seconds, ArtistURL: "https://example.com/" + artist}}
}

func TestLibrary(tracks ...*Track) *Library {
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
func TestManager(t testing.TB, tools *Tools, sources []Source, lib *Library, mutate func(*Config)) *Manager {
	t.Helper()
	dir := t.TempDir()
	cfg := defaultConfig()
	cfg.OutputDir = filepath.Join(dir, "music")
	cfg.Format, cfg.Enrich, cfg.SearchWorkers = "mp3", false, 2
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	m := NewManager(filepath.Join(dir, "config.json"), data, cfg)
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
		m.settling.Wait() // they write into the data folder, which is about to go
		m.cancel()
		m.wg.Wait()
		st.close()
	})
	return m
}

// runManager works until nothing is left to do and returns every job.
func RunManager(t testing.TB, m *Manager) map[string]JobView {
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

func waitIdle(t testing.TB, m *Manager) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	for deadline := time.Now().Add(90 * time.Second); !m.Idle(); time.Sleep(30 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still busy: %s", m.SummaryLine())
		}
	}
}

func WaitFor(t testing.TB, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
	}
}
