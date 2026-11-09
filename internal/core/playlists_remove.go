package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Removing playlists, one or many at a time, and deciding what happens to their songs' files.
// A file is only touched when the app itself downloaded it into the downloads folder and every
// playlist it belongs to is being removed; your library is never touched.

// RemovalPreview is what removing some playlists would leave to decide.
type RemovalPreview struct {
	Playlists int
	Files     int   // downloaded files that belong only to these playlists
	Bytes     int64 // their size
	Shared    int   // downloaded files that other playlists also use: these stay
}

// RemovalResult is what was done.
type RemovalResult struct {
	Playlists, Deleted, Moved, Kept int
	Errors                          []string
}

type removable struct {
	key  string
	path string
	size int64
}

func (m *Manager) removalFiles(names []string) (files []removable, shared int) {
	cfg := m.Config()
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	for _, j := range m.jobList() {
		j.mu.Lock()
		member, only := false, true
		for _, p := range j.playlists {
			if set[p] {
				member = true
			} else {
				only = false
			}
		}
		var rec Record
		done := member && j.state == JobDone && j.rec != nil
		if done {
			rec = *j.rec
		}
		key := j.Track.Key
		j.mu.Unlock()
		if !done || rec.Path == "" {
			continue
		}
		if !only {
			shared++
			continue
		}
		if !filepath.IsAbs(rec.Path) || !isWithin(rec.Path, cfg.OutputDir) || underAny(rec.Path, cfg.LibraryDirs) {
			continue // not in the downloads folder (any more): not ours to touch
		}
		if st, err := os.Lstat(rec.Path); err == nil && st.Mode().IsRegular() {
			files = append(files, removable{key, rec.Path, st.Size()})
		}
	}
	return files, shared
}

// RemovalPreview counts the files removing these playlists would concern.
func (m *Manager) RemovalPreview(names []string) RemovalPreview {
	files, shared := m.removalFiles(names)
	v := RemovalPreview{Playlists: len(names), Files: len(files), Shared: shared}
	for _, f := range files {
		v.Bytes += f.size
	}
	return v
}

// RemovePlaylists takes playlists out of the app, and their files with them when asked: files
// is "keep", "delete" or "move" (into moveTo). It waits until the playlists are gone; call it
// off the window's thread.
func (m *Manager) RemovePlaylists(names []string, files, moveTo string) (RemovalResult, error) {
	var res RemovalResult
	if len(names) == 0 {
		return res, nil
	}
	if files == "move" {
		if moveTo = CleanPath(moveTo); moveTo == "" {
			return res, errors.New("choose a folder to move the files to")
		}
		if err := os.MkdirAll(moveTo, 0o755); err != nil {
			return res, err
		}
	}
	list, _ := m.removalFiles(names) // before the playlists go: their jobs say what's whose
	var fromFiles []string
	for _, n := range names {
		if IsAddition(n) {
			m.adds.mu.Lock()
			for i, x := range m.adds.list {
				if x.Playlist() == n {
					m.adds.list = append(m.adds.list[:i], m.adds.list[i+1:]...)
					m.adds.saveLocked()
					break
				}
			}
			m.adds.mu.Unlock()
		} else {
			fromFiles = append(fromFiles, n)
		}
	}
	if len(fromFiles) > 0 {
		err := m.UpdateConfig(func(c *Config) {
			if c.Playlists == nil {
				c.Playlists = map[string]PlaylistCfg{}
			}
			for _, n := range fromFiles {
				pc := c.Playlists[n]
				pc.Removed = true
				c.Playlists[n] = pc
			}
		})
		if err != nil {
			return res, err
		}
	}
	m.importInputs()
	res.Playlists = len(names)
	cfg := m.Config()
	gone := make(map[string]bool, len(names))
	for _, n := range names {
		gone[n] = true
	}
	removePlaylistFiles(filepath.Join(cfg.OutputDir, "Playlists"), gone)
	switch files {
	case "delete":
		for _, f := range list {
			if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
				res.Errors = append(res.Errors, err.Error())
				continue
			}
			res.Deleted++
			pruneEmptyDirs(filepath.Dir(f.path), cfg.OutputDir)
		}
	case "move":
		for _, f := range list {
			dest, err := moveInto(f.path, cfg.OutputDir, moveTo)
			if err != nil {
				res.Errors = append(res.Errors, err.Error())
				continue
			}
			res.Moved++
			if r := m.state.get(f.key); r != nil && r.Status == "downloaded" {
				r.Path = dest
				m.state.put(*r)
			}
			pruneEmptyDirs(filepath.Dir(f.path), cfg.OutputDir)
		}
	default:
		res.Kept = len(list)
	}
	Logf("Removed %d playlists (%d files deleted, %d moved, %d left where they are)", res.Playlists, res.Deleted, res.Moved, res.Kept)
	for _, e := range res.Errors {
		Logf("Removing playlists: %s", e)
	}
	m.dirty.Store(true)
	return res, nil
}

// moveInto moves a file under root into dir, keeping where it was relative to root and never
// replacing what's there.
func moveInto(path, root, dir string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for n := 1; n < 1000; n++ {
		name := rel
		if n > 1 {
			name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		dest := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		err := moveNoReplace(path, dest)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return dest, err
	}
	return "", fmt.Errorf("no free name for %s in %s", rel, dir)
}

// pruneEmptyDirs removes dir and the parents it leaves empty, up to but not including root.
func pruneEmptyDirs(dir, root string) {
	for isWithin(dir, root) && dir != root && filepath.Clean(dir) != filepath.Clean(root) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// RemovedPlaylists are the playlists you've removed that a file still lists.
func (m *Manager) RemovedPlaylists() []string {
	var out []string
	for name, pc := range m.Config().Playlists {
		if pc.Removed {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// RestorePlaylists brings removed playlists back and says how many.
func (m *Manager) RestorePlaylists() int {
	n := len(m.RemovedPlaylists())
	if n == 0 {
		return 0
	}
	if err := m.UpdateConfig(func(c *Config) {
		for name, pc := range c.Playlists {
			if pc.Removed {
				pc.Removed = false
				c.Playlists[name] = pc
			}
		}
	}); err != nil {
		Logf("Couldn't bring the playlists back: %v", err)
		return 0
	}
	m.askImport()
	return n
}
