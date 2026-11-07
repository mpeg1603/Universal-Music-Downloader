package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// fakeSlskd answers like slskd's API: searches, their responses, and downloads that finish
// by putting the file into its downloads folder.
type fakeSlskd struct {
	t         *testing.T
	srv       *httptest.Server
	downloads string
	file      []byte
	mu        sync.Mutex
	searches  []string
	transfers map[string]string // filename -> state
	polls     int
}

const slskdKey = "0123456789abcdef0123"

func newFakeSlskd(t *testing.T, file []byte) *fakeSlskd {
	f := &fakeSlskd{t: t, downloads: t.TempDir(), file: file, transfers: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlskd) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != slskdKey {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/api/v0/")
	good := `@@abc\Music\Daft Punk\Random Access Memories\08 - Get Lucky.flac`
	switch {
	case r.Method == "POST" && p == "searches":
		var body struct {
			ID         string `json:"id"`
			SearchText string `json:"searchText"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.searches = append(f.searches, body.SearchText)
		json.NewEncoder(w).Encode(map[string]any{"id": body.ID, "state": "InProgress", "isComplete": false})
	case r.Method == "GET" && strings.HasSuffix(p, "/responses"):
		json.NewEncoder(w).Encode([]map[string]any{
			{"username": "locked", "hasFreeUploadSlot": true, "files": []map[string]any{
				{"filename": good, "size": len(f.file), "length": 4, "isLocked": true}}},
			{"username": "slow", "hasFreeUploadSlot": false, "queueLength": 40, "files": []map[string]any{
				{"filename": good, "size": len(f.file), "length": 4}}},
			{"username": "fast", "hasFreeUploadSlot": true, "uploadSpeed": 900000, "files": []map[string]any{
				{"filename": `@@x\Daft Punk\Random Access Memories\07 - Touch.flac`, "size": 10, "length": 498},
				{"filename": good, "size": len(f.file), "length": 4},
				{"filename": `@@x\Daft Punk\Random Access Memories\cover.jpg`, "size": 10}}},
		})
	case r.Method == "GET" && strings.HasPrefix(p, "searches/"):
		json.NewEncoder(w).Encode(map[string]any{"isComplete": true, "state": "Completed, TimedOut"})
	case r.Method == "DELETE" && strings.HasPrefix(p, "searches/"):
		w.WriteHeader(204)
	case r.Method == "POST" && p == "transfers/downloads/fast":
		var files []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		json.NewDecoder(r.Body).Decode(&files)
		for _, fl := range files {
			f.transfers[fl.Filename] = "Queued, Remotely"
		}
		w.WriteHeader(201)
	case r.Method == "GET" && p == "transfers/downloads/fast":
		f.polls++
		var list []map[string]any
		for name, st := range f.transfers {
			if f.polls == 2 {
				st = "InProgress"
			}
			if f.polls >= 3 && st != "Completed, Succeeded" {
				st = "Completed, Succeeded"
				dir := filepath.Join(f.downloads, "Random Access Memories")
				os.MkdirAll(dir, 0o755)
				os.WriteFile(filepath.Join(dir, "08 - Get Lucky.flac"), f.file, 0o644)
			}
			f.transfers[name] = st
			list = append(list, map[string]any{"id": "6f9619ff-8b86-d011-b42d-00cf4fc964ff", "filename": name, "state": st,
				"size": len(f.file), "bytesTransferred": len(f.file) / 2})
		}
		json.NewEncoder(w).Encode(map[string]any{"username": "fast", "directories": []map[string]any{{"directory": "x", "files": list}}})
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func TestSoulseekSource(t *testing.T) {
	tools := needFFmpeg(t)
	flac, err := os.ReadFile(sine(t, filepath.Join(t.TempDir(), "x.flac"), 4))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeSlskd(t, flac)
	s := newSlskd(SlskdConfig{URL: f.srv.URL + "/", APIKey: slskdKey, Downloads: f.downloads}, "flac", "")
	s.poll = 20 * time.Millisecond
	tr := getLucky()
	tr.Key, tr.DurationMs = "a", 4000
	m := testManager(t, tools, []Source{s}, testLibrary(tr), func(c *Config) { c.Format = "flac" })
	out := runManager(t, m)
	v := out["a"]
	if v.State != JobDone || !strings.HasPrefix(v.Rec.SourceURL, "soulseek://fast/") || !v.Rec.Quality.Lossless {
		t.Fatalf("%v %s %+v", v.State, v.Reason, v.Rec)
	}
	if len(f.searches) != 1 || f.searches[0] != "Daft Punk Get Lucky" {
		t.Errorf("searches %q", f.searches)
	}
	// slskd keeps its own copy.
	if _, err := os.Stat(filepath.Join(f.downloads, "Random Access Memories", "08 - Get Lucky.flac")); err != nil {
		t.Error(err)
	}

	bad := newSlskd(SlskdConfig{URL: f.srv.URL, APIKey: "wrong-key-wrong-key"}, "flac", "")
	if _, err := bad.Search(m.ctx, tr); !isUnavailable(err) || !strings.Contains(err.Error(), "API key") {
		t.Errorf("wrong key: %v", err)
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
	want := "+bandcamp +archive slskd +torrent +audius +youtube +youtube_music +soundcloud vk yandex"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
