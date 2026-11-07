package core

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bandcamp, using only downloads the artist offers for free. Paid releases are not
// downloaded; the failure report links to them so you can buy them.

var bandcampEncodings = map[string][]string{
	"mp3":      {"mp3-320", "mp3-v0", "flac"},
	"opus":     {"flac", "vorbis", "mp3-320"},
	"m4a":      {"aac-hi", "flac", "mp3-320"},
	"flac":     {"flac", "alac", "wav", "aiff-lossless", "mp3-320"},
	"original": {"flac", "alac", "mp3-320", "mp3-v0"},
}

var audioExts = map[string]bool{".mp3": true, ".flac": true, ".m4a": true, ".ogg": true, ".opus": true,
	".wav": true, ".aiff": true, ".aif": true, ".ape": true, ".wv": true, ".wma": true, ".aac": true}

type Bandcamp struct {
	http      *HTTP
	cacheDir  string
	encodings []string
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	mail      *bandcampMail // nil: no email address set
}

// Bandcamp answers HTTP 429 to a steady request every half second within minutes, and then
// for a long while; requests are spaced a second apart, more after a 429 (shared by all
// Bandcamp sources made from changed settings).
var bandcampPace = newAdaptiveLimiter(time.Second, 20*time.Second)

// Pages and searches are remembered for a while: an album's page is needed for each of its
// tracks, and a track waiting for another source searches again later.
var (
	bandcampPages    = &ttlCache[*tralbum]{ttl: 12 * time.Hour, max: 20000}
	bandcampSearches = &ttlCache[[]Candidate]{ttl: 12 * time.Hour, max: 50000}
)

func newBandcamp(cacheDir, format, route string, mail *bandcampMail) *Bandcamp {
	h := newRoutedHTTP("Bandcamp", 0, route)
	h.limiter = bandcampPace
	return &Bandcamp{
		http:      h,
		cacheDir:  filepath.Join(cacheDir, "bandcamp"),
		encodings: bandcampEncodings[format],
		locks:     map[string]*sync.Mutex{},
		mail:      mail,
	}
}

func (b *Bandcamp) Name() string  { return "bandcamp" }
func (b *Bandcamp) Label() string { return "Bandcamp" }

func (b *Bandcamp) MaxQuality() Quality { return encodingQuality(b.encodings[0]) }

func encodingQuality(enc string) Quality {
	switch enc {
	case "flac", "alac", "wav", "aiff-lossless":
		return Quality{Codec: strings.TrimSuffix(enc, "-lossless"), Lossless: true}
	case "mp3-320":
		return Quality{Codec: "mp3", Kbps: 320}
	case "mp3-v0":
		return Quality{Codec: "mp3", Kbps: 245}
	case "aac-hi":
		return Quality{Codec: "aac", Kbps: 256}
	case "vorbis":
		return Quality{Codec: "vorbis", Kbps: 190}
	}
	return Quality{}
}

// busy is how long a request would wait for its turn now; the scheduler skips Bandcamp for a
// while rather than have searches queue up for it.
func (b *Bandcamp) busy() time.Duration { return b.http.limiter.backlog() }

func (b *Bandcamp) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	return searchUntilFound(ctx, t, songQueries(t, 2), func(q string) ([]Candidate, error) {
		return bandcampSearches.get(strings.ToLower(q)+"\x00"+b.encodings[0], func() ([]Candidate, error) { return b.search(ctx, q) })
	})
}

func (b *Bandcamp) search(ctx context.Context, query string) ([]Candidate, error) {
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
	err := b.http.postJSON(ctx, "https://bandcamp.com/api/bcsearch_public_api/1/autocomplete_elastic", map[string]any{
		"search_text": query, "search_filter": "t", "full_page": false, "fan_id": nil,
	}, nil, &res)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range res.Auto.Results {
		if r.Type == "t" && r.ItemURLPath != "" {
			out = append(out, Candidate{Source: "bandcamp", URL: r.ItemURLPath, Title: r.Name, Artist: r.BandName,
				Album: r.AlbumName, ArtistURL: r.ItemURLRoot, ArtistIsCredit: true, Quality: b.MaxQuality()})
		}
	}
	return out, nil
}

type tralbum struct {
	FreeDownloadPage string `json:"freeDownloadPage"`
	AlbumURL         string `json:"album_url"`
	Current          struct {
		ID           int64    `json:"id"`
		Type         string   `json:"type"`
		MinimumPrice *float64 `json:"minimum_price"`
	} `json:"current"`
	TrackInfo []struct {
		Title     string  `json:"title"`
		TrackNum  int     `json:"track_num"`
		Duration  float64 `json:"duration"`
		TitleLink string  `json:"title_link"`
	} `json:"trackinfo"`
}

func dataAttr(page []byte, attr string, out any) bool {
	m := regexp.MustCompile(attr + `="([^"]*)"`).FindSubmatch(page)
	return m != nil && json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), out) == nil
}

func (b *Bandcamp) tralbum(ctx context.Context, u string) (*tralbum, error) {
	return bandcampPages.get(u, func() (*tralbum, error) { return b.fetchTralbum(ctx, u) })
}

func (b *Bandcamp) fetchTralbum(ctx context.Context, u string) (*tralbum, error) {
	status, body, err := b.http.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if isChallenge(body) {
		return nil, blockedf("Bandcamp is showing its bot check page to this connection")
	}
	if status != 200 {
		return nil, failf("couldn't open %s (HTTP %d)", u, status)
	}
	var t tralbum
	if !dataAttr(body, "data-tralbum", &t) {
		return nil, failf("couldn't read the Bandcamp page %s", u)
	}
	return &t, nil
}

func (b *Bandcamp) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	page, err := b.tralbum(ctx, c.URL)
	if err != nil {
		return "", err
	}
	if len(page.TrackInfo) > 0 && t.DurationMs > 0 {
		if l := page.TrackInfo[0].Duration; l > 0 && math.Abs(l-t.Duration()) > durationTolerance(t.Duration()) {
			return "", failf("%s is %s long, expected %s", c.URL, FmtDuration(l), FmtDuration(t.Duration()))
		}
	}
	if page.FreeDownloadPage != "" {
		return b.fetchFree(ctx, page.FreeDownloadPage, workdir, "", 0, c.Title)
	}
	var album *tralbum
	albumURL := ""
	if page.AlbumURL != "" {
		base, _ := url.Parse(c.URL)
		ref, _ := url.Parse(page.AlbumURL)
		albumURL = base.ResolveReference(ref).String()
		if album, err = b.tralbum(ctx, albumURL); err != nil {
			return "", err
		}
		if album.FreeDownloadPage != "" {
			return b.fetchFree(ctx, album.FreeDownloadPage, workdir, albumURL, trackNumberInAlbum(album, c.URL, c.Title), c.Title)
		}
	}
	// Name-your-price releases whose artist asks for an email address: Bandcamp emails the
	// download link. The album is preferred, so its other tracks come from the same download.
	for _, p := range []struct {
		t *tralbum
		u string
	}{{album, albumURL}, {page, c.URL}} {
		if p.t == nil || p.t.Current.MinimumPrice == nil || *p.t.Current.MinimumPrice != 0 {
			continue
		}
		if b.mail == nil {
			return "", failf("name-your-price on Bandcamp, but the artist wants an email address first; enter yours in Sources → Bandcamp, or grab it by hand: %s", p.u)
		}
		dl, err := b.emailDownload(ctx, p.t, p.u)
		if err != nil {
			return "", err
		}
		var file string
		if p.t == album {
			file, err = b.fetchFree(ctx, dl, workdir, albumURL, trackNumberInAlbum(album, c.URL, c.Title), c.Title)
		} else {
			file, err = b.fetchFree(ctx, dl, workdir, "", 0, c.Title)
		}
		if err != nil && ctx.Err() == nil && !isUnavailable(err) {
			b.mail.dropLink(itemKey(p.t.Current.Type, p.t.Current.ID)) // expired: ask again next time
		}
		return file, err
	}
	return "", failf("on Bandcamp but not a free download; you can buy it at %s", c.URL)
}

// emailDownload returns the download page Bandcamp emails for a release: asks Bandcamp to send
// it (once), then waits for it to show up in your mailbox, or for you to paste it.
func (b *Bandcamp) emailDownload(ctx context.Context, t *tralbum, pageURL string) (string, error) {
	key := itemKey(t.Current.Type, t.Current.ID)
	mb := b.mail
	if link := mb.link(key); link != "" {
		return link, nil
	}
	task := taskFrom(ctx)
	task.park() // waiting for an email needs no download slot
	lock := b.lockFor("email " + key)
	lock.Lock()
	defer lock.Unlock()
	if link := mb.link(key); link != "" {
		return link, nil
	}
	cfg, canFetch := mb.settings()
	ch := mb.wait(key)
	defer mb.forget(key, ch)
	// Asked once; again only if the link never came (a day with a mailbox, a week without).
	again := 7 * 24 * time.Hour
	if canFetch {
		again = 24 * time.Hour
	}
	if at := mb.requestedAt(key); at.IsZero() || time.Since(at) > again {
		task.setStage("asking Bandcamp to email the download")
		u, err := url.Parse(pageURL)
		if err != nil {
			return "", err
		}
		form := url.Values{"encoding_name": {"none"}, "item_id": {strconv.FormatInt(t.Current.ID, 10)},
			"item_type": {t.Current.Type}, "address": {cfg.Email}, "country": {FirstNonEmpty(cfg.Country, "United States")},
			"postcode": {FirstNonEmpty(cfg.Postcode, "10001")}}
		status, body, err := b.http.do(ctx, request{method: "POST", url: u.Scheme + "://" + u.Host + "/email_download",
			body: []byte(form.Encode()), headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded",
				"Referer": pageURL, "X-Requested-With": "XMLHttpRequest"}})
		if err != nil {
			return "", err
		}
		if isChallenge(body) {
			return "", blockedf("Bandcamp is showing its bot check page to this connection")
		}
		var ok struct {
			OK bool `json:"ok"`
		}
		if status != 200 || json.Unmarshal(body, &ok) != nil || !ok.OK {
			return "", failf("Bandcamp didn't accept the email address for %s (HTTP %d)", pageURL, status)
		}
		mb.markRequested(key)
		Logf("Bandcamp: asked for %s to be emailed to %s", pageURL, cfg.Email)
	}
	if !canFetch {
		return "", pendingf(6*time.Hour, "Bandcamp emails the download link to %s: paste it with b in the Sources tab (%s)", cfg.Email, pageURL)
	}
	task.setStage("waiting for Bandcamp's email")
	go mb.poll()
	select {
	case link := <-ch:
		return link, nil
	case <-time.After(10 * time.Minute):
		return "", pendingf(30*time.Minute, "Bandcamp's email to %s hasn't arrived yet; the mailbox is checked again later, or paste the link with b in the Sources tab (%s)", cfg.Email, pageURL)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func itemKey(typ string, id int64) string {
	if typ != "" {
		typ = typ[:1]
	}
	return fmt.Sprintf("%s%d", typ, id)
}

func trackNumberInAlbum(album *tralbum, trackURL, title string) int {
	p := ""
	if u, err := url.Parse(trackURL); err == nil {
		p = u.Path
	}
	best, bestScore := 0, -1.0
	for _, ti := range album.TrackInfo {
		if ti.TitleLink == p {
			return ti.TrackNum
		}
		if s := ratio(normalize(ti.Title), normalize(title)); s > bestScore {
			best, bestScore = ti.TrackNum, s
		}
	}
	return best
}

func (b *Bandcamp) lockFor(key string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.locks[key] == nil {
		b.locks[key] = &sync.Mutex{}
	}
	return b.locks[key]
}

func (b *Bandcamp) fetchFree(ctx context.Context, pageURL, workdir, albumURL string, trackNum int, title string) (string, error) {
	var file string
	var err error
	if albumURL != "" {
		// Album zips are cached so other tracks from the same album reuse them.
		sum := sha1.Sum([]byte(albumURL))
		stem := hex.EncodeToString(sum[:8])
		lock := b.lockFor(albumURL)
		lock.Lock()
		matches, _ := filepath.Glob(filepath.Join(b.cacheDir, stem+".*"))
		for _, m := range matches {
			if !strings.HasSuffix(m, ".part") {
				file = m
			}
		}
		if file == "" {
			if err = os.MkdirAll(b.cacheDir, 0o755); err == nil {
				file, err = b.downloadFromPage(ctx, pageURL, b.cacheDir, stem)
			}
		}
		lock.Unlock()
	} else {
		file, err = b.downloadFromPage(ctx, pageURL, workdir, "bandcamp-download")
	}
	if err != nil {
		return "", err
	}
	if zr, zerr := zip.OpenReader(file); zerr == nil {
		defer zr.Close()
		return extractTrack(&zr.Reader, workdir, trackNum, title)
	}
	return file, nil
}

func (b *Bandcamp) downloadFromPage(ctx context.Context, pageURL, dir, stem string) (string, error) {
	_, body, err := b.http.get(ctx, pageURL)
	if err != nil {
		return "", err
	}
	type item struct {
		Downloads map[string]struct {
			URL string `json:"url"`
		} `json:"downloads"`
	}
	var blob struct {
		DigitalItems  []item `json:"digital_items"`
		DownloadItems []item `json:"download_items"`
	}
	dataAttr(body, "data-blob", &blob)
	items := append(blob.DigitalItems, blob.DownloadItems...)
	if len(items) == 0 || len(items[0].Downloads) == 0 {
		return "", failf("Bandcamp's free download page offered no files")
	}
	downloads := items[0].Downloads
	link := ""
	for _, enc := range b.encodings {
		if d, ok := downloads[enc]; ok {
			link = d.URL
			break
		}
	}
	if link == "" {
		for _, d := range downloads {
			link = d.URL
			break
		}
	}
	signed, err := b.resolve(ctx, link)
	if err != nil {
		return "", err
	}
	return b.http.download(ctx, signed, dir, stem)
}

// resolve trades the download page's link for a signed file URL via /statdownload/.
func (b *Bandcamp) resolve(ctx context.Context, link string) (string, error) {
	for i := 0; i < 4; i++ {
		statURL := strings.Replace(link, "/download/", "/statdownload/", 1)
		sep := "?"
		if strings.Contains(statURL, "?") {
			sep = "&"
		}
		_, body, err := b.http.do(ctx, request{url: statURL + sep + ".vrs=1", headers: map[string]string{"Accept": "application/json"}})
		if err != nil {
			return "", err
		}
		stat := parseStatResponse(body)
		if stat.Result == "ok" && stat.DownloadURL != "" {
			return stat.DownloadURL, nil
		}
		if stat.RetryURL == "" {
			break
		}
		link = stat.RetryURL
	}
	return "", failf("Bandcamp didn't hand out a download link")
}

type statResponse struct {
	Result      string `json:"result"`
	DownloadURL string `json:"download_url"`
	RetryURL    string `json:"retry_url"`
}

// parseStatResponse reads statdownload's answer, which is JSON, sometimes wrapped as JSONP.
func parseStatResponse(body []byte) statResponse {
	var s statResponse
	text := string(body)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		json.Unmarshal([]byte(text[start:end+1]), &s)
	}
	return s
}

var (
	reZipTrackNum  = regexp.MustCompile(` - (\d{1,3}) `)
	reLeadTrackNum = regexp.MustCompile(`^(\d{1,3})[ ._-]`)
)

// extractTrack copies one track out of an album zip ("Artist - Album - 03 Title.flac").
func extractTrack(zr *zip.Reader, workdir string, trackNum int, title string) (string, error) {
	var members []*zip.File
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && audioExts[strings.ToLower(path.Ext(f.Name))] {
			members = append(members, f)
		}
	}
	if len(members) == 0 {
		return "", failf("the Bandcamp album download contained no audio files")
	}
	number := func(f *zip.File) int {
		stem := strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name))
		m := reZipTrackNum.FindStringSubmatch(stem)
		if m == nil {
			m = reLeadTrackNum.FindStringSubmatch(stem)
		}
		if m == nil {
			return 0
		}
		n, _ := strconv.Atoi(m[1])
		return n
	}
	similarity := func(f *zip.File) float64 {
		return partialRatio(normalize(title), normalize(strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name))))
	}
	var pick *zip.File
	if trackNum > 0 {
		var numbered []*zip.File
		for _, f := range members {
			if number(f) == trackNum {
				numbered = append(numbered, f)
			}
		}
		if len(numbered) == 1 {
			pick = numbered[0]
		}
	}
	if pick == nil {
		best := -1.0
		for _, f := range members {
			if s := similarity(f); s > best {
				pick, best = f, s
			}
		}
		if best < 80 {
			return "", failf("couldn't find the track inside the Bandcamp album download")
		}
	}
	src, err := pick.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()
	out := filepath.Join(workdir, "bandcamp"+strings.ToLower(path.Ext(pick.Name)))
	dst, err := os.Create(out)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return out, err
}
