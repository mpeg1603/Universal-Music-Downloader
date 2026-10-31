package main

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Internet Archive, limited to items published under a license (Creative Commons, public
// domain). Live concert tapes (the etree collection) are left out: they're not the studio
// recordings on Spotify.

var (
	archiveLossless = []string{"Flac", "24bit Flac"}
	archiveLossy    = []string{"VBR MP3", "320Kbps MP3", "256Kbps MP3", "MP3", "Ogg Vorbis", "128Kbps MP3", "64Kbps MP3"}
)

type Archive struct {
	http *HTTP
	rank map[string]int // file format -> preference (lower is better)
}

func newArchive(format string) *Archive {
	order := append(append([]string{}, archiveLossy...), archiveLossless...)
	if format == "flac" || format == "original" || format == "opus" {
		order = append(append([]string{}, archiveLossless...), archiveLossy...)
	}
	rank := map[string]int{}
	for i, f := range order {
		rank[f] = i
	}
	return &Archive{http: newHTTP("Internet Archive", 300*time.Millisecond), rank: rank}
}

func (a *Archive) Name() string  { return "archive" }
func (a *Archive) Label() string { return "Internet Archive" }

func lucenePhrase(s string) string {
	return strings.TrimSpace(strings.NewReplacer(`\`, " ", `"`, " ").Replace(s))
}

// jsonText accepts a JSON string or a list of strings (Archive metadata uses both).
type jsonText string

func (j *jsonText) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*j = jsonText(s)
		return nil
	}
	var list []string
	if json.Unmarshal(b, &list) == nil && len(list) > 0 {
		*j = jsonText(list[0])
	}
	var num float64
	if json.Unmarshal(b, &num) == nil {
		*j = jsonText(strconv.FormatFloat(num, 'f', -1, 64))
	}
	return nil
}

func (a *Archive) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	q := `mediatype:audio AND licenseurl:* AND NOT collection:etree` +
		` AND creator:("` + lucenePhrase(t.Artists[0]) + `") AND ("` + lucenePhrase(searchTitle(t.Title)) + `")`
	var res struct {
		Response struct {
			Docs []struct {
				Identifier string `json:"identifier"`
			} `json:"docs"`
		} `json:"response"`
	}
	u := "https://archive.org/advancedsearch.php?" + url.Values{"q": {q}, "fl[]": {"identifier"}, "rows": {"3"}, "output": {"json"}}.Encode()
	if err := a.http.getJSON(ctx, u, &res); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, d := range res.Response.Docs {
		cands, err := a.itemCandidates(ctx, d.Identifier)
		if err != nil {
			return nil, err
		}
		out = append(out, cands...)
	}
	return out, nil
}

type archiveFile struct {
	Name     string   `json:"name"`
	Format   string   `json:"format"`
	Source   string   `json:"source"`
	Original string   `json:"original"`
	Title    jsonText `json:"title"`
	Creator  jsonText `json:"creator"`
	Album    jsonText `json:"album"`
	Length   jsonText `json:"length"`
}

func (a *Archive) itemCandidates(ctx context.Context, ident string) ([]Candidate, error) {
	var meta struct {
		Metadata struct {
			Title   jsonText `json:"title"`
			Creator jsonText `json:"creator"`
		} `json:"metadata"`
		Files []archiveFile `json:"files"`
	}
	if err := a.http.getJSON(ctx, "https://archive.org/metadata/"+url.PathEscape(ident), &meta); err != nil {
		return nil, err
	}
	byName := map[string]archiveFile{}
	for _, f := range meta.Files {
		byName[f.Name] = f
	}
	// One file per recording: derivatives (e.g. MP3s made from a FLAC) are grouped with
	// their original and the preferred format wins.
	best := map[string]archiveFile{}
	var groups []string
	for _, f := range meta.Files {
		r, ok := a.rank[f.Format]
		if !ok {
			continue
		}
		group := f.Name
		if f.Source == "derivative" && f.Original != "" {
			group = f.Original
		}
		cur, seen := best[group]
		if !seen {
			groups = append(groups, group)
		}
		if !seen || r < a.rank[cur.Format] {
			best[group] = f
		}
	}
	page := "https://archive.org/details/" + ident
	var out []Candidate
	for _, g := range groups {
		f := best[g]
		info := f
		if info.Title == "" {
			if orig, ok := byName[g]; ok {
				info = orig
			}
		}
		length := float64(parseDurationMs(string(firstText(info.Length, f.Length)))) / 1000
		out = append(out, Candidate{
			Source:         "archive",
			URL:            "https://archive.org/download/" + url.PathEscape(ident) + "/" + escapePath(f.Name),
			Title:          firstNonEmpty(string(info.Title), strings.TrimSuffix(path.Base(f.Name), path.Ext(f.Name))),
			Artist:         firstNonEmpty(string(info.Creator), string(meta.Metadata.Creator)),
			Album:          firstNonEmpty(string(info.Album), string(meta.Metadata.Title)),
			Duration:       length,
			ArtistURL:      page,
			ArtistIsCredit: true,
		})
	}
	return out, nil
}

func firstText(values ...jsonText) jsonText {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func (a *Archive) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	return a.http.download(ctx, c.URL, workdir, "archive")
}
