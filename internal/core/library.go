package core

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Track struct {
	Key             string            `json:"key"` // Spotify track ID, or a hash when the input had none
	Title           string            `json:"title"`
	Artists         []string          `json:"artists"`
	Album           string            `json:"album,omitempty"`
	AlbumArtist     string            `json:"album_artist,omitempty"`
	DurationMs      int               `json:"duration_ms,omitempty"`
	TrackNumber     int               `json:"track_number,omitempty"`
	DiscNumber      int               `json:"disc_number,omitempty"`
	TotalTracks     int               `json:"total_tracks,omitempty"`
	ReleaseDate     string            `json:"release_date,omitempty"`
	ISRC            string            `json:"isrc,omitempty"`
	CoverURL        string            `json:"cover_url,omitempty"`
	SpotifyURL      string            `json:"spotify_url,omitempty"`
	ArtistURLs      map[string]string `json:"artist_urls,omitempty"` // artist name -> Spotify artist page
	DeezerArtistURL string            `json:"deezer_artist_url,omitempty"`
	IsLocal         bool              `json:"is_local,omitempty"`
	SourceURL       string            `json:"source_url,omitempty"` // where it came from, when added as a link
	Enriched        bool              `json:"enriched,omitempty"`   // Deezer lookup already attempted
	Also            []string          `json:"also,omitempty"`       // keys of other listings of this song, merged into it

	anyArtist bool // searched for under another name, by title alone (see NameVariant)
}

func (t *Track) Duration() float64 { return float64(t.DurationMs) / 1000 }

func (t *Track) ArtistStr() string { return strings.Join(t.Artists, ", ") }

func (t *Track) AlbumArtistStr() string {
	if t.AlbumArtist != "" {
		return t.AlbumArtist
	}
	return t.Artists[0]
}

func (t *Track) String() string { return t.ArtistStr() + " - " + t.Title }

// fillFrom copies fields this track lacks from another sighting of the same song.
func (t *Track) fillFrom(o *Track) {
	if t.Album == "" {
		t.Album = o.Album
	}
	if t.AlbumArtist == "" {
		t.AlbumArtist = o.AlbumArtist
	}
	if t.DurationMs == 0 {
		t.DurationMs = o.DurationMs
	}
	if t.TrackNumber == 0 {
		t.TrackNumber, t.DiscNumber, t.TotalTracks = o.TrackNumber, o.DiscNumber, o.TotalTracks
	}
	if t.ReleaseDate == "" {
		t.ReleaseDate = o.ReleaseDate
	}
	if t.ISRC == "" {
		t.ISRC = o.ISRC
	}
	if t.CoverURL == "" {
		t.CoverURL = o.CoverURL
	}
	if t.SpotifyURL == "" {
		t.SpotifyURL = o.SpotifyURL
	}
	if t.SourceURL == "" {
		t.SourceURL = o.SourceURL
	}
	for name, url := range o.ArtistURLs {
		if t.ArtistURLs == nil {
			t.ArtistURLs = map[string]string{}
		}
		if t.ArtistURLs[name] == "" {
			t.ArtistURLs[name] = url
		}
	}
}

// trackKey is the Spotify ID when known, otherwise a hash of artist and title.
func trackKey(spotifyID, artist, title string) string {
	if spotifyID != "" {
		return spotifyID
	}
	sum := sha1.Sum([]byte(normalize(artist) + "|" + normalize(title)))
	return "x-" + hex.EncodeToString(sum[:8])
}

type Playlist struct {
	Name string   `json:"name"`
	Keys []string `json:"keys"`

	has map[string]bool // Keys as a set, while importing
}

// Skipped is a playlist entry that isn't a downloadable song (podcast episode, removed track, ...).
type Skipped struct {
	Title    string `json:"title"`
	Artists  string `json:"artists"`
	Reason   string `json:"reason"`
	Playlist string `json:"playlist"`
}

type Library struct {
	ImportedAt string            `json:"imported_at"`
	Inputs     []string          `json:"inputs"`
	Tracks     map[string]*Track `json:"tracks"`
	Order      []string          `json:"order"`
	Playlists  []*Playlist       `json:"playlists"`
	Skipped    []Skipped         `json:"skipped"`

	// Repeats: the songs seen so far by primary artist, and the keys merged into another.
	songs  map[string][]sighting
	alias  map[string]string
	strs   map[string]string // imported text, kept once
	merged int               // listings merged into the same song under a different key
	quiet  bool              // nothing to the log (the playlists are being read again)
}

// sighting is one listing of a song, as far as recognizing a repeat of it goes.
type sighting struct {
	key   string // the track it is (or was merged into)
	title string // cleanTitle
	isrc  string
	ms    int
}

// How far the lengths of two listings may differ for them to be the same recording.
// Re-releases of one master differ by well under a second; edits and other versions by more.
const (
	sameTitleSlackMs = 2000
	sameISRCSlackMs  = 1000 // ISRCs alone are unreliable: re-releases sometimes swap them between songs
)

func newLibrary() *Library {
	return &Library{ImportedAt: time.Now().Format(time.RFC3339), Tracks: map[string]*Track{}}
}

func (l *Library) playlist(name string) *Playlist {
	for _, p := range l.Playlists {
		if p.Name == name {
			return p
		}
	}
	p := &Playlist{Name: name}
	if name == likedSongs { // listed first
		l.Playlists = append([]*Playlist{p}, l.Playlists...)
	} else {
		l.Playlists = append(l.Playlists, p)
	}
	return p
}

// add puts a track into a playlist, merging it with an earlier sighting of the same song, so
// a song listed twice is downloaded once and listed once per playlist. Besides the same
// Spotify ID, playlists often hold one song under two: from the album and from a re-release
// or compilation. Those are recognized by the same primary artist and length with the same
// title or ISRC.
func (l *Library) add(playlistName string, t *Track) {
	if len(t.Artists) == 0 || t.Artists[0] == "" {
		t.Artists = []string{"Unknown Artist"}
	}
	l.indexSongs()
	key := t.Key
	if k, ok := l.alias[key]; ok {
		key = k
	}
	if existing, ok := l.Tracks[key]; ok {
		existing.fillFrom(t)
	} else if same := l.sameSong(t); same != nil {
		if !l.quiet {
			debugf("%s: %q (%s) is the same song as %q (%s); downloading it once", playlistName, t.String(), t.Key, same.String(), same.Key)
		}
		same.fillFrom(t)
		same.Also = append(same.Also, t.Key)
		l.alias[t.Key] = same.Key
		l.merged++
		key = same.Key
	} else {
		l.Tracks[key] = t
		l.Order = append(l.Order, key)
	}
	l.remember(key, t)
	p := l.playlist(playlistName)
	if p.has == nil {
		p.has = make(map[string]bool, len(p.Keys))
		for _, k := range p.Keys {
			p.has[k] = true
		}
	}
	if !p.has[key] {
		p.has[key] = true
		p.Keys = append(p.Keys, key)
	}
}

// withoutRemoved takes out the playlists you've removed; a track that was only in those goes too.
func (l *Library) withoutRemoved(cfg map[string]PlaylistCfg) {
	removed := false
	var keep []*Playlist
	for _, p := range l.Playlists {
		if cfg[p.Name].Removed {
			removed = true
		} else {
			keep = append(keep, p)
		}
	}
	if !removed {
		return
	}
	l.Playlists = keep
	used := make(map[string]bool, len(l.Tracks))
	for _, p := range keep {
		for _, k := range p.Keys {
			used[k] = true
		}
	}
	var order []string
	for _, k := range l.Order {
		if used[k] {
			order = append(order, k)
		} else {
			delete(l.Tracks, k)
		}
	}
	l.Order = order
}

// trim lets go of what only importing needs (it's made again if more is imported).
func (l *Library) trim() {
	l.songs, l.alias, l.strs = nil, nil, nil
	for _, p := range l.Playlists {
		p.has = nil
	}
}

// text keeps one copy of a piece of imported text: names repeat a lot, and text cut out of a
// bigger string would keep the whole of it in memory.
func (l *Library) text(s string) string {
	if l.strs == nil {
		l.strs = map[string]string{}
	}
	return intern(l.strs, s)
}

func intern(strs map[string]string, s string) string {
	if s == "" {
		return ""
	}
	if v, ok := strs[s]; ok {
		return v
	}
	v := strings.Clone(s)
	strs[v] = v
	return v
}

// indexSongs builds the repeat index for a library loaded from disk.
func (l *Library) indexSongs() {
	if l.songs != nil {
		return
	}
	l.songs, l.alias = map[string][]sighting{}, map[string]string{}
	for _, k := range l.Order {
		if t := l.Tracks[k]; t != nil {
			l.remember(k, t)
			for _, a := range t.Also {
				l.alias[a] = k
			}
		}
	}
}

func songArtist(t *Track) string {
	if a := normalizedArtists(t); len(a) > 0 {
		return a[0]
	}
	return ""
}

// remember notes a listing of the song stored under key; every listing is kept, so a later
// one can match any of them (a translated title by ISRC, then a copy of that by title).
func (l *Library) remember(key string, t *Track) {
	artist := songArtist(t)
	if artist == "" {
		return
	}
	s := sighting{key: key, title: cleanTitle(t.Title, false), isrc: strings.ToUpper(strings.TrimSpace(t.ISRC)), ms: t.DurationMs}
	for _, o := range l.songs[artist] {
		if o == s {
			return
		}
	}
	l.songs[artist] = append(l.songs[artist], s)
}

// sameSong finds an earlier track that is the same recording as t under another key.
func (l *Library) sameSong(t *Track) *Track {
	artist := songArtist(t)
	if artist == "" {
		return nil
	}
	title, isrc := cleanTitle(t.Title, false), strings.ToUpper(strings.TrimSpace(t.ISRC))
	var byISRC *Track
	for _, o := range l.songs[artist] {
		// A listing without a length stands for the song it was merged into, so that it
		// can't bridge two versions of different lengths.
		ms := o.ms
		if ms == 0 {
			ms = l.Tracks[o.key].DurationMs
		}
		known := t.DurationMs > 0 && ms > 0
		gap := max(t.DurationMs-ms, ms-t.DurationMs)
		switch {
		case title != "" && o.title == title && (!known || gap <= sameTitleSlackMs):
			return l.Tracks[o.key]
		case byISRC == nil && isrc != "" && o.isrc == isrc && known && gap <= sameISRCSlackMs:
			byISRC = l.Tracks[o.key]
		}
	}
	return byISRC
}

func (l *Library) Ordered() []*Track {
	out := make([]*Track, 0, len(l.Order))
	for _, k := range l.Order {
		out = append(out, l.Tracks[k])
	}
	return out
}

// filtered returns a view with only the chosen playlists and the tracks they contain.
// Tracks are shared with l, so lookups made on the view are saved with l.
func (l *Library) filtered(only, exclude []string, noLiked bool) *Library {
	matches := func(name string, list []string) bool {
		for _, n := range list {
			if strings.EqualFold(strings.TrimSpace(n), name) {
				return true
			}
		}
		return false
	}
	out := &Library{ImportedAt: l.ImportedAt, Inputs: l.Inputs, Tracks: map[string]*Track{}}
	for _, p := range l.Playlists {
		if (len(only) > 0 && !matches(p.Name, only)) || matches(p.Name, exclude) || (noLiked && p.Name == likedSongs) {
			continue
		}
		out.Playlists = append(out.Playlists, p)
		for _, k := range p.Keys {
			out.Tracks[k] = l.Tracks[k]
		}
	}
	for _, k := range l.Order {
		if out.Tracks[k] != nil {
			out.Order = append(out.Order, k)
		}
	}
	for _, s := range l.Skipped {
		if (len(only) == 0 || matches(s.Playlist, only)) && !matches(s.Playlist, exclude) && !(noLiked && s.Playlist == likedSongs) {
			out.Skipped = append(out.Skipped, s)
		}
	}
	return out
}

func (l *Library) save(path string) error {
	data, err := json.MarshalIndent(l, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func LoadLibrary(path string) (*Library, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l := newLibrary()
	if err := json.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return l, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
