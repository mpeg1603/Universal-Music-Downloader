package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// What the app keeps for itself is kept in bounds. All of it can be made again: album zips (for
// an album's other tracks), .torrent files, searches already made, logs.

// pruneGrace: anything changed more recently than this may be in use.
const pruneGrace = 10 * time.Minute

// pruneRule says how much of a folder's files to keep, the least recently used going first:
// files last used longer ago than maxAge go regardless of the total.
type pruneRule struct {
	dir      string
	maxAge   time.Duration
	maxBytes int64 // 0: no limit
	keep     int   // the newest few stay in any case
}

func (m *Manager) pruneRules() []pruneRule {
	cache := filepath.Join(m.dataDir, "cache")
	return []pruneRule{
		{dir: filepath.Join(cache, "bandcamp"), maxAge: 3 * 24 * time.Hour, maxBytes: 5 << 30},
		{dir: filepath.Join(cache, "torrents"), maxAge: 14 * 24 * time.Hour, maxBytes: 200 << 20},
		{dir: filepath.Join(cache, "torrent-searches"), maxAge: 24 * time.Hour}, // answers count for 12 hours at most
		{dir: filepath.Join(m.dataDir, "logs"), maxAge: 14 * 24 * time.Hour, maxBytes: 100 << 20, keep: 1},
	}
}

// pruneDir removes what a rule doesn't keep and says how many files and bytes that was.
func pruneDir(r pruneRule, now time.Time) (files int, freed int64) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return 0, 0
	}
	type file struct {
		path string
		size int64
		at   time.Time
	}
	var list []file
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if info, err := e.Info(); err == nil {
			list = append(list, file{filepath.Join(r.dir, e.Name()), info.Size(), info.ModTime()})
			total += info.Size()
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	for i, f := range list {
		if i >= len(list)-r.keep || now.Sub(f.at) < pruneGrace {
			break // the rest are newer still
		}
		if now.Sub(f.at) > r.maxAge || (r.maxBytes > 0 && total > r.maxBytes) {
			if os.Remove(f.path) == nil {
				files++
				freed += f.size
				total -= f.size
			}
		}
	}
	return files, freed
}

// CacheInfo is one kind of file the app keeps for itself and can do without.
type CacheInfo struct {
	Name, Label, About string
	Bytes              int64
	Files              int
}

// CacheKinds are the kinds, without their sizes (see CacheUsage).
var CacheKinds = []CacheInfo{
	{Name: "albums", Label: "Bandcamp albums", About: "Whole albums, kept so their other songs don't download them again."},
	{Name: "torrents", Label: "Torrent files", About: "Small files describing torrents already looked at."},
	{Name: "searches", Label: "Torrent searches", About: "Answers from torrent sites, kept for half a day."},
	{Name: "logs", Label: "Logs", About: "What the app did, in case something goes wrong. The current one stays."},
	{Name: "sort-tags", Label: "Sort & merge: details of your files", About: "Without it, the next plan has to read every file again."},
	{Name: "sort-lookups", Label: "Sort & merge: online answers", About: "Without it, the next plan asks MusicBrainz and Deezer again."},
}

// cacheFile is where a kind that's a single file lives.
func (m *Manager) cacheFile(name string) string {
	switch name {
	case "sort-tags":
		return filepath.Join(m.dataDir, "sort-tags.jsonl")
	case "sort-lookups":
		return filepath.Join(m.dataDir, "sort-lookups.json")
	}
	return ""
}

// cacheDir is where a kind that's a folder lives.
func (m *Manager) cacheDir(name string) (string, int) {
	switch name {
	case "albums":
		return filepath.Join(m.dataDir, "cache", "bandcamp"), 0
	case "torrents":
		return filepath.Join(m.dataDir, "cache", "torrents"), 0
	case "searches":
		return filepath.Join(m.dataDir, "cache", "torrent-searches"), 0
	case "logs":
		return filepath.Join(m.dataDir, "logs"), 1
	}
	return "", 0
}

// CacheUsage says how much each kind takes up. It reads the folders: not for the window's thread.
func (m *Manager) CacheUsage() []CacheInfo {
	out := append([]CacheInfo(nil), CacheKinds...)
	for i := range out {
		if p := m.cacheFile(out[i].Name); p != "" {
			if st, err := os.Stat(p); err == nil {
				out[i].Bytes, out[i].Files = st.Size(), 1
			}
			continue
		}
		dir, _ := m.cacheDir(out[i].Name)
		out[i].Files, out[i].Bytes = walkSize(dir)
	}
	return out
}

// ClearCache removes one kind ("" is every kind) and says how much that freed. What may be in
// use right now is left alone: files used in the last few minutes, and Sort & merge's while
// it's running.
func (m *Manager) ClearCache(name string) (files int, freed int64, err error) {
	busy := m.SortStatus().Busy
	known := false
	for _, k := range CacheKinds {
		if name != "" && k.Name != name {
			continue
		}
		known = true
		if p := m.cacheFile(k.Name); p != "" {
			if busy {
				if name != "" {
					return 0, 0, errors.New("Sort & merge is running; try again when it's done")
				}
				continue
			}
			if st, err := os.Stat(p); err == nil && os.Remove(p) == nil {
				files++
				freed += st.Size()
			}
			continue
		}
		dir, keep := m.cacheDir(k.Name)
		n, b := pruneDir(pruneRule{dir: dir, keep: keep}, time.Now())
		files, freed = files+n, freed+b
	}
	if !known {
		return 0, 0, fmt.Errorf("no such cache: %s", name)
	}
	Logf("Cleared %s: %d files, %s", FirstNonEmpty(name, "all temporary files"), files, FmtBytes(freed))
	return files, freed, nil
}

// housekeeping trims the app's own files soon after it starts, then every hour.
func (m *Manager) housekeeping() {
	wait := time.Minute
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		files, freed := 0, int64(0)
		for _, r := range m.pruneRules() {
			n, b := pruneDir(r, time.Now())
			files, freed = files+n, freed+b
		}
		if files > 0 {
			Logf("Cleaned up %d old files of the app's own (%s)", files, FmtBytes(freed))
		}
	}
}
