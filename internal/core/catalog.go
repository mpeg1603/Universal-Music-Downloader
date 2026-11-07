package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Finding music by name, for the Search tab and for plain-text lists: what you type ("Artist
// - Title", "Title - Artist", "Artist - Album", or just "Artist") is looked up in Deezer's
// catalog, and a track, a whole album or an artist's whole discography becomes a list of
// tracks to download.

// MusicResult is something found in the catalog.
type MusicResult struct {
	Kind     string  // "track", "album" or "artist"
	ID       int64   // Deezer's
	Title    string  // the track's or album's title; the artist's name for an artist
	Artist   string  // for tracks and albums
	Album    string  // for tracks
	Year     string  // release year
	Count    int     // tracks of an album; albums of an artist
	Duration int     // seconds, for tracks
	Score    float64 // how well it fits what was typed (0-100)
}

func (r MusicResult) String() string {
	switch r.Kind {
	case "artist":
		return r.Title + " (discography)"
	case "album":
		return r.Artist + " - " + r.Title + " (album)"
	}
	return r.Artist + " - " + r.Title
}

var (
	reKindPrefix = regexp.MustCompile(`(?i)^\s*(track|song|album|artist|discography)\s*:\s*`)
	reMusicDash  = regexp.MustCompile(`\s+[-–—]\s+`)
)

// musicQuery is what was typed: an optional kind ("track:", "album:", "artist:") and one or
// two parts around " - ".
type musicQuery struct {
	kind string
	a, b string
}

func parseMusicQuery(s string) musicQuery {
	var q musicQuery
	if m := reKindPrefix.FindStringSubmatch(s); m != nil {
		q.kind = strings.ToLower(m[1])
		switch q.kind {
		case "song":
			q.kind = "track"
		case "discography":
			q.kind = "artist"
		}
		s = s[len(m[0]):]
	}
	parts := reMusicDash.Split(strings.TrimSpace(s), 2)
	q.a = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		q.b = strings.TrimSpace(parts[1])
	}
	if q.kind == "artist" && q.b != "" {
		q.a, q.b = q.a+" - "+q.b, ""
	}
	return q
}

// sim is how alike two names are (0-100); unlike a plain token-set match, extra words count
// against it, so "Get Lucky" fits "Get Lucky" better than "Get Lucky (Radio Edit)".
func sim(a, b string) float64 {
	na, nb := normalize(a), normalize(b)
	return (tokenSetRatio(na, nb) + tokenSortRatio(na, nb)) / 2
}

// titleSim compares a wanted title with a found one, also without the found one's version
// ("(feat. …)", "- Remastered").
func titleSim(want, got string) float64 {
	s := max(sim(want, got), sim(want, songKey(got))-2)
	if reVersionWord.MatchString(got) && !reVersionWord.MatchString(want) {
		s -= 6 // a radio edit, remix or live version you didn't ask for
	}
	return s
}

var reVersionWord = regexp.MustCompile(`(?i)\b(radio edit|edit|remix|mix|live|acoustic|instrumental|demo|karaoke|cover|extended|sped up|slowed)\b`)

type dzArtist struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Link    string `json:"link"`
	NbAlbum int    `json:"nb_album"`
	NbFan   int    `json:"nb_fan"`
}

type dzAlbum struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	CoverXL     string   `json:"cover_xl"`
	NbTracks    int      `json:"nb_tracks"`
	ReleaseDate string   `json:"release_date"`
	RecordType  string   `json:"record_type"`
	Artist      dzArtist `json:"artist"`
}

type dzTrack struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	ISRC          string   `json:"isrc"`
	Duration      int      `json:"duration"`
	TrackPosition int      `json:"track_position"`
	DiskNumber    int      `json:"disk_number"`
	ReleaseDate   string   `json:"release_date"`
	Artist        dzArtist `json:"artist"`
	Album         dzAlbum  `json:"album"`
	Contributors  []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"contributors"`
}

func year(date string) string {
	if len(date) >= 4 && date[:4] != "0000" {
		return date[:4]
	}
	return ""
}

func (d *Deezer) search(ctx context.Context, kind, q string, limit int, out any) error {
	path := "search"
	if kind != "track" {
		path += "/" + kind
	}
	return d.get(ctx, fmt.Sprintf(d.base+"/%s?limit=%d&q=%s", path, limit, url.QueryEscape(q)), out)
}

// SearchMusic finds tracks, albums and artists fitting what was typed, best first.
func (d *Deezer) SearchMusic(ctx context.Context, text string) ([]MusicResult, error) {
	q := parseMusicQuery(text)
	if q.a == "" {
		return nil, nil
	}
	want := func(kind string) bool { return q.kind == "" || q.kind == kind }
	// Deezer's field searches (artist:"…" track:"…") find nothing any more, so plain words
	// are searched, and what comes back is scored against both readings of "A - B".
	type ask struct {
		kind, query string
		orders      [][2]string // artist, title ("" artist: the whole text against both)
	}
	var asks []ask
	if q.b == "" {
		for _, k := range []string{"artist", "album", "track"} {
			if want(k) {
				asks = append(asks, ask{k, q.a, [][2]string{{"", q.a}}})
			}
		}
	} else {
		orders := [][2]string{{q.a, q.b}, {q.b, q.a}}
		for _, k := range []string{"track", "album"} {
			if want(k) {
				asks = append(asks, ask{k, q.a + " " + q.b, orders})
			}
		}
	}
	score := func(a ask, artist, title string) float64 {
		best := 0.0
		for _, o := range a.orders {
			var s float64
			if o[0] == "" {
				s = max(sim(o[1], artist+" "+title), titleSim(o[1], title)-5)
			} else {
				s = 0.45*sim(o[0], artist) + 0.55*titleSim(o[1], title)
			}
			best = max(best, s)
		}
		return best
	}
	var mu sync.Mutex
	best := map[string]MusicResult{}
	keep := func(r MusicResult) {
		k := r.Kind + ":" + strconv.FormatInt(r.ID, 10)
		mu.Lock()
		if old, ok := best[k]; !ok || r.Score > old.Score {
			best[k] = r
		}
		mu.Unlock()
	}
	var firstErr error
	for _, a := range asks {
		switch a.kind {
		case "artist":
			var res struct{ Data []dzArtist }
			if err := d.search(ctx, "artist", a.query, 6, &res); err != nil {
				firstErr = err
				continue
			}
			for i, x := range res.Data {
				// Popular artists first among equally fitting names.
				keep(MusicResult{Kind: "artist", ID: x.ID, Title: x.Name, Count: x.NbAlbum, Score: sim(a.query, x.Name) - float64(i)*0.1})
			}
		case "album":
			var res struct{ Data []dzAlbum }
			if err := d.search(ctx, "album", a.query, 15, &res); err != nil {
				firstErr = err
				continue
			}
			for i, x := range res.Data {
				keep(MusicResult{Kind: "album", ID: x.ID, Title: x.Title, Artist: x.Artist.Name, Year: year(x.ReleaseDate),
					Count: x.NbTracks, Score: score(a, x.Artist.Name, x.Title) - float64(i)*0.1})
			}
		case "track":
			var res struct{ Data []dzTrack }
			if err := d.search(ctx, "track", a.query, 15, &res); err != nil {
				firstErr = err
				continue
			}
			for i, x := range res.Data {
				keep(MusicResult{Kind: "track", ID: x.ID, Title: x.Title, Artist: x.Artist.Name, Album: x.Album.Title,
					Duration: x.Duration, Score: score(a, x.Artist.Name, x.Title) - float64(i)*0.1})
			}
		}
	}
	if len(best) == 0 && firstErr != nil {
		return nil, firstErr
	}
	out := make([]MusicResult, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	rank := map[string]int{"artist": 0, "album": 1, "track": 2}
	if q.b != "" {
		rank = map[string]int{"track": 0, "album": 1, "artist": 2}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Score, out[j].Score; a != b {
			return a > b
		}
		return rank[out[i].Kind] < rank[out[j].Kind]
	})
	return out[:min(len(out), 30)], nil
}

// Expand turns a result into the tracks to download, and names the list ("Artist - Album").
// progress, if not nil, hears how far a discography is.
func (d *Deezer) Expand(ctx context.Context, r MusicResult, progress func(string)) ([]*Track, string, error) {
	switch r.Kind {
	case "track":
		var t dzTrack
		if err := d.get(ctx, fmt.Sprintf(d.base+"/track/%d", r.ID), &t); err != nil {
			return nil, "", err
		}
		tr := dzToTrack(t, t.Album, "")
		return []*Track{tr}, tr.String(), nil
	case "album":
		tracks, al, err := d.albumTracks(ctx, r.ID)
		if err != nil {
			return nil, "", err
		}
		return tracks, al.Artist.Name + " - " + al.Title, nil
	case "artist":
		tracks, err := d.discography(ctx, r.ID, r.Title, progress)
		return tracks, r.Title + " (discography)", err
	}
	return nil, "", fmt.Errorf("unknown kind %q", r.Kind)
}

func dzToTrack(t dzTrack, al dzAlbum, albumArtist string) *Track {
	artists := []string{t.Artist.Name}
	for _, c := range t.Contributors {
		if c.Name != "" && !ContainsString(artists, c.Name) && strings.EqualFold(c.Role, "Main") {
			artists = append(artists, c.Name)
		}
	}
	tr := &Track{Key: "deezer:" + strconv.FormatInt(t.ID, 10), Title: t.Title, Artists: artists, Album: al.Title,
		AlbumArtist: FirstNonEmpty(albumArtist, al.Artist.Name), DurationMs: t.Duration * 1000, TrackNumber: t.TrackPosition,
		DiscNumber: t.DiskNumber, TotalTracks: al.NbTracks, ReleaseDate: FirstNonEmpty(t.ReleaseDate, al.ReleaseDate), ISRC: t.ISRC,
		CoverURL: al.CoverXL, DeezerArtistURL: t.Artist.Link, Enriched: true}
	if tr.ReleaseDate == "0000-00-00" {
		tr.ReleaseDate = ""
	}
	return tr
}

func (d *Deezer) albumTracks(ctx context.Context, id int64) ([]*Track, dzAlbum, error) {
	var al dzAlbum
	if err := d.get(ctx, fmt.Sprintf(d.base+"/album/%d", id), &al); err != nil {
		return nil, al, err
	}
	var res struct{ Data []dzTrack }
	if err := d.get(ctx, fmt.Sprintf(d.base+"/album/%d/tracks?limit=500", id), &res); err != nil {
		return nil, al, err
	}
	var out []*Track
	for _, t := range res.Data {
		out = append(out, dzToTrack(t, al, al.Artist.Name))
	}
	return out, al, nil
}

var reVersionPart = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]|\s+-\s+.*$`)

// songKey is a title without its version ("(Radio Edit)", "- Remastered 2011", "[Live]"): the
// same song on a single, an album and a reissue gets the same key.
func songKey(title string) string {
	if k := normalize(reVersionPart.ReplaceAllString(title, "")); k != "" {
		return k
	}
	return normalize(title)
}

var reEdition = regexp.MustCompile(`(?i)\b(deluxe|edition|remaster(ed)?|anniversary|expanded|reissue|bonus|drumless|instrumental|karaoke)\b`)

// discography is every song of an artist's albums, EPs and singles, each once: the original
// albums come first, so a song is taken from its album rather than a single or a reissue.
func (d *Deezer) discography(ctx context.Context, id int64, name string, progress func(string)) ([]*Track, error) {
	var albums []dzAlbum
	next := fmt.Sprintf(d.base+"/artist/%d/albums?limit=100", id)
	for next != "" && len(albums) < 1000 {
		var res struct {
			Data []dzAlbum
			Next string
		}
		if err := d.get(ctx, next, &res); err != nil {
			return nil, err
		}
		albums = append(albums, res.Data...)
		next = res.Next
	}
	typeRank := map[string]int{"album": 0, "ep": 1, "single": 2}
	var keep []dzAlbum
	for _, a := range albums {
		if _, ok := typeRank[a.RecordType]; ok {
			keep = append(keep, a)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i], keep[j]
		if typeRank[a.RecordType] != typeRank[b.RecordType] {
			return typeRank[a.RecordType] < typeRank[b.RecordType]
		}
		if ea, eb := reEdition.MatchString(a.Title), reEdition.MatchString(b.Title); ea != eb {
			return !ea
		}
		return a.ReleaseDate < b.ReleaseDate
	})
	seen := map[string]bool{}
	var out []*Track
	for i, a := range keep {
		if progress != nil {
			progress(fmt.Sprintf("%s: release %d of %d (%s)", name, i+1, len(keep), a.Title))
		}
		tracks, _, err := d.albumTracks(ctx, a.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			debugf("discography of %s: %s: %v", name, a.Title, err)
			continue
		}
		for _, t := range tracks {
			if !strings.EqualFold(t.Artists[0], name) && sim(t.Artists[0], name) < 90 {
				continue // someone else's track on a split or compilation
			}
			k := songKey(t.Title)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("found no releases of %s", name)
	}
	return out, nil
}

// --- plain-text lists -----------------------------------------------------------------------

// Resolver turns the lines of a plain-text list into tracks, through the catalog. Answers are
// kept (a discography takes many requests), and looked up again after a month.
type Resolver struct {
	dz    *Deezer
	path  string
	mu    sync.Mutex
	cache map[string]resolved
	dirty bool
}

type resolved struct {
	Tracks []*Track `json:"tracks"`
	Label  string   `json:"label"`
	At     int64    `json:"at"`
}

func newResolver(dz *Deezer, path string) *Resolver {
	r := &Resolver{dz: dz, path: path, cache: map[string]resolved{}}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &r.cache)
	}
	return r
}

func (r *Resolver) save() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.dirty || r.path == "" {
		return
	}
	data, err := json.Marshal(r.cache)
	if err == nil {
		os.MkdirAll(filepath.Dir(r.path), 0o755)
		err = writeFileAtomic(r.path, data)
	}
	if err == nil {
		r.dirty = false
	}
}

// Resolve finds what a line names: "Artist - Title" (or "Title - Artist"), "Artist - Album",
// or just "Artist" (the whole discography). "track:", "album:" and "artist:" in front say
// which is meant. ok is false when nothing in the catalog fits well enough.
func (r *Resolver) Resolve(ctx context.Context, line string) (tracks []*Track, label string, ok bool, err error) {
	key := normalize(line)
	if q := parseMusicQuery(line); q.kind != "" {
		key = q.kind + ":" + key
	}
	r.mu.Lock()
	c, hit := r.cache[key]
	r.mu.Unlock()
	if hit && time.Since(time.Unix(c.At, 0)) < 30*24*time.Hour {
		return c.Tracks, c.Label, len(c.Tracks) > 0, nil
	}
	results, err := r.dz.SearchMusic(ctx, line)
	if err != nil {
		return nil, "", false, err
	}
	best, found := pickResult(parseMusicQuery(line), results)
	if found {
		tracks, label, err = r.dz.Expand(ctx, best, nil)
		if err != nil {
			return nil, "", false, err
		}
	}
	r.mu.Lock()
	r.cache[key] = resolved{Tracks: tracks, Label: label, At: time.Now().Unix()}
	r.dirty = true
	r.mu.Unlock()
	return tracks, label, len(tracks) > 0, nil
}

// pickResult chooses for a list line: with " - ", the track or album that fits best (an album
// only when its title fits clearly better than any track's); without, the artist.
func pickResult(q musicQuery, results []MusicResult) (MusicResult, bool) {
	var bestTrack, bestAlbum, bestArtist *MusicResult
	for i := range results {
		r := &results[i]
		switch {
		case r.Kind == "track" && bestTrack == nil:
			bestTrack = r
		case r.Kind == "album" && bestAlbum == nil:
			bestAlbum = r
		case r.Kind == "artist" && bestArtist == nil:
			bestArtist = r
		}
	}
	good := func(r *MusicResult) bool { return r != nil && r.Score >= 75 }
	switch {
	case q.kind == "track" || q.kind == "album" || q.kind == "artist":
		for i := range results {
			if results[i].Kind == q.kind && results[i].Score >= 70 {
				return results[i], true
			}
		}
		return MusicResult{}, false
	case q.b == "":
		if good(bestArtist) {
			return *bestArtist, true
		}
	case good(bestAlbum) && (!good(bestTrack) || bestAlbum.Score > bestTrack.Score+5):
		return *bestAlbum, true
	case good(bestTrack):
		return *bestTrack, true
	}
	return MusicResult{}, false
}
