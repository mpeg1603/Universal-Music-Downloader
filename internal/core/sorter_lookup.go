package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sort & merge, the web: for files whose tags and names don't say enough. MusicBrainz finds the
// album a known artist and title were released on; Deezer is the fallback, and handles bare file
// names. Every answer is remembered, so an interrupted or repeated plan doesn't ask again.

type sortMatch struct {
	Source      string `json:"source"`
	Artist      string `json:"artist,omitempty"`
	Title       string `json:"title,omitempty"`
	Album       string `json:"album,omitempty"`
	AlbumArtist string `json:"album_artist,omitempty"`
	Track       int    `json:"track,omitempty"`
	Disc        int    `json:"disc,omitempty"`
	Year        string `json:"year,omitempty"`
	Single      bool   `json:"single,omitempty"`
}

type sortIdentifier struct {
	path     string
	mb       *HTTP
	deezer   *Deezer
	mu       sync.Mutex
	cache    map[string]*sortMatch
	dirty    int
	failures map[string]int
	disabled map[string]bool
	requests int
}

func newSortIdentifier(path string, dz *Deezer) *sortIdentifier {
	mb := newHTTP("MusicBrainz", 1100*time.Millisecond) // MusicBrainz asks for one request a second
	mb.ua = "UniversalMusicDownloader/1 ( personal music library organizer )"
	id := &sortIdentifier{path: path, mb: mb, deezer: dz, cache: map[string]*sortMatch{}, failures: map[string]int{}, disabled: map[string]bool{}}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &id.cache)
	}
	return id
}

func (id *sortIdentifier) save() {
	id.mu.Lock()
	defer id.mu.Unlock()
	if data, err := json.Marshal(id.cache); err == nil {
		writeFileAtomic(id.path, data)
	}
	id.dirty = 0
}

// sortAsker answers one question to one service.
type sortAsker func(service, key string, do func() (*sortMatch, error)) *sortMatch

func (id *sortIdentifier) lookup(ctx context.Context, a *sortAudio, m *sortMeta, nameQuery string) *sortMatch {
	return id.consult(ctx, a, m, nameQuery, func(service, key string, do func() (*sortMatch, error)) *sortMatch {
		return id.ask(ctx, service, key, do)
	})
}

// answered says whether everything a lookup would ask has been asked before.
func (id *sortIdentifier) answered(a *sortAudio, m *sortMeta, nameQuery string) bool {
	all := true
	id.consult(context.Background(), a, m, nameQuery, func(service, key string, _ func() (*sortMatch, error)) *sortMatch {
		id.mu.Lock()
		defer id.mu.Unlock()
		match, found := id.cache[service+"|"+key]
		all = all && found
		return match
	})
	return all
}

func (id *sortIdentifier) consult(ctx context.Context, a *sortAudio, m *sortMeta, nameQuery string, ask sortAsker) *sortMatch {
	bucket := int(math.Round(a.Duration / 4))
	if m.artist != "" && m.title != "" {
		key := fmt.Sprintf("%s|%s|%d", sortArtistKey(m.artist), sortTitleKey(m.title), bucket)
		match := ask("musicbrainz", key, func() (*sortMatch, error) { return id.musicbrainz(ctx, m.artist, m.title, a.Duration) })
		if match != nil && (match.Album != "" || match.Single) {
			return match
		}
		q := sortPrimaryArtist(m.artist) + " " + m.title
		if d := ask("deezer", key, func() (*sortMatch, error) { return id.deezerSearch(ctx, q, m.artist, m.title, a.Duration) }); d != nil {
			return d
		}
		return match
	}
	var parts []string
	if m.artist != "" {
		parts = append(parts, m.artist)
	}
	if t := FirstNonEmpty(m.title, nameQuery); t != "" {
		parts = append(parts, t)
	}
	q := strings.Join(parts, " ")
	if q == "" {
		return nil
	}
	key := fmt.Sprintf("q:%s|%d", sortFold(q, false), bucket)
	return ask("deezer", key, func() (*sortMatch, error) { return id.deezerSearch(ctx, q, "", m.title, a.Duration) })
}

func (id *sortIdentifier) ask(ctx context.Context, service, key string, ask func() (*sortMatch, error)) *sortMatch {
	full := service + "|" + key
	id.mu.Lock()
	cached, found := id.cache[full]
	off := id.disabled[service]
	id.mu.Unlock()
	if found {
		return cached
	}
	if off || ctx.Err() != nil {
		return nil
	}
	match, err := ask()
	id.mu.Lock()
	defer id.mu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			if id.failures[service]++; id.failures[service] >= 5 {
				id.disabled[service] = true
				Logf("Sort & merge: %s keeps failing (%v); not using it for the rest of this plan", service, err)
			}
		}
		return nil
	}
	id.requests++
	id.failures[service] = 0
	id.cache[full] = match
	if id.dirty++; id.dirty >= 50 {
		id.dirty = 0
		go id.save()
	}
	return match
}

type mbCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
}

func mbName(c []mbCredit) string {
	var b strings.Builder
	for _, x := range c {
		b.WriteString(x.Name + x.JoinPhrase)
	}
	return strings.TrimSpace(b.String())
}

type mbRelease struct {
	Status       string     `json:"status"`
	Title        string     `json:"title"`
	Date         string     `json:"date"`
	TrackCount   int        `json:"track-count"`
	ArtistCredit []mbCredit `json:"artist-credit"`
	Group        struct {
		Title     string   `json:"title"`
		Primary   string   `json:"primary-type"`
		Secondary []string `json:"secondary-types"`
	} `json:"release-group"`
	Media []struct {
		Position int `json:"position"`
		Offset   int `json:"track-offset"`
		Track    []struct {
			Number string `json:"number"`
		} `json:"track"`
	} `json:"media"`
}

type mbRecording struct {
	Score          int         `json:"score"`
	Video          bool        `json:"video"`
	Title          string      `json:"title"`
	Length         float64     `json:"length"`
	Disambiguation string      `json:"disambiguation"`
	ArtistCredit   []mbCredit  `json:"artist-credit"`
	Releases       []mbRelease `json:"releases"`
}

var reSortLucene = regexp.MustCompile(`([+\-&|!(){}\[\]^"~*?:\\/])`)

func sortLucene(s string) string { return reSortLucene.ReplaceAllString(s, `\$1`) }

var sortVersionWords = []string{"live", "demo", "acoustic", "remix", "instrumental", "karaoke", "rehearsal", "unplugged", "edit"}

// Album < EP < Single; compilations, live albums, remix albums and the like aren't used for filing.
func sortTypeRank(primary string, secondary []string, releaseArtist, artist string) (int, bool) {
	for _, s := range secondary {
		if !strings.EqualFold(s, "soundtrack") {
			return 0, false
		}
	}
	if releaseArtist != "" && !sortSameArtist(releaseArtist, artist) {
		return 0, false
	}
	switch strings.ToLower(primary) {
	case "album":
		return 0, true
	case "ep":
		return 1, true
	case "single":
		return 2, true
	}
	return 0, false
}

func (id *sortIdentifier) musicbrainz(ctx context.Context, artist, title string, duration float64) (*sortMatch, error) {
	query := fmt.Sprintf(`recording:"%s" AND artist:"%s"`, sortLucene(title), sortLucene(sortPrimaryArtist(artist)))
	if duration > 0 {
		query += fmt.Sprintf(" AND dur:[%d TO %d]", int((duration-10)*1000), int((duration+10)*1000))
	}
	var data struct {
		Recordings []mbRecording `json:"recordings"`
	}
	u := "https://musicbrainz.org/ws/2/recording/?fmt=json&limit=50&query=" + url.QueryEscape(query)
	if err := id.mb.getJSON(ctx, u, &data); err != nil {
		return nil, err
	}
	type pick struct {
		rank, disc, tracks int
		date               string
		rec                *mbRecording
		rel                *mbRelease
		credit             string
	}
	var best *pick
	var fallback *sortMatch
	for i := range data.Recordings {
		rec := &data.Recordings[i]
		if !sortSameRecording(rec, artist, title, duration) {
			continue
		}
		credit := FirstNonEmpty(mbName(rec.ArtistCredit), artist)
		if fallback == nil {
			fallback = &sortMatch{Source: "musicbrainz", Artist: credit, Title: rec.Title}
		}
		for j := range rec.Releases {
			rel := &rec.Releases[j]
			if s := strings.ToLower(rel.Status); s != "" && s != "official" {
				continue
			}
			rank, ok := sortTypeRank(rel.Group.Primary, rel.Group.Secondary, mbName(rel.ArtistCredit), credit)
			if !ok {
				continue
			}
			disc := 1
			if len(rel.Media) > 0 && rel.Media[0].Position > 0 {
				disc = rel.Media[0].Position
			}
			p := &pick{rank, disc, rel.TrackCount, FirstNonEmpty(rel.Date, "9999"), rec, rel, credit}
			// the earliest release wins; among editions of a day, the one with the song on disc 1
			if best == nil || p.rank < best.rank || (p.rank == best.rank && (p.date < best.date ||
				(p.date == best.date && (p.disc < best.disc || (p.disc == best.disc && p.tracks < best.tracks))))) {
				best = p
			}
		}
	}
	if best == nil {
		return fallback, nil
	}
	m := &sortMatch{Source: "musicbrainz", Artist: best.credit, Title: best.rec.Title}
	if best.date != "9999" && len(best.date) >= 4 {
		m.Year = best.date[:4]
	}
	if best.rank == 2 {
		m.Single = true
		return m, nil
	}
	m.Album = sortStripEdition(FirstNonEmpty(best.rel.Group.Title, best.rel.Title))
	m.AlbumArtist = FirstNonEmpty(mbName(best.rel.ArtistCredit), best.credit)
	if len(best.rel.Media) > 0 {
		media := best.rel.Media[0]
		number := ""
		if len(media.Track) > 0 {
			number = media.Track[0].Number
		}
		if n, err := strconv.Atoi(number); err == nil {
			m.Track = n
		} else {
			m.Track = media.Offset + 1
		}
		m.Disc = media.Position
	}
	return m, nil
}

func sortSameRecording(rec *mbRecording, artist, title string, duration float64) bool {
	if rec.Score < 80 || rec.Video || !sortSameArtist(mbName(rec.ArtistCredit), artist) ||
		!sortSimilar(sortTitleKey(rec.Title), sortTitleKey(title)) {
		return false
	}
	if length := rec.Length / 1000; duration > 0 && length > 0 && abs(length-duration) > 10 {
		return false
	}
	note, wanted := strings.Fields(sortFold(rec.Disambiguation, false)), strings.Fields(sortFold(title, false))
	for _, w := range sortVersionWords {
		if ContainsString(note, w) && !ContainsString(wanted, w) {
			return false
		}
	}
	return true
}

type sortDeezerHit struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	TitleShort string `json:"title_short"`
	Duration   int    `json:"duration"`
	Artist     struct {
		Name string `json:"name"`
	} `json:"artist"`
	Album struct {
		ID int64 `json:"id"`
	} `json:"album"`
}

func (id *sortIdentifier) deezerSearch(ctx context.Context, query, artist, title string, duration float64) (*sortMatch, error) {
	var data struct {
		Data []sortDeezerHit `json:"data"`
	}
	if err := id.deezer.get(ctx, id.deezer.base+"/search?limit=25&q="+url.QueryEscape(query), &data); err != nil {
		return nil, err
	}
	var hits []sortDeezerHit
	for _, h := range data.Data {
		if sortAcceptable(&h, query, artist, title, duration) {
			hits = append(hits, h)
		}
	}
	if len(hits) > 5 { // the same song usually appears on the album, singles and compilations
		hits = hits[:5]
	}
	var best *sortMatch
	bestRank := 3
	for i := range hits {
		m := id.deezerMatch(ctx, &hits[i])
		rank := 2
		if m.Album != "" {
			rank = 0
		} else if m.Single {
			rank = 1
		}
		if rank < bestRank {
			best, bestRank = m, rank
		}
	}
	return best, nil
}

func sortAcceptable(h *sortDeezerHit, query, artist, title string, duration float64) bool {
	gap := 0.0
	if duration > 0 {
		gap = abs(float64(h.Duration) - duration)
	}
	if artist != "" {
		return gap <= 5 && sortSameArtist(h.Artist.Name, artist) && sortSimilar(sortTitleKey(h.Title), sortTitleKey(FirstNonEmpty(title, query)))
	}
	// No artist known: the query is a bare file name like "linkin park in the end".
	words := setOf(strings.Fields(sortFold(query, false))...)
	titleWords := strings.Fields(sortTitleKey(FirstNonEmpty(h.TitleShort, h.Title)))
	if len(titleWords) == 0 {
		return false
	}
	for _, w := range titleWords {
		if !words[w] {
			return false
		}
	}
	all := true
	for _, w := range strings.Fields(sortFold(h.Artist.Name, true)) {
		all = all && words[w]
	}
	if all {
		return gap <= 5
	}
	return duration > 0 && gap <= 2 && sortTitleKey(h.Title) == sortFold(query, false)
}

func (id *sortIdentifier) deezerMatch(ctx context.Context, h *sortDeezerHit) *sortMatch {
	m := &sortMatch{Source: "deezer", Artist: h.Artist.Name, Title: h.Title}
	if h.Album.ID == 0 {
		return m
	}
	var album struct {
		Title       string `json:"title"`
		RecordType  string `json:"record_type"`
		ReleaseDate string `json:"release_date"`
		Artist      struct {
			Name string `json:"name"`
		} `json:"artist"`
		Tracks struct {
			Data []struct {
				ID int64 `json:"id"`
			} `json:"data"`
		} `json:"tracks"`
	}
	if id.deezer.get(ctx, fmt.Sprintf("%s/album/%d", id.deezer.base, h.Album.ID), &album) != nil {
		return m
	}
	switch strings.ToLower(album.RecordType) {
	case "single":
		m.Single = true
	case "album", "ep":
		if !sortIsVarious(album.Artist.Name) && sortSameArtist(album.Artist.Name, h.Artist.Name) {
			m.Album, m.AlbumArtist = sortStripEdition(album.Title), album.Artist.Name
			if len(album.ReleaseDate) >= 4 {
				m.Year = album.ReleaseDate[:4]
			}
			for i, t := range album.Tracks.Data {
				if t.ID == h.ID {
					m.Track = i + 1
				}
			}
		}
	}
	return m
}
