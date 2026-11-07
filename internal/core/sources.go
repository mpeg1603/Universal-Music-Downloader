package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Source is a place to look for a track. Search returns candidates for the matcher to
// score; Download fetches one into workdir. Errors are *SourceError where possible.
type Source interface {
	Name() string
	Label() string
	Search(ctx context.Context, t *Track) ([]Candidate, error)
	Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error)
}

// qualityCapper is implemented by sources that know the best quality they can offer, so a
// track you have in lower quality isn't searched for where it can't get better.
type qualityCapper interface {
	MaxQuality() Quality
}

// --- yt-dlp --------------------------------------------------------------------------------

type YtDlp struct {
	bin     string
	args    []string
	cookies *ytCookies   // your YouTube login; each run gets its own copy (see cookies.go)
	pacer   *rateLimiter // spaces out requests to a site that flags busy clients (YouTube)
}

// youtubePacer is shared by YouTube and YouTube Music: they're one site as far as bot checks go.
var youtubePacer = &rateLimiter{}

// newYtDlp runs yt-dlp with cookies (nil: none) through proxy. Warnings aren't silenced:
// yt-dlp's "your cookies are no longer valid" ends up in the log with the error it explains.
func newYtDlp(tools *Tools, cookies *ytCookies, proxy string) *YtDlp {
	args := []string{"--no-progress", "--socket-timeout", "30", "--retries", "3", "--ignore-config"}
	for _, rt := range tools.JSRuntimes {
		args = append(args, "--js-runtimes", rt)
	}
	if proxy != "" {
		args = append(args, "--proxy", proxy)
	}
	return &YtDlp{bin: tools.YtDlp, args: args, cookies: cookies}
}

// paced makes every yt-dlp run wait for its turn, at least interval apart, and space the
// requests within a run too.
func (y *YtDlp) paced(p *rateLimiter, interval time.Duration) *YtDlp {
	p.mu.Lock()
	p.interval = interval
	p.mu.Unlock()
	c := *y
	c.pacer = p
	if interval > 0 {
		c.args = append(append([]string{}, y.args...), "--sleep-requests", fmt.Sprintf("%.2f", interval.Seconds()/2))
	}
	return &c
}

var (
	reANSI       = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	unreachable  = []string{"unable to download webpage", "unable to download api page", "ssl", "timed out", "connection", "temporary failure in name resolution", "network is unreachable", "http error 5"}
	botCheckHint = []string{"sign in to confirm you", "not a bot", "http error 429", "too many requests", "rate-limit", "rate limit"}
)

func ytdlpError(stderr string, err error) error {
	msg := lastErrorLine(stderr)
	if msg == "" {
		msg = fmt.Sprintf("yt-dlp failed (%v)", err)
	}
	msg, _, _ = strings.Cut(msg, "; please report this issue")
	msg, _, _ = strings.Cut(msg, " (caused by")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	low := strings.ToLower(msg)
	for _, s := range botCheckHint {
		if strings.Contains(low, s) {
			if strings.Contains(low, "soundcloud") {
				return blockedf("SoundCloud is rate-limiting us")
			}
			return blockedf("YouTube asked to sign in (bot check)")
		}
	}
	for _, s := range unreachable {
		if strings.Contains(low, s) {
			return unavailablef("%s", msg)
		}
	}
	return failf("%s", msg)
}

func (y *YtDlp) run(ctx context.Context, extra ...string) ([]byte, error) {
	if y.bin == "" {
		return nil, unavailablef("yt-dlp is not available")
	}
	if y.pacer != nil {
		if err := y.pacer.wait(ctx); err != nil {
			return nil, err
		}
	}
	args := append([]string{}, y.args...)
	if y.cookies != nil {
		path, remove, err := y.cookies.copyFor(ctx)
		defer remove()
		if path != "" {
			args = append(args, "--cookies", path)
		} else if err != nil {
			debugf("yt-dlp runs without cookies: %v", err)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd := command(ctx, y.bin, append(args, extra...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if task := taskFrom(ctx); task != nil {
		// Download progress comes as lines we asked for with --progress-template (see download).
		cmd.Stdout = &progressLines{out: &stdout, task: task}
		cmd.Stderr = &progressLines{out: &stderr, task: task}
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		debugf("yt-dlp %q failed: %s", extra, stderr.String())
		err := ytdlpError(stderr.String(), err)
		if isBlocked(err) && strings.HasPrefix(err.Error(), "YouTube") {
			err = blockedf("%s; %s", err, y.cookies.botCheckAdvice(ctx))
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

const progressMarker = "@sdl-progress "

// progressLines passes a child's output through to out, except progress lines
// ("@sdl-progress DONE TOTAL ESTIMATE"), which go to the task.
type progressLines struct {
	out     *bytes.Buffer
	task    *Task
	partial []byte
}

func (p *progressLines) Write(b []byte) (int, error) {
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexAny(p.partial, "\r\n")
		if i < 0 {
			return len(b), nil
		}
		line := string(p.partial[:i+1])
		p.partial = p.partial[i+1:]
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), strings.TrimSpace(progressMarker)); ok {
			var nums [3]int64
			for j, f := range strings.Fields(rest) {
				if v, err := strconv.ParseFloat(f, 64); err == nil && j < 3 {
					nums[j] = int64(v)
				}
			}
			p.task.setProgress(nums[0], max(nums[1], nums[2]))
			continue
		}
		p.out.WriteString(line)
	}
}

type flatEntry struct {
	ID          string  `json:"id"`
	URL         string  `json:"url"`
	WebpageURL  string  `json:"webpage_url"`
	Title       string  `json:"title"`
	Duration    float64 `json:"duration"`
	Channel     string  `json:"channel"`
	ChannelURL  string  `json:"channel_url"`
	Uploader    string  `json:"uploader"`
	UploaderURL string  `json:"uploader_url"`
	LiveStatus  string  `json:"live_status"`
}

func (y *YtDlp) search(ctx context.Context, query string) ([]flatEntry, error) {
	out, err := y.run(ctx, "--flat-playlist", "-J", query)
	if err != nil {
		return nil, err
	}
	var res struct {
		Entries []flatEntry `json:"entries"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, failf("yt-dlp returned unreadable search results")
	}
	return res.Entries, nil
}

func (y *YtDlp) download(ctx context.Context, u, workdir string) (string, error) {
	out, err := y.run(ctx, "-f", "bestaudio/best", "--no-playlist", "-o", filepath.Join(workdir, "ytdlp.%(ext)s"),
		"--progress", "--newline", "--progress-template", "download:"+progressMarker+
			"%(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s",
		"--print", "after_move:filepath", u)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	p := strings.TrimSpace(lines[len(lines)-1])
	if _, err := os.Stat(p); err != nil {
		return "", failf("yt-dlp finished without producing a file")
	}
	return p, nil
}

// --- YouTube Music ---------------------------------------------------------------------------

type YouTubeMusic struct {
	ytdlp *YtDlp
	http  *HTTP
}

func (s *YouTubeMusic) Name() string        { return "youtube_music" }
func (s *YouTubeMusic) Label() string       { return "YouTube Music" }
func (s *YouTubeMusic) MaxQuality() Quality { return youtubeQuality }

// YouTube's audio is Opus at about 130 kbps (AAC 256 for Premium accounts). It's also used as
// the most YouTube can offer, so tracks you have in about that quality aren't searched for
// there (every YouTube request adds to the risk of a bot check).
var youtubeQuality = Quality{Codec: "opus", Kbps: 130}

func (s *YouTubeMusic) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	return s.ytdlp.download(ctx, c.URL, workdir)
}

func (s *YouTubeMusic) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	// Searching by ISRC often lands exactly on the official upload; the matcher still checks it.
	var queries []string
	if t.ISRC != "" {
		queries = append(queries, t.ISRC)
	}
	queries = append(queries, songQueries(t, 3)...)
	return searchUntilFound(ctx, t, queries, func(q string) ([]Candidate, error) {
		var resp any
		err := s.http.postJSON(ctx, "https://music.youtube.com/youtubei/v1/search?prettyPrint=false", map[string]any{
			"context": map[string]any{"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": "1.20251020.01.00", "hl": "en", "gl": "US"}},
			"query":   q,
			"params":  "EgWKAQIIAWoMEA4QChADEAQQCRAF", // "songs" filter
		}, map[string]string{"Origin": "https://music.youtube.com", "Referer": "https://music.youtube.com/"}, &resp)
		if err != nil {
			return nil, err
		}
		return parseYTMusicResults(resp), nil
	})
}

var reClock = regexp.MustCompile(`^\d+:\d{2}(?::\d{2})?$`)

func parseYTMusicResults(resp any) []Candidate {
	var out []Candidate
	for _, r := range findKey(resp, "musicResponsiveListItemRenderer") {
		vid, _ := dig(r, "playlistItemData", "videoId").(string)
		if vid == "" {
			vid, _ = firstKey(dig(r, "overlay"), "videoId").(string)
		}
		cols, _ := dig(r, "flexColumns").([]any)
		if vid == "" || len(cols) < 2 {
			continue
		}
		c := Candidate{Source: "youtube_music", URL: "https://music.youtube.com/watch?v=" + vid, ArtistIsCredit: true, Quality: youtubeQuality}
		for _, run := range asSlice(dig(cols[0], "musicResponsiveListItemFlexColumnRenderer", "text", "runs")) {
			c.Title += str(dig(run, "text"))
		}
		var artists []string
		segment := 0
		for _, run := range asSlice(dig(cols[1], "musicResponsiveListItemFlexColumnRenderer", "text", "runs")) {
			text := str(dig(run, "text"))
			pageType := str(dig(run, "navigationEndpoint", "browseEndpoint", "browseEndpointContextSupportedConfigs", "browseEndpointContextMusicConfig", "pageType"))
			browseID := str(dig(run, "navigationEndpoint", "browseEndpoint", "browseId"))
			switch {
			case text == " • ":
				segment++
			case pageType == "MUSIC_PAGE_TYPE_ARTIST":
				artists = append(artists, text)
				if c.ArtistURL == "" && browseID != "" {
					c.ArtistURL = "https://music.youtube.com/channel/" + browseID
				}
			case pageType == "MUSIC_PAGE_TYPE_ALBUM":
				c.Album = text
			case reClock.MatchString(text):
				c.Duration = float64(parseDurationMs(text)) / 1000
			case segment == 0 && text != ", " && text != " & " && text != "Song" && text != "Video":
				artists = append(artists, text) // artist without a channel link
			}
		}
		c.Artist = strings.Join(artists, ", ")
		out = append(out, c)
	}
	return out
}

// --- YouTube --------------------------------------------------------------------------------

type YouTube struct {
	ytdlp *YtDlp
	http  *HTTP
}

func (s *YouTube) Name() string        { return "youtube" }
func (s *YouTube) Label() string       { return "YouTube" }
func (s *YouTube) MaxQuality() Quality { return youtubeQuality }

func (s *YouTube) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	return s.ytdlp.download(ctx, c.URL, workdir)
}

func (s *YouTube) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	queries := songQueries(t, 3)
	if len(queries) > 0 && !strings.Contains(queries[0], " - ") && len(t.Artists) > 0 {
		// Uploads are mostly titled "Artist - Title".
		queries[0] = t.Artists[0] + " - " + strings.TrimSpace(strings.TrimPrefix(queries[0], t.Artists[0]))
	}
	return searchUntilFound(ctx, t, queries, func(q string) ([]Candidate, error) { return s.search(ctx, q) })
}

func (s *YouTube) search(ctx context.Context, query string) ([]Candidate, error) {
	var resp any
	err := s.http.postJSON(ctx, "https://www.youtube.com/youtubei/v1/search?prettyPrint=false", map[string]any{
		"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": "2.20251020.00.00", "hl": "en", "gl": "US"}},
		"query":   query,
		"params":  "EgIQAQ==", // videos only
	}, nil, &resp)
	if err == nil {
		if out := parseYouTubeResults(resp); len(out) > 0 {
			return out, nil
		}
	}
	debugf("native YouTube search for %q gave nothing (%v); asking yt-dlp", query, err)
	entries, err := s.ytdlp.search(ctx, "ytsearch8:"+query)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, e := range entries {
		if e.ID == "" || e.LiveStatus == "is_live" || e.LiveStatus == "is_upcoming" {
			continue
		}
		out = append(out, Candidate{Source: "youtube", URL: "https://www.youtube.com/watch?v=" + e.ID, Title: e.Title,
			Artist: FirstNonEmpty(e.Channel, e.Uploader), Duration: e.Duration, ArtistURL: FirstNonEmpty(e.ChannelURL, e.UploaderURL), Quality: youtubeQuality})
	}
	return out, nil
}

func parseYouTubeResults(resp any) []Candidate {
	var out []Candidate
	for _, v := range findKey(resp, "videoRenderer") {
		id := str(dig(v, "videoId"))
		length := str(dig(v, "lengthText", "simpleText"))
		if id == "" || length == "" { // no length: live stream or premiere
			continue
		}
		title := str(dig(v, "title", "simpleText"))
		for _, run := range asSlice(dig(v, "title", "runs")) {
			title += str(dig(run, "text"))
		}
		c := Candidate{Source: "youtube", URL: "https://www.youtube.com/watch?v=" + id, Title: title,
			Duration: float64(parseDurationMs(length)) / 1000, Quality: youtubeQuality}
		if owner := asSlice(dig(v, "ownerText", "runs")); len(owner) > 0 {
			c.Artist = str(dig(owner[0], "text"))
			if p := str(dig(owner[0], "navigationEndpoint", "browseEndpoint", "canonicalBaseUrl")); p != "" {
				c.ArtistURL = "https://www.youtube.com" + p
			}
		}
		out = append(out, c)
	}
	return out
}

// --- SoundCloud -----------------------------------------------------------------------------

type SoundCloud struct{ ytdlp *YtDlp }

func (s *SoundCloud) Name() string        { return "soundcloud" }
func (s *SoundCloud) Label() string       { return "SoundCloud" }
func (s *SoundCloud) MaxQuality() Quality { return Quality{Codec: "aac", Kbps: 160} }

func (s *SoundCloud) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	return searchUntilFound(ctx, t, songQueries(t, 3), func(q string) ([]Candidate, error) { return s.search(ctx, q) })
}

func (s *SoundCloud) search(ctx context.Context, query string) ([]Candidate, error) {
	entries, err := s.ytdlp.search(ctx, "scsearch8:"+query)
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, e := range entries {
		u := FirstNonEmpty(e.WebpageURL, e.URL)
		if u == "" {
			continue
		}
		out = append(out, Candidate{Source: "soundcloud", URL: u, Title: e.Title, Artist: e.Uploader,
			Duration: e.Duration, ArtistURL: e.UploaderURL, Quality: Quality{Codec: "mp3", Kbps: 128}})
	}
	return out, nil
}

// SoundCloud "Go+" tracks only stream a 30-second preview; the length check after
// downloading catches those.
func (s *SoundCloud) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	return s.ytdlp.download(ctx, c.URL, workdir)
}

// --- JSON helpers -----------------------------------------------------------------------------

func dig(v any, path ...string) any {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

func asSlice(v any) []any { s, _ := v.([]any); return s }

// findKey collects the values of every object member named key, at any depth.
func findKey(v any, key string) []any {
	var out []any
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == key {
					out = append(out, x[k])
				} else {
					walk(x[k])
				}
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(v)
	return out
}

func firstKey(v any, key string) any {
	if found := findKey(v, key); len(found) > 0 {
		return found[0]
	}
	return nil
}

func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
