package core

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// What you add from the Search tab: a track, an album or an artist's discography. Each
// addition is a playlist of its own ("Search: Artist - Album"), kept in the data folder, so
// it can be turned off, prioritized or removed like the others.

const (
	additionPrefix = "Search: "
	upgradePrefix  = "Upgrade: "
)

type Addition struct {
	ID        string    `json:"id"` // kind:deezer id, or upgrade:threshold
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Added     time.Time `json:"added"`
	Tracks    []*Track  `json:"tracks,omitempty"`
	Threshold Threshold `json:"threshold,omitempty"` // an upgrade's: library files below it are downloaded again
}

func (a *Addition) Playlist() string {
	if a.Kind == "upgrade" {
		return upgradePrefix + a.Name
	}
	return additionPrefix + a.Name
}

// IsAddition tells whether a playlist came from the Search tab or a library upgrade.
func IsAddition(playlist string) bool {
	return strings.HasPrefix(playlist, additionPrefix) || IsUpgrade(playlist)
}

// IsUpgrade tells whether a playlist is a library upgrade.
func IsUpgrade(playlist string) bool { return strings.HasPrefix(playlist, upgradePrefix) }

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

// mergeAdditions adds every addition's tracks to lib (an upgrade's come from the library scan).
func (m *Manager) mergeAdditions(lib *Library) {
	for _, ad := range m.adds.all() {
		tracks := ad.Tracks
		if ad.Kind == "upgrade" {
			tracks = m.upgradeTracks(ad.Threshold)
		}
		lib.playlist(ad.Playlist())
		for _, t := range tracks {
			c := *t
			lib.add(ad.Playlist(), &c)
		}
	}
}

// SearchMusic looks up what you typed in the music catalog.
func (m *Manager) SearchMusic(ctx context.Context, text string) ([]MusicResult, error) {
	return m.catalog.Search(ctx, text)
}

// ArtistReleases lists what an artist has released, so a part of it can be picked.
func (m *Manager) ArtistReleases(ctx context.Context, r MusicResult) ([]Release, error) {
	return m.catalog.ArtistReleases(ctx, r)
}

// ReleaseTracks lists the tracks of a release.
func (m *Manager) ReleaseTracks(ctx context.Context, rel Release) ([]*Track, error) {
	return m.catalog.ReleaseTracks(ctx, rel)
}

// AddReleases adds part of an artist's discography: whole releases, or chosen tracks of them.
// Adding more of the same artist later joins the same playlist.
func (m *Manager) AddReleases(ctx context.Context, artist MusicResult, picks []ReleasePick, progress func(string)) (string, int, error) {
	tracks, err := m.catalog.tracksOf(ctx, artist.Title, picks, progress)
	if err != nil {
		return "", 0, err
	}
	return m.addTracks("artist:"+foldName(artist.Title), "artist", artist.Title+" (discography)", tracks)
}

// AddMusic adds a search result to the queue: the track, the whole album, or the artist's
// whole discography (progress hears how far that is). It returns the new playlist's name and
// how many tracks it has; its tracks go first.
func (m *Manager) AddMusic(ctx context.Context, r MusicResult, progress func(string)) (string, int, error) {
	tracks, name, err := m.catalog.Expand(ctx, r, progress)
	if err != nil {
		return "", 0, err
	}
	if len(tracks) == 0 {
		return "", 0, fmt.Errorf("found no tracks for %s", r)
	}
	id := fmt.Sprintf("%s:%d", r.Kind, r.ID)
	if r.Provider != "" && r.Provider != provDeezer {
		id = r.Kind + ":" + r.Provider + ":" + r.Ref
	}
	if r.Kind == "artist" {
		id = "artist:" + foldName(r.Title)
	}
	return m.addTracks(id, r.Kind, name, tracks)
}

// addTracks makes (or adds to) the playlist of a search; its tracks go first in the queue.
func (m *Manager) addTracks(id, kind, name string, tracks []*Track) (string, int, error) {
	ad := &Addition{ID: id, Kind: kind, Name: name, Added: time.Now(), Tracks: tracks}
	m.adds.mu.Lock()
	for i, x := range m.adds.list {
		if x.ID == ad.ID { // added again: what's there stays, and the new ones join it
			have := map[string]bool{}
			for _, t := range tracks {
				have[t.Key] = true
			}
			for _, t := range x.Tracks {
				if !have[t.Key] {
					ad.Tracks = append(ad.Tracks, t)
				}
			}
			m.adds.list = append(m.adds.list[:i], m.adds.list[i+1:]...)
			break
		}
	}
	m.adds.list = append(m.adds.list, ad)
	err := m.adds.saveLocked()
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
		m.askImport()
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

// --- upgrading the library ------------------------------------------------------------------

// libraryBelow lists the library files (with an artist and title) below a quality level.
func (m *Manager) libraryBelow(th Threshold) []*LibFile {
	if m.index == nil {
		return nil
	}
	roots := m.Config().LibraryDirs
	var out []*LibFile
	for _, f := range m.index.all() {
		if f.Title != "" && f.Artist != "" && underAny(f.Path, roots) && !th.meets(f.Q) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// UpgradeCounts says how many library files are below each quality level.
func (m *Manager) UpgradeCounts() map[Threshold]int {
	out := map[Threshold]int{}
	if m.index == nil {
		return out
	}
	roots := m.Config().LibraryDirs
	files := m.index.all()
	for _, th := range Thresholds {
		if th.Name == "any" {
			continue
		}
		for _, f := range files {
			if f.Title != "" && f.Artist != "" && underAny(f.Path, roots) && !th.Name.meets(f.Q) {
				out[th.Name]++
			}
		}
	}
	return out
}

func (m *Manager) upgradeTracks(th Threshold) []*Track {
	var out []*Track
	for _, f := range m.libraryBelow(th) {
		sum := sha1.Sum([]byte(f.Path))
		t := &Track{Key: "lib:" + hex.EncodeToString(sum[:8]), Title: f.Title, Artists: splitArtistTag(f.Artist),
			AlbumArtist: f.AlbumArtist, Album: f.Album, DurationMs: int(f.Duration * 1000), ISRC: f.ISRC}
		out = append(out, t)
	}
	return out
}

// splitArtistTag splits an artist tag holding several ("A; B", "A / B").
func splitArtistTag(s string) []string {
	var out []string
	for _, p := range reTagArtistSep.Split(s, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{s}
	}
	return out
}

var reTagArtistSep = regexp.MustCompile(`\s*(?:;|\s/\s|\x00)\s*`)

// UpgradeLibrary queues every library file below th to be downloaded again in better quality
// (a download only counts when it's clearly better than your copy, which stays as it is). It
// returns the new playlist and its size; running it again refreshes it.
func (m *Manager) UpgradeLibrary(th Threshold) (string, int, error) {
	if th.min() <= 0 || th == "any" {
		return "", 0, fmt.Errorf("pick a quality level to upgrade to")
	}
	tracks := m.upgradeTracks(th)
	if len(tracks) == 0 {
		return "", 0, fmt.Errorf("no library files below %s", th)
	}
	label := string(th)
	for _, t := range Thresholds {
		if t.Name == th {
			label = strings.SplitN(t.Label, " (", 2)[0]
		}
	}
	ad := &Addition{ID: "upgrade:" + string(th), Kind: "upgrade", Name: "below " + label, Added: time.Now(), Threshold: th}
	m.adds.mu.Lock()
	for i, x := range m.adds.list {
		if x.ID == ad.ID {
			m.adds.list = append(m.adds.list[:i], m.adds.list[i+1:]...)
			break
		}
	}
	m.adds.list = append(m.adds.list, ad)
	err := m.adds.saveLocked()
	m.adds.mu.Unlock()
	if err != nil {
		return "", 0, err
	}
	m.libMu.Lock()
	m.full.playlist(ad.Playlist())
	for _, t := range tracks {
		m.full.add(ad.Playlist(), t)
	}
	m.libMu.Unlock()
	m.saveLibrary()
	m.rebuildJobs()
	Logf("Library upgrade: %d files below %s queued (%s)", len(tracks), label, ad.Playlist())
	return ad.Playlist(), len(tracks), nil
}

// upgradeThresholds maps upgrade playlists to the quality their tracks must reach.
func (m *Manager) upgradeThresholds() map[string]Threshold {
	out := map[string]Threshold{}
	for _, a := range m.adds.all() {
		if a.Kind == "upgrade" {
			out[a.Playlist()] = a.Threshold
		}
	}
	return out
}
