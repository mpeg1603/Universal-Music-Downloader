package core

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Playlists and albums added as a link: Spotify, Deezer, Apple Music, Bandcamp, and through
// yt-dlp YouTube, SoundCloud and the many other sites it knows. What a link holds is kept, so a
// site being down doesn't lose the playlist and reading the playlists again costs nothing.
// Songs that came from a link are downloaded from it first.

const (
	linkMaxAge     = 7 * 24 * time.Hour
	linkSourceName = "link"
	linkMaxEntries = 5000
)

type linkCache struct {
	Name   string   `json:"name"`
	Tracks []*Track `json:"tracks"`
}

func (m *Manager) linkCachePath(link string) string {
	sum := sha1.Sum([]byte(link))
	return filepath.Join(m.dataDir, "cache", "links", hex.EncodeToString(sum[:8])+".json")
}

func (m *Manager) importLink(ctx context.Context, lib *Library, link string, refresh bool) error {
	path := m.linkCachePath(link)
	var cached *linkCache
	fresh := false
	if data, err := os.ReadFile(path); err == nil {
		var c linkCache
		if json.Unmarshal(data, &c) == nil && len(c.Tracks) > 0 {
			cached = &c
			if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < linkMaxAge {
				fresh = true
			}
		}
	}
	res := cached
	if !fresh || refresh {
		got, err := m.fetchLink(ctx, link)
		switch {
		case err == nil && len(got.Tracks) > 0:
			res = got
			if data, jerr := json.Marshal(got); jerr == nil {
				writeFileAtomic(path, data)
			}
		case cached == nil && err == nil:
			return fmt.Errorf("found no songs at %s", link)
		case cached == nil:
			return err
		default:
			Logf("Couldn't read %s again (%v); using what it held before", link, err)
		}
	}
	lib.playlist(res.Name)
	for _, t := range res.Tracks {
		c := *t
		lib.add(res.Name, &c)
	}
	return nil
}

var (
	reDeezerLink = regexp.MustCompile(`deezer\.com/(?:[a-z]{2}/)?(playlist|album|track)/(\d+)`)
	reAppleAlbum = regexp.MustCompile(`music\.apple\.com/[a-z]{2}/album/[^/]*/(\d+)`)
	reBandcamp   = regexp.MustCompile(`^https?://[^/]+\.bandcamp\.com/(album|track)/`)
)

func (m *Manager) fetchLink(ctx context.Context, link string) (*linkCache, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	switch {
	case reSpotifyLink.MatchString(link):
		tmp := newLibrary()
		if err := importEmbed(ctx, tmp, link, debugf); err != nil {
			return nil, err
		}
		if len(tmp.Playlists) == 0 {
			return nil, fmt.Errorf("found no songs at %s", link)
		}
		return &linkCache{tmp.Playlists[0].Name, tmp.Ordered()}, nil
	case reDeezerLink.MatchString(link):
		return m.deezerLink(ctx, link)
	case reAppleAlbum.MatchString(link):
		id := reAppleAlbum.FindStringSubmatch(link)[1]
		tracks, artist, err := m.catalog.itTracks(ctx, Release{Ref: CatalogRef{provITunes, id}})
		if err != nil {
			return nil, err
		}
		name := "Links"
		if len(tracks) > 0 {
			name = artist + " - " + tracks[0].Album
		}
		return &linkCache{name, tracks}, nil
	case reBandcamp.MatchString(link):
		return m.bandcampLink(ctx, link)
	}
	return m.ytdlpLink(ctx, link)
}

func (m *Manager) deezerLink(ctx context.Context, link string) (*linkCache, error) {
	ma := reDeezerLink.FindStringSubmatch(link)
	kind, id := ma[1], ma[2]
	d := m.deezer
	switch kind {
	case "track":
		var t dzTrack
		if err := d.get(ctx, d.base+"/track/"+id, &t); err != nil {
			return nil, err
		}
		return &linkCache{"Links", []*Track{dzToTrack(t, t.Album, "")}}, nil
	case "album":
		n := int64(0)
		fmt.Sscan(id, &n)
		tracks, al, err := d.albumTracks(ctx, n)
		if err != nil {
			return nil, err
		}
		return &linkCache{al.Artist.Name + " - " + al.Title, tracks}, nil
	}
	var pl struct {
		Title  string `json:"title"`
		Tracks struct {
			Data []dzTrack
			Next string
		} `json:"tracks"`
	}
	if err := d.get(ctx, d.base+"/playlist/"+id, &pl); err != nil {
		return nil, err
	}
	all := pl.Tracks.Data
	next := pl.Tracks.Next
	for next != "" && len(all) < linkMaxEntries {
		var page struct {
			Data []dzTrack
			Next string
		}
		if err := d.get(ctx, next, &page); err != nil {
			break
		}
		all = append(all, page.Data...)
		next = page.Next
	}
	out := make([]*Track, 0, len(all))
	for _, t := range all {
		out = append(out, dzToTrack(t, t.Album, ""))
	}
	return &linkCache{pl.Title, out}, nil
}

func (m *Manager) bandcampLink(ctx context.Context, link string) (*linkCache, error) {
	link = strings.Split(strings.Split(link, "?")[0], "#")[0]
	tracks, artist, err := m.catalog.bcTracks(ctx, Release{Ref: CatalogRef{provBandcamp, link}})
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, fmt.Errorf("found no songs at %s", link)
	}
	if strings.Contains(link, "/track/") && len(tracks) == 1 {
		return &linkCache{"Links", tracks}, nil
	}
	return &linkCache{artist + " - " + tracks[0].Album, tracks}, nil
}

type ytdlpLinkInfo struct {
	flatEntry
	Type     string          `json:"_type"`
	Artist   string          `json:"artist"`
	Track    string          `json:"track"`
	Album    string          `json:"album"`
	Entries  []ytdlpLinkInfo `json:"entries"`
	Playlist string          `json:"playlist_title"`
}

func isYouTubeLink(link string) bool {
	return strings.Contains(link, "youtube.com/") || strings.Contains(link, "youtu.be/")
}

// linkYtDlp is yt-dlp set up for a link's site: its route and, for YouTube, your login.
func (m *Manager) linkYtDlp(link string) *YtDlp {
	bin := m.ytdlpPath()
	if bin == "" {
		return nil
	}
	cfg := m.Config()
	tools := *m.tools
	tools.YtDlp = bin
	route := RouteSystem
	var cookies *ytCookies
	switch {
	case isYouTubeLink(link):
		route = NormalizeRoute(cfg.Source("youtube").Route)
		cookies = m.youtubeCookies(cfg, bin)
	case strings.Contains(link, "soundcloud.com/"):
		route = NormalizeRoute(cfg.Source("soundcloud").Route)
	case strings.Contains(link, "music.yandex."):
		route = NormalizeRoute(cfg.Source("yandex").Route)
	case strings.Contains(link, "vk.com/"):
		route = NormalizeRoute(cfg.Source("vk").Route)
	}
	return newYtDlp(&tools, cookies, m.proxy.URL(route))
}

func (m *Manager) ytdlpLink(ctx context.Context, link string) (*linkCache, error) {
	y := m.linkYtDlp(link)
	if y == nil {
		return nil, fmt.Errorf("yt-dlp is needed to read that link and isn't available")
	}
	args := []string{"-J", "--ignore-errors", "--playlist-end", fmt.Sprint(linkMaxEntries)}
	if isYouTubeLink(link) {
		args = append(args, "--flat-playlist") // full details of each video would take minutes
	}
	out, err := y.run(ctx, append(args, link)...)
	if err != nil {
		return nil, err
	}
	var top ytdlpLinkInfo
	if err := json.Unmarshal(out, &top); err != nil {
		return nil, fmt.Errorf("couldn't read what yt-dlp said about that link")
	}
	var tracks []*Track
	if len(top.Entries) == 0 && top.Title != "" {
		if t := trackFromEntry(top, "", link); t != nil {
			return &linkCache{"Links", []*Track{t}}, nil
		}
		return nil, fmt.Errorf("found no songs at %s", link)
	}
	var walk func(list []ytdlpLinkInfo, album string)
	walk = func(list []ytdlpLinkInfo, album string) {
		for _, e := range list {
			if len(e.Entries) > 0 {
				walk(e.Entries, album)
				continue
			}
			if t := trackFromEntry(e, album, link); t != nil && len(tracks) < linkMaxEntries {
				tracks = append(tracks, t)
			}
		}
	}
	album := ""
	if top.Type == "album" {
		album = top.Title
	}
	walk(top.Entries, album)
	if len(tracks) == 0 {
		return nil, fmt.Errorf("found no songs at %s", link)
	}
	return &linkCache{FirstNonEmpty(top.Title, top.Playlist, top.Uploader, top.Channel, hostOf(link)), tracks}, nil
}

func hostOf(link string) string {
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Host, "www.")
	}
	return "Links"
}

// reUploadNoise is a bracket holding only the kind of thing uploads add to a title.
var reUploadNoise = regexp.MustCompile(`(?i)\s*[\(\[【][^\)\]】]*\b(?:official|video|audio|lyrics?|visuali[sz]er|hd|hq|4k|mv|m/v|full album)\b[^\)\]】]*[\)\]】]`)

// reSeparator is "Artist - Title", also written with a long dash or without one of the spaces.
var reSeparator = regexp.MustCompile(`\s[-–—]\s|[-–—]\s|\s[-–—]`)

func trackFromEntry(e ytdlpLinkInfo, album, page string) *Track {
	title := strings.TrimSpace(FirstNonEmpty(e.Track, e.Title))
	if title == "" || title == "[Deleted video]" || title == "[Private video]" || e.LiveStatus == "is_live" {
		return nil
	}
	artist := strings.TrimSpace(e.Artist)
	if artist == "" {
		clean := strings.TrimSpace(reUploadNoise.ReplaceAllString(title, ""))
		if loc := reSeparator.FindStringIndex(clean); loc != nil && loc[0] > 0 && loc[1] < len(clean) {
			artist, title = strings.TrimSpace(clean[:loc[0]]), strings.TrimSpace(clean[loc[1]:])
		} else {
			title = clean
		}
	}
	if artist == "" {
		artist = strings.TrimSpace(reChannelNoise.ReplaceAllString(FirstNonEmpty(e.Uploader, e.Channel), ""))
	}
	if artist == "" {
		return nil
	}
	if title == "" {
		return nil
	}
	src := FirstNonEmpty(e.WebpageURL, e.URL)
	if src == "" && e.ID != "" {
		if isYouTubeLink(page) {
			src = "https://www.youtube.com/watch?v=" + e.ID
		} else {
			src = page
		}
	}
	return &Track{Key: trackKey("", artist, title), Title: title, Artists: []string{artist}, Album: FirstNonEmpty(album, e.Album),
		DurationMs: int(e.Duration * 1000), SourceURL: src}
}

// linkedSrc downloads a song from the link it was added with.
type linkedSrc struct {
	Source // Download: Bandcamp or the yt-dlp below
	cand   Candidate
	label  string
}

func (s *linkedSrc) Name() string  { return linkSourceName }
func (s *linkedSrc) Label() string { return s.label }

func (s *linkedSrc) Search(context.Context, *Track) ([]Candidate, error) {
	return []Candidate{s.cand}, nil
}

// ytdlpFetcher is the Source that fetches a single page with yt-dlp.
type ytdlpFetcher struct {
	y    *YtDlp
	name string
}

func (s *ytdlpFetcher) Name() string  { return s.name }
func (s *ytdlpFetcher) Label() string { return s.name }

func (s *ytdlpFetcher) Search(context.Context, *Track) ([]Candidate, error) { return nil, nil }

func (s *ytdlpFetcher) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	return s.y.download(ctx, c.URL, workdir)
}

// linkSites are the sources a link's own site is searched through: a song added as a YouTube
// link isn't looked for on YouTube or YouTube Music, a SoundCloud link's on SoundCloud, and so
// on. The link is the copy you chose; searching its site would only turn up others.
func linkSites(link string) []string {
	switch {
	case link == "":
		return nil
	case isYouTubeLink(link):
		return []string{"youtube", "youtube_music"}
	case strings.Contains(link, "soundcloud.com/"):
		return []string{"soundcloud"}
	case strings.Contains(link, ".bandcamp.com/"):
		return []string{"bandcamp"}
	}
	return nil
}

// linkedSource is what to fetch a link's song with, or nil when nothing can right now; wait
// says the link's site is only paused for now (it's tried again later, not given up on).
func (m *Manager) linkedSource(t *Track) (src Source, wait bool) {
	link := t.SourceURL
	cand := Candidate{URL: link, Title: t.Title, Artist: t.ArtistStr(), Album: t.Album, Duration: t.Duration(), ArtistIsCredit: true}
	var base Source
	label := hostOf(link)
	switch {
	case strings.Contains(link, ".bandcamp.com/"):
		cand.Source, label = "bandcamp", "Bandcamp"
		for _, s := range m.currentSources() {
			if s.Name() == "bandcamp" {
				base = s
				cand.Quality = s.(qualityCapper).MaxQuality()
			}
		}
	case isYouTubeLink(link):
		cand.Source, cand.Quality, label = "youtube", youtubeQuality, "YouTube"
	case strings.Contains(link, "soundcloud.com/"):
		cand.Source, label = "soundcloud", "SoundCloud"
	default:
		cand.Source = "link"
	}
	if cool, _, _ := m.cooling(cand.Source); cool && cand.Source != "link" {
		return nil, true
	}
	if base == nil {
		y := m.linkYtDlp(link)
		if y == nil {
			return nil, false
		}
		base = &ytdlpFetcher{y, "link"}
	}
	return &linkedSrc{base, cand, linkLabelPrefix + label + ")"}, false
}

// linkLabelPrefix starts the name the link goes by in a track's report.
const linkLabelPrefix = "the link you added ("
