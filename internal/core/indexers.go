package core

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The built-in indexers come from Jackett's site definitions
// (github.com/Jackett/Jackett, src/Jackett.Common/Definitions): the public ones that need no
// login. They're fetched from GitHub the first time, kept in the data folder and checked for
// changes once a week. Jackett itself (or Prowlarr) can still be added on top.
//
// Some come with the app (indexers/*.yml): sites Jackett runs from C# code rather than a
// definition, written for this app in the same format; RuTracker.org, which needs your
// account (cardigann_login.go).

//go:embed indexers/*.yml
var ownIndexers embed.FS

const jackettDefsAPI = "https://api.github.com/repos/Jackett/Jackett/contents/src/Jackett.Common/Definitions?ref=master"

// defaultIndexers are searched unless you choose others: general trackers with music.
var defaultIndexers = []string{"1337x", "thepiratebay", "limetorrents", "torrentdownload", "torrentdownloads",
	"kickasstorrents-to", "therarbg", "uindex", "torrentgalaxyclone", "magnetcat", "extratorrent-st", "noname-club", "rutracker-ru"}

// IndexerConfig chooses the built-in indexers.
type IndexerConfig struct {
	NoBuiltin    bool     `json:"no_builtin,omitempty"` // only Jackett/Prowlarr
	Chosen       bool     `json:"chosen,omitempty"`     // On was set by you (else the defaults)
	On           []string `json:"on,omitempty"`
	FlareSolverr string   `json:"flaresolverr,omitempty"` // e.g. http://127.0.0.1:8191, for Cloudflare's checks
	// Settings are your settings for an indexer (its ID → setting → value): a login's
	// username and password.
	Settings map[string]map[string]string `json:"settings,omitempty"`
}

func (c IndexerConfig) enabled() []string {
	if c.NoBuiltin {
		return nil
	}
	if !c.Chosen {
		return defaultIndexers
	}
	return c.On
}

// IndexerInfo describes a built-in indexer.
type IndexerInfo struct {
	ID, Name, Description, Language, Site string
	Music                                 bool // has music categories (searched with them)
	Login                                 bool // needs your account (Settings)
	Settings                              []IndexerSetting
}

// IndexerSetting is one of an indexer's settings a frontend asks for.
type IndexerSetting struct {
	Name, Label string
	Secret      bool // a password
}

type indexerDefs struct {
	dir string

	mu       sync.Mutex
	defs     map[string][]byte
	info     map[string]IndexerInfo
	own      map[string]bool // came with the app
	checked  time.Time
	updating bool
}

type defsIndex struct {
	Checked time.Time         `json:"checked"`
	SHAs    map[string]string `json:"shas"` // every definition file's, public or not
}

func loadIndexerDefs(dir string) *indexerDefs {
	d := &indexerDefs{dir: dir, defs: map[string][]byte{}, info: map[string]IndexerInfo{}}
	var idx defsIndex
	if data, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		json.Unmarshal(data, &idx)
	}
	d.checked = idx.Checked
	files, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil {
			d.add(data, false)
		}
	}
	d.addOwn()
	return d
}

// addOwn adds the definitions that come with the app (over any of the same name).
func (d *indexerDefs) addOwn() {
	files, _ := ownIndexers.ReadDir("indexers")
	for _, f := range files {
		if data, err := ownIndexers.ReadFile("indexers/" + f.Name()); err == nil {
			if _, ok := d.add(data, true); !ok {
				Logf("Torrent: the built-in definition %s doesn't load", f.Name())
			}
		}
	}
}

// add keeps a definition if it's public and needs no login; own: one that comes with the app
// (it may need a login).
func (d *indexerDefs) add(data []byte, own bool) (string, bool) {
	def, _, err := parseCardigann(data)
	if err != nil || !own && (def.Type != "public" || def.Login != nil) || def.Login != nil && !def.Login.supported() {
		return "", false
	}
	if _, mine := d.own[def.ID]; mine && !own {
		return "", false // the app's own wins over Jackett's
	}
	if own {
		if d.own == nil {
			d.own = map[string]bool{}
		}
		d.own[def.ID] = true
	}
	info := IndexerInfo{ID: def.ID, Name: def.Name, Description: def.Description, Language: def.Language,
		Site: def.Links[0], Music: len(def.musicCats()) > 0, Login: def.Login != nil}
	for _, s := range def.Settings {
		switch s.Type {
		case "text", "password":
			info.Settings = append(info.Settings, IndexerSetting{Name: s.Name, Label: FirstNonEmpty(s.Label, s.Name), Secret: s.Type == "password"})
		}
	}
	if !info.Login {
		info.Settings = nil // the public ones' settings (sort order and such) keep their defaults
	}
	d.defs[def.ID] = data
	d.info[def.ID] = info
	return def.ID, true
}

func (d *indexerDefs) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.defs)
}

func (d *indexerDefs) get(id string) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.defs[id]
}

func (d *indexerDefs) list() []IndexerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]IndexerInfo, 0, len(d.info))
	for _, i := range d.info {
		out = append(out, i)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (d *indexerDefs) stale() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return time.Since(d.checked) > 7*24*time.Hour
}

// refresh fetches the definitions that changed since the last time.
func (d *indexerDefs) refresh(ctx context.Context, route string) (int, error) {
	d.mu.Lock()
	if d.updating {
		d.mu.Unlock()
		return 0, fmt.Errorf("already updating")
	}
	d.updating = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.updating = false
		d.mu.Unlock()
	}()
	client := &http.Client{Timeout: 60 * time.Second, Transport: router.Transport(route)}
	get := func(u string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "universal-music-downloader")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, u)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	}
	data, err := get(jackettDefsAPI)
	if err != nil {
		return 0, fmt.Errorf("listing Jackett's indexer definitions: %w", err)
	}
	var list []struct {
		Name        string `json:"name"`
		SHA         string `json:"sha"`
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return 0, fmt.Errorf("GitHub sent an unreadable list: %w", err)
	}
	var idx defsIndex
	if old, err := os.ReadFile(filepath.Join(d.dir, "index.json")); err == nil {
		json.Unmarshal(old, &idx)
	}
	if idx.SHAs == nil {
		idx.SHAs = map[string]string{}
	}
	os.MkdirAll(d.dir, 0o755)
	type job struct{ name, sha, url string }
	var jobs []job
	for _, f := range list {
		if strings.HasSuffix(f.Name, ".yml") && idx.SHAs[f.Name] != f.SHA {
			jobs = append(jobs, job{f.Name, f.SHA, f.DownloadURL})
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	changed := 0
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := get(j.url)
			if err != nil {
				debugf("indexer definition %s: %v", j.name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			idx.SHAs[j.name] = j.sha
			d.mu.Lock()
			_, keep := d.add(data, false)
			d.mu.Unlock()
			if keep {
				writeFileAtomic(filepath.Join(d.dir, j.name), data)
				changed++
			} else {
				os.Remove(filepath.Join(d.dir, j.name))
			}
		}()
	}
	wg.Wait()
	idx.Checked = time.Now()
	if out, err := json.Marshal(idx); err == nil {
		writeFileAtomic(filepath.Join(d.dir, "index.json"), out)
	}
	d.mu.Lock()
	d.checked = idx.Checked
	d.mu.Unlock()
	return changed, ctx.Err()
}

// --- for frontends --------------------------------------------------------------------------

// IndexerStatus is a built-in indexer with whether it's searched and how it's doing.
type IndexerStatus struct {
	IndexerInfo
	On      bool
	Default bool
	Health  *FeedStatus // while torrents are set up
}

// Indexers lists the built-in indexers.
func (m *Manager) Indexers() []IndexerStatus {
	cfg := m.Config()
	on := cfg.Indexers.enabled()
	health := map[string]FeedStatus{}
	for _, f := range m.TorrentFeeds() {
		health[f.Name] = f
	}
	var out []IndexerStatus
	for _, i := range m.indexerDefs.list() {
		s := IndexerStatus{IndexerInfo: i, On: ContainsString(on, i.ID), Default: ContainsString(defaultIndexers, i.ID)}
		if h, ok := health[i.Name]; ok {
			s.Health = &h
		}
		out = append(out, s)
	}
	return out
}

// SetIndexer turns a built-in indexer on or off.
func (m *Manager) SetIndexer(id string, on bool) error {
	return m.UpdateConfig(func(c *Config) {
		list := c.Indexers.enabled()
		var keep []string
		for _, x := range list {
			if x != id {
				keep = append(keep, x)
			}
		}
		if on {
			keep = append(keep, id)
		}
		c.Indexers.On, c.Indexers.Chosen = keep, true
		if keep == nil {
			c.Indexers.On = []string{}
		}
	})
}

// SetIndexerSettings saves your settings for an indexer (a login), and turns it on.
func (m *Manager) SetIndexerSettings(id string, settings map[string]string) error {
	if err := m.UpdateConfig(func(c *Config) {
		if c.Indexers.Settings == nil {
			c.Indexers.Settings = map[string]map[string]string{}
		}
		clean := map[string]string{}
		for k, v := range settings {
			if v = strings.TrimSpace(v); v != "" {
				clean[k] = v
			}
		}
		if len(clean) == 0 {
			delete(c.Indexers.Settings, id)
		} else {
			c.Indexers.Settings[id] = clean
		}
	}); err != nil {
		return err
	}
	return m.SetIndexer(id, len(settings) > 0)
}

// RefreshIndexers fetches the indexer definitions that changed on GitHub.
func (m *Manager) RefreshIndexers(ctx context.Context) (int, error) {
	n, err := m.indexerDefs.refresh(ctx, m.Config().Source("torrent").Route)
	if err == nil && n > 0 {
		m.srcMu.Lock()
		m.torKey = "" // set up again with the new definitions
		m.srcMu.Unlock()
		go m.rebuildSources()
	}
	return n, err
}
