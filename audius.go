package main

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Audius: an open music platform where artists upload their own tracks (lots of
// independent electronic music), with a free public API and full streams as MP3 320. Tracks
// the artist restricted to paying fans are left out.

const audiusApp = "spotify-downloader"

var audiusQuality = Quality{Codec: "mp3", Kbps: 320}

type Audius struct {
	http     *HTTP
	discover string // where the list of API hosts comes from

	mu   sync.Mutex
	host string
}

func newAudius(route string) *Audius {
	return &Audius{http: newRoutedHTTP("Audius", 200*time.Millisecond, route), discover: "https://api.audius.co"}
}

func (a *Audius) Name() string        { return "audius" }
func (a *Audius) Label() string       { return "Audius" }
func (a *Audius) MaxQuality() Quality { return audiusQuality }

// api returns the API host to use, asking for the list once.
func (a *Audius) api(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.host != "" {
		return a.host, nil
	}
	var res struct {
		Data []string `json:"data"`
	}
	if err := a.http.getJSON(ctx, a.discover, &res); err != nil {
		return "", err
	}
	if len(res.Data) == 0 {
		return "", unavailablef("Audius listed no API hosts")
	}
	a.host = strings.TrimRight(res.Data[0], "/")
	return a.host, nil
}

type audiusTrack struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Duration    float64 `json:"duration"`
	Permalink   string  `json:"permalink"`
	Streamable  bool    `json:"is_streamable"`
	StreamGated bool    `json:"is_stream_gated"`
	Conditions  any     `json:"stream_conditions"`
	User        struct {
		Name   string `json:"name"`
		Handle string `json:"handle"`
	} `json:"user"`
}

func (a *Audius) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	host, err := a.api(ctx)
	if err != nil {
		return nil, err
	}
	return searchUntilFound(ctx, t, songQueries(t, 2), func(q string) ([]Candidate, error) {
		var res struct {
			Data []audiusTrack `json:"data"`
		}
		u := host + "/v1/tracks/search?" + url.Values{"query": {q}, "app_name": {audiusApp}}.Encode()
		if err := a.http.getJSON(ctx, u, &res); err != nil {
			return nil, err
		}
		var out []Candidate
		for _, r := range res.Data {
			if r.ID == "" || !r.Streamable || r.StreamGated || r.Conditions != nil {
				continue
			}
			c := Candidate{Source: "audius", URL: "https://audius.co" + r.Permalink, Ref: r.ID, Title: r.Title,
				Artist: r.User.Name, Duration: r.Duration, ArtistIsCredit: true, Quality: audiusQuality}
			if r.User.Handle != "" {
				c.ArtistURL = "https://audius.co/" + r.User.Handle
			}
			out = append(out, c)
		}
		return out, nil
	})
}

func (a *Audius) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	host, err := a.api(ctx)
	if err != nil {
		return "", err
	}
	u := host + "/v1/tracks/" + url.PathEscape(c.Ref) + "/stream?" + url.Values{"app_name": {audiusApp}}.Encode()
	return a.http.download(ctx, u, workdir, "audius")
}
