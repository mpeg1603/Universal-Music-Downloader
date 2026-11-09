package core

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Playlist files and playlists, changed the same way from every frontend.

// AddInput adds a playlist file, folder, zip, text list or link to read the queue from.
func (m *Manager) AddInput(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Errorf("nothing to add")
	}
	if !isLink(p) {
		p = CleanPath(p)
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}
	return m.UpdateConfig(func(c *Config) {
		for _, in := range c.Inputs {
			if in.Path == p {
				return
			}
		}
		c.Inputs = append(c.Inputs, InputCfg{Path: p, Enabled: true})
	})
}

// RemoveInput takes a playlist file off the list (the file itself isn't touched).
func (m *Manager) RemoveInput(p string) error {
	return m.UpdateConfig(func(c *Config) {
		var keep []InputCfg
		for _, in := range c.Inputs {
			if in.Path != p {
				keep = append(keep, in)
			}
		}
		c.Inputs = keep
	})
}

// SetInputEnabled turns a playlist file on or off.
func (m *Manager) SetInputEnabled(p string, on bool) error {
	return m.UpdateConfig(func(c *Config) {
		for i := range c.Inputs {
			if c.Inputs[i].Path == p {
				c.Inputs[i].Enabled = on
			}
		}
	})
}

// SetPlaylist turns a playlist on or off and moves its priority by delta (higher goes first).
func (m *Manager) SetPlaylist(name string, on bool, delta int) error {
	return m.UpdateConfig(func(c *Config) {
		if c.Playlists == nil {
			c.Playlists = map[string]PlaylistCfg{}
		}
		pc := c.Playlists[name]
		pc.Disabled = !on
		pc.Priority += delta
		c.Playlists[name] = pc
	})
}

// MusicQuery is a search text from the Find field and the Artist, Album, Track and Year fields
// (added as tags).
func MusicQuery(find, artist, album, track, year string) string {
	q := strings.TrimSpace(find)
	for _, f := range [][2]string{{"artist", artist}, {"album", album}, {"track", track}, {"year", year}} {
		if v := strings.TrimSpace(f[1]); v != "" {
			q += " " + f[0] + ":" + v
		}
	}
	return strings.TrimSpace(q)
}

// AddLibraryDir adds a library folder (only ever read).
func (m *Manager) AddLibraryDir(p string) error {
	p = CleanPath(p)
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", p)
	}
	return m.UpdateConfig(func(c *Config) {
		if !ContainsString(c.LibraryDirs, p) {
			c.LibraryDirs = append(c.LibraryDirs, p)
		}
	})
}

// RemoveLibraryDir stops checking a library folder (nothing in it is touched).
func (m *Manager) RemoveLibraryDir(p string) error {
	return m.UpdateConfig(func(c *Config) {
		var keep []string
		for _, x := range c.LibraryDirs {
			if x != p {
				keep = append(keep, x)
			}
		}
		c.LibraryDirs = keep
	})
}

// isLink tells a web address from a file on this computer.
func isLink(p string) bool { return strings.Contains(p, "://") || strings.HasPrefix(p, "spotify:") }

// inputSignature changes when a playlist file or folder listed in inputs changes: which files
// there are, their sizes and times. Links never do (see importLink).
func inputSignature(inputs []InputCfg) string {
	var b strings.Builder
	note := func(p string, info fs.FileInfo) {
		fmt.Fprintf(&b, "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
	}
	for _, in := range inputs {
		if !in.Enabled || isLink(in.Path) {
			continue
		}
		root := ExpandHome(in.Path)
		info, err := os.Stat(root)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "%s\x00gone\n", root)
		case !info.IsDir():
			note(root, info)
		default:
			filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if fi, err := d.Info(); err == nil {
						note(p, fi)
					}
				}
				return nil
			})
		}
	}
	return b.String()
}

// watchInputs reads the playlists again once their files have changed and settled: delete a
// CSV from the folder and its playlist goes, drop one in and it appears.
func (m *Manager) watchInputs() {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	last := ""
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		}
		if !m.ready.Load() || m.importing.Load() {
			continue
		}
		now := inputSignature(m.Config().Inputs)
		m.importMu.Lock()
		read := m.inputsSig
		m.importMu.Unlock()
		if now != read && now == last { // the same two looks running: finished changing
			Logf("Your playlist files changed; reading them again")
			m.askImport()
		}
		last = now
	}
}
