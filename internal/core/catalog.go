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
	AlbumID  int64   // the album of a track (an album's own ID)
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
	reTag       = regexp.MustCompile(`(?i)(?:^|\s)(track|song|title|album|artist|discography|year)\s*:\s*`)
	reMusicDash = regexp.MustCompile(`\s+[-–—]\s+`)
	reYears     = regexp.MustCompile(`^(\d{4})(?:\s*[-–]\s*(\d{4}))?$`)
)

// musicQuery is what was typed: free text (one or two parts around " - "), and fields given
// with tags (artist:, album:, track:, year:).
type musicQuery struct {
	kind                       string // track, album or artist when it's clear ("" any)
	a, b                       string // the free text, split around " - " (b empty without)
	artist, album, track, year string
}

func (q musicQuery) tagged() bool {
	return q.artist != "" || q.album != "" || q.track != "" || q.year != ""
}

func tagName(k string) string {
	switch k = strings.ToLower(k); k {
	case "song", "title":
		return "track"
	case "discography":
		return "artist"
	}
	return k
}

func parseMusicQuery(s string) musicQuery {
	s = strings.TrimSpace(s)
	var q musicQuery
	locs := reTag.FindAllStringSubmatchIndex(s, -1)
	free := s
	if len(locs) > 0 {
		free = s[:locs[0][0]]
	}
	type tag struct{ k, v string }
	var tags []tag
	for i, l := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		tags = append(tags, tag{tagName(s[l[2]:l[3]]), strings.Trim(strings.TrimSpace(s[l[1]:end]), `"'`)})
	}
	split := func(text string) (string, string) {
		parts := reMusicDash.Split(strings.TrimSpace(text), 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		return strings.TrimSpace(parts[0]), ""
	}
	// "album: Artist - Album": one kind in front of "A - B" says which it is.
	if strings.TrimSpace(free) == "" && len(tags) == 1 && tags[0].k != "year" && reMusicDash.MatchString(tags[0].v) {
		q.kind = tags[0].k
		if q.kind == "artist" {
			q.a = tags[0].v
		} else {
			q.a, q.b = split(tags[0].v)
		}
		return q
	}
	for _, t := range tags {
		switch t.k {
		case "artist":
			q.artist = t.v
		case "album":
			q.album = t.v
		case "track":
			q.track = t.v
		case "year":
			q.year = t.v
		}
	}
	q.a, q.b = split(free)
	switch {
	case q.track != "":
		q.kind = "track"
	case q.album != "", q.year != "" && q.artist != "":
		q.kind = "album"
	case q.artist != "" && q.a == "":
		q.kind = "artist"
	}
	return q
}

// years is the range a year field asks for (0, 0: any).
func (q musicQuery) years() (int, int) {
	m := reYears.FindStringSubmatch(strings.TrimSpace(q.year))
	if m == nil {
		return 0, 0
	}
	from, _ := strconv.Atoi(m[1])
	to := from
	if m[2] != "" {
		to, _ = strconv.Atoi(m[2])
	}
	return min(from, to), max(from, to)
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

// SearchMusic finds tracks, albums and artists fitting what was typed, best first. Besides
// "A - B" text, artist:, album:, track: and year: fields narrow it down; each is matched
// loosely (a typo or a missing word still fits).
func (d *Deezer) SearchMusic(ctx context.Context, text string) ([]MusicResult, error) {
	q := parseMusicQuery(text)
	words := strings.Join(nonEmpty(q.artist, q.album, q.track, q.a, q.b), " ")
	if words == "" {
		return nil, nil
	}
	want := func(kind string) bool { return q.kind == "" || q.kind == kind }
	// Deezer's field searches (artist:"…" track:"…") find nothing any more, so plain words are
	// searched, and what comes back is scored here.
	score := func(kind, artist, title, album string) float64 {
		var parts []float64
		if q.artist != "" {
			if kind == "artist" {
				parts = append(parts, sim(q.artist, title))
			} else {
				parts = append(parts, sim(q.artist, artist))
			}
		}
		if q.album != "" {
			switch kind {
			case "album":
				parts = append(parts, titleSim(q.album, title))
			case "track":
				parts = append(parts, titleSim(q.album, album))
			}
		}
		if q.track != "" && kind == "track" {
			parts = append(parts, titleSim(q.track, title))
		}
		if q.a != "" {
			var free float64
			if q.b == "" {
				name := artist + " " + title
				if kind == "artist" {
					name = title
				}
				free = max(sim(q.a, name), titleSim(q.a, title)-5)
			} else {
				for _, o := range [][2]string{{q.a, q.b}, {q.b, q.a}} {
					free = max(free, 0.45*sim(o[0], artist)+0.55*titleSim(o[1], title))
				}
			}
			parts = append(parts, free)
		}
		if len(parts) == 0 {
			return 50
		}
		var sum float64
		for _, p := range parts {
			sum += p
		}
		return sum / float64(len(parts))
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
	if want("artist") && (q.b == "" || q.kind == "artist") && q.album == "" && q.track == "" {
		var res struct{ Data []dzArtist }
		if err := d.search(ctx, "artist", FirstNonEmpty(q.artist, q.a), 6, &res); err != nil {
			firstErr = err
		}
		for i, x := range res.Data {
			// Popular artists first among equally fitting names.
			keep(MusicResult{Kind: "artist", ID: x.ID, Title: x.Name, Count: x.NbAlbum, Score: score("artist", "", x.Name, "") - float64(i)*0.1})
		}
	}
	if want("album") {
		var res struct{ Data []dzAlbum }
		if err := d.search(ctx, "album", words, 15, &res); err != nil {
			firstErr = err
		}
		for i, x := range res.Data {
			keep(MusicResult{Kind: "album", ID: x.ID, AlbumID: x.ID, Title: x.Title, Artist: x.Artist.Name, Year: year(x.ReleaseDate),
				Count: x.NbTracks, Score: score("album", x.Artist.Name, x.Title, "") - float64(i)*0.1})
		}
	}
	if want("track") {
		var res struct{ Data []dzTrack }
		if err := d.search(ctx, "track", words, 15, &res); err != nil {
			firstErr = err
		}
		for i, x := range res.Data {
			keep(MusicResult{Kind: "track", ID: x.ID, AlbumID: x.Album.ID, Title: x.Title, Artist: x.Artist.Name, Album: x.Album.Title,
				Duration: x.Duration, Score: score("track", x.Artist.Name, x.Title, x.Album.Title) - float64(i)*0.1})
		}
	}
	if len(best) == 0 && firstErr != nil {
		return nil, firstErr
	}
	out := make([]MusicResult, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sortResults(out, q)
	// Search answers don't say when an album came out: look that up for the best ones.
	d.fillYears(ctx, out[:min(len(out), 14)])
	if from, to := q.years(); from > 0 {
		for i := range out {
			y, _ := strconv.Atoi(out[i].Year)
			switch {
			case out[i].Kind == "artist":
			case y == 0:
				out[i].Score -= 5
			case y < from || y > to:
				out[i].Score -= 40
			}
		}
		sortResults(out, q)
	}
	return out[:min(len(out), 30)], nil
}

func nonEmpty(list ...string) []string {
	var out []string
	for _, s := range list {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func sortResults(out []MusicResult, q musicQuery) {
	rank := map[string]int{"artist": 0, "album": 1, "track": 2}
	if q.b != "" || q.track != "" {
		rank = map[string]int{"track": 0, "album": 1, "artist": 2}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Score, out[j].Score; a != b {
			return a > b
		}
		return rank[out[i].Kind] < rank[out[j].Kind]
	})
}

// fillYears looks up the release year of the results' albums (kept for later searches).
func (d *Deezer) fillYears(ctx context.Context, rs []MusicResult) {
	for i := range rs {
		r := &rs[i]
		if r.Year != "" || r.AlbumID == 0 {
			continue
		}
		d.mu.Lock()
		y, ok := d.years[r.AlbumID]
		d.mu.Unlock()
		if !ok {
			var al dzAlbum
			if err := d.get(ctx, fmt.Sprintf(d.base+"/album/%d", r.AlbumID), &al); err != nil {
				return
			}
			y = year(al.ReleaseDate)
			d.mu.Lock()
			if d.years == nil {
				d.years = map[int64]string{}
			}
			d.years[r.AlbumID] = y
			d.mu.Unlock()
		}
		r.Year = y
	}
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
