package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
		if err := importInput(m.ctx, lib, in.Path, m.resolver, debugf); err != nil {
			errs[in.Path] = err.Error()
			Logf("Couldn't read %s: %v", in.Path, err)
		}
	}
	m.mergeAdditions(lib)
	lib.trim()
	m.libMu.Lock()
	if old := m.full; old != nil {
		for k, t := range lib.Tracks { // keep earlier Deezer lookups
			if o := old.Tracks[k]; o != nil && o.Enriched {
				t.fillFrom(o)
				t.DeezerArtistURL, t.Enriched = FirstNonEmpty(t.DeezerArtistURL, o.DeezerArtistURL), true
			}
		}
	}
	m.full, m.inputErrs = lib, errs
	m.libMu.Unlock()
	m.saveLibrary()
	m.rebuildJobs()
	if len(cfg.Inputs) > 0 {
		msg := fmt.Sprintf("Playlists: %d tracks in %d playlists", len(lib.Tracks), len(lib.Playlists))
		if lib.merged > 0 {
			msg += fmt.Sprintf(" (%d repeats of a song under another ID merged)", lib.merged)
		}
		Logf("%s", msg)
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
// already exist, and checks them against the library. Tracks no longer in any enabled
// playlist are dropped (and stopped).
func (m *Manager) rebuildJobs() {
	m.makeJobs()
	m.reevaluateLibrary()
	m.dirty.Store(true)
	m.revs.queue.Add(1)
	m.wake()
}

// makeJobs is rebuildJobs without the library check.
func (m *Manager) makeJobs() {
	cfg := m.Config()
	lib := m.enabledView(cfg)
	recs := m.state.all()
	members := map[string][]string{}
	prio := map[string]int{}
	upTh := m.upgradeThresholds()
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
	for i, t := range lib.Ordered() {
		j := m.jobs[t.Key]
		if j == nil {
			j = &Job{Track: t}
			m.initJob(j, recordFor(recs, t))
		}
		j.mu.Lock()
		if !j.state.Active() {
			j.Track = t
		}
		j.idx, j.playlists, j.prio = i, members[t.Key], prio[t.Key]
		j.minQuality = ""
		for _, p := range j.playlists {
			if th, ok := upTh[p]; ok && (j.minQuality == "" || th.min() > j.minQuality.min()) {
				j.minQuality = th
			}
		}
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
}

// recordFor is the track's own record, or a download made earlier under the key of another
// listing of the song that has since been merged into it.
func recordFor(recs map[string]Record, t *Track) Record {
	rec := recs[t.Key]
	if rec.Status == "downloaded" {
		return rec
	}
	for _, k := range t.Also {
		if r := recs[k]; r.Status == "downloaded" && filepath.IsAbs(r.Path) {
			if _, err := os.Stat(r.Path); err == nil {
				return r
			}
		}
	}
	return rec
}

func (m *Manager) initJob(j *Job, rec Record) {
	switch rec.Status {
	case "downloaded":
		if _, err := os.Stat(rec.Path); err == nil && filepath.IsAbs(rec.Path) {
			j.state, j.rec = JobDone, &rec
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
// source offers better. With tens of thousands of tracks this is a lot of fuzzy matching, so it
// runs on several cores (and one check at a time); it stops when the app does.
func (m *Manager) reevaluateLibrary() {
	if m.index == nil {
		return
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()
	th := Threshold(m.Config().Threshold)
	m.jobsMu.Lock()
	list := append([]*Job(nil), m.order...)
	m.jobsMu.Unlock()
	m.checkDone.Store(0)
	m.checkTotal.Store(int64(len(list)))
	defer m.checkTotal.Store(0)
	start := time.Now()
	var next, n atomic.Int64
	var wg sync.WaitGroup
	m.index.beginCheck()
	defer func() {
		m.index.endCheck()
		releaseMemory()
	}()
	for range max(1, runtime.NumCPU()/2) { // leaves room for everything else on the computer
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m.ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(list) {
					return
				}
				if m.checkJob(list[i], th) {
					n.Add(1)
				}
				m.checkDone.Add(1)
			}
		}()
	}
	wg.Wait()
	debugf("library check of %d tracks took %s; %d more already in your library", len(list), time.Since(start).Round(time.Millisecond), n.Load())
	m.wake()
}

// checkJob looks for the job's track in the library. It reports whether the track is now
// newly "in library".
func (m *Manager) checkJob(j *Job, th Threshold) bool {
	j.mu.Lock()
	st, force, t := j.state, j.force, j.Track
	if j.minQuality != "" && j.minQuality.min() > th.min() {
		th = j.minQuality // an upgrade asks for more than your threshold
	}
	j.mu.Unlock()
	if st.Active() || st == JobDone || st == JobCancelled || force {
		return false
	}
	matches := m.index.Match(t)
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != st { // changed meanwhile
		return false
	}
	j.lib = matches
	j.needBetter = len(matches) > 0
	switch {
	case len(matches) > 0 && th.meets(matches[0].Q):
		newly := j.state != JobInLibrary
		j.state = JobInLibrary
		return newly
	case j.state == JobInLibrary:
		j.state = JobQueued
	}
	return false
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
	Logf("Scanning your library: %s", strings.Join(cfg.LibraryDirs, ", "))
	if err := m.index.Scan(ctx, cfg.LibraryDirs); err != nil {
		if ctx.Err() == nil {
			Logf("Library scan failed: %v", err)
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
	Logf("%s", msg)
	m.reevaluateLibrary()
	if m.slskClient() != nil {
		m.shareFolders(m.Config(), true) // the shared files' details come from the scan
	}
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

// Finished says the app has nothing left to do for this playlist: every song was downloaded, is
// in your library, or couldn't be found. One where nothing at all was found isn't finished.
func (s PlaylistStatus) Finished() bool {
	return s.Enabled && s.Done+s.InLib > 0 && s.Done+s.InLib+s.Failed == s.Tracks
}

func (m *Manager) Playlists() []PlaylistStatus {
	cfg := m.Config()
	m.libMu.RLock()
	pls := append([]*Playlist(nil), m.full.Playlists...)
	m.libMu.RUnlock()
	var out []PlaylistStatus
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	for _, p := range pls {
		pc := cfg.Playlists[p.Name]
		s := PlaylistStatus{Name: p.Name, Enabled: !pc.Disabled, Priority: pc.Priority, Tracks: len(p.Keys)}
		for _, k := range p.Keys {
			j := m.jobs[k]
			if j == nil {
				continue
			}
			j.mu.Lock()
			st := j.state
			j.mu.Unlock()
			switch {
			case st == JobDone:
				s.Done++
			case st == JobInLibrary:
				s.InLib++
			case st == JobFailed || st == JobCancelled:
				s.Failed++
			case st.Active():
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
	st := m.index.Status()
	st.Known = m.index.Size()
	return st
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

var stateOrder = map[JobState]int{JobDownloading: 0, JobReady: 1, JobSearching: 2, JobQueued: 3, JobWaiting: 4,
	JobFailed: 5, JobCancelled: 6, JobDone: 7, JobInLibrary: 8}
