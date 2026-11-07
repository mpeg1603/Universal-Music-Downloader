package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudiusSource(t *testing.T) {
	tools := needFFmpeg(t)
	audio := sine(t, filepath.Join(t.TempDir(), "x.mp3"), 4)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			fmt.Fprintf(w, `{"data":[%q]}`, srv.URL)
		case r.URL.Path == "/v1/tracks/search":
			if r.URL.Query().Get("app_name") == "" {
				w.WriteHeader(400)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"id": "gated", "title": "Get Lucky", "duration": 4, "permalink": "/x/gated", "is_streamable": true,
					"is_stream_gated": true, "user": map[string]any{"name": "Daft Punk", "handle": "daftpunk"}},
				{"id": "ok1", "title": "Get Lucky", "duration": 4, "permalink": "/daftpunk/get-lucky", "is_streamable": true,
					"user": map[string]any{"name": "Daft Punk", "handle": "daftpunk"}},
			}})
		case r.URL.Path == "/v1/tracks/ok1/stream":
			w.Header().Set("Content-Type", "audio/mpeg")
			http.ServeFile(w, r, audio)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a := newAudius("")
	a.discover = srv.URL
	tr := getLucky()
	tr.Key, tr.DurationMs = "a", 4000
	m := testManager(t, tools, []Source{a}, testLibrary(tr), nil)
	out := runManager(t, m)
	v := out["a"]
	if v.State != JobDone || v.Rec.SourceURL != "https://audius.co/daftpunk/get-lucky" || v.Rec.SourceArtistURL != "https://audius.co/daftpunk" {
		t.Fatalf("%v %s %+v", v.State, v.Reason, v.Rec)
	}
	if _, err := os.Stat(v.Rec.Path); err != nil {
		t.Error(err)
	}
}

func TestNewSourcesGoIntoYourOrder(t *testing.T) {
	var c Config
	json.Unmarshal([]byte(`{"sources":[{"name":"bandcamp","enabled":true},{"name":"archive","enabled":true},
		{"name":"torrent","enabled":true},{"name":"youtube","enabled":true},{"name":"youtube_music","enabled":true},
		{"name":"soundcloud","enabled":true},{"name":"vk"},{"name":"yandex"}]}`), &c)
	c.fillDefaults()
	var names []string
	for _, s := range c.Sources {
		on := ""
		if s.Enabled {
			on = "+"
		}
		names = append(names, on+s.Name)
	}
	want := "+bandcamp +archive soulseek +torrent +audius +youtube +youtube_music +soundcloud vk yandex"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSlskdEntryBecomesSoulseek(t *testing.T) {
	var c Config
	json.Unmarshal([]byte(`{"sources":[{"name":"bandcamp","enabled":true},{"name":"slskd","route":"direct"},{"name":"torrent","enabled":true}]}`), &c)
	c.fillDefaults()
	if c.Sources[1].Name != "soulseek" || c.Sources[1].Route != "direct" {
		t.Errorf("%+v", c.Sources)
	}
	n := 0
	for _, s := range c.Sources {
		if s.Name == "soulseek" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d soulseek entries", n)
	}
}
