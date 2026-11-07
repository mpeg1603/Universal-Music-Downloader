package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// What you add from the Search tab: a track, an album or an artist's discography. Each
// addition is a playlist of its own ("Search: Artist - Album"), kept in the data folder, so
// it can be turned off, prioritized or removed like the others.

const additionPrefix = "Search: "

type Addition struct {
	ID     string    `json:"id"` // kind:deezer id
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	Added  time.Time `json:"added"`
	Tracks []*Track  `json:"tracks"`
}

func (a *Addition) Playlist() string { return additionPrefix + a.Name }

// IsAddition tells whether a playlist came from the Search tab.
func IsAddition(playlist string) bool { return strings.HasPrefix(playlist, additionPrefix) }

type additions struct {
	mu   sync.Mutex
	path string
	list []*Addition
}

func loadAdditions(path string) *additions {
	a := &additions{path: path}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &a.list)
	}
	return a
}

func (a *additions) saveLocked() error {
	data, err := json.MarshalIndent(a.list, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.path, data)
}

func (a *additions) all() []*Addition {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*Addition(nil), a.list...)
}

// mergeInto adds every addition's tracks to lib.
func (a *additions) mergeInto(lib *Library) {
	for _, ad := range a.all() {
		lib.playlist(ad.Playlist())
		for _, t := range ad.Tracks {
			c := *t
			lib.add(ad.Playlist(), &c)
		}
	}
}

// SearchMusic looks up what you typed in the music catalog.
func (m *Manager) SearchMusic(ctx context.Context, text string) ([]MusicResult, error) {
	return m.deezer.SearchMusic(ctx, text)
}

// AddMusic adds a search result to the queue: the track, the whole album, or the artist's
// whole discography (progress hears how far that is). It returns the new playlist's name and
// how many tracks it has; its tracks go first.
func (m *Manager) AddMusic(ctx context.Context, r MusicResult, progress func(string)) (string, int, error) {
	tracks, name, err := m.deezer.Expand(ctx, r, progress)
	if err != nil {
		return "", 0, err
	}
	if len(tracks) == 0 {
		return "", 0, fmt.Errorf("found no tracks for %s", r)
	}
	ad := &Addition{ID: fmt.Sprintf("%s:%d", r.Kind, r.ID), Kind: r.Kind, Name: name, Added: time.Now(), Tracks: tracks}
	m.adds.mu.Lock()
	for i, x := range m.adds.list {
		if x.ID == ad.ID { // added again: refreshed
			m.adds.list = append(m.adds.list[:i], m.adds.list[i+1:]...)
			break
		}
	}
	m.adds.list = append(m.adds.list, ad)
	err = m.adds.saveLocked()
	m.adds.mu.Unlock()
	if err != nil {
		return "", 0, err
	}
	m.libMu.Lock()
	m.full.playlist(ad.Playlist())
	for _, t := range tracks {
		c := *t
		m.full.add(ad.Playlist(), &c)
	}
	m.libMu.Unlock()
	m.saveLibrary()
	m.rebuildJobs()
	keys := map[string]bool{}
	for _, t := range tracks {
		keys[t.Key] = true
	}
	m.jobsMu.Lock()
	for _, j := range m.order {
		j.mu.Lock()
		member := keys[j.Track.Key]
		for _, k := range j.Track.Also {
			member = member || keys[k]
		}
		if member && j.state == JobQueued {
			j.bump = m.bumpSeq.Add(1)
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	m.wake()
	Logf("Added %s: %d tracks", ad.Playlist(), len(tracks))
	return ad.Playlist(), len(tracks), nil
}

// RemoveAddition takes a playlist added from the Search tab away again.
func (m *Manager) RemoveAddition(playlist string) bool {
	m.adds.mu.Lock()
	found := false
	for i, x := range m.adds.list {
		if x.Playlist() == playlist {
			m.adds.list = append(m.adds.list[:i], m.adds.list[i+1:]...)
			found = true
			break
		}
	}
	if found {
		m.adds.saveLocked()
	}
	m.adds.mu.Unlock()
	if found {
		go m.importInputs()
	}
	return found
}

// AdditionStatus is an addition as the Search tab lists it.
type AdditionStatus struct {
	Playlist string
	Kind     string
	Tracks   int
	Added    time.Time
}

func (m *Manager) Additions() []AdditionStatus {
	var out []AdditionStatus
	for _, a := range m.adds.all() {
		out = append(out, AdditionStatus{Playlist: a.Playlist(), Kind: a.Kind, Tracks: len(a.Tracks), Added: a.Added})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added.After(out[j].Added) })
	return out
}
