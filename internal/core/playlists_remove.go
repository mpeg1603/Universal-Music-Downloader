package core

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// Removing playlists, one or many at a time. A playlist leaves the app and its songs leave the
// queue, and music files are never touched: this file doesn't know where any of them are. The one
// thing to decide is what happens to the CSV or text file the playlist was read from.

// RemovalPreview is what removing some playlists would leave to decide.
type RemovalPreview struct {
	Playlists int
	Files     []string // the files that hold these playlists and nothing else
	Shared    int      // files that hold other playlists too: these stay
}

// RemovalResult is what was done.
type RemovalResult struct {
	Playlists, Deleted, Moved int // Deleted and Moved count playlist files
	Errors                    []string
}

type sourceFile struct {
	path  string
	holds map[string]bool
}

// removalFiles finds the files in the enabled inputs that the named playlists were read from.
// What's inside a zip can't be moved on its own, and a link is no file.
func (m *Manager) removalFiles(names []string) (files []sourceFile, shared int) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	seen := map[string]bool{}
	check := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		holds := listFilePlaylists(p)
		member, only := false, true
		for n := range holds {
			if set[n] {
				member = true
			} else {
				only = false
			}
		}
		switch {
		case member && only:
			files = append(files, sourceFile{p, holds})
		case member:
			shared++
		}
	}
	for _, in := range m.Config().Inputs {
		if !in.Enabled || isLink(in.Path) {
			continue
		}
		src := ExpandHome(in.Path)
		info, err := os.Stat(src)
		switch {
		case err != nil:
		case info.IsDir():
			filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					check(p)
				}
				return nil
			})
		case info.Mode().IsRegular():
			check(src)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, shared
}

// listFilePlaylists names the playlists reading a .csv or .txt file gives, nil for any other file
// or one that can't be read as a list.
func listFilePlaylists(path string) map[string]bool {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	switch strings.ToLower(ext) {
	case ".txt":
		return map[string]bool{stem: true}
	case ".csv":
	default:
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	cr := csv.NewReader(f)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil || csvColumn(header, "title") < 0 || csvColumn(header, "artists") < 0 {
		return nil
	}
	col := csvColumn(header, "playlist")
	if col < 0 {
		return map[string]bool{stem: true}
	}
	rows, err := cr.ReadAll()
	if err != nil {
		return nil
	}
	holds := map[string]bool{}
	for _, row := range rows {
		name := stem
		if col < len(row) && strings.TrimSpace(row[col]) != "" {
			name = strings.TrimSpace(row[col])
		}
		holds[name] = true
	}
	if len(holds) == 0 {
		holds[stem] = true
	}
	return holds
}

// csvColumn is the index of the first header cell naming field, or -1.
func csvColumn(header []string, field string) int {
	for i, h := range header {
		if slices.Contains(csvColumns[field], strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))) {
			return i
		}
	}
	return -1
}

// RemovalPreview says which playlist files removing these playlists would concern.
func (m *Manager) RemovalPreview(names []string) RemovalPreview {
	files, shared := m.removalFiles(names)
	v := RemovalPreview{Playlists: len(names), Shared: shared}
	for _, f := range files {
		v.Files = append(v.Files, f.path)
	}
	return v
}

// RemovePlaylists takes playlists out of the app. files says what happens to the CSV or text
// files that hold only these playlists: "keep", "delete" or "move" (into moveTo). It waits until
// the playlists are gone; call it off the window's thread.
func (m *Manager) RemovePlaylists(names []string, files, moveTo string) (RemovalResult, error) {
	var res RemovalResult
	if len(names) == 0 {
		return res, nil
	}
	if files == "move" {
		if moveTo = CleanPath(moveTo); moveTo == "" {
			return res, errors.New("choose a folder to move the files to")
		}
		for _, in := range m.Config().Inputs {
			if !isLink(in.Path) && isWithin(moveTo, CleanPath(in.Path)) {
				return res, errors.New("the app reads playlists from that folder, so the playlist would come back; choose another one")
			}
		}
		if err := os.MkdirAll(moveTo, 0o755); err != nil {
			return res, err
		}
	}
	list, _ := m.removalFiles(names)
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
	if files == "delete" || files == "move" {
		m.disposeOf(list, files, moveTo, &res)
	}
	Logf("Removed %d playlists (%d playlist files deleted, %d moved; music files aren't touched)", res.Playlists, res.Deleted, res.Moved)
	for _, e := range res.Errors {
		Logf("Removing playlists: %s", e)
	}
	m.dirty.Store(true)
	return res, nil
}

// disposeOf deletes or moves the playlists' files. A playlist whose files are all gone has nothing
// left to bring back, and a file added on its own stops being listed.
func (m *Manager) disposeOf(list []sourceFile, how, moveTo string, res *RemovalResult) {
	done, failed, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range list {
		var err error
		if how == "delete" {
			if err = os.Remove(f.path); errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		} else {
			err = moveInto(f.path, moveTo)
		}
		mark := done
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			mark = failed
		} else {
			paths[f.path] = true
			if how == "delete" {
				res.Deleted++
			} else {
				res.Moved++
			}
		}
		for n := range f.holds {
			mark[n] = true
		}
	}
	if len(paths) == 0 {
		return
	}
	err := m.UpdateConfig(func(c *Config) {
		for n := range done {
			if !failed[n] {
				delete(c.Playlists, n)
			}
		}
		c.Inputs = slices.DeleteFunc(c.Inputs, func(in InputCfg) bool { return paths[ExpandHome(in.Path)] })
	})
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
}

// moveInto moves a file into dir, never replacing what's there.
func moveInto(path, dir string) error {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for n := 1; n < 1000; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		err := moveNoReplace(path, filepath.Join(dir, name))
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return err
	}
	return fmt.Errorf("no free name for %s in %s", base, dir)
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

// RestorePlaylists takes the named playlists off the ignored list, or all of them when none are
// named, and says how many came off. Those a file still lists come back; the rest are just gone
// from the list.
func (m *Manager) RestorePlaylists(names ...string) int {
	if len(names) == 0 {
		names = m.RemovedPlaylists()
	}
	if len(names) == 0 {
		return 0
	}
	n := 0
	if err := m.UpdateConfig(func(c *Config) {
		n = 0
		for _, name := range names {
			pc, ok := c.Playlists[name]
			if !ok || !pc.Removed {
				continue
			}
			n++
			if pc.Removed = false; pc == (PlaylistCfg{}) {
				delete(c.Playlists, name)
			} else {
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
