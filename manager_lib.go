package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The playlists (imported from your files), the jobs made from them, and the library check.

// importInputs reads every enabled playlist file again and rebuilds the jobs.
func (m *Manager) importInputs() {
	m.importing.Store(true)
	defer m.importing.Store(false)
	cfg := m.Config()
	lib := newLibrary()
	errs := map[string]string{}
	for _, in := range cfg.Inputs {
		lib.Inputs = append(lib.Inputs, in.Path)
		if !in.Enabled {
			continue
		}
		if err := importInput(m.ctx, lib, in.Path, debugf); err != nil {
			errs[in.Path] = err.Error()
			logf("Couldn't read %s: %v", in.Path, err)
		}
	}
	m.libMu.Lock()
	if old := m.full; old != nil {
		for k, t := range lib.Tracks { // keep earlier Deezer lookups
			if o := old.Tracks[k]; o != nil && o.Enriched {
				t.fillFrom(o)
				t.DeezerArtistURL, t.Enriched = firstNonEmpty(t.DeezerArtistURL, o.DeezerArtistURL), true
			}
		}
	}
	m.full, m.inputErrs = lib, errs
	m.libMu.Unlock()
	m.saveLibrary()
	m.rebuildJobs()
	if len(cfg.Inputs) > 0 {
		logf("Playlists: %d tracks in %d playlists", len(lib.Tracks), len(lib.Playlists))
	}
}

func (m *Manager) saveLibrary() {
	m.libMu.RLock()
	defer m.libMu.RUnlock()
	if m.full != nil {
		if err := m.full.save(filepath.Join(m.dataDir, "library.json")); err != nil {
			debugf("saving library.json: %v", err)
		}
	}
}

// enabledView is the library limited to the enabled playlists.
func (m *Manager) enabledView(cfg Config) *Library {
	m.libMu.RLock()
	defer m.libMu.RUnlock()
	var disabled []string
	for _, p := range m.full.Playlists {
		if cfg.Playlists[p.Name].Disabled {
			disabled = append(disabled, p.Name)
		}
	}
	return m.full.filtered(nil, disabled, false)
}

// rebuildJobs makes a job for every track of the enabled playlists, keeping the jobs that
// already exist. Tracks no longer in any enabled playlist are dropped (and stopped).
func (m *Manager) rebuildJobs() {
	cfg := m.Config()
	lib := m.enabledView(cfg)
	recs := m.state.all()
	members := map[string][]string{}
	prio := map[string]int{}
	for _, p := range lib.Playlists {
		pr := cfg.Playlists[p.Name].Priority
		for _, k := range p.Keys {
			if _, ok := prio[k]; !ok || pr > prio[k] {
				prio[k] = pr
			}
			members[k] = append(members[k], p.Name)
		}
	}
	m.jobsMu.Lock()
	jobs := make(map[string]*Job, len(lib.Tracks))
	order := make([]*Job, 0, len(lib.Tracks))
	for i, t := range lib.ordered() {
		j := m.jobs[t.Key]
		if j == nil {
			j = &Job{Track: t, tried: map[string]bool{}}
			m.initJob(j, recs[t.Key])
		}
		j.mu.Lock()
		if !j.state.active() {
			j.Track = t
		}
		j.idx, j.playlists, j.prio = i, members[t.Key], prio[t.Key]
		j.mu.Unlock()
		jobs[t.Key] = j
		order = append(order, j)
	}
	for k, j := range m.jobs {
		if jobs[k] == nil {
			j.mu.Lock()
			if j.cancel != nil {
				j.cancelWhy = "remove"
				j.cancel()
			}
			j.gen++
			j.mu.Unlock()
		}
	}
	m.jobs, m.order = jobs, order
	m.jobsMu.Unlock()
	m.reevaluateLibrary()
	m.dirty.Store(true)
	m.wake()
}

func (m *Manager) initJob(j *Job, rec Record) {
	switch rec.Status {
	case "downloaded":
		if _, err := os.Stat(rec.Path); err == nil && filepath.IsAbs(rec.Path) {
			j.state, j.rec = JobDone, rec
			return
		}
		j.state = JobQueued
	case "failed":
		j.state, j.lastErr = JobFailed, rec.Reason
	case "cancelled":
		j.state, j.lastErr = JobCancelled, rec.Reason
	default: // new, or a source was unreachable last time
		j.state = JobQueued
	}
}

// reevaluateLibrary checks the jobs not yet downloaded against the library: tracks you have in
// good enough quality are skipped; ones you have in lower quality are only downloaded if a
// source offers better.
func (m *Manager) reevaluateLibrary() {
	if m.index == nil {
		return
	}
	th := Threshold(m.Config().Threshold)
	m.jobsMu.Lock()
	list := append([]*Job(nil), m.order...)
	m.jobsMu.Unlock()
	n := 0
	for _, j := range list {
		j.mu.Lock()
		st, force, t := j.state, j.force, j.Track
		j.mu.Unlock()
		if st.active() || st == JobDone || st == JobCancelled || force {
			continue
		}
		matches := m.index.Match(t)
		j.mu.Lock()
		if j.state != st { // changed meanwhile
			j.mu.Unlock()
			continue
		}
		j.lib = matches
		j.needBetter = len(matches) > 0
		switch {
		case len(matches) > 0 && th.meets(matches[0].Q):
			if j.state != JobInLibrary {
				n++
			}
			j.state = JobInLibrary
		case j.state == JobInLibrary:
			j.state = JobQueued
		}
		j.mu.Unlock()
	}
	if n > 0 {
		debugf("library check: %d more tracks already in your library", n)
	}
	m.wake()
}

// scanLibrary reads the library folders; no new downloads start meanwhile.
func (m *Manager) scanLibrary(announce bool) {
	cfg := m.Config()
	if len(cfg.LibraryDirs) == 0 {
		m.reevaluateLibrary()
		return
	}
	m.scanMu.Lock()
	if m.scanStop != nil {
		m.scanStop()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.scanStop = cancel
	m.scanMu.Unlock()
	wasReady := m.ready.Swap(false)
	defer func() {
		if wasReady {
			m.ready.Store(true)
			m.wake()
		}
	}()
	logf("Scanning your library: %s", strings.Join(cfg.LibraryDirs, ", "))
	if err := m.index.Scan(ctx, cfg.LibraryDirs); err != nil {
		if ctx.Err() == nil {
			logf("Library scan failed: %v", err)
		}
		return
	}
	st := m.index.Status()
	msg := fmt.Sprintf("Library: %d audio files (scanned in %s)", st.Files, st.Took.Round(100*time.Millisecond))
	if guessed := st.Files - st.ReadFromTags; guessed > 0 {
		msg += fmt.Sprintf("; %d without usable tags were matched by file name", guessed)
	}
	if st.Err != "" {
		msg += "; " + st.Err
	}
	logf("%s", msg)
	m.reevaluateLibrary()
}

// Rescan scans the library again (in the background).
func (m *Manager) Rescan() {
	go m.scanLibrary(true)
}

// Reimport reads the playlist files again (in the background).
func (m *Manager) Reimport() {
	go m.importInputs()
}

func (ix *LibIndex) restrict(roots []string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if len(roots) == 0 {
		ix.byArtist, ix.byTitle, ix.byISRC = map[string][]*LibFile{}, map[string][]*LibFile{}, map[string][]*LibFile{}
		return
	}
	ix.build(roots)
}

// --- status for the UI ------------------------------------------------------------------------

type PlaylistStatus struct {
	Name                                      string
	Enabled                                   bool
	Priority                                  int
	Tracks, Done, InLib, Failed, Todo, Active int
}

func (m *Manager) Playlists() []PlaylistStatus {
	cfg := m.Config()
	m.libMu.RLock()
	pls := append([]*Playlist(nil), m.full.Playlists...)
	m.libMu.RUnlock()
	m.jobsMu.Lock()
	jobs := m.jobs
	states := map[string]JobState{}
	for k, j := range jobs {
		j.mu.Lock()
		states[k] = j.state
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	var out []PlaylistStatus
	for _, p := range pls {
		pc := cfg.Playlists[p.Name]
		s := PlaylistStatus{Name: p.Name, Enabled: !pc.Disabled, Priority: pc.Priority, Tracks: len(p.Keys)}
		for _, k := range p.Keys {
			st, ok := states[k]
			if !ok {
				continue
			}
			switch {
			case st == JobDone:
				s.Done++
			case st == JobInLibrary:
				s.InLib++
			case st == JobFailed || st == JobCancelled:
				s.Failed++
			case st.active():
				s.Active++
				s.Todo++
			default:
				s.Todo++
			}
		}
		out = append(out, s)
	}
	return out
}

type InputStatus struct {
	Path    string
	Enabled bool
	Err     string
}

func (m *Manager) Inputs() []InputStatus {
	cfg := m.Config()
	m.libMu.RLock()
	defer m.libMu.RUnlock()
	var out []InputStatus
	for _, in := range cfg.Inputs {
		out = append(out, InputStatus{Path: in.Path, Enabled: in.Enabled, Err: m.inputErrs[in.Path]})
	}
	return out
}

func (m *Manager) LibraryStatus() LibScanStatus {
	if m.index == nil {
		return LibScanStatus{}
	}
	return m.index.Status()
}

// --- reports ----------------------------------------------------------------------------------

// writeReports writes the reports and .m3u8 playlists into the downloads folder.
func (m *Manager) writeReports() {
	if m.state == nil || m.full == nil {
		return
	}
	cfg := m.Config()
	lib := m.enabledView(cfg)
	if len(lib.Tracks) == 0 {
		return
	}
	views := map[string]JobView{}
	for _, v := range m.Jobs() {
		views[v.Key] = v
	}
	rep := buildReport(lib, views)
	if _, err := writeReports(rep, filepath.Join(cfg.OutputDir, "reports")); err != nil {
		debugf("writing the reports: %v", err)
		return
	}
	if _, err := writePlaylists(lib, views, filepath.Join(cfg.OutputDir, "Playlists")); err != nil {
		debugf("writing the playlists: %v", err)
	}
}

func sortViews(views []JobView, by string) {
	sort.SliceStable(views, func(a, b int) bool {
		x, y := views[a], views[b]
		switch by {
		case "artist":
			return strings.ToLower(x.Track.ArtistStr()+x.Track.Title) < strings.ToLower(y.Track.ArtistStr()+y.Track.Title)
		case "state":
			if x.State != y.State {
				return stateOrder[x.State] < stateOrder[y.State]
			}
		}
		return x.idx < y.idx
	})
}

var stateOrder = map[JobState]int{JobDownloading: 0, JobReady: 1, JobSearching: 2, JobQueued: 3, JobWaiting: 4,
	JobFailed: 5, JobCancelled: 6, JobDone: 7, JobInLibrary: 8}
