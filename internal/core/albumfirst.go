package core

import (
	"context"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Album first. When several tracks of one album are still to be downloaded (a whole album or
// discography added, or half an album's songs in your playlists), each source that can hand
// out whole albums is searched once for the album, for all of them: one search instead of one
// per track, and the tracks come from the same download (a torrent, one Soulseek user's
// folder, an album page). Only the tracks that the album searches didn't find go on to
// searches of their own, source by source as usual.

// albumSource is a source that can look for a whole album.
type albumSource interface {
	Source
	// searchAlbum looks for the album the tracks share (tracks[0] is the one asking) and keeps
	// what it found for each of them, which its Search then hands out. It returns how many it
	// found.
	searchAlbum(ctx context.Context, tracks []*Track) (int, error)
	// holds says whether the source has something from an album search for t.
	holds(t *Track) bool
}

const (
	// An album search is done again for the same tracks after this long, if any are left.
	albumSearchTTL = time.Hour
	// albumMinTracks: this many tracks of an album still to do make it worth an album search;
	// fewer do when they're at least half the album.
	albumMinTracks = 3
)

// albumKey says which album a track belongs to, for grouping: its album artist and album. ""
// for a track without one, or a single named after its song.
func albumKey(t *Track) string {
	if t.Album == "" || len(t.Artists) == 0 {
		return ""
	}
	album := normalize(baseAlbum(t.Album))
	if album == "" || album == normalize(searchTitle(t.Title)) {
		return ""
	}
	return foldName(t.AlbumArtistStr()) + "\x00" + album
}

// wantAlbum says whether n tracks still to do of an album with total tracks are worth an
// album search.
func wantAlbum(n, total int) bool {
	return n >= albumMinTracks || n >= 2 && total > 0 && 2*n >= total
}

// albumSearches remembers which album searches were made, and which are under way (the
// tracks of the album that come to that source meanwhile wait for it).
type albumSearches struct {
	mu      sync.Mutex
	done    map[string]time.Time
	running map[string]chan struct{}
}

// albumMates are the tracks of j's album (j's own first) that still need src, by the album
// index of the jobs (see makeJobs).
func (m *Manager) albumMates(j *Job, src string) (string, []*Track) {
	j.mu.Lock()
	t, key := j.Track, j.album
	j.mu.Unlock()
	if key == "" {
		return "", nil
	}
	m.jobsMu.Lock()
	group := append([]*Job(nil), m.byAlbum[key]...)
	m.jobsMu.Unlock()
	out := []*Track{t}
	for _, o := range group {
		if o == j {
			continue
		}
		o.mu.Lock()
		if (o.state == JobQueued || o.state == JobSearching || o.state == JobWaiting && o.pendingOn == "") &&
			!o.tried[src] && o.alt == nil && o.Track.SourceURL == "" {
			out = append(out, o.Track)
		}
		o.mu.Unlock()
	}
	return key, out
}

// nextAlbumSource is the next source, in your order, that can look for whole albums and that
// j hasn't been through (looked) or tried yet. Sources paused or busy are left for the
// track's own searches.
func (m *Manager) nextAlbumSource(j *Job, needBetter bool, libQ Quality, looked map[string]bool) albumSource {
	j.mu.Lock()
	t := j.Track
	tried := make(map[string]bool, len(j.tried))
	for k, v := range j.tried {
		tried[k] = v
	}
	j.mu.Unlock()
	for _, src := range m.currentSources() {
		as, ok := src.(albumSource)
		name := src.Name()
		if !ok || tried[name] || looked[name] {
			continue
		}
		looked[name] = true
		if c, ok := src.(qualityCapper); ok && needBetter && !better(c.MaxQuality(), libQ) {
			continue
		}
		if cool, _, _ := m.cooling(name); cool {
			continue
		}
		if b, ok := src.(interface{ busy(*Track) time.Duration }); ok && b.busy(t) > busyLimit {
			continue
		}
		return as
	}
	return nil
}

// albumFirst makes src's album search for j's album, once for all of its tracks still to do,
// and says whether src now has something for j.
func (m *Manager) albumFirst(ctx context.Context, j *Job, src albumSource, task *Task) bool {
	key, mates := m.albumMates(j, src.Name())
	if len(mates) == 0 {
		return false
	}
	t := mates[0]
	if src.holds(t) {
		return true
	}
	id := src.Name() + "\x00" + key
	a := &m.albums
	a.mu.Lock()
	if a.done == nil {
		a.done, a.running = map[string]time.Time{}, map[string]chan struct{}{}
	}
	if at, ok := a.done[id]; ok && time.Since(at) < albumSearchTTL {
		a.mu.Unlock()
		return false // searched already; this track wasn't found then
	}
	if ch, ok := a.running[id]; ok {
		a.mu.Unlock()
		task.setStage("waiting for the album search on " + src.Label())
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
		return src.holds(t)
	}
	if !wantAlbum(len(mates), t.TotalTracks) {
		a.mu.Unlock()
		return false
	}
	ch := make(chan struct{})
	a.running[id] = ch
	a.mu.Unlock()
	searched := false
	defer func() {
		a.mu.Lock()
		delete(a.running, id)
		if searched {
			a.done[id] = time.Now()
			for k, at := range a.done {
				if time.Since(at) > albumSearchTTL {
					delete(a.done, k)
				}
			}
		}
		a.mu.Unlock()
		close(ch)
	}()

	task.setStage("waiting to search " + src.Label() + " for the album")
	if err := m.searchGate.acquire(ctx, j.priority()+1); err != nil {
		return false
	}
	task.setStage("searching " + src.Label() + " for the whole album")
	n, err := src.searchAlbum(ctx, mates)
	m.searchGate.release()
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		if !isBusy(err) {
			m.recordResult(src, err, true)
		}
		debugf("album search for “%s” on %s: %v", t.Album, src.Label(), err)
		searched = !isBusy(err) && !isUnavailable(err) && !isBlocked(err)
		return false
	}
	m.recordResult(src, nil, true)
	searched = true
	if n == 0 {
		debugf("album search for “%s” (%d tracks to do) on %s found nothing", t.Album, len(mates), src.Label())
		return false
	}
	Logf("%s has %d of the %d tracks of “%s” still to download, as one album", src.Label(), n, len(mates), t.Album)
	// The album's tracks come next, so that they're fetched together.
	held := map[string]bool{}
	for _, mt := range mates[1:] {
		held[mt.Key] = src.holds(mt)
	}
	m.jobsMu.Lock()
	group := append([]*Job(nil), m.byAlbum[key]...)
	m.jobsMu.Unlock()
	for _, o := range group {
		o.mu.Lock()
		if o != j && held[o.Track.Key] && (o.state == JobQueued || o.state == JobWaiting && o.pendingOn == "") {
			o.state, o.retryAt, o.bump = JobQueued, time.Time{}, m.bumpSeq.Add(1)
		}
		o.mu.Unlock()
	}
	m.revs.queue.Add(1)
	m.wake()
	return src.holds(t)
}

// --- what album searches found, until each track takes its part ----------------------------

type albumHit struct {
	cands []Candidate
	at    time.Time
}

// albumHits keeps candidates by track. The zero value is ready to use.
type albumHits struct {
	mu sync.Mutex
	m  map[string]albumHit
}

func (h *albumHits) put(key string, cands []Candidate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]albumHit{}
	}
	for k, x := range h.m {
		if time.Since(x.at) > albumSearchTTL {
			delete(h.m, k)
		}
	}
	h.m[key] = albumHit{cands, time.Now()}
}

func (h *albumHits) has(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	x, ok := h.m[key]
	return ok && time.Since(x.at) < albumSearchTTL
}

// take returns (once) what was kept for key.
func (h *albumHits) take(key string) ([]Candidate, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	x, ok := h.m[key]
	delete(h.m, key)
	if !ok || time.Since(x.at) > albumSearchTTL {
		return nil, false
	}
	return x.cands, true
}

// keepAccepted puts, for each track, the candidates that fit it; it returns how many tracks
// got any.
func keepAccepted(h *albumHits, tracks []*Track, cands []Candidate) int {
	n := 0
	for _, t := range tracks {
		accepted, _ := rank(t, cands)
		if len(accepted) == 0 {
			continue
		}
		var keep []Candidate
		for _, mt := range accepted {
			keep = append(keep, mt.Cand)
		}
		h.put(t.Key, keep)
		n++
	}
	return n
}

// --- the sources ------------------------------------------------------------------------------

// Torrent: the album's searches (by its album artist, then the artists' discographies), and
// the torrent with the most of the tracks.
func (s *TorrentSource) holds(t *Track) bool { return s.hinted(t.Key) }

func (s *TorrentSource) searchAlbum(ctx context.Context, tracks []*Track) (int, error) {
	lead := tracks[0]
	select {
	case s.lane <- struct{}{}:
		defer func() { <-s.lane }()
	case <-time.After(torrentLaneWait):
		return 0, busyf("%d tracks are being looked for on the torrent sites; tried again later", cap(s.lane))
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	var searchErr error
	inspected := map[string]bool{}
	for _, q := range torrentQueries(lead) {
		if q.edition == "" { // the song's own search
			continue
		}
		results, err := s.searchAll(ctx, q.text)
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err != nil {
			searchErr = err
			continue
		}
		var picks []torrentResult
		for _, r := range rankTorrents(results, q.by(lead), q, s.lossless) {
			if len(picks) < q.inspect && !inspected[r.id()] {
				inspected[r.id()] = true
				picks = append(picks, r)
			}
		}
		if n := s.albumIn(ctx, picks, tracks); n > 0 {
			return n, nil
		}
	}
	return 0, searchErr
}

// albumIn reads the file lists of picks (in parallel) and hands the tracks the torrent that
// has the most of them (the best-ranked one of those that have as many). It returns how many
// tracks got it.
func (s *TorrentSource) albumIn(ctx context.Context, picks []torrentResult, tracks []*Track) int {
	type answer struct {
		i    int
		meta *torrentMeta
		err  error
	}
	answers := make(chan answer, len(picks))
	for i, r := range picks {
		go func() {
			m, err := s.meta(ctx, r)
			answers <- answer{i, m, err}
		}()
	}
	fits := make([]map[string]Candidate, len(picks))
	quick := 0
	for _, r := range picks {
		if r.Link != "" {
			quick++
		}
	}
	all := false
	for left := len(picks); left > 0 && !(all && quick == 0); left-- {
		var a answer
		select {
		case a = <-answers:
		case <-ctx.Done():
			return 0
		}
		r := picks[a.i]
		if r.Link != "" {
			quick--
		}
		if a.err != nil {
			debugf("torrent %q: %v", r.Title, a.err)
			continue
		}
		audio := 0
		for _, f := range a.meta.files {
			if audioExts[strings.ToLower(path.Ext(f))] {
				audio++
			}
		}
		found := map[string]Candidate{}
		budget := alongBudget
		for _, t := range tracks {
			if budget -= audio; budget < 0 {
				break
			}
			i, stem := bestFile(t, a.meta.files)
			if i < 0 {
				continue
			}
			c := torrentCandidate(t, r.Title, r.Details, a.meta, i, stem)
			if evaluate(t, c).Reject != "" {
				continue
			}
			// The others' files are a shortcut (searched properly if they don't work out); the
			// asking track's download hands the torrent on to the artist's other albums' tracks
			// still to do (fetchAlong), as a search of its own would.
			c.Unsearched = t != tracks[0]
			found[t.Key] = c
		}
		fits[a.i] = found
		if len(found) == len(tracks) {
			all = true
		}
	}
	best := -1
	for i, f := range fits {
		if len(f) > 0 && (best < 0 || len(f) > len(fits[best])) {
			best = i
		}
	}
	if best < 0 {
		return 0
	}
	for key, c := range fits[best] {
		s.hintFor(key, c, key != tracks[0].Key)
	}
	debugf("“%s” has %d of the %d tracks looked for", picks[best].Title, len(fits[best]), len(tracks))
	return len(fits[best])
}

// Soulseek: one search for the album (the one each of its tracks would make first anyway),
// and each track's file from the user whose folder has the most of them, so the album comes
// from one place.
func (s *Soulseek) holds(t *Track) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.hits[t.Key]
	return ok
}

func (s *Soulseek) searchAlbum(ctx context.Context, tracks []*Track) (int, error) {
	q := slskAlbumQuery(tracks[0])
	if q == "" {
		return 0, nil
	}
	resps, _, err := s.c.searchWithin(ctx, q, slskLineLimit)
	if err != nil {
		return 0, err
	}
	accepted := make([][]Match, len(tracks))
	has := map[string]int{} // user → how many of the tracks they have
	order := map[string]int{}
	for i, t := range tracks {
		accepted[i], _ = rank(t, s.candidates(t, resps))
		seen := map[string]bool{}
		for _, mt := range accepted[i] {
			u, _, _ := strings.Cut(mt.Cand.Ref, "\x00")
			if !seen[u] {
				seen[u] = true
				has[u]++
			}
			if _, ok := order[u]; !ok {
				order[u] = len(order)
			}
		}
	}
	best := ""
	for u, n := range has {
		if best == "" || n > has[best] || n == has[best] && order[u] < order[best] {
			best = u
		}
	}
	n := 0
	for i, t := range tracks {
		if len(accepted[i]) == 0 {
			continue
		}
		var first, rest []Candidate
		for _, mt := range accepted[i] {
			if u, _, _ := strings.Cut(mt.Cand.Ref, "\x00"); u == best {
				first = append(first, mt.Cand)
			} else {
				rest = append(rest, mt.Cand)
			}
		}
		sortByPref(first)
		sortByPref(rest)
		cands := append(first, rest...)
		for k := range cands {
			cands[k].Pref = k + 1
			cands[k].Unsearched = true // waits for this user instead of skipping on; searched properly if it doesn't work out
		}
		s.remember(t.Key, cands)
		n++
	}
	if n > 0 {
		debugf("Soulseek: %s has %d of the %d tracks of “%s”", best, has[best], len(tracks), tracks[0].Album)
	}
	return n, nil
}

func sortByPref(c []Candidate) {
	for i := 1; i < len(c); i++ {
		for k := i; k > 0 && c[k].Pref < c[k-1].Pref; k-- {
			c[k], c[k-1] = c[k-1], c[k]
		}
	}
}

// YouTube Music: the album's page, which lists each of its songs.
func (s *YouTubeMusic) holds(t *Track) bool { return s.hits.has(t.Key) }

const ytmAlbumsFilter = "EgWKAQIYAWoMEA4QChADEAQQCRAF"

func (s *YouTubeMusic) post(ctx context.Context, path string, body map[string]any) (any, error) {
	body["context"] = map[string]any{"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": "1.20251020.01.00", "hl": "en", "gl": "US"}}
	var resp any
	err := s.http.postJSON(ctx, "https://music.youtube.com/youtubei/v1/"+path+"?prettyPrint=false", body,
		map[string]string{"Origin": "https://music.youtube.com", "Referer": "https://music.youtube.com/"}, &resp)
	return resp, err
}

func (s *YouTubeMusic) searchAlbum(ctx context.Context, tracks []*Track) (int, error) {
	lead := tracks[0]
	album, artist := baseAlbum(lead.Album), lead.AlbumArtistStr()
	if variousArtists(artist) {
		artist = ""
	}
	resp, err := s.post(ctx, "search", map[string]any{"query": queryText(artist + " " + album), "params": ytmAlbumsFilter})
	if err != nil {
		return 0, err
	}
	want := &Track{Title: album, Artists: []string{artist}}
	if artist == "" {
		want.Artists = nil
	}
	for _, it := range firstN(findKey(resp, "musicResponsiveListItemRenderer"), 5) {
		id := str(dig(it, "navigationEndpoint", "browseEndpoint", "browseId"))
		if !strings.HasPrefix(id, "MPRE") {
			continue
		}
		title := ytRuns(ytColumn(it, 0))
		by := ""
		if parts := strings.Split(ytRuns(ytColumn(it, 1)), " • "); len(parts) >= 2 {
			by = parts[1]
		}
		if m := evaluate(want, Candidate{Title: title, Artist: by, ArtistIsCredit: true}); m.Reject != "" {
			continue
		}
		page, err := s.post(ctx, "browse", map[string]any{"browseId": id})
		if err != nil {
			return 0, err
		}
		var cands []Candidate
		for _, row := range findKey(page, "musicResponsiveListItemRenderer") {
			name := ytRuns(ytColumn(row, 0))
			vid := str(firstKey(dig(row, "flexColumns"), "videoId"))
			if vid == "" {
				vid = str(firstKey(dig(row, "overlay"), "videoId"))
			}
			if name == "" || vid == "" {
				continue
			}
			c := Candidate{Source: "youtube_music", URL: "https://music.youtube.com/watch?v=" + vid, Title: name,
				Artist: FirstNonEmpty(ytRuns(ytColumn(row, 1)), by), Album: title, ArtistIsCredit: true, Quality: youtubeQuality}
			for _, col := range asSlice(dig(row, "fixedColumns")) {
				if t := ytRuns(dig(col, "musicResponsiveListItemFixedColumnRenderer", "text", "runs")); reClock.MatchString(t) {
					c.Duration = float64(parseDurationMs(t)) / 1000
				}
			}
			cands = append(cands, c)
		}
		if n := keepAccepted(&s.hits, tracks, cands); n > 0 {
			return n, nil
		}
	}
	return 0, nil
}

// Bandcamp: the album's page, found by its name.
func (b *Bandcamp) holds(t *Track) bool { return b.hits.has(t.Key) }

func (b *Bandcamp) searchAlbum(ctx context.Context, tracks []*Track) (int, error) {
	lead := tracks[0]
	album, artist := baseAlbum(lead.Album), lead.AlbumArtistStr()
	if variousArtists(artist) {
		artist = ""
	}
	q := strings.TrimSpace(artist + " " + album)
	type result struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		BandName    string `json:"band_name"`
		ItemURLRoot string `json:"item_url_root"`
		ItemURLPath string `json:"item_url_path"`
	}
	var res struct {
		Auto struct {
			Results []result `json:"results"`
		} `json:"auto"`
	}
	err := b.http.postJSON(ctx, "https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic", map[string]any{
		"search_text": q, "search_filter": "a", "full_page": false, "fan_id": nil,
	}, nil, &res)
	if err != nil {
		return 0, err
	}
	want := &Track{Title: album, Artists: []string{artist}}
	if artist == "" {
		want.Artists = nil
	}
	for _, r := range firstN(res.Auto.Results, 4) {
		if r.Type != "a" || r.ItemURLPath == "" {
			continue
		}
		if m := evaluate(want, Candidate{Title: r.Name, Artist: r.BandName, ArtistIsCredit: true}); m.Reject != "" {
			continue
		}
		page, err := b.tralbum(ctx, r.ItemURLPath)
		if err != nil {
			return 0, err
		}
		if page.FreeDownloadPage == "" && (page.Current.MinimumPrice == nil || *page.Current.MinimumPrice != 0) {
			debugf("Bandcamp: “%s” by %s isn't a free download", r.Name, r.BandName)
			return 0, nil // the tracks' own searches find any that are free on their own
		}
		base, _ := url.Parse(r.ItemURLPath)
		var cands []Candidate
		for _, ti := range page.TrackInfo {
			if ti.TitleLink == "" || base == nil {
				continue
			}
			ref, err := url.Parse(ti.TitleLink)
			if err != nil {
				continue
			}
			cands = append(cands, Candidate{Source: "bandcamp", URL: base.ResolveReference(ref).String(), Title: ti.Title,
				Artist: r.BandName, Album: r.Name, Duration: ti.Duration, ArtistURL: r.ItemURLRoot, ArtistIsCredit: true, Quality: b.MaxQuality()})
		}
		if n := keepAccepted(&b.hits, tracks, cands); n > 0 {
			return n, nil
		}
	}
	return 0, nil
}
