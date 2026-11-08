package core

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Reading the track list without the Spotify Web API (which needs Premium for your own app):
//   - Spotify's own data export (Account privacy → Download your data → Account data),
//   - CSVs from Exportify, TuneMyMusic and similar tools,
//   - plain text files with one "Artist - Title" per line,
//   - links to public playlists/albums (Spotify's embed page shows at most 100 tracks).

const likedSongs = "Liked Songs"

var (
	reExportPlaylist = regexp.MustCompile(`(?i)^playlist\d*\.json$`)
	reSpotifyLink    = regexp.MustCompile(`(?:open\.spotify\.com/(?:intl-[a-z]+/)?|spotify:)(playlist|album|track)[/:]([A-Za-z0-9]{22})`)
	reSpotifyID      = regexp.MustCompile(`(?:spotify:track:|open\.spotify\.com/(?:intl-[a-z]+/)?track/)([A-Za-z0-9]{22})|^([A-Za-z0-9]{22})$`)
	reNextData       = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__" type="application/json">(.*?)</script>`)
)

func spotifyTrackID(s string) string {
	if m := reSpotifyID.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return m[1] + m[2]
	}
	return ""
}

func spotifyTrackURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://open.spotify.com/track/" + id
}

// importInput reads one --from argument (file, folder, zip or link) into the library.
// importInput reads a playlist file, folder, zip or link into lib; res (nil: offline) looks up
// the lines of plain-text lists.
func importInput(ctx context.Context, lib *Library, src string, res *Resolver, logf func(string, ...any)) error {
	if reSpotifyLink.MatchString(src) {
		return importEmbed(ctx, lib, src, logf)
	}
	src = ExpandHome(src)
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("can't read %s: %w", src, err)
	}
	found := 0
	visit := func(name string, open func() (io.ReadCloser, error)) error {
		ok, err := importFile(ctx, lib, name, open, res, logf)
		if ok {
			found++
		}
		return err
	}
	switch {
	case info.IsDir():
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.EqualFold(filepath.Ext(p), ".zip") {
				return importZip(p, visit)
			}
			return visit(p, func() (io.ReadCloser, error) { return os.Open(p) })
		})
	case strings.EqualFold(filepath.Ext(src), ".zip"):
		err = importZip(src, visit)
	default:
		err = visit(src, func() (io.ReadCloser, error) { return os.Open(src) })
	}
	if err != nil {
		return err
	}
	if found == 0 {
		return fmt.Errorf("found nothing usable in %s (expected Spotify export JSON, CSV or TXT files)", src)
	}
	return nil
}

func importZip(p string, visit func(string, func() (io.ReadCloser, error)) error) error {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return fmt.Errorf("can't open %s: %w", p, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if err := visit(f.Name, f.Open); err != nil {
			return err
		}
	}
	return nil
}

func importFile(ctx context.Context, lib *Library, name string, open func() (io.ReadCloser, error), res *Resolver, logf func(string, ...any)) (bool, error) {
	base := filepath.Base(filepath.ToSlash(name))
	ext := strings.ToLower(filepath.Ext(base))
	var parse func(io.Reader) error
	switch {
	case reExportPlaylist.MatchString(base):
		parse = func(r io.Reader) error { return importExportPlaylists(lib, r) }
	case strings.EqualFold(base, "YourLibrary.json"):
		parse = func(r io.Reader) error { return importExportLibrary(lib, r) }
	case ext == ".csv":
		parse = func(r io.Reader) error { return importCSV(lib, strings.TrimSuffix(base, filepath.Ext(base)), r) }
	case ext == ".txt":
		parse = func(r io.Reader) error {
			return importText(ctx, lib, strings.TrimSuffix(base, filepath.Ext(base)), r, res, logf)
		}
	default:
		return false, nil
	}
	rc, err := open()
	if err != nil {
		return false, err
	}
	defer rc.Close()
	if err := parse(rc); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	logf("  read %s", name)
	return true, nil
}

// --- Spotify data export ---------------------------------------------------------------

type exportTrack struct {
	TrackName  string `json:"trackName"`
	ArtistName string `json:"artistName"`
	AlbumName  string `json:"albumName"`
	TrackURI   string `json:"trackUri"`
}

func importExportPlaylists(lib *Library, r io.Reader) error {
	var data struct {
		Playlists []struct {
			Name  string `json:"name"`
			Items []struct {
				Track   *exportTrack `json:"track"`
				Episode *struct {
					EpisodeName string `json:"episodeName"`
					ShowName    string `json:"showName"`
				} `json:"episode"`
				Audiobook  json.RawMessage `json:"audiobook"`
				LocalTrack *struct {
					URI string `json:"uri"`
				} `json:"localTrack"`
			} `json:"items"`
		} `json:"playlists"`
	}
	if err := json.NewDecoder(r).Decode(&data); err != nil {
		return fmt.Errorf("not a Spotify playlist export: %w", err)
	}
	for _, p := range data.Playlists {
		lib.playlist(p.Name)
		for _, it := range p.Items {
			switch {
			case it.Track != nil && it.Track.TrackName != "":
				lib.add(p.Name, fromExportTrack(it.Track))
			case it.LocalTrack != nil:
				if t := parseLocalURI(it.LocalTrack.URI); t != nil {
					lib.add(p.Name, t)
				}
			case it.Episode != nil:
				lib.Skipped = append(lib.Skipped, Skipped{it.Episode.EpisodeName, it.Episode.ShowName, "podcast episode, not a song", p.Name})
			case len(it.Audiobook) > 0 && string(it.Audiobook) != "null":
				lib.Skipped = append(lib.Skipped, Skipped{"(audiobook)", "", "audiobook, not a song", p.Name})
			}
		}
	}
	return nil
}

func fromExportTrack(e *exportTrack) *Track {
	id := spotifyTrackID(e.TrackURI)
	return &Track{
		Key:        trackKey(id, e.ArtistName, e.TrackName),
		Title:      e.TrackName,
		Artists:    []string{e.ArtistName},
		Album:      e.AlbumName,
		SpotifyURL: spotifyTrackURL(id),
	}
}

func importExportLibrary(lib *Library, r io.Reader) error {
	var data struct {
		Tracks []struct {
			Artist string `json:"artist"`
			Album  string `json:"album"`
			Track  string `json:"track"`
			URI    string `json:"uri"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(r).Decode(&data); err != nil {
		return fmt.Errorf("not a Spotify library export: %w", err)
	}
	lib.playlist(likedSongs)
	for _, t := range data.Tracks {
		if t.Track != "" {
			lib.add(likedSongs, fromExportTrack(&exportTrack{t.Track, t.Artist, t.Album, t.URI}))
		}
	}
	return nil
}

// parseLocalURI reads "spotify:local:Artist:Album:Title:Seconds" (parts are URL-encoded).
func parseLocalURI(uri string) *Track {
	parts := strings.SplitN(strings.TrimPrefix(uri, "spotify:local:"), ":", 4)
	if len(parts) < 3 {
		return nil
	}
	for i, p := range parts {
		if u, err := url.QueryUnescape(p); err == nil {
			parts[i] = u
		}
	}
	if parts[2] == "" {
		return nil
	}
	t := &Track{Title: parts[2], Artists: []string{parts[0]}, Album: parts[1], IsLocal: true}
	if len(parts) == 4 {
		if s, err := strconv.Atoi(parts[3]); err == nil {
			t.DurationMs = s * 1000
		}
	}
	t.Key = "local-" + strings.TrimPrefix(trackKey("", parts[0], parts[2]), "x-")
	return t
}

// --- CSV (Exportify, TuneMyMusic, ...) ---------------------------------------------------

var csvColumns = map[string][]string{
	"uri":          {"track uri", "spotify uri", "uri", "spotify - id", "spotify id", "track id", "spotify track id"},
	"title":        {"track name", "title", "name", "track", "song", "song name", "track title"},
	"artists":      {"artist name(s)", "artist name", "artist names", "artists", "artist"},
	"artist_uris":  {"artist uri(s)"},
	"album":        {"album name", "album", "album title"},
	"album_artist": {"album artist name(s)", "album artist", "album artist name"},
	"duration_ms":  {"duration (ms)", "track duration (ms)", "duration_ms"},
	"duration":     {"duration", "length", "time"},
	"isrc":         {"isrc"},
	"release":      {"release date", "album release date", "year"},
	"track_number": {"track number", "track no", "track #", "tracknumber"},
	"disc_number":  {"disc number", "disc", "disc no"},
	"cover":        {"album image url", "cover", "image url"},
	"playlist":     {"playlist name", "playlist"},
	"type":         {"type"},
}

func importCSV(lib *Library, defaultPlaylist string, r io.Reader) error {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("empty or unreadable CSV: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		for field, names := range csvColumns {
			for _, n := range names {
				if _, taken := col[field]; h == n && !taken {
					col[field] = i
				}
			}
		}
	}
	if _, ok := col["title"]; !ok {
		return fmt.Errorf("no track name column in CSV header %q", header)
	}
	if _, ok := col["artists"]; !ok {
		return fmt.Errorf("no artist column in CSV header %q", header)
	}
	lib.playlist(defaultPlaylist)
	rows, err := cr.ReadAll()
	if err != nil {
		return err
	}
	// Exportify separates artists with ";" these days, and with "," before; names like
	// "Woe, Is Me" are why ";" wins when it's there.
	sep := map[string]string{}
	for _, field := range []string{"artists", "artist_uris", "album_artist"} {
		sep[field] = ","
		if i, ok := col[field]; ok {
			for _, row := range rows {
				if i < len(row) && strings.Contains(row[i], ";") {
					sep[field] = ";"
					break
				}
			}
		}
	}
	for _, row := range rows {
		get := func(field string) string {
			if i, ok := col[field]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		title := strings.Clone(get("title"))
		artists := SplitBy(get("artists"), sep["artists"])
		for i, a := range artists {
			artists[i] = lib.text(a)
		}
		if title == "" || len(artists) == 0 {
			continue
		}
		playlist := defaultPlaylist
		if p := get("playlist"); p != "" {
			playlist = p
		}
		if typ := strings.ToLower(get("type")); typ == "episode" || typ == "podcast" {
			lib.Skipped = append(lib.Skipped, Skipped{title, strings.Join(artists, ", "), "podcast episode, not a song", playlist})
			continue
		}
		id := spotifyTrackID(get("uri"))
		t := &Track{
			Key:         strings.Clone(trackKey(id, artists[0], title)),
			Title:       title,
			Artists:     artists,
			Album:       lib.text(get("album")),
			AlbumArtist: lib.text(strings.Join(SplitBy(get("album_artist"), sep["album_artist"]), ", ")),
			ISRC:        strings.Clone(get("isrc")),
			ReleaseDate: lib.text(get("release")),
			CoverURL:    lib.text(get("cover")),
			SpotifyURL:  spotifyTrackURL(id),
		}
		t.TrackNumber, _ = strconv.Atoi(get("track_number"))
		t.DiscNumber, _ = strconv.Atoi(get("disc_number"))
		if ms, err := strconv.Atoi(get("duration_ms")); err == nil {
			t.DurationMs = ms
		} else {
			t.DurationMs = parseDurationMs(get("duration"))
		}
		if uris := SplitBy(get("artist_uris"), sep["artist_uris"]); len(uris) == len(artists) {
			t.ArtistURLs = map[string]string{}
			for i, u := range uris {
				if id := strings.TrimPrefix(u, "spotify:artist:"); id != u {
					t.ArtistURLs[artists[i]] = lib.text("https://open.spotify.com/artist/" + id)
				}
			}
		}
		lib.add(playlist, t)
	}
	return nil
}

func SplitList(s string) []string { return SplitBy(s, ",") }

func SplitBy(s, sep string) []string {
	var out []string
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDurationMs accepts "3:25", "1:02:03", seconds or milliseconds.
func parseDurationMs(s string) int {
	if s == "" {
		return 0
	}
	if strings.Contains(s, ":") {
		total := 0.0
		for _, p := range strings.Split(s, ":") {
			v, err := strconv.ParseFloat(p, 64)
			if err != nil {
				return 0
			}
			total = total*60 + v
		}
		return int(total * 1000)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	if v > 10000 {
		return int(v)
	}
	return int(v * 1000)
}

// --- Plain text --------------------------------------------------------------------------

// importText reads a plain-text list: one track, album or artist per line ("Artist - Title",
// "Title - Artist", "Artist - Album", "Artist"; "track:", "album:" or "artist:" in front says
// which). An album brings all its tracks, an artist the whole discography. Without the
// catalog (res nil, or unreachable) a line is taken as "Artist - Title".
func importText(ctx context.Context, lib *Library, playlist string, r io.Reader, res *Resolver, logf func(string, ...any)) error {
	lib.playlist(playlist)
	sc := bufio.NewScanner(r)
	n, looked := 0, 0
	offline := res == nil
	for sc.Scan() {
		n++
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if !offline {
			tracks, label, ok, err := res.Resolve(ctx, line)
			switch {
			case err != nil && ctx.Err() != nil:
				return ctx.Err()
			case err != nil:
				logf("  %s: the music catalog can't be reached (%v); the rest is read as \"Artist - Title\"", playlist, err)
				offline = true
			case ok:
				for _, t := range tracks {
					c := *t
					lib.add(playlist, &c)
				}
				if looked++; looked%25 == 0 {
					logf("  %s: %d lines looked up", playlist, looked)
				}
				if len(tracks) > 1 {
					logf("  %s line %d: %s, %d tracks", playlist, n, label, len(tracks))
				}
				continue
			}
		}
		q := parseMusicQuery(line)
		if q.b == "" || q.kind == "album" || q.kind == "artist" {
			logf("  %s line %d: nothing in the catalog fits, skipped: %s", playlist, n, line)
			continue
		}
		lib.add(playlist, &Track{Key: trackKey("", q.a, q.b), Title: q.b, Artists: []string{q.a}})
	}
	if res != nil {
		res.save()
	}
	return sc.Err()
}

// --- Public links via Spotify's embed page -------------------------------------------------

func importEmbed(ctx context.Context, lib *Library, link string, logf func(string, ...any)) error {
	m := reSpotifyLink.FindStringSubmatch(link)
	kind, id := m[1], m[2]
	h := newHTTP("Spotify", 300*time.Millisecond)
	status, body, err := h.get(ctx, "https://open.spotify.com/embed/"+kind+"/"+id)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("Spotify answered HTTP %d for %s (is it public?)", status, link)
	}
	nd := reNextData.FindSubmatch(body)
	if nd == nil {
		return fmt.Errorf("couldn't read the track list of %s", link)
	}
	var data struct {
		Props struct {
			PageProps struct {
				State struct {
					Data struct {
						Entity struct {
							Name      string `json:"name"`
							Subtitle  string `json:"subtitle"`
							TrackList []struct {
								URI      string `json:"uri"`
								Title    string `json:"title"`
								Subtitle string `json:"subtitle"`
								Duration int    `json:"duration"`
							} `json:"trackList"`
						} `json:"entity"`
					} `json:"data"`
				} `json:"state"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(nd[1], &data); err != nil {
		return fmt.Errorf("couldn't read the track list of %s: %w", link, err)
	}
	e := data.Props.PageProps.State.Data.Entity
	name := html.UnescapeString(e.Name)
	if kind == "track" {
		name = "Links"
	}
	lib.playlist(name)
	for _, it := range e.TrackList {
		tid := spotifyTrackID(it.URI)
		artists := SplitList(html.UnescapeString(strings.ReplaceAll(it.Subtitle, " ", " ")))
		if len(artists) == 0 {
			continue
		}
		t := &Track{Key: trackKey(tid, artists[0], it.Title), Title: it.Title, Artists: artists,
			DurationMs: it.Duration, SpotifyURL: spotifyTrackURL(tid)}
		if kind == "album" {
			t.Album, t.AlbumArtist = name, html.UnescapeString(e.Subtitle)
		}
		lib.add(name, t)
	}
	logf("  read %q from Spotify: %d tracks", name, len(e.TrackList))
	if kind == "playlist" && len(e.TrackList) >= 100 {
		logf("  warning: Spotify only shows the first 100 tracks of a playlist this way; use the data export for the rest of %q", name)
	}
	return nil
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
