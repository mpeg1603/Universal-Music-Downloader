package main

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
}

func newTorznabFeed(name, feed, key string) *Torznab {
	feed = strings.TrimRight(feed, "/")
	if u, err := url.Parse(feed); err == nil && !strings.HasSuffix(u.Path, "/api") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api"
		feed = u.String()
	}
	return &Torznab{Name: name, feed: feed, key: key, http: newHTTP("Torznab feed "+name, time.Second)}
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
	Seeders                      int // -1 when the indexer doesn't say
	Size                         int64
}

func (r torrentResult) id() string { return firstNonEmpty(r.Link, r.Magnet) + "|" + r.Title }

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
			Title     string `xml:"title"`
			GUID      string `xml:"guid"`
			Link      string `xml:"link"`
			Comments  string `xml:"comments"`
			Size      int64  `xml:"size"`
			Enclosure struct {
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
		r := torrentResult{Title: it.Title, Link: firstNonEmpty(it.Enclosure.URL, it.Link), Size: it.Size, Seeders: -1}
		for _, d := range []string{it.Comments, it.GUID} {
			if strings.HasPrefix(d, "http") && r.Details == "" {
				r.Details = d
			}
		}
		for _, a := range it.Attrs {
			switch a.Name {
			case "seeders":
				r.Seeders, _ = strconv.Atoi(a.Value)
			case "magneturl":
				r.Magnet = a.Value
			case "size":
				if r.Size == 0 {
					r.Size, _ = strconv.ParseInt(a.Value, 10, 64)
				}
			}
		}
		if strings.HasPrefix(r.Link, "magnet:") {
			r.Magnet, r.Link = r.Link, ""
		}
		out = append(out, r)
	}
	return out, nil
}

// resolveFeeds turns the configured URLs into Torznab feeds. A bare Jackett address becomes
// its all-indexers feed; a bare Prowlarr address becomes one feed per enabled torrent indexer.
func resolveFeeds(ctx context.Context, cfg TorrentConfig) ([]*Torznab, error) {
	var feeds []*Torznab
	h := newHTTP("Jackett/Prowlarr", 0)
	h.retries = 2
	for _, raw := range cfg.Feeds {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("%q is not a URL (expected e.g. http://127.0.0.1:9117)", raw)
		}
		if strings.Trim(u.Path, "/") != "" {
			feeds = append(feeds, newTorznabFeed(u.Host+u.Path, raw, cfg.Key))
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
					feeds = append(feeds, newTorznabFeed("Prowlarr: "+ix.Name, fmt.Sprintf("%s/%d/api", base, ix.ID), cfg.Key))
					found++
				}
			}
			if found == 0 {
				return nil, fmt.Errorf("Prowlarr at %s has no enabled torrent indexers; add some under Indexers → Add Indexer", base)
			}
			continue
		}
		// Otherwise assume Jackett and use its feed that searches all configured indexers.
		feeds = append(feeds, newTorznabFeed("Jackett (all indexers)", base+"/api/v2.0/indexers/all/results/torznab/api", cfg.Key))
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
	v, err := bdecode(data)
	if err != nil {
		return nil, fmt.Errorf("not a torrent file: %w", err)
	}
	info, _ := dig(v, "info").(map[string]any)
	if info == nil {
		return nil, errors.New("torrent has no info section")
	}
	name := str(firstOf(info["name.utf-8"], info["name"]))
	files, multi := info["files"].([]any)
	if !multi {
		return []string{name}, nil
	}
	var out []string
	for _, f := range files {
		var parts []string
		for _, p := range asSlice(firstOf(dig(f, "path.utf-8"), dig(f, "path"))) {
			parts = append(parts, str(p))
		}
		out = append(out, path.Join(append([]string{name}, parts...)...))
	}
	return out, nil
}

func firstOf(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

var reLeadingNumber = regexp.MustCompile(`^\s*(?:\d{1,2}[-.])?(\d{1,3})[\s.\-_)]+`)

// bestFile picks the audio file in a torrent that best fits the track, by file name.
func bestFile(t *Track, files []string) (int, string) {
	best, bestScore, bestStem := -1, 0.0, ""
	for i, f := range files {
		if !audioExts[strings.ToLower(path.Ext(f))] {
			continue
		}
		stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
		num := 0
		if m := reLeadingNumber.FindStringSubmatch(stem); m != nil {
			num, _ = strconv.Atoi(m[1])
			stem = stem[len(m[0]):]
		}
		c := Candidate{Title: stem}
		if versionConflict(t, &c) != "" {
			continue
		}
		s := titleScore(t, &c)
		if t.TrackNumber > 0 && num == t.TrackNumber {
			s += 5
		}
		if s > bestScore {
			best, bestScore, bestStem = i, s, stem
		}
	}
	if bestScore < 75 {
		return -1, ""
	}
	return best, bestStem
}

// --- the source -------------------------------------------------------------------------------

type once[T any] struct {
	once sync.Once
	val  T
	err  error
}

// memo runs f once per key (concurrent callers wait for the first) and remembers the result.
func memo[T any](mu *sync.Mutex, m map[string]*once[T], key string, f func() (T, error)) (T, error) {
	mu.Lock()
	o := m[key]
	if o == nil {
		o = &once[T]{}
		m[key] = o
	}
	mu.Unlock()
	o.once.Do(func() { o.val, o.err = f() })
	return o.val, o.err
}

type torrentMeta struct {
	file  string // the .torrent on disk
	files []string
}

type TorrentSource struct {
	feeds    []*Torznab
	aria2c   string
	timeout  time.Duration
	dir      string // cached .torrent files
	lossless bool
	client   *http.Client
	slots    chan struct{} // limits simultaneous aria2c downloads

	mu       sync.Mutex
	searches map[string]*once[[]torrentResult]
	metas    map[string]*once[*torrentMeta]
}

func newTorrentSource(feeds []*Torznab, aria2c, cacheDir, format string, timeout time.Duration) *TorrentSource {
	return &TorrentSource{
		feeds: feeds, aria2c: aria2c, timeout: timeout, dir: filepath.Join(cacheDir, "torrents"),
		lossless: format == "flac" || format == "original",
		client: &http.Client{Timeout: time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme == "magnet" {
				return http.ErrUseLastResponse // some indexers redirect to a magnet link
			}
			return nil
		}},
		slots:    make(chan struct{}, 2),
		searches: map[string]*once[[]torrentResult]{},
		metas:    map[string]*once[*torrentMeta]{},
	}
}

func (s *TorrentSource) Name() string  { return "torrent" }
func (s *TorrentSource) Label() string { return "Torrent" }

// searchAll queries every feed and merges the results (each query only once per run).
func (s *TorrentSource) searchAll(ctx context.Context, query string) ([]torrentResult, error) {
	return memo(&s.mu, s.searches, strings.ToLower(query), func() ([]torrentResult, error) {
		var out []torrentResult
		var firstErr error
		failed := 0
		seen := map[string]bool{}
		for _, f := range s.feeds {
			results, err := f.search(ctx, query)
			if err != nil {
				debugf("%s search %q: %v", f.Name, query, err)
				failed++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			for _, r := range results {
				if !seen[r.id()] {
					seen[r.id()] = true
					out = append(out, r)
				}
			}
		}
		if failed == len(s.feeds) {
			return nil, firstErr
		}
		return out, nil
	})
}

// rankTorrents orders results by how well their name fits artist and target (album or song),
// preferring the wanted format and well-seeded torrents. Dead torrents are dropped.
func rankTorrents(results []torrentResult, artist, target string, lossless bool) []torrentResult {
	type scored struct {
		r     torrentResult
		score float64
	}
	var list []scored
	na, nt := normalize(artist), normalize(target)
	for _, r := range results {
		if r.Seeders == 0 {
			continue
		}
		title := normalize(r.Title)
		score := partialRatio(na, title) + partialRatio(nt, title)
		if lossless == (strings.Contains(title, "flac") || strings.Contains(title, "lossless")) {
			score += 10
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

func (s *TorrentSource) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	type query struct{ text, target string }
	var queries []query
	if t.Album != "" && !strings.EqualFold(t.Album, t.Title) {
		queries = append(queries, query{t.AlbumArtistStr() + " " + t.Album, t.Album}) // the album
	}
	queries = append(queries, query{t.Artists[0] + " " + searchTitle(t.Title), searchTitle(t.Title)}) // singles, discographies
	var out []Candidate
	found, inspected := 0, 0
	for _, q := range queries {
		results, err := s.searchAll(ctx, q.text)
		if err != nil {
			return nil, err
		}
		found += len(results)
		ranked := rankTorrents(results, t.Artists[0], q.target, s.lossless)
		for _, r := range ranked[:min(3, len(ranked))] {
			meta, err := s.meta(ctx, r)
			if err != nil {
				debugf("torrent %q: %v", r.Title, err)
				continue
			}
			inspected++
			if i, stem := bestFile(t, meta.files); i >= 0 {
				out = append(out, Candidate{Source: "torrent", URL: r.Details, Title: stem, Artist: r.Title,
					Album: r.Title, Ref: meta.file + "#" + strconv.Itoa(i)})
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	switch {
	case found == 0:
		return nil, nil
	case inspected == 0:
		return nil, failf("%d torrents found, but none with seeders and a readable torrent file", found)
	}
	return nil, failf("%d torrents found; the %d best-matching ones don't contain this track", found, inspected)
}

// meta gets a result's .torrent (from its link, or a magnet's metadata via aria2c) and file list.
func (s *TorrentSource) meta(ctx context.Context, r torrentResult) (*torrentMeta, error) {
	return memo(&s.mu, s.metas, r.id(), func() (*torrentMeta, error) {
		data, err := s.torrentBytes(ctx, r)
		if err != nil {
			return nil, err
		}
		files, err := torrentFiles(data)
		if err != nil {
			return nil, err
		}
		sum := sha1.Sum([]byte(r.id()))
		p := filepath.Join(s.dir, hex.EncodeToString(sum[:10])+".torrent")
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return nil, err
		}
		return &torrentMeta{file: p, files: files}, nil
	})
}

func (s *TorrentSource) torrentBytes(ctx context.Context, r torrentResult) ([]byte, error) {
	magnet := r.Magnet
	if r.Link != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", r.Link, nil)
		req.Header.Set("User-Agent", userAgent)
		if resp, err := s.client.Do(req); err == nil {
			var buf bytes.Buffer
			buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
				magnet = loc
			} else if resp.StatusCode == 200 && bytes.HasPrefix(buf.Bytes(), []byte("d")) {
				return buf.Bytes(), nil
			}
		}
	}
	if magnet == "" {
		return nil, failf("couldn't get the torrent file for “%s”", r.Title)
	}
	if s.aria2c == "" {
		return nil, failf("“%s” only has a magnet link, which needs aria2c", r.Title)
	}
	// Only a magnet link: ask the swarm for the torrent's metadata.
	tmp, err := os.MkdirTemp("", "spotify-downloader-meta-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	mctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(mctx, s.aria2c, "--dir="+tmp, "--bt-metadata-only=true", "--bt-save-metadata=true",
		"--bt-stop-timeout=90", "--summary-interval=0", "--console-log-level=error", "--download-result=hide", magnet).CombinedOutput()
	matches, _ := filepath.Glob(filepath.Join(tmp, "*.torrent"))
	if len(matches) == 0 {
		debugf("aria2c metadata fetch for %q failed: %v %s", r.Title, err, out)
		return nil, failf("no peers answered for the magnet link of “%s”", r.Title)
	}
	return os.ReadFile(matches[0])
}

func (s *TorrentSource) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	file, idxText, ok := strings.Cut(c.Ref, "#")
	idx, err := strconv.Atoi(idxText)
	if !ok || err != nil {
		return "", failf("bad torrent reference")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	files, err := torrentFiles(data)
	if err != nil || idx >= len(files) {
		return "", failf("bad torrent file")
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	dir := filepath.Join(workdir, "torrent")
	if err := runAria2c(ctx, s.aria2c, file, dir, idx, s.timeout); err != nil {
		return "", err
	}
	got := findDownloaded(dir, files[idx])
	if _, err := os.Stat(got); err != nil {
		return "", failf("aria2c finished but %s isn't there", path.Base(files[idx]))
	}
	return got, nil
}

func runAria2c(ctx context.Context, bin, torrentFile, dir string, index int, timeout time.Duration) error {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(dctx, bin, "--dir="+dir, "--select-file="+strconv.Itoa(index+1),
		"--seed-time=0", "--bt-stop-timeout=300", "--bt-remove-unselected-file=true", "--file-allocation=none",
		"--allow-overwrite=true", "--auto-file-renaming=false", "--summary-interval=0",
		"--console-log-level=error", "--download-result=hide", torrentFile)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	debugf("aria2c failed: %v %s", err, out)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dctx.Err() != nil {
		return failf("torrent download didn't finish within %s (raise --torrent-timeout?)", timeout)
	}
	return failf("aria2c couldn't download it (no seeder sent data for 5 minutes)")
}

// findDownloaded locates a torrent file on disk, falling back to a search by file name in
// case aria2c adjusted the path.
func findDownloaded(content, rel string) string {
	want := filepath.Join(content, filepath.FromSlash(rel))
	if _, err := os.Stat(want); err == nil {
		return want
	}
	base := path.Base(rel)
	filepath.WalkDir(content, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == base {
			want = p
			return filepath.SkipAll
		}
		return nil
	})
	return want
}

// --- setup check (--check-torrents) ------------------------------------------------------------

// checkTorrents walks through the torrent setup step by step and explains what to fix.
func checkTorrents(ctx context.Context, cfg TorrentConfig, aria2c, query string) bool {
	ok := func(format string, args ...any) {
		fmt.Printf("  %s %s\n", paint("32", "✓"), fmt.Sprintf(format, args...))
	}
	bad := func(format string, args ...any) {
		fmt.Printf("  %s %s\n", paint("31", "✗"), fmt.Sprintf(format, args...))
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
		fmt.Printf("  %s no API key given (--torznab-key); Jackett and Prowlarr need one\n", paint("33", "!"))
	}
	feeds, err := resolveFeeds(ctx, cfg)
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
	ranked := rankTorrents(results, query, query, false)
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
		fmt.Println("\nAll good. Torrents are now tried after the other sources; to change that, e.g.:")
		fmt.Println("  spotify-downloader --sources torrent,youtube_music")
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
