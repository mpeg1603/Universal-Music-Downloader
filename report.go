package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The three lists (artists, downloaded tracks, failed tracks) and local .m3u8 playlists.

var failureCategories = []struct{ key, text string }{
	{"not_found", "No acceptable match on any source"},
	{"source_error", "A source was unreachable or errored (retried automatically on the next run)"},
	{"missing_file", "Downloaded earlier but the file is gone (downloaded again on the next run)"},
	{"pending", "Not attempted yet (run interrupted or limited with --limit)"},
	{"skipped", "Not a song (podcast, audiobook) or no longer on Spotify"},
}

type downloadedEntry struct {
	Track *Track
	Rec   Record
}

type failedEntry struct {
	Artists, Title, Album, SpotifyURL, Category, Reason string
}

type artistEntry struct {
	Name   string
	Tracks int
	Links  []link
}

type link struct{ Label, URL string }

type Report struct {
	Downloaded []downloadedEntry
	Failed     []failedEntry
	Artists    []*artistEntry
}

func buildReport(lib *Library, recs map[string]Record, musicDir string) *Report {
	rep := &Report{}
	for _, t := range lib.ordered() {
		r, ok := recs[t.Key]
		var cat, reason string
		switch {
		case ok && r.Status == "downloaded":
			if _, err := os.Stat(filepath.Join(musicDir, filepath.FromSlash(r.Path))); err == nil {
				rep.Downloaded = append(rep.Downloaded, downloadedEntry{t, r})
				continue
			}
			cat, reason = "missing_file", r.Path+" no longer exists"
		case !ok:
			cat, reason = "pending", "not attempted yet"
		case r.Status == "error":
			cat, reason = "source_error", r.Reason
		default:
			cat, reason = "not_found", r.Reason
		}
		if t.IsLocal {
			reason = "(local file in your Spotify playlist) " + reason
		}
		rep.Failed = append(rep.Failed, failedEntry{t.ArtistStr(), t.Title, t.Album, t.SpotifyURL, cat, reason})
	}
	for _, s := range lib.Skipped {
		rep.Failed = append(rep.Failed, failedEntry{s.Artists, s.Title, "", "", "skipped", fmt.Sprintf("%s (in “%s”)", s.Reason, s.Playlist)})
	}
	sort.SliceStable(rep.Downloaded, func(i, j int) bool {
		a, b := rep.Downloaded[i].Track, rep.Downloaded[j].Track
		if x, y := strings.ToLower(a.ArtistStr()), strings.ToLower(b.ArtistStr()); x != y {
			return x < y
		}
		if x, y := strings.ToLower(a.Album), strings.ToLower(b.Album); x != y {
			return x < y
		}
		if a.DiscNumber != b.DiscNumber {
			return a.DiscNumber < b.DiscNumber
		}
		return a.TrackNumber < b.TrackNumber
	})
	order := map[string]int{}
	for i, c := range failureCategories {
		order[c.key] = i
	}
	sort.SliceStable(rep.Failed, func(i, j int) bool {
		a, b := rep.Failed[i], rep.Failed[j]
		if order[a.Category] != order[b.Category] {
			return order[a.Category] < order[b.Category]
		}
		return strings.ToLower(a.Artists+a.Title) < strings.ToLower(b.Artists+b.Title)
	})
	rep.Artists = collectArtists(rep.Downloaded)
	return rep
}

func collectArtists(downloaded []downloadedEntry) []*artistEntry {
	byName := map[string]*artistEntry{}
	for _, d := range downloaded {
		for i, name := range d.Track.Artists {
			key := strings.ToLower(name)
			a := byName[key]
			if a == nil {
				a = &artistEntry{Name: name}
				byName[key] = a
				if u := d.Track.ArtistURLs[name]; u != "" {
					a.Links = append(a.Links, link{"Spotify", u})
				} else {
					a.Links = append(a.Links, link{"Spotify (search)", "https://open.spotify.com/search/" + url.PathEscape(name) + "/artists"})
				}
			}
			a.Tracks++
			if i == 0 { // links found while downloading belong to the main artist
				a.addLink("Deezer", d.Track.DeezerArtistURL)
				a.addLink(sourceLabels[d.Rec.Source], d.Rec.SourceArtistURL)
			}
		}
	}
	var out []*artistEntry
	for _, a := range byName {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (a *artistEntry) addLink(label, u string) {
	if u == "" {
		return
	}
	for _, l := range a.Links {
		if l.URL == u {
			return
		}
	}
	a.Links = append(a.Links, link{label, u})
}

func mdEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "|", `\|`, "\n", " ").Replace(s)
}

func mdLink(label, u string) string {
	if u == "" {
		return mdEscape(label)
	}
	label = strings.NewReplacer("[", `\[`, "]", `\]`).Replace(mdEscape(label))
	return "[" + label + "](" + strings.NewReplacer(" ", "%20", ")", "%29").Replace(u) + ")"
}

func writeCSV(path string, header []string, rows [][]string) error {
	var buf bytes.Buffer
	buf.WriteString("\uFEFF") // so Excel reads it as UTF-8
	w := csv.NewWriter(&buf)
	w.Write(header)
	w.WriteAll(rows)
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func writeReports(rep *Report, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stamp := "generated " + time.Now().Format("2006-01-02 15:04")
	total := len(rep.Downloaded) + len(rep.Failed)

	var b strings.Builder
	fmt.Fprintf(&b, "# Downloaded artists\n\n%s · %d artists across %d downloaded tracks\n\n", stamp, len(rep.Artists), len(rep.Downloaded))
	b.WriteString("| Artist | Tracks | Links |\n|---|---:|---|\n")
	var rows [][]string
	for _, a := range rep.Artists {
		var md, urls []string
		for _, l := range a.Links {
			md = append(md, mdLink(l.Label, l.URL))
			urls = append(urls, l.URL)
		}
		fmt.Fprintf(&b, "| %s | %d | %s |\n", mdEscape(a.Name), a.Tracks, strings.Join(md, " · "))
		rows = append(rows, []string{a.Name, fmt.Sprint(a.Tracks), strings.Join(urls, " ")})
	}
	files := []string{filepath.Join(dir, "artists.md"), filepath.Join(dir, "downloaded.md"), filepath.Join(dir, "failed.md")}
	if err := os.WriteFile(files[0], []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	writeCSV(filepath.Join(dir, "artists.csv"), []string{"artist", "tracks_downloaded", "links"}, rows)

	b.Reset()
	rows = nil
	fmt.Fprintf(&b, "# Downloaded tracks\n\n%s · %d of %d tracks downloaded\n\n", stamp, len(rep.Downloaded), total)
	b.WriteString("| # | Artist | Title | Album | Source | File |\n|---:|---|---|---|---|---|\n")
	for i, d := range rep.Downloaded {
		t, r := d.Track, d.Rec
		label := sourceLabels[r.Source]
		if r.Source == "torrent" && r.SourceTitle != "" {
			label = "Torrent: " + r.SourceTitle
		}
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | `%s` |\n", i+1, mdEscape(t.ArtistStr()), mdLink(t.Title, t.SpotifyURL),
			mdEscape(t.Album), mdLink(label, r.SourceURL), strings.ReplaceAll(r.Path, "`", "'"))
		rows = append(rows, []string{t.ArtistStr(), t.Title, t.Album, label, r.SourceURL, r.Path, t.SpotifyURL, t.ISRC})
	}
	if err := os.WriteFile(files[1], []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	writeCSV(filepath.Join(dir, "downloaded.csv"), []string{"artists", "title", "album", "source", "source_url", "file", "spotify_url", "isrc"}, rows)

	b.Reset()
	rows = nil
	fmt.Fprintf(&b, "# Tracks not downloaded\n\n%s · %d of %d entries\n\n", stamp, len(rep.Failed), total)
	for _, c := range failureCategories {
		n := 0
		for _, f := range rep.Failed {
			if f.Category == c.key {
				n++
			}
		}
		if n > 0 {
			fmt.Fprintf(&b, "- **%d** · %s\n", n, c.text)
		}
	}
	b.WriteString("\n| # | Artist | Title | Album | Why |\n|---:|---|---|---|---|\n")
	for i, f := range rep.Failed {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s |\n", i+1, mdEscape(f.Artists), mdLink(f.Title, f.SpotifyURL), mdEscape(f.Album), mdEscape(f.Reason))
		rows = append(rows, []string{f.Artists, f.Title, f.Album, f.Category, f.Reason, f.SpotifyURL})
	}
	if err := os.WriteFile(files[2], []byte(b.String()), 0o644); err != nil {
		return nil, err
	}
	writeCSV(filepath.Join(dir, "failed.csv"), []string{"artists", "title", "album", "category", "reason", "spotify_url"}, rows)
	return files, nil
}

// writePlaylists writes one .m3u8 per playlist (and Liked Songs) listing the downloaded tracks.
func writePlaylists(lib *Library, recs map[string]Record, musicDir string) (int, error) {
	dir := filepath.Join(musicDir, "Playlists")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	used := map[string]bool{}
	for _, p := range lib.Playlists {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#PLAYLIST:" + p.Name + "\n")
		for _, k := range p.Keys {
			r, ok := recs[k]
			if !ok || r.Status != "downloaded" {
				continue
			}
			full := filepath.Join(musicDir, filepath.FromSlash(r.Path))
			if _, err := os.Stat(full); err != nil {
				continue
			}
			t := lib.Tracks[k]
			rel, _ := filepath.Rel(dir, full)
			fmt.Fprintf(&b, "#EXTINF:%d,%s - %s\n%s\n", t.DurationMs/1000, t.ArtistStr(), t.Title, filepath.ToSlash(rel))
		}
		name := sanitize(p.Name)
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s (%d)", sanitize(p.Name), n)
		}
		used[strings.ToLower(name)] = true
		if err := os.WriteFile(filepath.Join(dir, name+".m3u8"), []byte(b.String()), 0o644); err != nil {
			return 0, err
		}
	}
	return len(lib.Playlists), nil
}
