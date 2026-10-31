package main

import (
	"bytes"
	"context"
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
	"time"
)

// Torrents, the last resort: tracks no other source had are looked up album by album through
// a Torznab feed (the API Jackett and Prowlarr expose for the indexers you configure there)
// and fetched with aria2c. Only the needed files are downloaded and nothing is seeded after.

type Torznab struct {
	endpoint string
	key      string
	http     *HTTP
}

func newTorznab(feed, key string) *Torznab {
	feed = strings.TrimRight(feed, "/")
	if !strings.HasSuffix(feed, "/api") {
		feed += "/api"
	}
	return &Torznab{endpoint: feed, key: key, http: newHTTP("Torznab", time.Second)}
}

type torrentResult struct {
	Title, Link, Magnet, Details string
	Seeders                      int
	Size                         int64
}

func (z *Torznab) search(ctx context.Context, query string) ([]torrentResult, error) {
	q := url.Values{"t": {"search"}, "q": {query}, "cat": {"3000"}, "limit": {"50"}}
	if z.key != "" {
		q.Set("apikey", z.key)
	}
	status, body, err := z.http.get(ctx, z.endpoint+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, unavailablef("Torznab feed answered HTTP %d", status)
	}
	return parseTorznab(body)
}

func parseTorznab(body []byte) ([]torrentResult, error) {
	var apiErr struct {
		XMLName     xml.Name `xml:"error"`
		Description string   `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &apiErr) == nil && apiErr.XMLName.Local == "error" {
		return nil, unavailablef("Torznab feed error: %s", apiErr.Description)
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

// torrentFiles lists the files of a .torrent as slash paths relative to aria2c's --dir,
// in the order aria2c's --select-file numbers them (from 1).
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

// --- matching albums and files --------------------------------------------------------------------

var reLeadingNumber = regexp.MustCompile(`^\s*(?:\d{1,2}[-.])?(\d{1,3})[\s.\-_)]+`)

// matchTorrentFiles maps tracks to audio files in a torrent (by file name), using each file once.
func matchTorrentFiles(tracks []*Track, files []string) map[string]int {
	used := map[int]bool{}
	out := map[string]int{}
	for _, t := range tracks {
		best, bestScore := -1, 0.0
		for i, f := range files {
			if used[i] || !audioExts[strings.ToLower(path.Ext(f))] {
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
				best, bestScore = i, s
			}
		}
		if best >= 0 && bestScore >= 85 {
			out[t.Key] = best
			used[best] = true
		}
	}
	return out
}

func rankTorrents(results []torrentResult, artist, album string, lossless bool) []torrentResult {
	type scored struct {
		r     torrentResult
		score float64
	}
	var list []scored
	na, nb := normalize(artist), normalize(album)
	for _, r := range results {
		title := normalize(r.Title)
		if r.Seeders == 0 || partialRatio(na, title) < 90 {
			continue
		}
		score := 0.0
		if nb != "" {
			score = partialRatio(nb, title)
		}
		if lossless == strings.Contains(title, "flac") {
			score += 5
		}
		score += math.Min(float64(max(r.Seeders, 0)), 50) / 10
		list = append(list, scored{r, score})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	var out []torrentResult
	for _, s := range list {
		out = append(out, s.r)
	}
	return out
}

// --- the torrent pass -------------------------------------------------------------------------

type TorrentFetcher struct {
	torznab *Torznab
	tools   *Tools
	timeout time.Duration
	http    *http.Client
}

func newTorrentFetcher(feed, key string, tools *Tools, timeout time.Duration) *TorrentFetcher {
	return &TorrentFetcher{
		torznab: newTorznab(feed, key),
		tools:   tools,
		timeout: timeout,
		http: &http.Client{Timeout: time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme == "magnet" {
				return http.ErrUseLastResponse
			}
			return nil
		}},
	}
}

// torrentBytes gets the .torrent for a result: from its link (which may redirect to a magnet),
// or by fetching the metadata of the magnet with aria2c.
func (f *TorrentFetcher) torrentBytes(ctx context.Context, r torrentResult, dir string) ([]byte, error) {
	if r.Link != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", r.Link, nil)
		if resp, err := f.http.Do(req); err == nil {
			var buf bytes.Buffer
			buf.ReadFrom(resp.Body)
			resp.Body.Close()
			if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "magnet:") {
				r.Magnet = loc
			} else if resp.StatusCode == 200 && bytes.HasPrefix(buf.Bytes(), []byte("d")) {
				return buf.Bytes(), nil
			}
		}
	}
	if r.Magnet == "" {
		return nil, failf("couldn't get the torrent file for “%s”", r.Title)
	}
	metaDir := filepath.Join(dir, "meta")
	os.MkdirAll(metaDir, 0o755)
	mctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(mctx, f.tools.Aria2c, "--dir="+metaDir, "--bt-metadata-only=true", "--bt-save-metadata=true",
		"--bt-stop-timeout=150", "--summary-interval=0", "--console-log-level=error", "--download-result=hide", r.Magnet)
	out, err := cmd.CombinedOutput()
	matches, _ := filepath.Glob(filepath.Join(metaDir, "*.torrent"))
	if len(matches) == 0 {
		debugf("aria2c metadata fetch failed: %v %s", err, out)
		return nil, failf("no peers answered for “%s”", r.Title)
	}
	defer os.RemoveAll(metaDir)
	return os.ReadFile(matches[0])
}

func (f *TorrentFetcher) aria2c(ctx context.Context, torrentFile, dir string, indices []int) error {
	var sel []string
	for _, i := range indices {
		sel = append(sel, strconv.Itoa(i+1))
	}
	dctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	cmd := exec.CommandContext(dctx, f.tools.Aria2c, "--dir="+dir, "--select-file="+strings.Join(sel, ","),
		"--seed-time=0", "--bt-stop-timeout=300", "--bt-remove-unselected-file=true", "--file-allocation=none",
		"--allow-overwrite=true", "--auto-file-renaming=false", "--summary-interval=0",
		"--console-log-level=error", "--download-result=hide", torrentFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		debugf("aria2c failed: %v %s", err, out)
		if dctx.Err() != nil && ctx.Err() == nil {
			return failf("torrent download didn't finish within %s", f.timeout)
		}
		return failf("aria2c couldn't download the torrent (no seeders?)")
	}
	return nil
}

type torrentGroup struct {
	artist, album string
	tracks        []*Track
}

func groupForTorrents(tracks []*Track) []*torrentGroup {
	var groups []*torrentGroup
	index := map[string]*torrentGroup{}
	for _, t := range tracks {
		key := normalize(t.AlbumArtistStr()) + "|" + normalize(t.Album)
		if t.Album == "" {
			key = "single|" + t.Key
		}
		g := index[key]
		if g == nil {
			g = &torrentGroup{artist: t.AlbumArtistStr(), album: t.Album}
			index[key] = g
			groups = append(groups, g)
		}
		g.tracks = append(g.tracks, t)
	}
	return groups
}

// runTorrents tries torrents for the tracks no other source had, reporting each outcome.
func (e *Engine) runTorrents(ctx context.Context, stop <-chan struct{}, f *TorrentFetcher, tracks []*Track, onDone func(Outcome)) map[string]int {
	counts := map[string]int{}
	lossless := e.set.Format == "flac" || e.set.Format == "original"
	for _, g := range groupForTorrents(tracks) {
		select {
		case <-stop:
			return counts
		default:
		}
		for _, o := range e.torrentGroup(ctx, f, g, lossless) {
			if o.Status == "aborted" {
				return counts
			}
			counts[o.Status]++
			onDone(o)
		}
	}
	return counts
}

func (e *Engine) torrentGroup(ctx context.Context, f *TorrentFetcher, g *torrentGroup, lossless bool) []Outcome {
	dir := filepath.Join(e.workDir, "torrent-"+g.tracks[0].Key)
	os.MkdirAll(dir, 0o755)
	defer os.RemoveAll(dir)

	failAll := func(reason string, transient bool) []Outcome {
		var out []Outcome
		for _, t := range g.tracks {
			e.appendFailure(t, "Torrent: "+reason, transient)
			out = append(out, Outcome{t, "failed", "Torrent: " + reason})
		}
		return out
	}
	query := g.artist + " " + g.album
	if g.album == "" {
		query = g.artist + " " + searchTitle(g.tracks[0].Title)
	}
	results, err := f.torznab.search(ctx, query)
	if ctx.Err() != nil {
		return []Outcome{{Status: "aborted"}}
	}
	if err != nil {
		return failAll(err.Error(), isUnavailable(err))
	}
	ranked := rankTorrents(results, g.artist, g.album, lossless)
	if len(ranked) == 0 {
		return failAll(fmt.Sprintf("nothing with seeders found for “%s” (%d results)", query, len(results)), false)
	}

	var torrentFile string
	var files []string
	var mapping map[string]int
	var chosen torrentResult
	for _, r := range ranked[:min(3, len(ranked))] {
		data, err := f.torrentBytes(ctx, r, dir)
		if err != nil {
			debugf("torrent %q: %v", r.Title, err)
			continue
		}
		list, err := torrentFiles(data)
		if err != nil {
			continue
		}
		if m := matchTorrentFiles(g.tracks, list); len(m) > len(mapping) {
			mapping, files, chosen = m, list, r
			torrentFile = filepath.Join(dir, "chosen.torrent")
			os.WriteFile(torrentFile, data, 0o644)
		}
		if len(mapping) == len(g.tracks) {
			break
		}
	}
	if len(mapping) == 0 {
		return failAll(fmt.Sprintf("no torrent for “%s” contained these tracks", query), false)
	}
	var indices []int
	for _, i := range mapping {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	content := filepath.Join(dir, "content")
	if err := f.aria2c(ctx, torrentFile, content, indices); err != nil {
		if ctx.Err() != nil {
			return []Outcome{{Status: "aborted"}}
		}
		return failAll(err.Error(), true)
	}

	var out []Outcome
	for _, t := range g.tracks {
		i, ok := mapping[t.Key]
		if !ok {
			e.appendFailure(t, fmt.Sprintf("Torrent: not in “%s”", chosen.Title), false)
			out = append(out, Outcome{t, "failed", "Torrent: not in the torrent found for its album"})
			continue
		}
		workdir := filepath.Join(dir, "work-"+t.Key)
		os.MkdirAll(workdir, 0o755)
		raw := findDownloaded(content, files[i])
		c := Candidate{Source: "torrent", URL: chosen.Details, Title: chosen.Title}
		if err := e.finalize(ctx, raw, t, c, 0, workdir); err != nil {
			if ctx.Err() != nil {
				return append(out, Outcome{Status: "aborted"})
			}
			e.appendFailure(t, "Torrent: "+err.Error(), false)
			out = append(out, Outcome{t, "failed", "Torrent: " + err.Error()})
			continue
		}
		out = append(out, Outcome{t, "downloaded", "Torrent"})
	}
	return out
}

// appendFailure adds a torrent failure to the reasons recorded by the main pass.
func (e *Engine) appendFailure(t *Track, reason string, transient bool) {
	if prev := e.state.get(t.Key); prev != nil && prev.Status != "downloaded" && prev.Reason != "" {
		reason = prev.Reason + "; " + reason
		transient = transient || prev.Status == "error"
	}
	e.state.markFailed(t.Key, reason, transient)
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
