package core

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blockingSource finds every track; its downloads hang until cancelled, while block is set.
type blockingSource struct {
	FakeSource
	block     atomic.Bool
	started   atomic.Int32
	cancelled atomic.Int32
}

func (b *blockingSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	b.searched.Add(1)
	return Found(b.SrcName, tr.Title, tr.Artists[0], tr.Duration()), nil
}

func (b *blockingSource) Download(ctx context.Context, c Candidate, tr *Track, workdir string) (string, error) {
	b.started.Add(1)
	if b.block.Load() {
		<-ctx.Done()
		b.cancelled.Add(1)
		return "", ctx.Err()
	}
	return b.FakeSource.Download(ctx, c, tr, workdir)
}

func startManager(m *Manager) {
	m.ready.Store(true)
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.dispatchLoop() }()
	go func() { defer m.wg.Done(); m.governor() }()
	m.wake()
}

// P used to only keep new tracks from starting: everything under way went on, for hours.
func TestPauseStopsWhatsInProgress(t *testing.T) {
	tools := NeedFFmpeg(t)
	var tracks []*Track
	for i := 0; i < 4; i++ {
		tr := MakeTrack(fmt.Sprintf("Song %d", i), []string{"Band"}, "", 4)
		tr.Key = fmt.Sprintf("t%d", i)
		tracks = append(tracks, tr)
	}
	empty := &FakeSource{SrcName: "archive", T: t}
	src := &blockingSource{FakeSource: FakeSource{SrcName: "youtube", T: t}}
	src.block.Store(true)
	m := TestManager(t, tools, []Source{empty, src}, TestLibrary(tracks...), func(c *Config) { c.SearchWorkers, c.MaxDownloads = 4, 4 })
	startManager(m)
	WaitFor(t, func() bool { return src.started.Load() == 4 })

	m.SetPaused(true)
	WaitFor(t, func() bool { return src.cancelled.Load() == 4 && m.Overview().Counts[JobQueued] == 4 })
	time.Sleep(300 * time.Millisecond)
	if n := src.started.Load(); n != 4 {
		t.Errorf("%d downloads started while paused", n)
	}
	if o := m.Overview(); o.Counts[JobQueued] != 4 || o.Counts[JobSearching]+o.Counts[JobDownloading] != 0 {
		t.Errorf("while paused: %v", o.Counts)
	}

	src.block.Store(false)
	m.SetPaused(false)
	waitIdle(t, m)
	for _, v := range m.Jobs() {
		if v.State != JobDone {
			t.Errorf("%s: %v %s", v.Key, v.State, v.Reason)
		}
	}
	// They went on from the source they were at: the first one wasn't searched again.
	if n := empty.searched.Load(); n != 4 {
		t.Errorf("first source searched %d times", n)
	}
}

// A track waiting only for a paused source used to give up after 8 rounds ("gave up after
// several tries"), although no source had failed; 14,000 tracks waited on Bandcamp that way.
func TestWaitingForAPausedSourceDoesntGiveUp(t *testing.T) {
	tr := MakeTrack("Song", []string{"Band"}, "", 4)
	tr.Key = "a"
	m := TestManager(t, &Tools{}, nil, TestLibrary(tr), nil)
	j := m.job("a")
	for i := 0; i < 20; i++ {
		m.finish(j, j.gen, result{state: JobWaiting, transient: true, retryAt: time.Now().Add(time.Hour)})
	}
	if v, _ := m.JobView("a"); v.State != JobWaiting {
		t.Fatalf("after 20 rounds of waiting: %v %s", v.State, v.Reason)
	}
	for i := 0; i < 9; i++ {
		m.finish(j, j.gen, result{state: JobWaiting, transient: true, counted: true})
	}
	if v, _ := m.JobView("a"); v.State != JobFailed || !strings.Contains(v.Reason, "gave up") {
		t.Fatalf("after 9 failing rounds: %v %s", v.State, v.Reason)
	}
}

// What the sources said in earlier rounds stays in the report; it used to say just the last
// round's ("Bandcamp: no results", or even "no sources enabled").
func TestReasonsAreKeptAcrossRounds(t *testing.T) {
	tools := NeedFFmpeg(t)
	tr := MakeTrack("Song", []string{"Band"}, "", 4)
	tr.Key = "a"
	first := &FakeSource{SrcName: "bandcamp", searchErr: blockedf("Bandcamp is rate-limiting us (HTTP 429)"), T: t}
	second := &FakeSource{SrcName: "archive", T: t}
	m := TestManager(t, tools, []Source{first, second}, TestLibrary(tr), nil)
	out := RunManager(t, m)
	if v := out["a"]; v.State != JobWaiting || !strings.Contains(v.Reason, "archive: no results") {
		t.Fatalf("round 1: %v %s", v.State, v.Reason)
	}
	m.PauseSource("bandcamp", false)
	first.searchErr = nil
	m.Prioritize("a")
	waitIdle(t, m)
	v, _ := m.JobView("a")
	if v.State != JobFailed || v.Reason != "archive: no results; bandcamp: no results" {
		t.Errorf("round 2: %v %q", v.State, v.Reason)
	}
	if second.searched.Load() != 1 {
		t.Errorf("archive searched %d times", second.searched.Load())
	}
}

// Requests under way when a source got paused each counted as a new failure: Internet
// Archive and SoundCloud went from a 2-minute pause to an hour within seconds.
func TestFailuresWhilePausedDontMakeThePauseLonger(t *testing.T) {
	m := TestManager(t, &Tools{}, nil, TestLibrary(MakeTrack("Song", []string{"Band"}, "", 4)), nil)
	src := &FakeSource{SrcName: "soundcloud"}
	for i := 0; i < 5; i++ {
		m.recordResult(src, unavailablef("HTTP Error 403"), true)
	}
	_, until, _ := m.cooling("soundcloud")
	if d := time.Until(until); d < time.Minute || d > 2*time.Minute {
		t.Fatalf("first pause: %s", d)
	}
	for i := 0; i < 50; i++ {
		m.recordResult(src, unavailablef("HTTP Error 403"), true)
		m.recordResult(src, blockedf("rate-limited"), true)
	}
	_, until2, _ := m.cooling("soundcloud")
	if !until2.Equal(until) {
		t.Errorf("pause went from %s to %s", until.Format(time.TimeOnly), until2.Format(time.TimeOnly))
	}
}

type busySource struct {
	FakeSource
	wait time.Duration
}

func (b *busySource) busy() time.Duration { return b.wait }

func TestBusySourceIsSkippedForNow(t *testing.T) {
	tools := NeedFFmpeg(t)
	tr := MakeTrack("Song", []string{"Band"}, "", 4)
	tr.Key = "a"
	busy := &busySource{FakeSource: FakeSource{SrcName: "bandcamp", T: t, Results: map[string][]Candidate{"a": Found("bandcamp", "Song", "Band", 4)}}, wait: time.Minute}
	other := &FakeSource{SrcName: "youtube", T: t, Results: map[string][]Candidate{"a": Found("youtube", "Song", "Band", 4)}}
	m := TestManager(t, tools, []Source{busy, other}, TestLibrary(tr), nil)
	out := RunManager(t, m)
	if v := out["a"]; v.State != JobDone || v.Rec.Source != "youtube" || busy.searched.Load() != 0 {
		t.Errorf("%v from %s, busy searched %d times", v.State, v.Rec.Source, busy.searched.Load())
	}
}

// Pausing stops a running torrent in aria2c (it keeps what it has) and resumes it.
func TestRealAria2cPause(t *testing.T) {
	aria2c, err := exec.LookPath("aria2c")
	if err != nil {
		t.Skip("aria2c not installed")
	}
	name := "Pause Artist - Pause Album"
	files := []string{"01 - Only Song.mp3"}
	contents := [][]byte{bytes.Repeat([]byte{'p'}, 400_000)}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, name), 0o755)
	os.WriteFile(filepath.Join(root, name, files[0]), contents[0], 0o644)
	var served atomic.Int64
	webseed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.FileServer(http.Dir(root)).ServeHTTP(throttled{w, &served}, r)
	}))
	defer webseed.Close()
	torrentFile := filepath.Join(t.TempDir(), "album.torrent")
	os.WriteFile(torrentFile, makeTorrent(name, files, contents, webseed.URL+"/"), 0o644)

	m := newTorrentManager(newAria2(aria2c), t.TempDir(), 2*time.Minute)
	m.gather = 100 * time.Millisecond
	defer m.close()
	task := &Task{Track: &Track{Title: "Only Song"}}
	done := make(chan error, 1)
	go func() {
		_, err := m.fetch(context.Background(), torrentFile, 0, t.TempDir(), task)
		done <- err
	}()
	WaitFor(t, func() bool { return served.Load() > 50_000 })
	m.setPaused(true)
	WaitFor(t, func() bool { return task.view().Stage == "paused" })
	time.Sleep(time.Second)
	before := served.Load()
	time.Sleep(1500 * time.Millisecond)
	if after := served.Load(); after-before > 100_000 {
		t.Errorf("%d bytes downloaded while paused", after-before)
	}
	select {
	case err := <-done:
		t.Fatalf("finished while paused: %v", err)
	default:
	}
	m.setPaused(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("didn't finish after resuming")
	}
}
