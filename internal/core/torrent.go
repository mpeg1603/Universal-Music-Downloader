package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Torrents as a download source. Searches go through Torznab, the API that Jackett and
// Prowlarr expose for the indexers you add to them; downloads go through aria2c, fetching
// only the file needed and not seeding afterwards. See TORRENTS.md.

// --- settings -------------------------------------------------------------------------------

type TorrentConfig struct {
	Feeds []string `json:"feeds"` // Torznab feed URLs, or Jackett/Prowlarr base URLs
	Key   string   `json:"key"`   // API key (Jackett and Prowlarr use one key for all feeds)
}

func (c TorrentConfig) configured() bool { return len(c.Feeds) > 0 }

func torrentConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.TempDir(), "spotify-downloader-config")
	}
	return filepath.Join(dir, "spotify-downloader", "torrents.json")
}

func loadTorrentConfig() TorrentConfig {
	var c TorrentConfig
	if data, err := os.ReadFile(torrentConfigPath()); err == nil {
		json.Unmarshal(data, &c)
	}
	return c
}

func saveTorrentConfig(c TorrentConfig) error {
	p := torrentConfigPath()
	if !c.configured() {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	data, _ := json.MarshalIndent(c, "", " ")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600) // holds an API key
}

// --- Torznab --------------------------------------------------------------------------------

type Torznab struct {
	Name string
	feed string // ends in /api
	key  string
	http *HTTP
	sem  chan struct{} // searches at once on this indexer

	// Health: an indexer that keeps failing is left out for a while, so it doesn't hold up
	// every search.
	mu       sync.Mutex
	fails    int
	until    time.Time
	lastErr  string
	lastOK   time.Time
	searches int
	errors   int
	avg      time.Duration
}

func newTorznabFeed(name, feed, key string, timeout time.Duration) *Torznab {
	feed = strings.TrimRight(feed, "/")
	if u, err := url.Parse(feed); err == nil && !strings.HasSuffix(u.Path, "/api") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api"
		feed = u.String()
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	h := newHTTP("Torznab feed "+name, 0)
	h.retries, h.timeout = 1, timeout // a slow indexer is skipped, not waited for again and again
	return &Torznab{Name: name, feed: feed, key: key, http: h, sem: make(chan struct{}, 2)}
}

func (z *Torznab) available() bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return !time.Now().Before(z.until)
}

// record notes how a search went. Three failures in a row pause the indexer: 5 minutes,
// then 10, 20, 40, up to 80.
func (z *Torznab) record(err error, took time.Duration) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // given up on by us, not the indexer's fault
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	z.searches++
	if err == nil {
		z.fails, z.lastOK = 0, time.Now()
		if z.avg == 0 {
			z.avg = took
		} else {
			z.avg = (z.avg*3 + took) / 4
		}
		return
	}
	z.errors++
	z.fails++
	z.lastErr = err.Error()
	if z.fails >= 3 {
		d := 5 * time.Minute << min(z.fails-3, 4)
		z.until = time.Now().Add(d)
		Logf("Torrent indexer %s failed %d times in a row (%v); leaving it out for %s", z.Name, z.fails, err, FmtWait(d))
	}
}

type FeedStatus struct {
	Name             string
	OK               bool
	Until            time.Time
	LastErr          string
	Searches, Errors int
	Avg              time.Duration
}

func (z *Torznab) status() FeedStatus {
	z.mu.Lock()
	defer z.mu.Unlock()
	return FeedStatus{Name: z.Name, OK: !time.Now().Before(z.until), Until: z.until, LastErr: z.lastErr,
		Searches: z.searches, Errors: z.errors, Avg: z.avg}
}

func (z *Torznab) url(params url.Values) string {
	u, _ := url.Parse(z.feed)
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if z.key != "" && q.Get("apikey") == "" {
		q.Set("apikey", z.key)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// String shows the feed without its API key.
func (z *Torznab) String() string {
	u, err := url.Parse(z.feed)
	if err != nil {
		return z.feed
	}
	q := u.Query()
	if q.Has("apikey") {
		q.Set("apikey", "…")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func (z *Torznab) get(ctx context.Context, params url.Values) ([]byte, error) {
	status, body, err := z.http.get(ctx, z.url(params))
	if err != nil {
		return nil, err
	}
	switch {
	case status == 401 || status == 403:
		return nil, unavailablef("%s rejected the API key (HTTP %d)", z.Name, status)
	case status == 404:
		return nil, unavailablef("%s: no Torznab feed at %s (HTTP 404)", z.Name, z)
	case status != 200:
		return nil, unavailablef("%s answered HTTP %d", z.Name, status)
	}
	if err := torznabError(body); err != nil {
		return nil, fmt.Errorf("%s: %w", z.Name, err)
	}
	return body, nil
}

func torznabError(body []byte) error {
	var e struct {
		XMLName     xml.Name
		Code        string `xml:"code,attr"`
		Description string `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &e) == nil && e.XMLName.Local == "error" {
		return unavailablef("error %s: %s", e.Code, e.Description)
	}
	return nil
}

// caps checks that the feed answers and accepts the key.
func (z *Torznab) caps(ctx context.Context) error {
	body, err := z.get(ctx, url.Values{"t": {"caps"}})
	if err != nil {
		return err
	}
	var root struct{ XMLName xml.Name }
	if xml.Unmarshal(body, &root) != nil || root.XMLName.Local != "caps" {
		return unavailablef("%s: %s doesn't look like a Torznab feed", z.Name, z)
	}
	return nil
}

type torrentResult struct {
	Title, Link, Magnet, Details string
	InfoHash                     string
	Seeders                      int // -1 when the indexer doesn't say
	Size                         int64
	Categories                   []int
}

var reBTIH = regexp.MustCompile(`(?i)xt=urn:btih:([0-9a-z]+)`)

// id tells torrents apart across searches and indexers. Download links don't: Jackett's
// contain a token that changes with every search.
func (r torrentResult) id() string {
	switch {
	case r.InfoHash != "":
		return "btih:" + strings.ToLower(r.InfoHash)
	case r.Details != "":
		return r.Details
	}
	return FirstNonEmpty(r.Link, r.Magnet) + "|" + r.Title
}

func (z *Torznab) search(ctx context.Context, query string) ([]torrentResult, error) {
	body, err := z.get(ctx, url.Values{"t": {"search"}, "q": {query}, "cat": {"3000"}, "limit": {"100"}})
	if err != nil {
		return nil, err
	}
	return parseTorznab(body)
}

func parseTorznab(body []byte) ([]torrentResult, error) {
	if err := torznabError(body); err != nil {
		return nil, err
	}
	var rss struct {
		Items []struct {
			Title      string   `xml:"title"`
			GUID       string   `xml:"guid"`
			Link       string   `xml:"link"`
			Comments   string   `xml:"comments"`
			Size       int64    `xml:"size"`
			Categories []string `xml:"category"`
			Enclosure  struct {
				URL string `xml:"url,attr"`
			} `xml:"enclosure"`
			Attrs []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"attr"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &rss); err != nil {
		return nil, failf("unreadable Torznab answer")
	}
	var out []torrentResult
	for _, it := range rss.Items {
		r := torrentResult{Title: it.Title, Link: FirstNonEmpty(it.Enclosure.URL, it.Link), Size: it.Size, Seeders: -1}
		for _, d := range []string{it.Comments, it.GUID} {
			if strings.HasPrefix(d, "http") && r.Details == "" {
				r.Details = d
			}
		}
		addCategory := func(v string) {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				r.Categories = append(r.Categories, n)
			}
		}
		for _, c := range it.Categories {
			addCategory(c)
		}
		for _, a := range it.Attrs {
			switch a.Name {
			case "seeders":
				r.Seeders, _ = strconv.Atoi(a.Value)
			case "magneturl":
				r.Magnet = a.Value
			case "infohash":
				r.InfoHash = a.Value
			case "category":
				addCategory(a.Value)
			case "size":
				if r.Size == 0 {
					r.Size, _ = strconv.ParseInt(a.Value, 10, 64)
				}
			}
		}
		if strings.HasPrefix(r.Link, "magnet:") {
			r.Magnet, r.Link = r.Link, ""
		}
		if m := reBTIH.FindStringSubmatch(r.Magnet); m != nil && r.InfoHash == "" {
			r.InfoHash = m[1]
		}
		if notMusic(r.Categories) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// notMusic is true for results filed only as audiobooks or outside Audio altogether
// (indexer-specific categories, 100000 and up, are given the benefit of the doubt).
func notMusic(cats []int) bool {
	if len(cats) == 0 {
		return false
	}
	for _, c := range cats {
		if (c >= 3000 && c < 4000 && c != 3030) || c >= 100000 {
			return false
		}
	}
	return true
}

// jackettIndexers lists the indexers configured in Jackett that carry music, each as its
// own feed: unlike Jackett's all-indexers feed, a single indexer's feed reports its errors
// instead of quietly returning nothing.
func jackettIndexers(ctx context.Context, all *Torznab) ([]*Torznab, error) {
	body, err := all.get(ctx, url.Values{"t": {"indexers"}, "configured": {"true"}})
	if err != nil {
		return nil, err
	}
	type category struct {
		ID      int `xml:"id,attr"`
		Subcats []struct {
			ID int `xml:"id,attr"`
		} `xml:"subcat"`
	}
	var doc struct {
		XMLName  xml.Name
		Indexers []struct {
			ID         string     `xml:"id,attr"`
			Title      string     `xml:"title"`
			Categories []category `xml:"caps>categories>category"`
		} `xml:"indexer"`
	}
	if xml.Unmarshal(body, &doc) != nil || doc.XMLName.Local != "indexers" {
		return nil, errors.New("not an indexer list")
	}
	hasAudio := func(cats []category) bool {
		for _, c := range cats {
			if c.ID >= 3000 && c.ID < 4000 {
				return true
			}
			for _, sc := range c.Subcats {
				if sc.ID >= 3000 && sc.ID < 4000 {
					return true
				}
			}
		}
		return len(cats) == 0 // unknown: try it
	}
	base := strings.TrimSuffix(all.feed, "/all/results/torznab/api")
	var feeds []*Torznab
	for _, ix := range doc.Indexers {
		if ix.ID == "" {
			continue
		}
		if !hasAudio(ix.Categories) {
			debugf("Jackett: leaving out %s, which has no music categories", ix.Title)
			continue
		}
		feeds = append(feeds, newTorznabFeed("Jackett: "+FirstNonEmpty(ix.Title, ix.ID),
			base+"/"+url.PathEscape(ix.ID)+"/results/torznab/api", all.key, all.http.timeout))
	}
	return feeds, nil
}

// resolveFeeds turns the configured URLs into Torznab feeds. A bare Jackett address becomes
// its all-indexers feed; a bare Prowlarr address becomes one feed per enabled torrent indexer.
func resolveFeeds(ctx context.Context, cfg TorrentConfig, timeout time.Duration) ([]*Torznab, error) {
	var feeds []*Torznab
	h := newHTTP("Jackett/Prowlarr", 0)
	h.retries = 2
	for _, raw := range cfg.Feeds {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%q is not a URL (expected e.g. http://127.0.0.1:9117)", raw)
		}
		if strings.Trim(u.Path, "/") != "" {
			feeds = append(feeds, newTorznabFeed(u.Host+u.Path, raw, cfg.Key, timeout))
			continue
		}
		base := strings.TrimRight(u.String(), "/")
		// Prowlarr lists its indexers at /api/v1/indexer.
		status, body, err := h.do(ctx, request{url: base + "/api/v1/indexer", headers: map[string]string{"X-Api-Key": cfg.Key}})
		if err != nil {
			return nil, fmt.Errorf("can't reach %s: %w", base, err)
		}
		if status == 401 {
			return nil, fmt.Errorf("%s (Prowlarr) rejected the API key", base)
		}
		var indexers []struct {
			ID             int    `json:"id"`
			Name           string `json:"name"`
			Enable         bool   `json:"enable"`
			Protocol       string `json:"protocol"`
			SupportsSearch *bool  `json:"supportsSearch"`
		}
		if status == 200 && json.Unmarshal(body, &indexers) == nil {
			found := 0
			for _, ix := range indexers {
				if ix.Enable && ix.Protocol == "torrent" && (ix.SupportsSearch == nil || *ix.SupportsSearch) {
					feeds = append(feeds, newTorznabFeed("Prowlarr: "+ix.Name, fmt.Sprintf("%s/%d/api", base, ix.ID), cfg.Key, timeout))
					found++
				}
			}
			if found == 0 {
				return nil, fmt.Errorf("Prowlarr at %s has no enabled torrent indexers; add some under Indexers → Add Indexer", base)
			}
			continue
		}
		// Otherwise assume Jackett: one feed per configured indexer, or if it can't list them,
		// its feed that searches all indexers at once.
		all := newTorznabFeed("Jackett (all indexers)", base+"/api/v2.0/indexers/all/results/torznab/api", cfg.Key, timeout)
		if list, err := jackettIndexers(ctx, all); err == nil && len(list) > 0 {
			feeds = append(feeds, list...)
		} else {
			debugf("Jackett at %s didn't list its indexers (%v); using its all-indexers feed", base, err)
			feeds = append(feeds, all)
		}
	}
	return feeds, nil
}

// --- .torrent files ----------------------------------------------------------------------------

// bdecode parses bencoded data into int64, string, []any and map[string]any values.
func bdecode(data []byte) (any, error) {
	v, rest, err := bdecodeValue(data)
	if err == nil && len(rest) > 0 {
		err = errors.New("trailing data")
	}
	return v, err
}

func bdecodeValue(b []byte) (any, []byte, error) {
	if len(b) == 0 {
		return nil, nil, errors.New("unexpected end")
	}
	switch c := b[0]; {
	case c == 'i':
		end := bytes.IndexByte(b, 'e')
		if end < 0 {
			return nil, nil, errors.New("bad integer")
		}
		n, err := strconv.ParseInt(string(b[1:end]), 10, 64)
		return n, b[end+1:], err
	case c == 'l':
		var list []any
		b = b[1:]
		for len(b) > 0 && b[0] != 'e' {
			v, rest, err := bdecodeValue(b)
			if err != nil {
				return nil, nil, err
			}
			list, b = append(list, v), rest
		}
		if len(b) == 0 {
			return nil, nil, errors.New("unterminated list")
		}
		return list, b[1:], nil
	case c == 'd':
		dict := map[string]any{}
		b = b[1:]
		for len(b) > 0 && b[0] != 'e' {
			k, rest, err := bdecodeValue(b)
			if err != nil {
				return nil, nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, nil, errors.New("non-string key")
			}
			v, rest, err := bdecodeValue(rest)
			if err != nil {
				return nil, nil, err
			}
			dict[key], b = v, rest
		}
		if len(b) == 0 {
			return nil, nil, errors.New("unterminated dictionary")
		}
		return dict, b[1:], nil
	case c >= '0' && c <= '9':
		colon := bytes.IndexByte(b, ':')
		if colon < 0 {
			return nil, nil, errors.New("bad string")
		}
		n, err := strconv.Atoi(string(b[:colon]))
		if err != nil || n < 0 || colon+1+n > len(b) {
			return nil, nil, errors.New("bad string length")
		}
		return string(b[colon+1 : colon+1+n]), b[colon+1+n:], nil
	}
	return nil, nil, fmt.Errorf("unexpected byte %q", b[0])
}

// torrentFiles lists the files of a .torrent as slash paths relative to aria2c's --dir, in
// the order aria2c's --select-file numbers them (from 1).
func torrentFiles(data []byte) ([]string, error) {
	names, _, err := torrentFileList(data)
	return names, err
}

// torrentFileList is torrentFiles with each file's size.
func torrentFileList(data []byte) ([]string, []int64, error) {
	v, err := bdecode(data)
	if err != nil {
		return nil, nil, fmt.Errorf("not a torrent file: %w", err)
	}
	info, _ := dig(v, "info").(map[string]any)
	if info == nil {
		return nil, nil, errors.New("torrent has no info section")
	}
	name := str(firstOf(info["name.utf-8"], info["name"]))
	files, multi := info["files"].([]any)
	if !multi {
		n, _ := info["length"].(int64)
		return []string{name}, []int64{n}, nil
	}
	var out []string
	var sizes []int64
	for _, f := range files {
		var parts []string
		for _, p := range asSlice(firstOf(dig(f, "path.utf-8"), dig(f, "path"))) {
			parts = append(parts, str(p))
		}
		out = append(out, path.Join(append([]string{name}, parts...)...))
		n, _ := dig(f, "length").(int64)
		sizes = append(sizes, n)
	}
	return out, sizes, nil
}

// fileQuality estimates a torrent file's quality from its name and size.
func fileQuality(name string, size int64, seconds float64) Quality {
	kbps := 0
	if seconds > 0 && size > 0 {
		kbps = int(float64(size) * 8 / seconds / 1000)
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".flac", ".ape", ".wv", ".wav", ".aiff", ".aif":
		return Quality{Codec: "flac", Lossless: true}
	case ".mp3":
		return Quality{Codec: "mp3", Kbps: kbps}
	case ".ogg":
		return Quality{Codec: "vorbis", Kbps: kbps}
	case ".opus":
		return Quality{Codec: "opus", Kbps: kbps}
	case ".m4a":
		if kbps > 500 {
			return Quality{Codec: "alac", Lossless: true}
		}
		return Quality{Codec: "aac", Kbps: kbps}
	}
	return Quality{}
}

func firstOf(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// A leading track number ("01 - ", "1-01. ", "07_"), or a vinyl side position ("A1. ", "B2 - ").
var reLeadingNumber = regexp.MustCompile(`^\s*(?:(?:\d{1,2}[-.])?(\d{1,3})|[A-Ha-h]\d{1,2})[\s.\-_)]+`)

// bestFile picks the audio file in a torrent that best fits the track, by file name, and
// returns it with the title to give its candidate. A matching track number counts, and so
// does a folder named after the track's album (for discographies, where the same song can
// be on several albums).
func bestFile(t *Track, files []string) (int, string) {
	best, bestScore, bestStem := -1, 0.0, ""
	album := normalize(baseAlbum(t.Album))
	// Shortened file names are only recognised for plain titles: in "Leif Erikson (Third
	// Demo)", the words after "Leif Erikson" say which version it is.
	title := cleanTitle(baseTitle(t.Title), false)
	plain := title == cleanTitle(searchTitle(t.Title), false)
	for i, f := range files {
		if !audioExts[strings.ToLower(path.Ext(f))] {
			continue
		}
		full := strings.TrimSuffix(path.Base(f), path.Ext(f))
		stem, num := full, 0
		if m := reLeadingNumber.FindStringSubmatch(full); m != nil && len(m[0]) < len(full) {
			num, _ = strconv.Atoi(m[1])
			stem = full[len(m[0]):]
		}
		c := Candidate{Title: stem}
		if versionConflict(t, &c) != "" {
			continue
		}
		s := titleScore(t, &c)
		if stem != full { // "22 Acacia Avenue" is a title, not track 22
			s = max(s, titleScore(t, &Candidate{Title: full}))
		}
		name := stem
		// Shortened names, like "05 - Say Hello.flac" for "Say Hello to the Angels": two or
		// more words that the title starts with. (The length check after downloading still
		// catches a wrong guess.)
		if cs := cleanTitle(stem, true); s < minTitle && plain && len(strings.Fields(cs)) >= 2 && strings.HasPrefix(title+" ", cs+" ") {
			s, name = minTitle, t.Title
		}
		if t.TrackNumber > 0 && num == t.TrackNumber {
			s += 5
		}
		if album != "" && containsPhrase(normalize(path.Dir(f)), album) {
			s += 3
		}
		if s > bestScore {
			best, bestScore, bestStem = i, s, name
		}
	}
	if bestScore < minTitle {
		return -1, ""
	}
	return best, bestStem
}

// --- searching ----------------------------------------------------------------------------------

// baseAlbum drops edition details that torrents rarely name the same way:
// "Turn On The Bright Lights: The Tenth Anniversary Edition (Remastered)" -> "Turn On The Bright Lights".
func baseAlbum(album string) string {
	s := reBrackets.ReplaceAllString(album, " ")
	if i := strings.Index(s, ": "); i > 0 {
		s = s[:i]
	}
	s = strings.Join(strings.Fields(reSpacedDash.Split(s, 2)[0]), " ")
	if s == "" {
		return strings.TrimSpace(album)
	}
	return s
}

// Indexers' search engines want plain words; brackets, colons, slashes and the like make
// many of them find nothing.
var queryPunct = strings.NewReplacer(":", " ", ";", " ", ",", " ", "(", " ", ")", " ", "[", " ", "]", " ",
	"{", " ", "}", " ", "/", " ", `\`, " ", "|", " ", `"`, " ", "“", " ", "”", " ", "«", " ", "»", " ",
	"!", " ", "?", " ", "*", " ", "#", " ", "+", " ", " - ", " ", " – ", " ", " — ", " ", "’", "'", "‘", "'")

func queryText(s string) string {
	return strings.Join(strings.Fields(queryPunct.Replace(" "+s+" ")), " ")
}

type torrentQuery struct {
	text    string
	artist  string   // who the torrent should be by ("": the track's first artist)
	targets []string // names the torrent should resemble besides the artist (album, title)
	edition string   // the full album name; its extra words ("Anniversary", "Deluxe") are a plus
	inspect int      // how many of the best results to look into
}

// torrentQueries lists the searches to try for a track, from most to least specific: its
// album by its album artist (what most music torrents are; just the album's name for a
// compilation), the song (singles), then the album artist and the artist alone
// (discographies, and albums the track list names differently from the indexer).
func torrentQueries(t *Track) []torrentQuery {
	var out []torrentQuery
	seen := map[string]bool{}
	add := func(q torrentQuery) {
		q.text = queryText(q.text)
		if key := strings.ToLower(q.text); q.text != "" && !seen[key] {
			seen[key] = true
			out = append(out, q)
		}
	}
	title := searchTitle(t.Title)
	album := baseAlbum(t.Album)
	hasAlbum := t.Album != "" && !strings.EqualFold(searchTitle(t.Album), title)
	albumArtist := t.AlbumArtistStr()
	compilation := variousArtists(albumArtist)
	if hasAlbum {
		if compilation {
			add(torrentQuery{text: album, artist: albumArtist, targets: []string{album}, edition: t.Album, inspect: 5})
		} else {
			add(torrentQuery{text: albumArtist + " " + album, artist: albumArtist, targets: []string{album}, edition: t.Album, inspect: 5})
		}
	}
	add(torrentQuery{text: t.Artists[0] + " " + title, targets: []string{title, album}, inspect: 5})
	// All of the artist's torrents: look further down the list, since many of them fit the
	// name equally well (they're mostly shared with the artist's other tracks anyway).
	if !hasAlbum {
		if !variousArtists(t.Artists[0]) {
			add(torrentQuery{text: t.Artists[0], targets: []string{title}, inspect: 10})
		}
		return out
	}
	// Release names don't mention album tracks, so only the album counts.
	if !compilation && !strings.EqualFold(albumArtist, t.Artists[0]) {
		add(torrentQuery{text: albumArtist, artist: albumArtist, targets: []string{album}, edition: t.Album, inspect: 10})
	}
	if !variousArtists(t.Artists[0]) {
		add(torrentQuery{text: t.Artists[0], targets: []string{album}, edition: t.Album, inspect: 10})
	}
	return out
}

func (q torrentQuery) by(t *Track) string { return FirstNonEmpty(q.artist, t.Artists[0]) }

var (
	reLosslessWords = wordRe(`flac|lossless|alac|ape|wavpack|wv|wav`)
	reImageWords    = wordRe(`image|img`)                                  // one file per CD plus a .cue sheet
	reHiResWords    = wordRe(`24 (?:96|88|176|192)|24 ?bit|hi ?res|vinyl`) // bigger and often a different master
	reCollection    = wordRe(`discography|дискография|collection|anthology|complete`)
	reVideo         = wordRe(`dvd|dvd5|dvd9|dvdrip|hdtv|720p|1080p|2160p|bdrip|blu ?ray|web ?dl|webrip|x264|x265|hevc|mkv`)
	reLeadingGroups = regexp.MustCompile(`^(?:\s*[(\[][^)\]]*[)\]])+`)
)

// artistPart is the part of a torrent's name that names who made it: what comes before the
// first " - ", once leading "(genre)" and "[format]" groups are left out. In "Nine Inch
// Nails - Every Day Is Exactly The Same (Interpol Remixes)" that's Nine Inch Nails.
func artistPart(title string) string {
	s := strings.TrimSpace(reLeadingGroups.ReplaceAllString(title, ""))
	if i := strings.Index(s, " - "); i > 0 {
		return s[:i]
	}
	return s
}

// rankTorrents orders results by how well their name fits the artist and query targets,
// preferring the wanted format, track-per-file releases and well-seeded torrents. Dead
// torrents and ones that fit neither the artist nor the target are dropped.
func rankTorrents(results []torrentResult, artist string, q torrentQuery, lossless bool) []torrentResult {
	type scored struct {
		r     torrentResult
		score float64
	}
	var list []scored
	na := normalize(artist)
	for _, r := range results {
		if r.Seeders == 0 {
			continue
		}
		title := normalize(r.Title)
		as := partialRatio(na, normalize(artistPart(r.Title)))
		ts := 0.0
		for _, target := range q.targets {
			if nt := normalize(target); nt != "" {
				ts = max(ts, partialRatio(nt, title))
			}
		}
		if as < 70 && ts < 90 {
			continue
		}
		score := as + ts
		if ts >= 90 { // the right album: prefer the edition the track list names
			score += editionBonus(q.edition, q.targets, title)
		}
		if lossless == reLosslessWords.MatchString(title) {
			score += 10
		}
		if reImageWords.MatchString(title) {
			score -= 25
		}
		if lossless && reHiResWords.MatchString(title) {
			score -= 6
		}
		if reCollection.MatchString(title) {
			score += 12
		}
		if reVideo.MatchString(title) {
			score -= 40 // concert films and music videos
		}
		if r.Link != "" {
			score += 5 // a .torrent file is quicker to look into than a magnet link
		}
		score += math.Min(float64(max(r.Seeders, 0)), 50) / 5
		list = append(list, scored{r, score})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	out := make([]torrentResult, len(list))
	for i, s := range list {
		out[i] = s.r
	}
	return out
}

// editionBonus rewards torrents naming the edition the track list has: the words of the full
// album name that the searched-for base name lacks ("tenth anniversary edition").
func editionBonus(edition string, targets []string, title string) float64 {
	if edition == "" {
		return 0
	}
	known := map[string]bool{}
	for _, t := range targets {
		for _, w := range strings.Fields(normalize(t)) {
			known[w] = true
		}
	}
	extra, hit := 0, 0
	for _, w := range strings.Fields(normalize(edition)) {
		if known[w] || len(w) < 4 {
			continue
		}
		extra++
		if containsPhrase(title, w) {
			hit++
		}
	}
	if extra == 0 {
		return 0
	}
	return 15 * float64(hit) / float64(extra)
}

// --- the source -------------------------------------------------------------------------------

// cache runs f once per key (concurrent callers wait for the first) and remembers the
// result. Failures are tried again once they're older than retryAfter.
type cache[T any] struct {
	mu         sync.Mutex
	entries    map[string]*cacheEntry[T]
	retryAfter time.Duration
}

type cacheEntry[T any] struct {
	done chan struct{}
	val  T
	err  error
	at   time.Time
}

func (c *cache[T]) get(ctx context.Context, key string, f func() (T, error)) (T, error) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*cacheEntry[T]{}
	}
	e := c.entries[key]
	if e != nil {
		select {
		case <-e.done:
			cancelled := errors.Is(e.err, context.Canceled) || errors.Is(e.err, context.DeadlineExceeded)
			if e.err != nil && (cancelled || time.Since(e.at) > c.retryAfter) {
				e = nil
			}
		default:
		}
	}
	if e == nil {
		e = &cacheEntry[T]{done: make(chan struct{}), err: errors.New("internal error")}
		c.entries[key] = e
		c.mu.Unlock()
		defer func() { e.at = time.Now(); close(e.done) }()
		e.val, e.err = f()
		return e.val, e.err
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e.val, e.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

type torrentMeta struct {
	file  string // the .torrent on disk
	files []string
	sizes []int64
}

type TorrentSource struct {
	feeds    []*Torznab
	aria2c   string
	dir      string // cached .torrent files (kept between runs)
	lossless bool
	client   *http.Client
	dl       torrentFetcher
	grace    time.Duration // once one indexer has answered, wait this long for the others
	timeout  time.Duration // per indexer

	searches cache[[]torrentResult]
	metas    cache[*torrentMeta]
	cacheDir string // search results (kept for a while between runs)

	hintMu sync.Mutex
	hints  map[string]torrentHint // by track key: found in another track's torrent
}

// newTorrentSource makes the torrent source; extra[0] is the per-indexer search timeout.
func newTorrentSource(feeds []*Torznab, aria2c, cacheDir, format string, timeout time.Duration, extra ...time.Duration) *TorrentSource {
	dir := filepath.Join(cacheDir, "torrents")
	indexerTimeout := 20 * time.Second
	if len(extra) > 0 && extra[0] > 0 {
		indexerTimeout = extra[0]
	}
	return &TorrentSource{
		feeds: feeds, aria2c: aria2c, dir: dir, grace: 6 * time.Second, timeout: indexerTimeout,
		cacheDir: filepath.Join(cacheDir, "torrent-searches"),
		lossless: format == "flac" || format == "original",
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme == "magnet" {
				return http.ErrUseLastResponse // some indexers redirect to a magnet link
			}
			return nil
		}},
		dl:       newTorrentManager(newAria2(aria2c), filepath.Join(dir, "downloads"), timeout),
		searches: cache[[]torrentResult]{retryAfter: time.Minute},
		metas:    cache[*torrentMeta]{retryAfter: 10 * time.Minute},
	}
}

func (s *TorrentSource) Name() string        { return "torrent" }
func (s *TorrentSource) Label() string       { return "Torrent" }
func (s *TorrentSource) MaxQuality() Quality { return Quality{Codec: "flac", Lossless: true} }

// Close stops aria2c.
func (s *TorrentSource) Close() { s.dl.close() }

// pause pauses (or resumes) the torrent downloads, keeping what they have.
func (s *TorrentSource) pause(p bool) {
	if d, ok := s.dl.(interface{ setPaused(bool) }); ok {
		d.setPaused(p)
	}
}

func (s *TorrentSource) FeedStatus() []FeedStatus {
	var out []FeedStatus
	for _, f := range s.feeds {
		out = append(out, f.status())
	}
	return out
}

// speed is aria2c's overall download speed (bytes/s).
func (s *TorrentSource) speed() float64 {
	if g, ok := s.dl.(interface{ globalSpeed() float64 }); ok {
		return g.globalSpeed()
	}
	return 0
}

// setLimit sets aria2c's overall download limit (bytes/s, 0: none).
func (s *TorrentSource) setLimit(bps float64) bool {
	if g, ok := s.dl.(interface{ setGlobalLimit(float64) bool }); ok {
		return g.setGlobalLimit(bps)
	}
	return false
}

type cachedSearch struct {
	At      time.Time       `json:"at"`
	Results []torrentResult `json:"results"`
}

func (s *TorrentSource) searchCachePath(query string) string {
	sum := sha1.Sum([]byte(strings.ToLower(query)))
	return filepath.Join(s.cacheDir, hex.EncodeToString(sum[:10])+".json")
}

// searchAll queries every working indexer at once and merges the results. It doesn't wait for
// slow indexers: once one has answered, the others get a few more seconds. Results are kept
// on disk for half a day, so restarting doesn't search everything again.
func (s *TorrentSource) searchAll(ctx context.Context, query string) ([]torrentResult, error) {
	return s.searches.get(ctx, strings.ToLower(query), func() ([]torrentResult, error) {
		if data, err := os.ReadFile(s.searchCachePath(query)); err == nil {
			var c cachedSearch
			if json.Unmarshal(data, &c) == nil {
				ttl := 12 * time.Hour
				if len(c.Results) == 0 {
					ttl = 3 * time.Hour
				}
				if time.Since(c.At) < ttl {
					return c.Results, nil
				}
			}
		}
		var live []*Torznab
		for _, f := range s.feeds {
			if f.available() {
				live = append(live, f)
			}
		}
		if len(live) == 0 {
			return nil, unavailablef("every torrent indexer is paused after repeated errors (see Sources → Torrent)")
		}
		// Not tied to the track that asked first: other tracks wait for the same answer.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.timeout+5*time.Second)
		defer cancel()
		type answer struct {
			i       int
			results []torrentResult
			err     error
		}
		answers := make(chan answer, len(live))
		for i, f := range live {
			go func() {
				select {
				case f.sem <- struct{}{}:
				case <-sctx.Done():
					answers <- answer{i, nil, sctx.Err()}
					return
				}
				start := time.Now()
				res, err := f.search(sctx, query)
				<-f.sem
				f.record(err, time.Since(start))
				answers <- answer{i, res, err}
			}()
		}
		var out []torrentResult
		var firstErr error
		got, waiting := 0, len(live)
		seen := map[string]bool{}
		var grace <-chan time.Time
	collect:
		for waiting > 0 {
			select {
			case a := <-answers:
				waiting--
				if a.err != nil {
					debugf("%s: search %q failed: %v", live[a.i].Name, query, a.err)
					if firstErr == nil {
						firstErr = a.err
					}
					continue
				}
				got++
				debugf("%s: %d results for %q", live[a.i].Name, len(a.results), query)
				for _, r := range a.results {
					if !seen[r.id()] {
						seen[r.id()] = true
						out = append(out, r)
					}
				}
				if grace == nil {
					grace = time.After(s.grace)
				}
			case <-grace:
				debugf("torrent search %q: going on without %d slow indexers", query, waiting)
				break collect
			case <-sctx.Done():
				break collect
			}
		}
		if got == 0 {
			if firstErr == nil || errors.Is(firstErr, context.DeadlineExceeded) {
				firstErr = unavailablef("no torrent indexer answered within %s", s.timeout)
			}
			return nil, firstErr
		}
		if data, err := json.Marshal(cachedSearch{At: time.Now(), Results: out}); err == nil {
			if os.MkdirAll(s.cacheDir, 0o755) == nil {
				writeFileAtomic(s.searchCachePath(query), data)
			}
		}
		return out, nil
	})
}

func (s *TorrentSource) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	if c, ok := s.takeHint(t.Key); ok {
		debugf("%s: in “%s”, which another track's download found", t, c.Album)
		return []Candidate{c}, nil
	}
	seen, inspected := map[string]bool{}, map[string]bool{}
	looked := 0
	var searchErr error
	for _, q := range torrentQueries(t) {
		results, err := s.searchAll(ctx, q.text)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			searchErr = err
			continue
		}
		for _, r := range results {
			seen[r.id()] = true
		}
		var picks []torrentResult
		for _, r := range rankTorrents(results, q.by(t), q, s.lossless) {
			if len(picks) < q.inspect && !inspected[r.id()] {
				inspected[r.id()] = true
				picks = append(picks, r)
			}
		}
		cands, n := s.inspect(ctx, t, picks)
		looked += n
		if len(cands) > 0 {
			return cands, nil
		}
	}
	switch {
	case searchErr != nil: // the track may well be there: try again next run
		return nil, searchErr
	case len(seen) == 0:
		return nil, nil
	case looked == 0:
		return nil, failf("%d torrents found, but none that fits, has seeders and a readable torrent file", len(seen))
	}
	return nil, failf("%d torrents found; the %d best-matching ones don't contain this track", len(seen), looked)
}

// inspect gets the file lists of picks (in parallel) and returns a candidate for each torrent
// with a file that fits the track, best-ranked torrent first, plus how many it could read.
// Results with a .torrent link answer quickly, magnet-only ones can take a minute: once the
// quick ones are in and one of them fits, it doesn't wait for the rest.
func (s *TorrentSource) inspect(ctx context.Context, t *Track, picks []torrentResult) ([]Candidate, int) {
	type answer struct {
		i    int
		meta *torrentMeta
		err  error
	}
	answers := make(chan answer, len(picks))
	quick := 0
	for i, r := range picks {
		if r.Link != "" {
			quick++
		}
		go func() {
			m, err := s.meta(ctx, r)
			answers <- answer{i, m, err}
		}()
	}
	found := make([]*Candidate, len(picks))
	looked, fits := 0, false
	for left := len(picks); left > 0 && !(fits && quick == 0); left-- {
		var a answer
		select {
		case a = <-answers:
		case <-ctx.Done():
			return nil, looked
		}
		r := picks[a.i]
		if r.Link != "" {
			quick--
		}
		if a.err != nil {
			debugf("torrent %q: %v", r.Title, a.err)
			continue
		}
		looked++
		i, stem := bestFile(t, a.meta.files)
		if i < 0 {
			debugf("torrent %q: no file fits %s among its %d files", r.Title, t, len(a.meta.files))
			continue
		}
		c := torrentCandidate(t, r.Title, r.Details, a.meta, i, stem)
		found[a.i] = &c
		fits = true
	}
	var out []Candidate
	for _, c := range found {
		if c != nil {
			out = append(out, *c)
		}
	}
	return out, looked
}

// torrentCandidate is the candidate for file i (named stem) of the torrent called title.
func torrentCandidate(t *Track, title, details string, meta *torrentMeta, i int, stem string) Candidate {
	var size int64
	if i < len(meta.sizes) {
		size = meta.sizes[i]
	}
	return Candidate{Source: "torrent", URL: details, Title: stem, Artist: title, Album: title,
		Ref: meta.file + "#" + strconv.Itoa(i), Quality: fileQuality(meta.files[i], size, t.Duration())}
}

// torrentRef splits a candidate's Ref into the .torrent file and the file's index in it.
func torrentRef(ref string) (string, int, bool) {
	file, idxText, ok := strings.Cut(ref, "#")
	idx, err := strconv.Atoi(idxText)
	return file, idx, ok && err == nil
}

// --- tracks fetched along ------------------------------------------------------------------------

// A torrent usually holds a whole album, often an artist's discography. When one track's
// torrent is about to be downloaded, the other tracks still to do that it also has are handed
// that torrent (a hint): their next search here returns it right away, and the download waits
// a little for them so that their files go into it too.

type torrentHint struct {
	cand Candidate
	at   time.Time
}

const (
	hintTTL = time.Hour
	// Matching every track of an artist against a big discography takes seconds, and the
	// first track's download waits for it: at most this many track-to-file comparisons per
	// torrent (tracks that aren't checked find it with their own search).
	alongBudget = 20000
)

// alongside returns, for each of tracks that c's torrent (found for track from) also has,
// the candidate for its file. A track gets one only if its own search would consider the
// torrent (see rankTorrents) and a file in it fits the track as well as a search's would.
// The tracks of from's album are looked at first.
func (s *TorrentSource) alongside(c Candidate, from *Track, tracks []*Track) map[string]Candidate {
	file, _, ok := torrentRef(c.Ref)
	if !ok {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	files, sizes, err := torrentFileList(data)
	if err != nil {
		return nil
	}
	meta := &torrentMeta{file: file, files: files, sizes: sizes}
	title := c.Album // a torrent candidate's album is the torrent's name
	audio := 0
	for _, f := range files {
		if audioExts[strings.ToLower(path.Ext(f))] {
			audio++
		}
	}
	album := baseAlbum(from.Album)
	sameAlbum := func(t *Track) bool { return t.Album != "" && strings.EqualFold(baseAlbum(t.Album), album) }
	tracks = append([]*Track(nil), tracks...)
	sort.SliceStable(tracks, func(i, j int) bool { return sameAlbum(tracks[i]) && !sameAlbum(tracks[j]) })
	out := map[string]Candidate{}
	budget := alongBudget
	for _, t := range tracks {
		if len(t.Artists) == 0 || !s.considers(t, title) {
			continue
		}
		if budget -= audio; budget < 0 {
			debugf("“%s” has %d audio files; not looking for more tracks in it", title, audio)
			break
		}
		i, stem := bestFile(t, files)
		if i < 0 {
			continue
		}
		cand := torrentCandidate(t, title, c.URL, meta, i, stem)
		// A track of another album may find a lossless copy of its own.
		if s.lossless && !cand.Quality.Lossless && !sameAlbum(t) {
			continue
		}
		if evaluate(t, cand).Reject != "" {
			continue
		}
		cand.Unsearched = true
		out[t.Key] = cand
	}
	return out
}

// considers is whether a search for t would look into a torrent with this name.
func (s *TorrentSource) considers(t *Track, title string) bool {
	r := []torrentResult{{Title: title, Seeders: -1}}
	for _, q := range torrentQueries(t) {
		if len(rankTorrents(r, q.by(t), q, s.lossless)) > 0 {
			return true
		}
	}
	return false
}

// hint hands track key the candidate c found along with another track (see alongside).
func (s *TorrentSource) hint(key string, c Candidate) {
	now := time.Now()
	s.hintMu.Lock()
	if s.hints == nil {
		s.hints = map[string]torrentHint{}
	}
	for k, h := range s.hints {
		if now.Sub(h.at) > hintTTL {
			delete(s.hints, k)
		}
	}
	s.hints[key] = torrentHint{c, now}
	s.hintMu.Unlock()
	s.expect(c, true)
}

func (s *TorrentSource) hinted(key string) bool {
	s.hintMu.Lock()
	defer s.hintMu.Unlock()
	_, ok := s.hints[key]
	return ok
}

// takeHint returns (once) the candidate handed to track key, if it has one.
func (s *TorrentSource) takeHint(key string) (Candidate, bool) {
	s.hintMu.Lock()
	h, ok := s.hints[key]
	delete(s.hints, key)
	s.hintMu.Unlock()
	if ok && time.Since(h.at) > hintTTL {
		s.expect(h.cand, false)
		return Candidate{}, false
	}
	return h.cand, ok
}

// dropHint forgets a hint its track didn't use (it was found elsewhere, or cancelled).
func (s *TorrentSource) dropHint(key string) {
	s.hintMu.Lock()
	h, ok := s.hints[key]
	delete(s.hints, key)
	s.hintMu.Unlock()
	if ok {
		s.expect(h.cand, false)
	}
}

// expect tells the downloader that a track is (or no longer is) on its way for c's file.
func (s *TorrentSource) expect(c Candidate, coming bool) {
	e, ok := s.dl.(interface {
		expect(torrentFile string, index int)
		unexpect(torrentFile string, index int)
	})
	file, idx, ok2 := torrentRef(c.Ref)
	switch {
	case !ok || !ok2:
	case coming:
		e.expect(file, idx)
	default:
		e.unexpect(file, idx)
	}
}

// deadFor is how long a torrent whose file list couldn't be had (no peers for its magnet
// link, a broken link) is left alone, across restarts: trying again every few minutes, for
// each track it might hold, was most of what torrent searching did.
const deadFor = 12 * time.Hour

// meta gets a result's .torrent (from its link, or a magnet's metadata via aria2c) and file list.
func (s *TorrentSource) meta(ctx context.Context, r torrentResult) (*torrentMeta, error) {
	return s.metas.get(ctx, r.id(), func() (*torrentMeta, error) {
		sum := sha1.Sum([]byte(r.id()))
		p := filepath.Join(s.dir, hex.EncodeToString(sum[:10])+".torrent")
		if data, err := os.ReadFile(p); err == nil { // fetched on an earlier run
			if files, sizes, err := torrentFileList(data); err == nil {
				return &torrentMeta{file: p, files: files, sizes: sizes}, nil
			}
		}
		dead := strings.TrimSuffix(p, ".torrent") + ".dead"
		if st, err := os.Stat(dead); err == nil && time.Since(st.ModTime()) < deadFor {
			why, _ := os.ReadFile(dead)
			return nil, failf("%s (tried %s ago)", why, FmtWait(time.Since(st.ModTime()).Round(time.Minute)))
		}
		markDead := func(err error) {
			if os.MkdirAll(s.dir, 0o755) == nil {
				writeFileAtomic(dead, []byte(err.Error()))
			}
		}
		data, err := s.torrentBytes(context.WithoutCancel(ctx), r)
		if err != nil {
			markDead(err)
			return nil, err
		}
		files, sizes, err := torrentFileList(data)
		if err != nil {
			markDead(err)
			return nil, err
		}
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return nil, err
		}
		if err := writeFileAtomic(p, data); err != nil {
			return nil, err
		}
		return &torrentMeta{file: p, files: files, sizes: sizes}, nil
	})
}

func (s *TorrentSource) torrentBytes(ctx context.Context, r torrentResult) ([]byte, error) {
	magnet := r.Magnet
	if r.Link != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", r.Link, nil)
		req.Header.Set("User-Agent", userAgent)
		resp, err := s.client.Do(req)
		if err == nil {
			var buf bytes.Buffer
			buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
				magnet = loc
			} else if resp.StatusCode == 200 && bytes.HasPrefix(buf.Bytes(), []byte("d")) {
				return buf.Bytes(), nil
			} else {
				debugf("torrent %q: its link answered HTTP %d with %d bytes", r.Title, resp.StatusCode, buf.Len())
			}
		} else {
			debugf("torrent %q: fetching its link: %v", r.Title, err)
		}
	}
	if magnet == "" {
		return nil, failf("couldn't get the torrent file for “%s”", r.Title)
	}
	if s.aria2c == "" {
		return nil, failf("“%s” only has a magnet link, which needs aria2c", r.Title)
	}
	// Only a magnet link: ask the swarm for the torrent's metadata. Several of these run at
	// once: each gets its own ports and DHT table (a copy of aria2c's), so they don't fight
	// over them.
	tmp, err := os.MkdirTemp("", "spotify-downloader-meta-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dht := filepath.Join(tmp, "dht.dat")
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(filepath.Join(home, ".cache", "aria2", "dht.dat")); err == nil {
			os.WriteFile(dht, data, 0o600)
		}
	}
	port := 20000 + mrand.IntN(40000)
	mctx, cancel := context.WithTimeout(ctx, 50*time.Second)
	defer cancel()
	out, err := command(mctx, s.aria2c, "--dir="+tmp, "--bt-metadata-only=true", "--bt-save-metadata=true",
		"--bt-stop-timeout=45", "--dht-entry-point=dht.transmissionbt.com:6881", "--bt-tracker-connect-timeout=15",
		"--bt-tracker-timeout=30", "--listen-port="+strconv.Itoa(port), "--dht-listen-port="+strconv.Itoa(port+1),
		"--dht-file-path="+dht, "--summary-interval=0", "--show-console-readout=false", "--console-log-level=error",
		"--download-result=hide", magnet).CombinedOutput()
	matches, _ := filepath.Glob(filepath.Join(tmp, "*.torrent"))
	if len(matches) == 0 {
		debugf("aria2c metadata fetch for %q failed: %v %s", r.Title, err, lastLines(string(out), 2))
		return nil, failf("no peers answered for the magnet link of “%s”", r.Title)
	}
	return os.ReadFile(matches[0])
}

func (s *TorrentSource) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	file, idx, ok := torrentRef(c.Ref)
	if !ok {
		return "", failf("bad torrent reference")
	}
	task := taskFrom(ctx)
	task.park() // waiting for the swarm needs no worker slot
	got, err := s.dl.fetch(ctx, file, idx, filepath.Join(workdir, "torrent"), task)
	if rerr := task.resume(ctx); err == nil {
		err = rerr
	}
	return got, err
}

// --- setup check (--check-torrents) ------------------------------------------------------------

// checkTorrents walks through the torrent setup step by step and explains what to fix.
func CheckTorrents(ctx context.Context, cfg TorrentConfig, aria2c, query string) bool {
	ok := func(format string, args ...any) {
		fmt.Printf("  %s %s\n", Paint("32", "✓"), fmt.Sprintf(format, args...))
	}
	bad := func(format string, args ...any) {
		fmt.Printf("  %s %s\n", Paint("31", "✗"), fmt.Sprintf(format, args...))
	}
	hint := func(format string, args ...any) { fmt.Printf("      %s\n", fmt.Sprintf(format, args...)) }
	fmt.Println("Checking the torrent setup (TORRENTS.md explains each step):")

	good := true
	if aria2c == "" {
		bad("aria2c not found")
		hint("install the aria2 package: sudo pacman -S aria2 (Arch) or sudo apt install aria2 (Debian/Ubuntu)")
		good = false
	} else {
		v, _ := exec.CommandContext(ctx, aria2c, "--version").Output()
		ok("%s (%s)", strings.SplitN(string(v), "\n", 2)[0], aria2c)
	}

	if !cfg.configured() {
		bad("no Jackett or Prowlarr configured")
		hint("pass --torznab http://127.0.0.1:9117 (Jackett) or --torznab http://127.0.0.1:9696 (Prowlarr), plus --torznab-key YOUR_KEY")
		return false
	}
	if cfg.Key == "" {
		fmt.Printf("  %s no API key given (--torznab-key); Jackett and Prowlarr need one\n", Paint("33", "!"))
	}
	feeds, err := resolveFeeds(ctx, cfg, 30*time.Second)
	if err != nil {
		bad("%v", err)
		explainFeedError(err, hint)
		return false
	}
	var working []*Torznab
	for _, f := range feeds {
		if err := f.caps(ctx); err != nil {
			bad("%v", err)
			explainFeedError(err, hint)
			continue
		}
		ok("%s answers (%s)", f.Name, f)
		working = append(working, f)
	}
	if len(working) == 0 {
		return false
	}

	tmp, err := os.MkdirTemp("", "spotify-downloader-check-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(tmp)
	src := newTorrentSource(working, aria2c, tmp, "mp3", time.Minute)
	results, err := src.searchAll(ctx, query)
	if err != nil {
		bad("searching for %q failed: %v", query, err)
		explainFeedError(err, hint)
		return false
	}
	if len(results) == 0 {
		bad("searching for %q found nothing", query)
		hint("add (more) indexers that carry music in the Jackett/Prowlarr web page and check that their own \"Test\" passes")
		hint("or try another search with --torrent-query \"Artist Album\"")
		return false
	}
	ranked := rankTorrents(results, query, torrentQuery{targets: []string{query}}, false)
	ok("searching for %q: %d results, %d with seeders", query, len(results), len(ranked))
	for i, r := range ranked[:min(5, len(ranked))] {
		seeders := "unknown"
		if r.Seeders >= 0 {
			seeders = strconv.Itoa(r.Seeders)
		}
		hint("%d. %s  (seeders: %s)", i+1, r.Title, seeders)
	}
	if len(ranked) == 0 {
		bad("none of the results has seeders")
		return false
	}
	meta, err := src.meta(ctx, ranked[0])
	if err != nil {
		bad("couldn't get the torrent file of the top result: %v", err)
		hint("if the indexer is behind Cloudflare, set up FlareSolverr (TORRENTS.md, step 5)")
		return false
	}
	audio := 0
	for _, f := range meta.files {
		if audioExts[strings.ToLower(path.Ext(f))] {
			audio++
		}
	}
	ok("the top result's torrent file lists %d files (%d audio)", len(meta.files), audio)
	if good {
		fmt.Println("\nAll good. Enable the Torrent source (and set its place in the order) in the app's Sources tab.")
	}
	return good
}

func explainFeedError(err error, hint func(string, ...any)) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "api key") || strings.Contains(msg, "error 100"):
		hint("copy the API key again: Jackett shows it at the top right of its page; Prowlarr under Settings → General → Security")
	case strings.Contains(msg, "404") || strings.Contains(msg, "doesn't look like"):
		hint("give just the address (http://127.0.0.1:9117 or http://127.0.0.1:9696) or a Torznab feed URL copied from Jackett")
	case strings.Contains(msg, "reach") || strings.Contains(msg, "unreachable") || strings.Contains(msg, "connection"):
		hint("nothing answered there: is Jackett/Prowlarr running? (systemctl status jackett, or prowlarr)")
		hint("default addresses: Jackett http://127.0.0.1:9117, Prowlarr http://127.0.0.1:9696")
	}
}

// lastLines is the last n non-empty lines of a program's output.
func lastLines(out string, n int) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-n):], " / ")
}
