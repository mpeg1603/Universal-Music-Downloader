package core

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// The numbers at a glance. What takes a walk through folders is worked out in the background
// at most once a minute; a frontend always gets the latest it has.

// DashboardView is everything a dashboard shows.
type DashboardView struct {
	Overview
	Counts map[string]int // tracks under each queue filter
	Parts  TrackParts
	Disk   DiskUsage
	Slsk   SoulseekView
}

// TrackParts counts tracks so that each one is in exactly one part.
type TrackParts struct {
	InLibrary, Downloaded, NeedUpgrade int
	Downloading, Searching             int
	Waiting, Retrying                  int
	NotFound, Cancelled                int
}

// DiskUsage is what the music takes up, and what's left.
type DiskUsage struct {
	Known                       bool // has been worked out at least once
	LibraryBytes, DownloadBytes int64
	LibraryFiles, DownloadFiles int
	CacheBytes                  int64 // the app's caches and temporary files
	Disks                       []DiskInfo
	Updated                     time.Time
}

// DiskInfo is one disk the music is on.
type DiskInfo struct {
	Path               string // a folder on it
	Total, Free        int64
	Library, Downloads int64
}

type dashState struct {
	busy atomic.Bool
	last atomic.Pointer[DiskUsage]
}

// Dashboard is the numbers right now.
func (m *Manager) Dashboard() DashboardView {
	v := DashboardView{Overview: m.Overview()}
	v.Parts, v.Counts = m.trackParts()
	v.Slsk = m.SoulseekStatus()
	if d := m.dash.last.Load(); d != nil {
		v.Disk = *d
	}
	if d := m.dash.last.Load(); (d == nil || time.Since(d.Updated) > time.Minute) && m.dash.busy.CompareAndSwap(false, true) {
		go func() {
			defer m.dash.busy.Store(false)
			u := m.measureDisks()
			m.dash.last.Store(&u)
		}()
	}
	return v
}

func (m *Manager) trackParts() (TrackParts, map[string]int) {
	var p TrackParts
	_, counts := m.QueueKeys(QueueQuery{Filter: "none"})
	m.jobsMu.Lock()
	list := m.order
	m.jobsMu.Unlock()
	for _, j := range list {
		j.mu.Lock()
		state, better := j.state, j.needBetter && len(j.lib) > 0
		j.mu.Unlock()
		switch {
		case state == JobInLibrary:
			p.InLibrary++
		case state == JobDone:
			p.Downloaded++
		case state == JobDownloading || state == JobReady:
			p.Downloading++
		case state == JobSearching:
			p.Searching++
		case better && state != JobFailed && state != JobCancelled:
			p.NeedUpgrade++
		case state == JobWaiting:
			p.Retrying++
		case state == JobQueued:
			p.Waiting++
		case state == JobFailed:
			p.NotFound++
		case state == JobCancelled:
			p.Cancelled++
		}
	}
	return p, counts
}

func walkSize(root string) (files int, bytes int64) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	return
}

func (m *Manager) measureDisks() DiskUsage {
	cfg := m.Config()
	u := DiskUsage{Known: true, Updated: time.Now()}
	perRoot := map[string]int64{}
	if m.index != nil {
		for _, dir := range cfg.LibraryDirs {
			perRoot[dir] = 0
		}
		u.LibraryFiles, u.LibraryBytes = m.index.sizes(cfg.LibraryDirs, perRoot)
	}
	u.DownloadFiles, u.DownloadBytes = walkSize(cfg.OutputDir)
	_, cache := walkSize(filepath.Join(m.dataDir, "cache"))
	_, tmp := walkSize(filepath.Join(m.dataDir, "tmp"))
	u.CacheBytes = cache + tmp
	seen := map[string]int{}
	disk := func(path string) *DiskInfo {
		dev := deviceOf(nearestDir(path))
		if i, ok := seen[dev]; ok {
			return &u.Disks[i]
		}
		total, free := diskSpace(nearestDir(path))
		if total <= 0 {
			return nil
		}
		seen[dev] = len(u.Disks)
		u.Disks = append(u.Disks, DiskInfo{Path: path, Total: total, Free: free})
		return &u.Disks[len(u.Disks)-1]
	}
	for dir, n := range perRoot {
		if d := disk(dir); d != nil {
			d.Library += n
		}
	}
	if d := disk(cfg.OutputDir); d != nil {
		d.Downloads += u.DownloadBytes
	}
	return u
}

// sizes adds up the files under the folders, per folder too.
func (ix *LibIndex) sizes(roots []string, perRoot map[string]int64) (files int, bytes int64) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	sep := string(os.PathSeparator)
	for _, f := range ix.files {
		for _, r := range roots {
			if len(f.Path) > len(r) && f.Path[:len(r)] == r && f.Path[len(r):len(r)+1] == sep {
				perRoot[r] += f.Size
				files++
				bytes += f.Size
				break
			}
		}
	}
	return
}
