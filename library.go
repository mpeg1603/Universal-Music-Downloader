package main

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
	Enriched        bool              `json:"enriched,omitempty"` // Deezer lookup already attempted
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
}

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

// add puts a track into a playlist, merging it with an earlier sighting of the same song.
func (l *Library) add(playlistName string, t *Track) {
	if len(t.Artists) == 0 || t.Artists[0] == "" {
		t.Artists = []string{"Unknown Artist"}
	}
	if existing, ok := l.Tracks[t.Key]; ok {
		existing.fillFrom(t)
	} else {
		l.Tracks[t.Key] = t
		l.Order = append(l.Order, t.Key)
	}
	p := l.playlist(playlistName)
	for _, k := range p.Keys {
		if k == t.Key {
			return
		}
	}
	p.Keys = append(p.Keys, t.Key)
}

func (l *Library) ordered() []*Track {
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

func loadLibrary(path string) (*Library, error) {
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
