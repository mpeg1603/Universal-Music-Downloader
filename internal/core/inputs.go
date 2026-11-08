package core

import (
	"fmt"
	"os"
	"strings"
)

// Playlist files and playlists, changed the same way from every frontend.

// AddInput adds a playlist file, folder, zip, text list or link to read the queue from.
func (m *Manager) AddInput(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Errorf("nothing to add")
	}
	if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
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
