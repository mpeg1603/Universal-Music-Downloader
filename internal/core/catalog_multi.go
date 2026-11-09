package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Finding music by name across several catalogs. Deezer alone misses a lot, particularly small
// and non-English artists, so every search also asks MusicBrainz, the iTunes Store, YouTube
// Music, Bandcamp and Yandex Music, in both alphabets, and what they say about the same thing
// is merged. A misspelled artist is found through MusicBrainz's fuzzy search and looked up
// again under the right name.

type CatalogRef struct {
	Provider string
	ID       string
}

// Release is one album, EP or single of an artist.
type Release struct {
	Ref    CatalogRef   // where its tracks are read from
	Alts   []CatalogRef // the same release in other catalogs
	Title  string
	Artist string
	Year   string
	Kind   string // album, ep, single or other
	Tracks int    // 0: not known yet
}

// ReleasePick is what to add of a release; Tracks nil means all of it.
type ReleasePick struct {
	Release Release
	Tracks  []*Track
}

const (
	provDeezer   = "deezer"
	provMB       = "musicbrainz"
	provITunes   = "itunes"
	provYandex   = "yandex"
	provYTMusic  = "youtube_music"
	provBandcamp = "bandcamp"
)

var providerLabels = map[string]string{provDeezer: "Deezer", provMB: "MusicBrainz", provITunes: "iTunes",
	provYandex: "Yandex Music", provYTMusic: "YouTube Music", provBandcamp: "Bandcamp"}

// ProviderLabel names a catalog for people.
func ProviderLabel(p string) string {
	return FirstNonEmpty(providerLabels[p], providerLabels[provDeezer])
}

const catalogAskLimit = 12 * time.Second // one slow catalog doesn't hold up the others for long

type Catalog struct {
	dz                 *Deezer
	mb, it, yx, yt, bc *HTTP
	m                  *Manager // for what the catalogs of downloads need; nil in tests
}

func newCatalog(dz *Deezer, m *Manager) *Catalog {
	mk := func(name string, gap time.Duration, route string) *HTTP {
		h := newRoutedHTTP(name, gap, route)
		h.retries, h.timeout = 1, 20*time.Second
		return h
	}
	c := &Catalog{dz: dz, m: m,
		mb: mk("MusicBrainz", 1100*time.Millisecond, RouteSystem),
		it: mk("iTunes", 350*time.Millisecond, RouteSystem),
		yx: mk("Yandex Music", 250*time.Millisecond, RouteDirect), // it refuses most VPN exits
		yt: mk("YouTube Music", 200*time.Millisecond, RouteSystem),
		bc: mk("Bandcamp", 300*time.Millisecond, RouteSystem)}
	c.mb.ua = "UniversalMusicDownloader/1.2 ( personal music library tool )"
	return c
}

// Search finds tracks, albums and artists fitting what was typed in every catalog, best first.
func (c *Catalog) Search(ctx context.Context, text string) ([]MusicResult, error) {
	q := parseMusicQuery(text)
	if strings.Join(nonEmpty(q.artist, q.album, q.track, q.a, q.b), "") == "" {
		return nil, nil
	}
	texts := []string{text}
	if alt := altScript(text, false); alt != "" {
		texts = append(texts, alt)
	}
	var mu sync.Mutex
	var found []MusicResult
	var firstErr error
	collect := func(rs []MusicResult, err error) {
		mu.Lock()
		defer mu.Unlock()
		found = append(found, rs...)
		if err != nil && firstErr == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
	}
	var wg sync.WaitGroup
	ask := func(f func(context.Context) ([]MusicResult, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, catalogAskLimit)
			defer cancel()
			collect(f(cctx))
		}()
	}
	var mbArtists []MusicResult
	for i, tx := range texts {
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.dz.SearchMusic(ctx, tx) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.itSearch(ctx, tx, q) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.ytSearch(ctx, tx, q) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.bcSearch(ctx, tx, q) })
		if i == 0 {
			tx := tx
			ask(func(ctx context.Context) ([]MusicResult, error) {
				rs, names, err := c.mbSearch(ctx, tx, q)
				mu.Lock()
				mbArtists = names
				mu.Unlock()
				return rs, err
			})
		}
	}
	yxText := text
	if alt := altScript(text, true); alt != "" && !hasCyrillic(text) {
		yxText = alt
	}
	ask(func(ctx context.Context) ([]MusicResult, error) { return c.yxSearch(ctx, yxText, q) })
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// A typo: the catalogs were asked about the wrong name. MusicBrainz's fuzzy search knows the
	// right one; ask the others about that.
	if correct := correctedArtist(q, mbArtists); correct != "" {
		debugf("search %q: looking for %q instead", text, correct)
		fix := correct
		if b := FirstNonEmpty(q.b, q.track, q.album); b != "" {
			fix += " - " + b
		}
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.dz.SearchMusic(ctx, fix) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.itSearch(ctx, fix, q) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.ytSearch(ctx, fix, q) })
		ask(func(ctx context.Context) ([]MusicResult, error) { return c.bcSearch(ctx, fix, q) })
		wg.Wait()
	}
	if len(found) == 0 {
		return nil, firstErr
	}
	return mergeResults(found, q), nil
}

// correctedArtist is the artist MusicBrainz thinks was meant when that's not what was typed.
func correctedArtist(q musicQuery, artists []MusicResult) string {
	typed := FirstNonEmpty(q.artist, q.a)
	if typed == "" || len(artists) == 0 {
		return ""
	}
	best := artists[0]
	if sim(toLatin(typed), toLatin(best.Title)) >= 99 || best.Score < 88 {
		return "" // it was spelled right, or it's a poor guess
	}
	return best.Title
}

// score is how well a result fits what was typed; it also compares across alphabets.
func (q musicQuery) score(kind, artist, title, album string) float64 {
	fold := func(f func(string, string) float64, a, b string) float64 {
		s := f(a, b)
		if hasCyrillic(a) != hasCyrillic(b) {
			s = max(s, f(toLatin(a), toLatin(b)))
		}
		return max(s, f(foldName(a), foldName(b))-2)
	}
	var parts []float64
	add := func(f func(string, string) float64, a, b string) { parts = append(parts, fold(f, a, b)) }
	if q.artist != "" {
		if kind == "artist" {
			add(sim, q.artist, title)
		} else {
			add(sim, q.artist, artist)
		}
	}
	if q.album != "" {
		switch kind {
		case "album":
			add(titleSim, q.album, title)
		case "track":
			add(titleSim, q.album, album)
		}
	}
	if q.track != "" && kind == "track" {
		add(titleSim, q.track, title)
	}
	if q.a != "" {
		var free float64
		if q.b == "" {
			name := artist + " " + title
			if kind == "artist" {
				name = title
			}
			free = max(fold(sim, q.a, name), fold(titleSim, q.a, title)-5)
			if kind != "artist" {
				free = max(free, fold(sim, q.a, artist)-5) // just the artist's name: all their work fits
			}
		} else {
			for _, o := range [][2]string{{q.a, q.b}, {q.b, q.a}} {
				free = max(free, 0.45*fold(sim, o[0], artist)+0.55*fold(titleSim, o[1], title))
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

// mergeResults scores everything against what was typed and turns several catalogs' answers
// about the same thing into one, which remembers where else it can be found.
func mergeResults(rs []MusicResult, q musicQuery) []MusicResult {
	type group struct {
		best MusicResult
		seen map[string]bool
		refs []CatalogRef
	}
	groups := map[string]*group{}
	var order []string
	for _, r := range rs {
		r.Score = q.score(r.Kind, r.Artist, r.Title, r.Album)
		if r.Provider == "" {
			r.Provider, r.Ref = provDeezer, strconv.FormatInt(r.ID, 10)
		}
		// Spelled alike, not merely similar: "Metalica" and "Metallica" are two different bands.
		key := r.Kind + "\x00" + normalize(toLatin(r.Title))
		if r.Kind != "artist" {
			key += "\x00" + normalize(toLatin(r.Artist))
		}
		ref := CatalogRef{r.Provider, r.Ref}
		g := groups[key]
		if g == nil {
			g = &group{best: r, seen: map[string]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		if !g.seen[r.Provider] { // one per catalog: two artists there with one name are two artists
			g.seen[r.Provider] = true
			g.refs = append(g.refs, ref)
		}
		better := r.Score > g.best.Score+0.5 || r.Score >= g.best.Score-0.5 && providerRank(r.Provider) < providerRank(g.best.Provider)
		count, score, year := max(g.best.Count, r.Count), max(g.best.Score, r.Score), FirstNonEmpty(g.best.Year, r.Year)
		dur, album := g.best.Duration, g.best.Album
		if better {
			g.best = r
		}
		g.best.Count, g.best.Score, g.best.Year = count, score, FirstNonEmpty(g.best.Year, year)
		g.best.Duration, g.best.Album = max(g.best.Duration, dur), FirstNonEmpty(g.best.Album, album)
	}
	out := make([]MusicResult, 0, len(order))
	for _, k := range order {
		g := groups[k]
		g.best.Refs = g.refs
		if g.best.Score >= 45 {
			out = append(out, g.best)
		}
	}
	sortResults(out, q)
	kept := out[:0]
	counts := map[string]int{}
	for _, r := range out {
		if counts[r.Kind]++; counts[r.Kind] <= 15 {
			kept = append(kept, r)
		}
	}
	return kept
}

func providerRank(p string) int {
	switch p {
	case provDeezer:
		return 0
	case provITunes:
		return 1
	case provYandex:
		return 2
	case provMB:
		return 3
	case provBandcamp:
		return 4
	}
	return 5
}

// ownRef is where this result came from; a bare Deezer result only has its number.
func (r MusicResult) ownRef() CatalogRef {
	return CatalogRef{FirstNonEmpty(r.Provider, provDeezer), FirstNonEmpty(r.Ref, strconv.FormatInt(r.ID, 10))}
}

// Expand turns a result into tracks and names the list, like Deezer's for Deezer's results.
func (c *Catalog) Expand(ctx context.Context, r MusicResult, progress func(string)) ([]*Track, string, error) {
	switch r.Kind {
	case "track":
		t, err := c.trackOf(ctx, r)
		if err != nil {
			return nil, "", err
		}
		return []*Track{t}, t.String(), nil
	case "album":
		refs := r.Refs
		if len(refs) == 0 {
			refs = []CatalogRef{r.ownRef()}
		}
		var last error
		for _, ref := range refs {
			tracks, artist, err := c.releaseTracks(ctx, Release{Ref: ref, Title: r.Title, Artist: r.Artist, Year: r.Year})
			if err == nil && len(tracks) > 0 {
				return tracks, FirstNonEmpty(artist, r.Artist) + " - " + FirstNonEmpty(tracks[0].Album, r.Title), nil
			}
			last = err
		}
		if last == nil {
			last = fmt.Errorf("found no tracks on that album")
		}
		return nil, "", last
	case "artist":
		releases, err := c.ArtistReleases(ctx, r)
		if err != nil {
			return nil, "", err
		}
		picks := make([]ReleasePick, len(releases))
		for i := range releases {
			picks[i].Release = releases[i]
		}
		tracks, err := c.tracksOf(ctx, r.Title, picks, progress)
		return tracks, r.Title + " (discography)", err
	}
	return nil, "", fmt.Errorf("unknown kind %q", r.Kind)
}

// tracksOf is what's picked of an artist's releases: each song once, a song taken from the first
// release that has it (the albums come first).
func (c *Catalog) tracksOf(ctx context.Context, artist string, picks []ReleasePick, progress func(string)) ([]*Track, error) {
	seen := map[string]bool{}
	var out []*Track
	for i, p := range picks {
		if progress != nil {
			progress(fmt.Sprintf("%s: release %d of %d (%s)", artist, i+1, len(picks), p.Release.Title))
		}
		tracks := p.Tracks
		if tracks == nil {
			var err error
			tracks, err = c.ReleaseTracks(ctx, p.Release)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				debugf("discography of %s: %s: %v", artist, p.Release.Title, err)
				continue
			}
		}
		for _, t := range tracks {
			if len(t.Artists) > 0 && !strings.EqualFold(t.Artists[0], artist) && sim(toLatin(t.Artists[0]), toLatin(artist)) < 80 {
				continue // someone else's track on a split or compilation
			}
			k := foldName(songKey(t.Title))
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("found no tracks of %s", artist)
	}
	return out, nil
}

var releaseKindRank = map[string]int{"album": 0, "ep": 1, "single": 2, "other": 3}

// ArtistReleases is an artist's discography, from every catalog that knows them, newest albums
// first within each kind.
func (c *Catalog) ArtistReleases(ctx context.Context, r MusicResult) ([]Release, error) {
	refs := r.Refs
	if len(refs) == 0 {
		refs = []CatalogRef{r.ownRef()}
	}
	var mu sync.Mutex
	var all []Release
	var firstErr error
	var wg sync.WaitGroup
	for _, ref := range refs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			rs, err := c.releases(cctx, ref, r.Title)
			mu.Lock()
			defer mu.Unlock()
			all = append(all, rs...)
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}()
	}
	wg.Wait()
	if len(all) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("found no releases of %s", r.Title)
		}
		return nil, firstErr
	}
	return mergeReleases(all), nil
}

func mergeReleases(all []Release) []Release {
	groups := map[string]*Release{}
	var order []string
	for _, rel := range all {
		if rel.Kind == "" {
			rel.Kind = "other"
		}
		k := foldName(reEditionTail.ReplaceAllString(rel.Title, ""))
		g := groups[k]
		if g == nil {
			c := rel
			groups[k] = &c
			order = append(order, k)
			continue
		}
		better := rel.Tracks > g.Tracks || rel.Tracks == g.Tracks && providerRank(rel.Ref.Provider) < providerRank(g.Ref.Provider)
		other := rel.Ref
		if better {
			other = g.Ref
			keepKind, keepYear := g.Kind, g.Year
			alts := g.Alts
			*g = rel
			g.Alts = alts
			if releaseKindRank[keepKind] < releaseKindRank[g.Kind] {
				g.Kind = keepKind
			}
			g.Year = FirstNonEmpty(g.Year, keepYear)
		}
		g.Alts = append(g.Alts, other)
		g.Year = FirstNonEmpty(g.Year, rel.Year)
		if releaseKindRank[rel.Kind] < releaseKindRank[g.Kind] {
			g.Kind = rel.Kind
		}
	}
	out := make([]Release, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := releaseKindRank[out[i].Kind], releaseKindRank[out[j].Kind]; a != b {
			return a < b
		}
		if a, b := reEdition.MatchString(out[i].Title), reEdition.MatchString(out[j].Title); a != b {
			return !a
		}
		return out[i].Year > out[j].Year
	})
	return out
}

var reEditionTail = regexp.MustCompile(`(?i)\s*[\(\[][^\)\]]*(?:deluxe|edition|remaster|anniversary|expanded|reissue|lp|ep|single)[^\)\]]*[\)\]]\s*$|\s+-\s+(?:single|ep)\s*$`)

// ReleaseTracks reads a release's tracks, falling back on the other catalogs that have it.
func (c *Catalog) ReleaseTracks(ctx context.Context, rel Release) ([]*Track, error) {
	var last error
	for _, ref := range append([]CatalogRef{rel.Ref}, rel.Alts...) {
		cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
		r := rel
		r.Ref = ref
		tracks, _, err := c.releaseTracks(cctx, r)
		cancel()
		if err == nil && len(tracks) > 0 {
			return tracks, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("%s has no tracks", rel.Title)
	}
	return nil, last
}

func (c *Catalog) releases(ctx context.Context, ref CatalogRef, name string) ([]Release, error) {
	switch ref.Provider {
	case provDeezer:
		return c.dzReleases(ctx, ref.ID)
	case provMB:
		return c.mbReleases(ctx, ref.ID)
	case provITunes:
		return c.itReleases(ctx, ref.ID)
	case provYandex:
		return c.yxReleases(ctx, ref.ID, name)
	case provYTMusic:
		return c.ytReleases(ctx, ref.ID, name)
	case provBandcamp:
		return c.bcReleases(ctx, ref.ID, name)
	}
	return nil, fmt.Errorf("unknown catalog %q", ref.Provider)
}

func (c *Catalog) releaseTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	switch rel.Ref.Provider {
	case provDeezer, "":
		id, _ := strconv.ParseInt(rel.Ref.ID, 10, 64)
		tracks, al, err := c.dz.albumTracks(ctx, id)
		return tracks, al.Artist.Name, err
	case provMB:
		return c.mbTracks(ctx, rel)
	case provITunes:
		return c.itTracks(ctx, rel)
	case provYandex:
		return c.yxTracks(ctx, rel)
	case provYTMusic:
		return c.ytTracks(ctx, rel)
	case provBandcamp:
		return c.bcTracks(ctx, rel)
	}
	return nil, "", fmt.Errorf("unknown catalog %q", rel.Ref.Provider)
}

// trackOf makes the track of a search result.
func (c *Catalog) trackOf(ctx context.Context, r MusicResult) (*Track, error) {
	if r.Provider == "" || r.Provider == provDeezer {
		id := r.ID
		if id == 0 {
			id, _ = strconv.ParseInt(r.Ref, 10, 64)
		}
		var t dzTrack
		if err := c.dz.get(ctx, fmt.Sprintf(c.dz.base+"/track/%d", id), &t); err != nil {
			return nil, err
		}
		return dzToTrack(t, t.Album, ""), nil
	}
	t := &Track{Key: r.Provider + ":" + r.Ref, Title: r.Title, Artists: []string{r.Artist}, Album: r.Album,
		DurationMs: r.Duration * 1000, Enriched: r.Provider != provBandcamp && r.Provider != provYTMusic}
	if r.Year != "" {
		t.ReleaseDate = r.Year
	}
	switch r.Provider {
	case provYTMusic:
		t.SourceURL = "https://music.youtube.com/watch?v=" + r.Ref
	case provBandcamp:
		t.SourceURL = r.Ref
	}
	return t, nil
}

func releaseKind(s string) string {
	switch strings.ToLower(s) {
	case "album", "lp":
		return "album"
	case "ep":
		return "ep"
	case "single":
		return "single"
	}
	return "other"
}

// --- Deezer ------------------------------------------------------------------------------------

func (c *Catalog) dzReleases(ctx context.Context, id string) ([]Release, error) {
	var albums []dzAlbum
	next := c.dz.base + "/artist/" + id + "/albums?limit=100"
	for next != "" && len(albums) < 1000 {
		var res struct {
			Data []dzAlbum
			Next string
		}
		if err := c.dz.get(ctx, next, &res); err != nil {
			return nil, err
		}
		albums = append(albums, res.Data...)
		next = res.Next
	}
	var out []Release
	for _, a := range albums {
		out = append(out, Release{Ref: CatalogRef{provDeezer, strconv.FormatInt(a.ID, 10)}, Title: a.Title, Artist: a.Artist.Name,
			Year: year(a.ReleaseDate), Kind: releaseKind(a.RecordType), Tracks: a.NbTracks})
	}
	return out, nil
}

// --- MusicBrainz --------------------------------------------------------------------------------

type mbCredits []struct {
	Name   string `json:"name"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
}

func (a mbCredits) String() string {
	var names []string
	for _, x := range a {
		names = append(names, FirstNonEmpty(x.Name, x.Artist.Name))
	}
	return strings.Join(names, ", ")
}

const mbBase = "https://musicbrainz.org/ws/2/"

func mbFuzzy(s string) string {
	var clause []string
	for _, w := range strings.Fields(normalize(s)) {
		if len([]rune(w)) >= 4 {
			clause = append(clause, w+"~")
		} else {
			clause = append(clause, w)
		}
	}
	return strings.Join(clause, " AND ")
}

func (c *Catalog) mbSearch(ctx context.Context, text string, q musicQuery) ([]MusicResult, []MusicResult, error) {
	free := strings.Join(nonEmpty(q.artist, q.album, q.track, q.a, q.b), " ")
	get := func(kind, query string, out any) error {
		return c.mb.getJSON(ctx, mbBase+kind+"?fmt=json&limit=8&query="+url.QueryEscape(query), out)
	}
	var out, artists []MusicResult
	var firstErr error
	if q.track == "" && q.album == "" && q.b == "" {
		var res struct {
			Artists []struct {
				ID, Name string
				Score    int
			}
		}
		if err := get("artist", fmt.Sprintf("artist:(%s) OR artist:(%s)", luceneTerm(free), mbFuzzy(free)), &res); err != nil {
			firstErr = err
		}
		for _, x := range res.Artists {
			if x.Score >= 65 {
				r := MusicResult{Kind: "artist", Provider: provMB, Ref: x.ID, Title: x.Name, Count: 1, Score: float64(x.Score)}
				out = append(out, r)
				artists = append(artists, r)
			}
		}
	}
	if q.kind == "" || q.kind == "album" {
		var res struct {
			ReleaseGroups []struct {
				ID           string
				Title        string
				PrimaryType  string    `json:"primary-type"`
				FirstRelease string    `json:"first-release-date"`
				ArtistCredit mbCredits `json:"artist-credit"`
			} `json:"release-groups"`
		}
		query := fmt.Sprintf("%s", mbFuzzy(free))
		if q.b != "" {
			query = fmt.Sprintf("artist:(%s) AND releasegroup:(%s)", mbFuzzy(q.a), mbFuzzy(q.b))
		}
		if err := get("release-group", query, &res); err != nil && firstErr == nil {
			firstErr = err
		}
		for _, x := range res.ReleaseGroups {
			out = append(out, MusicResult{Kind: "album", Provider: provMB, Ref: x.ID, Title: x.Title, Artist: x.ArtistCredit.String(),
				Year: year(x.FirstRelease)})
		}
	}
	if q.kind == "" || q.kind == "track" {
		var res struct {
			Recordings []struct {
				ID, Title    string
				Length       int
				ArtistCredit mbCredits `json:"artist-credit"`
				Releases     []struct {
					Title string
				}
			}
		}
		query := fmt.Sprintf("recording:(%s)", mbFuzzy(FirstNonEmpty(q.track, q.b, free)))
		if a := FirstNonEmpty(q.artist, q.a); a != "" && (q.b != "" || q.track != "") {
			query += fmt.Sprintf(" AND artist:(%s)", mbFuzzy(a))
		}
		if err := get("recording", query, &res); err != nil && firstErr == nil {
			firstErr = err
		}
		for _, x := range res.Recordings {
			r := MusicResult{Kind: "track", Provider: provMB, Ref: x.ID, Title: x.Title, Artist: x.ArtistCredit.String(), Duration: x.Length / 1000}
			if len(x.Releases) > 0 {
				r.Album = x.Releases[0].Title
			}
			out = append(out, r)
		}
	}
	return out, artists, firstErr
}

func luceneTerm(s string) string {
	return strings.NewReplacer(`"`, " ", `\`, " ", "(", " ", ")", " ", ":", " ").Replace(s)
}

func (c *Catalog) mbReleases(ctx context.Context, artist string) ([]Release, error) {
	var res struct {
		ReleaseGroups []struct {
			ID           string
			Title        string
			PrimaryType  string `json:"primary-type"`
			FirstRelease string `json:"first-release-date"`
		} `json:"release-groups"`
	}
	if err := c.mb.getJSON(ctx, mbBase+"release-group?fmt=json&limit=100&artist="+url.QueryEscape(artist), &res); err != nil {
		return nil, err
	}
	var out []Release
	for _, x := range res.ReleaseGroups {
		out = append(out, Release{Ref: CatalogRef{provMB, x.ID}, Title: x.Title, Year: year(x.FirstRelease), Kind: releaseKind(x.PrimaryType)})
	}
	return out, nil
}

func (c *Catalog) mbTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	var res struct {
		Releases []struct {
			Title, Status, Date string
			ArtistCredit        mbCredits `json:"artist-credit"`
			Media               []struct {
				Position int
				Tracks   []struct {
					Number    string
					Position  int
					Title     string
					Length    int
					Recording struct {
						ID string
					}
				}
			}
		}
	}
	u := mbBase + "release?fmt=json&limit=25&inc=recordings+artist-credits&release-group=" + url.QueryEscape(rel.Ref.ID)
	if err := c.mb.getJSON(ctx, u, &res); err != nil {
		return nil, "", err
	}
	best := -1
	count := func(i int) int {
		n := 0
		for _, m := range res.Releases[i].Media {
			n += len(m.Tracks)
		}
		return n
	}
	for i := range res.Releases {
		if best < 0 || count(i) > count(best) || count(i) == count(best) && res.Releases[i].Status == "Official" && res.Releases[best].Status != "Official" {
			best = i
		}
	}
	if best < 0 {
		return nil, "", fmt.Errorf("MusicBrainz lists no release of %s", rel.Title)
	}
	r := res.Releases[best]
	artist := r.ArtistCredit.String()
	var out []*Track
	for _, m := range r.Media {
		for _, t := range m.Tracks {
			out = append(out, &Track{Key: provMB + ":" + t.Recording.ID, Title: t.Title, Artists: []string{artist}, Album: r.Title, AlbumArtist: artist,
				DurationMs: t.Length, TrackNumber: t.Position, DiscNumber: m.Position, TotalTracks: count(best), ReleaseDate: r.Date, Enriched: true})
		}
	}
	return out, artist, nil
}

// --- iTunes --------------------------------------------------------------------------------------

type itItem struct {
	WrapperType, CollectionType string
	ArtistID                    int64  `json:"artistId"`
	ArtistName                  string `json:"artistName"`
	CollectionID                int64  `json:"collectionId"`
	CollectionName              string `json:"collectionName"`
	TrackID                     int64  `json:"trackId"`
	TrackName                   string `json:"trackName"`
	TrackTimeMillis             int    `json:"trackTimeMillis"`
	TrackNumber                 int    `json:"trackNumber"`
	DiscNumber                  int    `json:"discNumber"`
	TrackCount                  int    `json:"trackCount"`
	ReleaseDate                 string `json:"releaseDate"`
	ArtworkURL100               string `json:"artworkUrl100"`
}

const itBase = "https://itunes.apple.com/"

func (c *Catalog) itSearch(ctx context.Context, text string, q musicQuery) ([]MusicResult, error) {
	term := strings.Join(nonEmpty(q.artist, q.album, q.track, q.a, q.b), " ")
	if text != "" && len(q.artist+q.album+q.track+q.a+q.b) == 0 {
		term = text
	}
	if altQ := parseMusicQuery(text); strings.Join(nonEmpty(altQ.artist, altQ.album, altQ.track, altQ.a, altQ.b), " ") != "" {
		term = strings.Join(nonEmpty(altQ.artist, altQ.album, altQ.track, altQ.a, altQ.b), " ")
	}
	var out []MusicResult
	var firstErr error
	ask := func(entity string, limit int, take func(itItem)) {
		var res struct{ Results []itItem }
		u := fmt.Sprintf(itBase+"search?media=music&limit=%d&entity=%s&term=%s", limit, entity, url.QueryEscape(term))
		if err := c.it.getJSON(ctx, u, &res); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		for _, x := range res.Results {
			take(x)
		}
	}
	if q.kind == "" || q.kind == "artist" {
		ask("musicArtist", 5, func(x itItem) {
			out = append(out, MusicResult{Kind: "artist", Provider: provITunes, Ref: strconv.FormatInt(x.ArtistID, 10), Title: x.ArtistName, Count: 1})
		})
	}
	if q.kind == "" || q.kind == "album" {
		ask("album", 10, func(x itItem) {
			out = append(out, MusicResult{Kind: "album", Provider: provITunes, Ref: strconv.FormatInt(x.CollectionID, 10), Title: x.CollectionName,
				Artist: x.ArtistName, Year: year(x.ReleaseDate), Count: x.TrackCount})
		})
	}
	if q.kind == "" || q.kind == "track" {
		ask("song", 10, func(x itItem) {
			out = append(out, MusicResult{Kind: "track", Provider: provITunes, Ref: strconv.FormatInt(x.TrackID, 10), Title: x.TrackName, Artist: x.ArtistName,
				Album: x.CollectionName, Year: year(x.ReleaseDate), Duration: x.TrackTimeMillis / 1000})
		})
	}
	return out, firstErr
}

func (c *Catalog) itReleases(ctx context.Context, id string) ([]Release, error) {
	var res struct{ Results []itItem }
	if err := c.it.getJSON(ctx, itBase+"lookup?entity=album&limit=200&id="+id, &res); err != nil {
		return nil, err
	}
	var out []Release
	for _, x := range res.Results {
		if x.WrapperType != "collection" {
			continue
		}
		kind, title := "album", x.CollectionName
		switch {
		case strings.HasSuffix(title, " - Single"):
			kind, title = "single", strings.TrimSuffix(title, " - Single")
		case strings.HasSuffix(title, " - EP"):
			kind, title = "ep", strings.TrimSuffix(title, " - EP")
		case x.TrackCount <= 2:
			kind = "single"
		}
		out = append(out, Release{Ref: CatalogRef{provITunes, strconv.FormatInt(x.CollectionID, 10)}, Title: title, Artist: x.ArtistName, Year: year(x.ReleaseDate), Kind: kind, Tracks: x.TrackCount})
	}
	return out, nil
}

func (c *Catalog) itTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	var res struct{ Results []itItem }
	if err := c.it.getJSON(ctx, itBase+"lookup?entity=song&limit=200&id="+rel.Ref.ID, &res); err != nil {
		return nil, "", err
	}
	var out []*Track
	artist := rel.Artist
	for _, x := range res.Results {
		if x.WrapperType == "track" {
			artist = FirstNonEmpty(artist, x.ArtistName)
			out = append(out, &Track{Key: provITunes + ":" + strconv.FormatInt(x.TrackID, 10), Title: x.TrackName, Artists: []string{x.ArtistName},
				Album: x.CollectionName, AlbumArtist: artist, DurationMs: x.TrackTimeMillis, TrackNumber: x.TrackNumber, DiscNumber: x.DiscNumber,
				TotalTracks: x.TrackCount, ReleaseDate: x.ReleaseDate, CoverURL: strings.Replace(x.ArtworkURL100, "100x100", "1000x1000", 1), Enriched: true})
		}
	}
	return out, artist, nil
}

// --- Yandex Music ---------------------------------------------------------------------------------

type yxArtist struct {
	ID   json.Number `json:"id"`
	Name string      `json:"name"`
}

type yxAlbum struct {
	ID         json.Number `json:"id"`
	Title      string      `json:"title"`
	Year       int         `json:"year"`
	TrackCount int         `json:"trackCount"`
	Type       string      `json:"type"`
	Artists    []yxArtist  `json:"artists"`
}

type yxTrack struct {
	ID         json.RawMessage `json:"id"`
	Title      string          `json:"title"`
	DurationMs int             `json:"durationMs"`
	Artists    []yxArtist      `json:"artists"`
	Albums     []yxAlbum       `json:"albums"`
}

func yxNames(a []yxArtist) string {
	var names []string
	for _, x := range a {
		names = append(names, x.Name)
	}
	return strings.Join(names, ", ")
}

func (c *Catalog) yxSearch(ctx context.Context, text string, q musicQuery) ([]MusicResult, error) {
	alt := parseMusicQuery(text)
	term := strings.Join(nonEmpty(alt.artist, alt.album, alt.track, alt.a, alt.b), " ")
	var res struct {
		Result struct {
			Artists struct{ Results []yxArtist }
			Albums  struct{ Results []yxAlbum }
			Tracks  struct{ Results []yxTrack }
		}
	}
	if err := c.yx.getJSON(ctx, yandexAPI+"/search?type=all&page=0&nocorrect=false&text="+url.QueryEscape(term), &res); err != nil {
		return nil, err
	}
	var out []MusicResult
	if q.kind == "" || q.kind == "artist" {
		for _, x := range firstN(res.Result.Artists.Results, 5) {
			out = append(out, MusicResult{Kind: "artist", Provider: provYandex, Ref: x.ID.String(), Title: x.Name, Count: 1})
		}
	}
	if q.kind == "" || q.kind == "album" {
		for _, x := range firstN(res.Result.Albums.Results, 10) {
			out = append(out, MusicResult{Kind: "album", Provider: provYandex, Ref: x.ID.String(), Title: x.Title, Artist: yxNames(x.Artists), Year: yearOf(x.Year), Count: x.TrackCount})
		}
	}
	if q.kind == "" || q.kind == "track" {
		for _, x := range firstN(res.Result.Tracks.Results, 10) {
			r := MusicResult{Kind: "track", Provider: provYandex, Ref: strings.Trim(string(x.ID), `"`), Title: x.Title, Artist: yxNames(x.Artists), Duration: x.DurationMs / 1000}
			if len(x.Albums) > 0 {
				r.Album, r.Year = x.Albums[0].Title, yearOf(x.Albums[0].Year)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func firstN[T any](list []T, n int) []T { return list[:min(len(list), n)] }

func yearOf(y int) string {
	if y > 0 {
		return strconv.Itoa(y)
	}
	return ""
}

func yxKind(a yxAlbum) string {
	if a.Type == "single" || a.TrackCount == 1 {
		return "single"
	}
	return "album"
}

func (c *Catalog) yxReleases(ctx context.Context, id, name string) ([]Release, error) {
	var res struct {
		Result struct{ Albums []yxAlbum }
	}
	if err := c.yx.getJSON(ctx, yandexAPI+"/artists/"+id+"/direct-albums?page-size=200", &res); err != nil {
		return nil, err
	}
	var out []Release
	for _, a := range res.Result.Albums {
		out = append(out, Release{Ref: CatalogRef{provYandex, a.ID.String()}, Title: a.Title, Artist: FirstNonEmpty(yxNames(a.Artists), name), Year: yearOf(a.Year), Kind: yxKind(a), Tracks: a.TrackCount})
	}
	return out, nil
}

func (c *Catalog) yxTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	var res struct {
		Result struct {
			yxAlbum
			Volumes [][]yxTrack
		}
	}
	if err := c.yx.getJSON(ctx, yandexAPI+"/albums/"+rel.Ref.ID+"/with-tracks", &res); err != nil {
		return nil, "", err
	}
	artist := FirstNonEmpty(yxNames(res.Result.Artists), rel.Artist)
	var out []*Track
	n := 0
	for vi, v := range res.Result.Volumes {
		for _, t := range v {
			n++
			out = append(out, &Track{Key: provYandex + ":" + strings.Trim(string(t.ID), `"`), Title: t.Title, Artists: []string{FirstNonEmpty(yxNames(t.Artists), artist)},
				Album: res.Result.Title, AlbumArtist: artist, DurationMs: t.DurationMs, TrackNumber: n, DiscNumber: vi + 1,
				TotalTracks: res.Result.TrackCount, ReleaseDate: yearOf(res.Result.Year), Enriched: true})
		}
	}
	return out, artist, nil
}

// --- YouTube Music ---------------------------------------------------------------------------------

func (c *Catalog) ytPost(ctx context.Context, path string, body map[string]any) (any, error) {
	body["context"] = map[string]any{"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": "1.20251020.01.00", "hl": "en", "gl": "US"}}
	var resp any
	err := c.yt.postJSON(ctx, "https://music.youtube.com/youtubei/v1/"+path+"?prettyPrint=false", body,
		map[string]string{"Origin": "https://music.youtube.com", "Referer": "https://music.youtube.com/"}, &resp)
	return resp, err
}

func ytRuns(v any) string {
	var b strings.Builder
	for _, r := range asSlice(v) {
		b.WriteString(str(dig(r, "text")))
	}
	return b.String()
}

func ytColumn(item any, i int) any {
	cols := asSlice(dig(item, "flexColumns"))
	if i >= len(cols) {
		return nil
	}
	return dig(cols[i], "musicResponsiveListItemFlexColumnRenderer", "text", "runs")
}

func (c *Catalog) ytSearch(ctx context.Context, text string, q musicQuery) ([]MusicResult, error) {
	alt := parseMusicQuery(text)
	term := strings.Join(nonEmpty(alt.artist, alt.album, alt.track, alt.a, alt.b), " ")
	var out []MusicResult
	var firstErr error
	search := func(params string) any {
		resp, err := c.ytPost(ctx, "search", map[string]any{"query": term, "params": params})
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return resp
	}
	if q.kind == "" || q.kind == "artist" {
		for _, it := range firstN(findKey(search("EgWKAQIgAWoMEA4QChADEAQQCRAF"), "musicResponsiveListItemRenderer"), 5) {
			id := str(dig(it, "navigationEndpoint", "browseEndpoint", "browseId"))
			if strings.HasPrefix(id, "UC") {
				out = append(out, MusicResult{Kind: "artist", Provider: provYTMusic, Ref: id, Title: ytRuns(ytColumn(it, 0)), Count: 1})
			}
		}
	}
	if q.kind == "" || q.kind == "album" {
		for _, it := range firstN(findKey(search("EgWKAQIYAWoMEA4QChADEAQQCRAF"), "musicResponsiveListItemRenderer"), 8) {
			id := str(dig(it, "navigationEndpoint", "browseEndpoint", "browseId"))
			if !strings.HasPrefix(id, "MPRE") {
				continue
			}
			r := MusicResult{Kind: "album", Provider: provYTMusic, Ref: id, Title: ytRuns(ytColumn(it, 0))}
			parts := strings.Split(ytRuns(ytColumn(it, 1)), " • ")
			if len(parts) >= 2 {
				r.Artist = parts[1]
			}
			if len(parts) >= 3 {
				r.Year = year(parts[len(parts)-1])
			}
			out = append(out, r)
		}
	}
	if q.kind == "" || q.kind == "track" {
		for _, x := range firstN(parseYTMusicResults(search("EgWKAQIIAWoMEA4QChADEAQQCRAF")), 8) {
			id := x.URL
			if i := strings.LastIndex(id, "v="); i >= 0 {
				id = id[i+2:]
			}
			out = append(out, MusicResult{Kind: "track", Provider: provYTMusic, Ref: id, Title: x.Title, Artist: x.Artist, Album: x.Album, Duration: int(x.Duration)})
		}
	}
	return out, firstErr
}

func (c *Catalog) ytReleases(ctx context.Context, id, name string) ([]Release, error) {
	resp, err := c.ytPost(ctx, "browse", map[string]any{"browseId": id})
	if err != nil {
		return nil, err
	}
	var out []Release
	seen := map[string]bool{}
	for _, it := range findKey(resp, "musicTwoRowItemRenderer") {
		runs := asSlice(dig(it, "title", "runs"))
		if len(runs) == 0 {
			continue
		}
		bid := str(dig(runs[0], "navigationEndpoint", "browseEndpoint", "browseId"))
		if !strings.HasPrefix(bid, "MPRE") || seen[bid] {
			continue
		}
		seen[bid] = true
		sub := ytRuns(dig(it, "subtitle", "runs"))
		parts := strings.Split(sub, " • ")
		kind := "album"
		if strings.HasPrefix(parts[0], "Single") {
			kind = "single"
		} else if strings.HasPrefix(parts[0], "EP") {
			kind = "ep"
		}
		out = append(out, Release{Ref: CatalogRef{provYTMusic, bid}, Title: str(dig(runs[0], "text")), Artist: name, Year: year(parts[len(parts)-1]), Kind: kind})
	}
	return out, nil
}

func (c *Catalog) ytTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	resp, err := c.ytPost(ctx, "browse", map[string]any{"browseId": rel.Ref.ID})
	if err != nil {
		return nil, "", err
	}
	items := findKey(resp, "musicResponsiveListItemRenderer")
	var out []*Track
	for i, it := range items {
		title := ytRuns(ytColumn(it, 0))
		id := str(firstKey(dig(it, "flexColumns"), "videoId"))
		if id == "" {
			id = str(firstKey(dig(it, "overlay"), "videoId"))
		}
		if title == "" || id == "" {
			continue
		}
		artist := ytRuns(ytColumn(it, 1))
		ms := 0
		for _, col := range asSlice(dig(it, "fixedColumns")) {
			if s := ytRuns(dig(col, "musicResponsiveListItemFixedColumnRenderer", "text", "runs")); reClock.MatchString(s) {
				ms = parseDurationMs(s)
			}
		}
		out = append(out, &Track{Key: provYTMusic + ":" + id, Title: title, Artists: []string{FirstNonEmpty(artist, rel.Artist)}, Album: rel.Title, AlbumArtist: rel.Artist,
			DurationMs: ms, TrackNumber: i + 1, TotalTracks: len(items), ReleaseDate: rel.Year, SourceURL: "https://music.youtube.com/watch?v=" + id})
	}
	return out, rel.Artist, nil
}

// --- Bandcamp ------------------------------------------------------------------------------------

func (c *Catalog) bcSearch(ctx context.Context, text string, q musicQuery) ([]MusicResult, error) {
	alt := parseMusicQuery(text)
	term := strings.Join(nonEmpty(alt.artist, alt.album, alt.track, alt.a, alt.b), " ")
	var res struct {
		Auto struct {
			Results []struct {
				Type        string `json:"type"`
				Name        string `json:"name"`
				BandName    string `json:"band_name"`
				AlbumName   string `json:"album_name"`
				ItemURLRoot string `json:"item_url_root"`
				ItemURLPath string `json:"item_url_path"`
			} `json:"results"`
		} `json:"auto"`
	}
	err := c.bc.postJSON(ctx, "https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic", map[string]any{
		"search_text": term, "search_filter": "", "full_page": false, "fan_id": nil}, nil, &res)
	if err != nil {
		return nil, err
	}
	var out []MusicResult
	for _, r := range res.Auto.Results {
		switch {
		case r.Type == "b" && (q.kind == "" || q.kind == "artist"):
			out = append(out, MusicResult{Kind: "artist", Provider: provBandcamp, Ref: r.ItemURLRoot, Title: r.Name, Count: 1})
		case r.Type == "a" && (q.kind == "" || q.kind == "album"):
			out = append(out, MusicResult{Kind: "album", Provider: provBandcamp, Ref: r.ItemURLPath, Title: r.Name, Artist: r.BandName})
		case r.Type == "t" && (q.kind == "" || q.kind == "track"):
			out = append(out, MusicResult{Kind: "track", Provider: provBandcamp, Ref: r.ItemURLPath, Title: r.Name, Artist: r.BandName, Album: r.AlbumName})
		}
	}
	return out, nil
}

var (
	reBCItem  = regexp.MustCompile(`(?s)<li[^>]*data-item-id="[^"]*"[^>]*>\s*<a href="(/(?:album|track)/[^"]+)".*?<p class="title">\s*(.*?)\s*(?:<br|</p>)`)
	reBCTitle = regexp.MustCompile(`(?s)<[^>]*>`)
)

func (c *Catalog) bcReleases(ctx context.Context, root, name string) ([]Release, error) {
	status, body, err := c.bc.get(ctx, strings.TrimSuffix(root, "/")+"/music")
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, failf("Bandcamp answered HTTP %d", status)
	}
	var out []Release
	for _, m := range reBCItem.FindAllSubmatch(body, 400) {
		title := strings.TrimSpace(htmlText(reBCTitle.ReplaceAllString(string(m[2]), "")))
		kind := "album"
		if strings.HasPrefix(string(m[1]), "/track/") {
			kind = "single"
		}
		out = append(out, Release{Ref: CatalogRef{provBandcamp, strings.TrimSuffix(root, "/") + string(m[1])}, Title: title, Artist: name, Kind: kind})
	}
	return out, nil
}

type bcPage struct {
	Artist  string `json:"artist"`
	Current struct {
		Title       string `json:"title"`
		ReleaseDate string `json:"release_date"`
	} `json:"current"`
	AlbumRelease string `json:"album_release_date"`
	Tracks       []struct {
		Title     string  `json:"title"`
		TrackNum  int     `json:"track_num"`
		Duration  float64 `json:"duration"`
		TitleLink string  `json:"title_link"`
		TrackID   int64   `json:"track_id"`
	} `json:"trackinfo"`
}

// bcTracks reads a Bandcamp album or track page; the tracks keep their page, where they're
// downloaded from.
func (c *Catalog) bcTracks(ctx context.Context, rel Release) ([]*Track, string, error) {
	status, body, err := c.bc.get(ctx, rel.Ref.ID)
	if err != nil {
		return nil, "", err
	}
	if status != 200 {
		return nil, "", failf("Bandcamp answered HTTP %d", status)
	}
	var page bcPage
	if !dataAttr(body, "data-tralbum", &page) {
		return nil, "", failf("couldn't read that Bandcamp page")
	}
	u, err := url.Parse(rel.Ref.ID)
	if err != nil {
		return nil, "", err
	}
	root := u.Scheme + "://" + u.Host
	artist := FirstNonEmpty(page.Artist, rel.Artist)
	album := FirstNonEmpty(page.Current.Title, rel.Title)
	date := FirstNonEmpty(page.AlbumRelease, page.Current.ReleaseDate)
	if t, err := time.Parse("02 Jan 2006 15:04:05 MST", date); err == nil {
		date = t.Format("2006-01-02")
	} else {
		date = ""
	}
	var out []*Track
	for _, t := range page.Tracks {
		src := rel.Ref.ID
		if t.TitleLink != "" {
			src = root + t.TitleLink
		}
		out = append(out, &Track{Key: provBandcamp + ":" + src, Title: t.Title, Artists: []string{artist}, Album: album, AlbumArtist: artist,
			DurationMs: int(t.Duration * 1000), TrackNumber: t.TrackNum, TotalTracks: len(page.Tracks), ReleaseDate: date, SourceURL: src})
	}
	return out, artist, nil
}

func htmlText(s string) string { return html.UnescapeString(s) }
