package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// The engine runs each track through the sources in order, then converts, tags and files
// the result.

type Settings struct {
	MusicDir       string
	Format         string
	Template       string
	Workers        int
	Limit          int
	RetryFailed    bool
	TriesPerSource int
	MaxOutages     int // after this many "unreachable" errors in a row a source is skipped for the run
}

type Outcome struct {
	Track  *Track
	Status string // downloaded | failed | error | aborted
	Detail string // where it came from, or why it failed
}

type Engine struct {
	set     Settings
	sources []Source
	state   *State
	tools   *Tools
	workDir string
	covers  *CoverCache

	pathMu  sync.Mutex
	claimed map[string]string

	outageMu sync.Mutex
	outages  map[string]int
}

func newEngine(set Settings, sources []Source, state *State, tools *Tools, workDir string) *Engine {
	return &Engine{set: set, sources: sources, state: state, tools: tools, workDir: workDir,
		covers: newCoverCache(filepath.Join(workDir, "covers")), claimed: map[string]string{}, outages: map[string]int{}}
}

func (e *Engine) fileExists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.set.MusicDir, filepath.FromSlash(rel)))
	return err == nil
}

// pending lists tracks to work on: new ones, ones whose file vanished, ones that hit a
// network error last time, and (with RetryFailed) ones that found no match.
func (e *Engine) pending(lib *Library) []*Track {
	var todo []*Track
	for _, t := range lib.ordered() {
		r := e.state.get(t.Key)
		switch {
		case r == nil, r.Status == "error":
			todo = append(todo, t)
		case r.Status == "downloaded":
			if !e.fileExists(r.Path) {
				todo = append(todo, t)
			}
		case e.set.RetryFailed:
			todo = append(todo, t)
		}
	}
	if e.set.Limit > 0 && len(todo) > e.set.Limit {
		todo = todo[:e.set.Limit]
	}
	return todo
}

// run processes tracks with a worker pool. Closing stop stops handing out new tracks;
// cancelling ctx also kills the ones in progress (those aren't recorded).
func (e *Engine) run(ctx context.Context, stop <-chan struct{}, tracks []*Track, onDone func(Outcome)) map[string]int {
	jobs := make(chan *Track)
	results := make(chan Outcome)
	var wg sync.WaitGroup
	for i := 0; i < max(1, e.set.Workers); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				results <- e.safeProcess(ctx, t)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, t := range tracks {
			select {
			case jobs <- t:
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	counts := map[string]int{}
	for o := range results {
		counts[o.Status]++
		onDone(o)
	}
	return counts
}

func (e *Engine) safeProcess(ctx context.Context, t *Track) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			debugf("panic while processing %s: %v", t, r)
			reason := fmt.Sprintf("internal error: %v", r)
			e.state.markFailed(t.Key, reason, true)
			out = Outcome{t, "error", reason}
		}
	}()
	return e.process(ctx, t)
}

func (e *Engine) process(ctx context.Context, t *Track) Outcome {
	workdir := filepath.Join(e.workDir, t.Key)
	os.RemoveAll(workdir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return Outcome{t, "error", err.Error()}
	}
	defer os.RemoveAll(workdir)

	var reasons []string
	transient := false
	note := func(s Source, err error) {
		reasons = append(reasons, s.Label()+": "+err.Error())
		if isUnavailable(err) {
			transient = true
		}
	}
	for _, src := range e.sources {
		if e.isDown(src) {
			reasons = append(reasons, src.Label()+": skipped, it kept failing to respond earlier in this run")
			transient = true
			continue
		}
		cands, err := src.Search(ctx, t)
		if ctx.Err() != nil {
			return Outcome{t, "aborted", ""}
		}
		if err != nil {
			note(src, err)
			e.recordOutage(src, err)
			continue
		}
		e.recordOutage(src, nil)
		accepted, rejected := rank(t, cands)
		debugf("%s / %s: %d accepted, %d rejected", t, src.Name(), len(accepted), len(rejected))
		if len(accepted) == 0 {
			reasons = append(reasons, src.Label()+": "+describeRejection(rejected))
			continue
		}
		for _, m := range accepted[:min(len(accepted), max(1, e.set.TriesPerSource))] {
			raw, err := src.Download(ctx, m.Cand, t, workdir)
			if err == nil {
				err = e.finalize(ctx, raw, t, m.Cand, m.Score, workdir)
			}
			if ctx.Err() != nil {
				return Outcome{t, "aborted", ""}
			}
			if err != nil {
				note(src, err)
				if isUnavailable(err) {
					break
				}
				continue
			}
			debugf("%s <- %s %s (score %.0f)", t, src.Name(), m.Cand.URL, m.Score)
			return Outcome{t, "downloaded", src.Label()}
		}
	}
	reason := strings.Join(reasons, "; ")
	if reason == "" {
		reason = "no sources enabled"
	}
	e.state.markFailed(t.Key, reason, transient)
	debugf("%s failed: %s", t, reason)
	status := "failed"
	if transient {
		status = "error"
	}
	return Outcome{t, status, reason}
}

// finalize checks the downloaded file's real length, converts and tags it, moves it into
// the music folder and records the result.
func (e *Engine) finalize(ctx context.Context, raw string, t *Track, c Candidate, score float64, workdir string) error {
	codec, length, err := probe(ctx, e.tools, raw)
	if err != nil {
		return err
	}
	if length > 0 && t.DurationMs > 0 && math.Abs(length-t.Duration()) > durationTolerance(t.Duration()) {
		return failf("%s turned out to be %s long instead of %s (preview clip or different cut)",
			c.URL, fmtDuration(length), fmtDuration(t.Duration()))
	}
	// Fill gaps the track list left (e.g. a song Deezer didn't know) from the matched source.
	if t.Album == "" {
		t.Album = firstNonEmpty(c.Album, t.Title) // a standalone single is its own album
	}
	if t.DurationMs == 0 && length > 0 {
		t.DurationMs = int(length * 1000)
	}
	ext := targetExtension(e.set.Format, codec)
	converted := filepath.Join(workdir, "final"+ext)
	if err := convertAndTag(ctx, e.tools, raw, converted, codec, t, e.covers.get(ctx, t.CoverURL)); err != nil {
		return err
	}
	rel := e.claim(relativePath(t, e.set.Template, ext), t.Key)
	dest := filepath.Join(e.set.MusicDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := moveFile(converted, dest); err != nil {
		return err
	}
	e.state.put(Record{Key: t.Key, Status: "downloaded", Path: rel, Source: c.Source, SourceURL: c.URL,
		SourceTitle: c.Title, SourceArtist: c.Artist, SourceArtistURL: c.ArtistURL, SourceAlbum: c.Album, Score: score})
	return nil
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src) // different filesystem
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}

// claim reserves a file path for a track, adding " (2)" etc. if another track has it.
func (e *Engine) claim(rel, key string) string {
	e.pathMu.Lock()
	defer e.pathMu.Unlock()
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	candidate := rel
	for n := 2; ; n++ {
		owner := e.claimed[candidate]
		if owner == "" {
			owner = e.state.ownerOf(candidate)
		}
		if owner == key || (owner == "" && !e.fileExists(candidate)) {
			e.claimed[candidate] = key
			return candidate
		}
		candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
	}
}

func (e *Engine) isDown(s Source) bool {
	e.outageMu.Lock()
	defer e.outageMu.Unlock()
	return e.outages[s.Name()] >= e.set.MaxOutages
}

func (e *Engine) recordOutage(s Source, err error) {
	e.outageMu.Lock()
	defer e.outageMu.Unlock()
	if err == nil || !isUnavailable(err) {
		if err == nil {
			e.outages[s.Name()] = 0
		}
		return
	}
	e.outages[s.Name()]++
	if e.outages[s.Name()] == e.set.MaxOutages {
		logf("%s failed to respond %d times in a row; skipping it for the rest of this run (%v)", s.Label(), e.set.MaxOutages, err)
	}
}
