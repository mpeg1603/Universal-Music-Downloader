package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Filling in what the track list lacks (length, album, cover, track number, ISRC) from
// Deezer's free public API. Spotify's data export, for instance, has no track lengths, and
// the length check is what keeps wrong versions out.

type deezerTrack struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Duration int    `json:"duration"`
	Artist   struct {
		Name string `json:"name"`
		Link string `json:"link"`
	} `json:"artist"`
	Album struct {
		Title   string `json:"title"`
		CoverXL string `json:"cover_xl"`
	} `json:"album"`
}

type deezerError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func needsEnrichment(t *Track) bool {
	return !t.Enriched && (t.DurationMs == 0 || t.Album == "" || t.CoverURL == "" || t.TrackNumber == 0)
}

type Deezer struct{ http *HTTP }

func newDeezer() *Deezer {
	// Deezer allows 50 requests per 5 seconds.
	return &Deezer{newHTTP("Deezer", 110*time.Millisecond)}
}

// get fetches a Deezer API URL, waiting out its quota errors (which come with HTTP 200).
func (d *Deezer) get(ctx context.Context, u string, out any) error {
	for attempt := 0; ; attempt++ {
		status, body, err := d.http.get(ctx, u)
		if err != nil {
			return err
		}
		if status != 200 {
			return failf("Deezer answered HTTP %d", status)
		}
		var probe struct {
			Error *deezerError `json:"error"`
		}
		if json.Unmarshal(body, &probe) == nil && probe.Error != nil {
			if probe.Error.Code != 4 || attempt == 3 {
				return failf("Deezer: %s", probe.Error.Message)
			}
			time.Sleep(5 * time.Second)
			continue
		}
		if err := json.Unmarshal(body, out); err != nil {
			return failf("Deezer sent an unreadable answer")
		}
		return nil
	}
}

func deezerQuote(s string) string { return strings.NewReplacer(`"`, " ", `\`, " ").Replace(s) }

func (d *Deezer) enrich(ctx context.Context, t *Track) error {
	artist, title := t.Artists[0], searchTitle(t.Title)
	var queries []string
	if t.Album != "" {
		queries = append(queries, fmt.Sprintf(`artist:"%s" track:"%s" album:"%s"`, deezerQuote(artist), deezerQuote(title), deezerQuote(t.Album)))
	}
	queries = append(queries, fmt.Sprintf(`artist:"%s" track:"%s"`, deezerQuote(artist), deezerQuote(title)), artist+" "+title)

	var best *deezerTrack
	albumOK := false
	for _, q := range queries {
		var res struct {
			Data []deezerTrack `json:"data"`
		}
		if err := d.get(ctx, "https://api.deezer.com/search?limit=10&q="+url.QueryEscape(q), &res); err != nil {
			return err
		}
		for i := range res.Data {
			r := &res.Data[i]
			m := evaluate(t, Candidate{Title: r.Title, Artist: r.Artist.Name, Album: r.Album.Title, Duration: float64(r.Duration), ArtistIsCredit: true})
			if m.Reject != "" {
				continue
			}
			sameAlbum := t.Album == "" || tokenSetRatio(cleanTitle(t.Album, false), cleanTitle(r.Album.Title, false)) >= 85
			if best == nil || (sameAlbum && !albumOK) {
				best, albumOK = r, sameAlbum
			}
			if albumOK {
				break
			}
		}
		if best != nil && albumOK {
			break
		}
	}
	if best == nil {
		return nil
	}
	if t.DeezerArtistURL == "" && artistScore(&Track{Artists: t.Artists[:1]}, &Candidate{Artist: best.Artist.Name, ArtistIsCredit: true}) >= minArtist {
		t.DeezerArtistURL = best.Artist.Link
	}
	if t.Album == "" {
		t.Album = best.Album.Title
	}
	if !albumOK {
		// A different release of the song (single vs album, remaster, ...) may have a
		// different length or cover, so don't borrow those.
		return nil
	}
	if t.DurationMs == 0 {
		t.DurationMs = best.Duration * 1000
	}
	if t.CoverURL == "" {
		t.CoverURL = best.Album.CoverXL
	}
	if t.TrackNumber == 0 || t.ISRC == "" || t.ReleaseDate == "" {
		var full struct {
			ISRC          string `json:"isrc"`
			TrackPosition int    `json:"track_position"`
			DiskNumber    int    `json:"disk_number"`
			ReleaseDate   string `json:"release_date"`
		}
		if err := d.get(ctx, fmt.Sprintf("https://api.deezer.com/track/%d", best.ID), &full); err != nil {
			return err
		}
		if t.TrackNumber == 0 {
			t.TrackNumber, t.DiscNumber = full.TrackPosition, full.DiskNumber
		}
		if t.ISRC == "" {
			t.ISRC = full.ISRC
		}
		if t.ReleaseDate == "" && full.ReleaseDate != "0000-00-00" {
			t.ReleaseDate = full.ReleaseDate
		}
	}
	return nil
}

// enrichLibrary looks up every track that lacks details. Results are kept in the library, so
// later runs skip tracks already looked up.
func enrichLibrary(ctx context.Context, lib *Library, logf func(string, ...any)) {
	var todo []*Track
	for _, t := range lib.ordered() {
		if needsEnrichment(t) {
			todo = append(todo, t)
		}
	}
	if len(todo) == 0 {
		return
	}
	logf("Looking up lengths, albums and covers on Deezer for %d tracks…", len(todo))
	d := newDeezer()
	jobs := make(chan *Track)
	var done, failed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if err := d.enrich(ctx, t); err != nil {
					if ctx.Err() != nil {
						continue
					}
					failed.Add(1)
					debugf("deezer lookup for %s failed: %v", t, err)
					if isUnavailable(err) {
						continue // try again next run
					}
				}
				t.Enriched = true
				if n := done.Add(1); n%100 == 0 {
					logf("  %d/%d", n, len(todo))
				}
			}
		}()
	}
loop:
	for _, t := range todo {
		select {
		case jobs <- t:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	if n := failed.Load(); n > 0 {
		logf("  %d lookups failed (details in the log); those tracks are matched without a length check", n)
	}
}
