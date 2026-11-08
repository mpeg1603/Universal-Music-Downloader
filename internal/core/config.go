package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Settings, all editable in the app and saved to config.json (which also holds tokens and
// passwords, so it's readable by you only).

type InputCfg struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type PlaylistCfg struct {
	Disabled bool `json:"disabled,omitempty"`
	Priority int  `json:"priority,omitempty"` // higher goes first
}

type SourceCfg struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Route   string `json:"route,omitempty"` // system | direct | proxy URL
}

type BandcampCfg struct {
	Email    string `json:"email,omitempty"`
	Country  string `json:"country,omitempty"`
	Postcode string `json:"postcode,omitempty"`
	// Where the emailed links come from: "paste" (you paste them), "gmail" or "imap".
	Mailbox  string `json:"mailbox,omitempty"`
	IMAPHost string `json:"imap_host,omitempty"` // host:port, e.g. imap.yandex.ru:993
	IMAPUser string `json:"imap_user,omitempty"`
	IMAPPass string `json:"imap_pass,omitempty"`
	// Gmail through Google sign-in: your own OAuth client (Desktop app) and the sign-in.
	GmailClientID     string `json:"gmail_client_id,omitempty"`
	GmailClientSecret string `json:"gmail_client_secret,omitempty"`
	GmailToken        string `json:"gmail_token,omitempty"` // refresh token
	GmailAccount      string `json:"gmail_account,omitempty"`
}

// mailbox is where the links come from; settings from before there was a choice used IMAP.
func (b BandcampCfg) MailboxMode() string {
	switch b.Mailbox {
	case "paste", "gmail", "imap":
		return b.Mailbox
	}
	if b.IMAPHost != "" {
		return "imap"
	}
	return "paste"
}

// fetcher reads the mailbox, or is nil when the links are pasted (or the mailbox isn't set up).
func (b BandcampCfg) fetcher() (mailFetcher, string) {
	switch b.MailboxMode() {
	case "gmail":
		if b.GmailToken == "" {
			return nil, "not signed in to Gmail yet"
		}
		return newGmailBox(b), ""
	case "imap":
		if b.IMAPHost == "" || b.IMAPUser == "" || b.IMAPPass == "" {
			return nil, "the IMAP server, user and password are needed"
		}
		return imapMailbox{host: b.IMAPHost, user: b.IMAPUser, pass: b.IMAPPass}, ""
	}
	return nil, ""
}

type Config struct {
	SetupDone bool                   `json:"setup_done,omitempty"` // the first-run setup was gone through
	Inputs    []InputCfg             `json:"inputs"`
	Playlists map[string]PlaylistCfg `json:"playlists,omitempty"`

	OutputDir   string   `json:"output_dir"`
	LibraryDirs []string `json:"library_dirs,omitempty"`
	Threshold   string   `json:"quality_threshold"`
	Format      string   `json:"format"`
	Template    string   `json:"template"`

	BandwidthMBps float64 `json:"bandwidth_mbps"` // 0: unlimited
	// NoQueueJump keeps every track waiting for a busy source (Soulseek's few download slots, a
	// torrent without peers) even while bandwidth is to spare.
	NoQueueJump   bool `json:"no_queue_jumping,omitempty"`
	MaxDownloads  int  `json:"max_downloads"`
	SearchWorkers int  `json:"search_workers"`
	TriesPerSrc   int  `json:"tries_per_source"`
	Enrich        bool `json:"enrich"`

	Sources            []SourceCfg `json:"sources"`
	DirectInterface    string      `json:"direct_interface,omitempty"`
	Cookies            string      `json:"cookies_file,omitempty"`
	CookiesFromBrowser string      `json:"cookies_from_browser,omitempty"`
	YtDlpPath          string      `json:"yt_dlp_path,omitempty"`
	YouTubePace        float64     `json:"youtube_pace_seconds"` // minimum gap between YouTube requests

	Torznab        TorrentConfig `json:"torznab"`
	Indexers       IndexerConfig `json:"indexers"`
	TorrentTimeout Duration      `json:"torrent_timeout"`
	IndexerTimeout Duration      `json:"indexer_timeout"`

	VKToken     string         `json:"vk_token,omitempty"`
	YandexToken string         `json:"yandex_token,omitempty"`
	Bandcamp    BandcampCfg    `json:"bandcamp"`
	Soulseek    SoulseekConfig `json:"soulseek"`
}

// Duration is a time.Duration written as "30m" in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		v, err := time.ParseDuration(s)
		*d = Duration(v)
		return err
	}
	var n int64
	err := json.Unmarshal(b, &n)
	*d = Duration(n)
	return err
}

var allSources = []string{"bandcamp", "archive", "yandex", "vk", "soulseek", "audius", "youtube_music", "soundcloud", "youtube", "torrent"}

var SourceLabels = map[string]string{
	"bandcamp": "Bandcamp", "archive": "Internet Archive", "youtube_music": "YouTube Music",
	"soundcloud": "SoundCloud", "youtube": "YouTube", "torrent": "Torrent", "vk": "VK", "yandex": "Yandex Music",
	"audius": "Audius", "soulseek": "Soulseek",
}

// Sources added in a later version go into your order before the first of these you have
// (else at the end): Soulseek (often lossless) before torrents and the streaming sites,
// Audius (MP3 320) before YouTube and SoundCloud.
var newSourcePlace = map[string][]string{
	"soulseek": {"torrent", "audius", "youtube_music", "youtube", "soundcloud"},
	"audius":   {"youtube_music", "youtube", "soundcloud"},
}

var sourceDefaultRoute = map[string]string{"vk": RouteDirect, "yandex": RouteDirect}

var Browsers = []string{"", "firefox", "chrome", "chromium", "brave", "edge", "opera", "vivaldi", "whale", "safari"}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	c := Config{
		OutputDir:      filepath.Join(home, "Music", "Universal Music Downloader"),
		Threshold:      "lossless",
		Format:         "original",
		Template:       defaultTemplate,
		MaxDownloads:   8,
		SearchWorkers:  4,
		TriesPerSrc:    2,
		Enrich:         true,
		YouTubePace:    2,
		TorrentTimeout: Duration(30 * time.Minute),
		IndexerTimeout: Duration(20 * time.Second),
		Playlists:      map[string]PlaylistCfg{},
		Bandcamp:       BandcampCfg{Country: "United States", Postcode: "10001"},
	}
	for _, n := range allSources {
		enabled := n != "vk" && n != "yandex" && n != "torrent" && n != "soulseek"
		c.Sources = append(c.Sources, SourceCfg{Name: n, Enabled: enabled, Route: sourceDefaultRoute[n]})
	}
	return c
}

func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = filepath.Join(os.TempDir(), "universal-music-downloader-config")
	}
	return filepath.Join(dir, "universal-music-downloader", "config.json")
}

func DefaultDataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "universal-music-downloader")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "universal-music-downloader")
}

// loadConfig reads the config, filling in defaults and sources added since it was written.
// A missing file gives the defaults (with the torrent settings of older versions).
func LoadConfig(path string) (Config, error) {
	c := defaultConfig()
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if old := loadTorrentConfig(); old.configured() {
			c.Torznab = old
			c.SetSource("torrent", true)
		}
		return c, nil
	case err != nil:
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return defaultConfig(), fmt.Errorf("%s is damaged: %w", path, err)
	}
	c.fillDefaults()
	return c, nil
}

func (c *Config) fillDefaults() {
	d := defaultConfig()
	if c.Playlists == nil {
		c.Playlists = map[string]PlaylistCfg{}
	}
	if c.OutputDir == "" {
		c.OutputDir = d.OutputDir
	}
	if !validThreshold(c.Threshold) {
		c.Threshold = d.Threshold
	}
	if c.Format == "" {
		c.Format = d.Format
	}
	if c.Template == "" {
		c.Template = d.Template
	}
	if c.MaxDownloads <= 0 {
		c.MaxDownloads = d.MaxDownloads
	}
	if c.SearchWorkers <= 0 {
		c.SearchWorkers = d.SearchWorkers
	}
	if c.TriesPerSrc <= 0 {
		c.TriesPerSrc = d.TriesPerSrc
	}
	if c.TorrentTimeout <= 0 {
		c.TorrentTimeout = d.TorrentTimeout
	}
	if c.IndexerTimeout <= 0 {
		c.IndexerTimeout = d.IndexerTimeout
	}
	have := map[string]bool{}
	var srcs []SourceCfg
	for i := range c.Sources {
		if c.Sources[i].Name == "slskd" { // Soulseek used to go through slskd; now it's built in
			c.Sources[i].Name = "soulseek"
		}
	}
	for _, s := range c.Sources {
		if SourceLabels[s.Name] != "" && !have[s.Name] {
			have[s.Name] = true
			srcs = append(srcs, s)
		}
	}
	for _, s := range d.Sources {
		if have[s.Name] {
			continue
		}
		at := len(srcs)
		for i, x := range srcs {
			if ContainsString(newSourcePlace[s.Name], x.Name) {
				at = i
				break
			}
		}
		srcs = append(srcs[:at], append([]SourceCfg{s}, srcs[at:]...)...)
	}
	c.Sources = srcs
}

func (c *Config) SetRoute(name, route string) {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			c.Sources[i].Route = route
		}
	}
}

func (c *Config) SetSource(name string, enabled bool) {
	for i := range c.Sources {
		if c.Sources[i].Name == name {
			c.Sources[i].Enabled = enabled
		}
	}
}

func (c Config) Source(name string) SourceCfg {
	for _, s := range c.Sources {
		if s.Name == name {
			return s
		}
	}
	return SourceCfg{Name: name}
}

func (c Config) clone() Config {
	data, _ := json.Marshal(c)
	var out Config
	json.Unmarshal(data, &out)
	out.fillDefaults()
	return out
}

func (c *Config) Validate() error {
	c.OutputDir = CleanPath(c.OutputDir)
	if c.OutputDir == "" {
		return errors.New("the downloads folder is not set")
	}
	var dirs []string
	for _, d := range c.LibraryDirs {
		if d = CleanPath(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	c.LibraryDirs = dirs
	for _, d := range dirs {
		if isWithin(c.OutputDir, d) {
			return fmt.Errorf("the downloads folder %s is inside your library folder %s; pick a folder outside the library (the library is never written to)", c.OutputDir, d)
		}
		if isWithin(d, c.OutputDir) {
			return fmt.Errorf("the library folder %s is inside the downloads folder %s; keep them apart (the library is never written to)", d, c.OutputDir)
		}
	}
	valid := false
	for _, f := range Formats {
		valid = valid || f == c.Format
	}
	if !valid {
		return fmt.Errorf("format must be one of %s", strings.Join(Formats, ", "))
	}
	if !validThreshold(c.Threshold) {
		return fmt.Errorf("quality threshold must be one of %s", strings.Join(thresholdNames(), ", "))
	}
	if c.BandwidthMBps < 0 {
		return errors.New("the bandwidth limit can't be negative")
	}
	if c.MaxDownloads < 1 || c.MaxDownloads > 64 {
		return errors.New("parallel downloads must be between 1 and 64")
	}
	if c.SearchWorkers < 1 || c.SearchWorkers > 32 {
		return errors.New("parallel searches must be between 1 and 32")
	}
	for _, s := range c.Sources {
		if err := ValidRoute(s.Route); err != nil {
			return fmt.Errorf("%s: %w", SourceLabels[s.Name], err)
		}
	}
	return c.Soulseek.validate()
}

// checkDataDir makes sure the app's own files (state, caches, temporary files) don't end up
// inside a library folder.
func CheckDataDir(c Config, dataDir string) error {
	for _, d := range c.LibraryDirs {
		if isWithin(dataDir, d) {
			return fmt.Errorf("the app's data folder %s is inside your library folder %s; use --data to put it elsewhere", dataDir, d)
		}
	}
	return nil
}

func CleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = ExpandHome(p)
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

func SaveConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) playlistCfg(name string) PlaylistCfg {
	return c.Playlists[name]
}

func ContainsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// legacyName is what the app was called before; its folders move to the new name.
const legacyName = "spotify-downloader"

// MigrateLegacyFolders moves the settings, data and cache folders of the app's old name to
// the new one, the first time, leaving a link at the old place (so an older copy still
// running finds them). It returns what was moved.
func MigrateLegacyFolders() []string {
	newCfg := filepath.Dir(DefaultConfigPath())
	pairs := [][2]string{
		{filepath.Join(filepath.Dir(newCfg), legacyName), newCfg},
		{filepath.Join(filepath.Dir(DefaultDataDir()), legacyName), DefaultDataDir()},
		{filepath.Join(filepath.Dir(defaultCacheDir()), legacyName), defaultCacheDir()},
	}
	var moved []string
	for _, p := range pairs {
		old, now := p[0], p[1]
		if old == now {
			continue
		}
		st, err := os.Lstat(old)
		if err != nil || !st.IsDir() {
			continue // nothing there, or already a link
		}
		if _, err := os.Stat(now); err == nil {
			continue
		}
		os.MkdirAll(filepath.Dir(now), 0o755)
		if err := os.Rename(old, now); err != nil {
			continue
		}
		if runtime.GOOS != "windows" {
			os.Symlink(now, old)
		}
		moved = append(moved, old+" → "+now)
	}
	return moved
}
