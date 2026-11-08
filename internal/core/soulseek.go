package core

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Soulseek as a source: people share their music folders, often in FLAC. The client is
// built in (slsk_*.go): it needs only your Soulseek account (a new name and password make
// a new account on first login).

type SoulseekConfig struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Server   string `json:"server,omitempty"`      // host:port; empty: server.slsknet.org:2242
	Port     int    `json:"listen_port,omitempty"` // 0: 2234
	NoUPnP   bool   `json:"no_upnp,omitempty"`     // don't ask the router to forward the port

	Downloads int      `json:"downloads_at_once,omitempty"`        // 1-4; 0: 2
	SearchGap float64  `json:"seconds_between_searches,omitempty"` // 0: 10; at least 8
	PerHour   int      `json:"searches_per_hour,omitempty"`        // 0: 120; 20 to 200
	QueueWait Duration `json:"queue_wait,omitempty"`               // give up on a user's queue after; 0: 5m

	NoShare     bool     `json:"no_sharing,omitempty"`
	Shares      []string `json:"shared_folders,omitempty"`       // folders ("Name=path" names one); empty: library + downloads
	UploadSlots int      `json:"upload_slots,omitempty"`         // 0: 2
	UploadKBps  int      `json:"upload_limit_kbps,omitempty"`    // 0: unlimited
	QueueFiles  int      `json:"queue_files_per_user,omitempty"` // 0: 100
	QueueMB     int      `json:"queue_mb_per_user,omitempty"`    // 0: 10000

	Banned      []string `json:"banned_users,omitempty"`
	Ignored     []string `json:"ignored_users,omitempty"` // their messages aren't shown
	Buddies     []string `json:"buddies,omitempty"`       // users you follow (online status)
	Likes       []string `json:"likes,omitempty"`         // interests, for recommendations
	Hates       []string `json:"hates,omitempty"`
	Rooms       []string `json:"rooms,omitempty"` // chat rooms to join when logged in
	Description string   `json:"description,omitempty"`
}

func (c SoulseekConfig) Configured() bool { return c.Username != "" && c.Password != "" }

func (c SoulseekConfig) ServerAddr() string {
	return FirstNonEmpty(strings.TrimSpace(c.Server), SlskDefaultServer)
}

func (c SoulseekConfig) ListenPort() int {
	switch {
	case c.Port < 0: // any free port (tests)
		return 0
	case c.Port == 0:
		return SlskDefaultPort
	}
	return c.Port
}

// downloads: files requested at once. The network asks for restraint, so at most 4.
func (c SoulseekConfig) MaxDownloads() int {
	if c.Downloads <= 0 {
		return 2
	}
	return min(c.Downloads, 4)
}

// SearchInterval is the least time between two searches. The server bans clients that search
// too often (see slsk_budget.go): at least 8 seconds, 10 by default.
func (c SoulseekConfig) SearchInterval() time.Duration {
	if c.SearchGap <= 0 {
		return 10 * time.Second
	}
	return time.Duration(max(c.SearchGap, 8) * float64(time.Second))
}

// SearchesPerHour caps the searches in any hour (and a quarter of it in any 10 minutes).
func (c SoulseekConfig) SearchesPerHour() int {
	if c.PerHour <= 0 {
		return 120
	}
	return min(max(c.PerHour, 20), 200)
}

func (c SoulseekConfig) QueueTimeout() time.Duration {
	if c.QueueWait <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(c.QueueWait)
}

func (c SoulseekConfig) Slots() int {
	if c.UploadSlots <= 0 {
		return 2
	}
	return c.UploadSlots
}

func (c SoulseekConfig) FilesPerUser() int {
	if c.QueueFiles <= 0 {
		return 100
	}
	return c.QueueFiles
}

func (c SoulseekConfig) MBPerUser() int {
	if c.QueueMB <= 0 {
		return 10000
	}
	return c.QueueMB
}

// connKey is what needs a new login when it changes.
func (c SoulseekConfig) connKey(route string) string {
	return strings.Join([]string{c.Username, c.Password, c.ServerAddr(), strconv.Itoa(c.ListenPort()), strconv.FormatBool(c.NoUPnP), NormalizeRoute(route)}, "\x00")
}

func (c SoulseekConfig) validate() error {
	if strings.TrimSpace(c.Username) != c.Username || len(c.Username) > 30 {
		return fmt.Errorf("Soulseek username: at most 30 characters, no spaces at the ends")
	}
	if c.Port != 0 && (c.Port < 1024 || c.Port > 65535) {
		return fmt.Errorf("Soulseek port must be between 1024 and 65535")
	}
	if c.Downloads < 0 || c.Downloads > 4 {
		return fmt.Errorf("Soulseek downloads at once: 1 to 4 (the network asks for restraint)")
	}
	if c.SearchGap < 0 || c.PerHour < 0 || c.PerHour > 200 {
		return fmt.Errorf("Soulseek searches: at most 200 an hour (the server bans clients that search too much)")
	}
	if c.UploadSlots < 0 || c.UploadSlots > 20 {
		return fmt.Errorf("Soulseek upload slots: 1 to 20")
	}
	if c.UploadKBps < 0 || c.QueueFiles < 0 || c.QueueMB < 0 {
		return fmt.Errorf("Soulseek limits can't be negative")
	}
	return nil
}

// shareDirs are the folders shared: the ones set, or your library and downloads folders.
func (cfg Config) ShareRoots() []slskShareRoot {
	if len(cfg.Soulseek.Shares) == 0 {
		return shareRoots(append(append([]string{}, cfg.LibraryDirs...), cfg.OutputDir))
	}
	var named []slskShareRoot
	var plain []string
	for _, s := range cfg.Soulseek.Shares {
		if name, p, ok := strings.Cut(s, "="); ok && !strings.ContainsAny(name, `/\`) && strings.TrimSpace(name) != "" {
			named = append(named, slskShareRoot{Name: strings.TrimSpace(name), Path: CleanPath(p)})
		} else {
			plain = append(plain, s)
		}
	}
	out := named
	used := map[string]bool{}
	for _, r := range named {
		used[strings.ToLower(r.Name)] = true
	}
	for _, r := range shareRoots(plain) {
		for i := 2; used[strings.ToLower(r.Name)]; i++ {
			r.Name = filepath.Base(r.Path) + " " + strconv.Itoa(i)
		}
		used[strings.ToLower(r.Name)] = true
		out = append(out, r)
	}
	return out
}

// --- the source -----------------------------------------------------------------------------

type Soulseek struct {
	c        *slskClient
	lossless bool

	mu   sync.Mutex
	hits map[string]slskHit // candidates a wishlist search found, by track
	// jump says whether a download may skip the wait for a slot (queue jumping; see
	// Manager.mayJump). nil: always wait.
	jump func() bool
	// goodSeeder hears about a user who just sent a file fast and in good quality.
	goodSeeder func(user string, speed float64)
}

// A seeder is "good" when a file came at least this fast in at least this quality: worth
// asking for more of the queue (see Manager.seederAffinity).
const (
	goodSeederSpeed = 250 << 10 // bytes/s
	goodSeederKbps  = 256
)

type slskHit struct {
	cands []Candidate
	at    time.Time
}

func newSoulseekSource(c *slskClient, format string) *Soulseek {
	return &Soulseek{c: c, lossless: format == "flac" || format == "original", hits: map[string]slskHit{}}
}

func (s *Soulseek) setFormat(format string) {
	s.mu.Lock()
	s.lossless = format == "flac" || format == "original"
	s.mu.Unlock()
}

func (s *Soulseek) Name() string        { return "soulseek" }
func (s *Soulseek) Label() string       { return "Soulseek" }
func (s *Soulseek) MaxQuality() Quality { return Quality{Codec: "flac", Lossless: true} }

// busy: searches are paced, so a long line of them means waiting; unless what the track needs
// was already found, or is on its way (its album's search, usually).
func (s *Soulseek) busy(t *Track) time.Duration {
	s.mu.Lock()
	_, hit := s.hits[t.Key]
	s.mu.Unlock()
	if q := slskQueries(t); hit || len(q) > 0 && s.c.answers.known(slskQuery(q[0])) {
		return 0
	}
	return s.c.searchBacklog()
}

func (s *Soulseek) remember(key string, cands []Candidate) {
	s.mu.Lock()
	s.hits[key] = slskHit{cands, time.Now()}
	s.mu.Unlock()
}

func (s *Soulseek) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	s.mu.Lock()
	hit, ok := s.hits[t.Key]
	delete(s.hits, t.Key)
	s.mu.Unlock()
	if ok && time.Since(hit.at) < 30*time.Minute {
		return hit.cands, nil
	}
	limit := slskLineLimit
	return searchUntilFound(ctx, t, slskQueries(t), func(q string) ([]Candidate, error) {
		resps, sent, err := s.c.searchWithin(ctx, q, limit)
		if err != nil {
			return nil, err
		}
		if sent { // it's been through the line: its next search doesn't go to the back of it
			limit = 0
		}
		return s.candidates(t, resps), nil
	})
}

// slskQueries are a track's Soulseek searches. Searches are scarce (the server bans clients
// that search much), and people share whole album folders, so the album comes first: its
// answers are kept for an hour and serve the album's other tracks without a search of their
// own. Then the song itself. Soulseek matches every word against file paths, so plain words.
func slskQueries(t *Track) []string {
	var queries []string
	album := baseAlbum(t.Album)
	if album != "" && !strings.EqualFold(normalize(album), normalize(searchTitle(t.Title))) {
		artist := t.AlbumArtistStr()
		if variousArtists(artist) {
			artist = ""
		}
		if q := queryText(artist + " " + album); len(strings.Fields(q)) >= 2 {
			queries = append(queries, q)
		}
	}
	for _, q := range songQueries(t, 1) {
		if q = queryText(q); !ContainsFold(queries, q) {
			queries = append(queries, q)
		}
	}
	return queries
}

// candidates picks, from each user's answer, the file that fits the track best. Users who
// can send right away, and quickly, come first (the matcher keeps that order for equal
// scores), and the wanted kind of file (lossless or not) before the other.
func (s *Soulseek) candidates(t *Track, resps []slskResponse) []Candidate {
	type pick struct {
		c    Candidate
		r    slskResponse
		loss bool
	}
	s.mu.Lock()
	lossless := s.lossless
	s.mu.Unlock()
	busy := s.c.busyUsers() // one file at a time from a user: others come sooner
	var picks []pick
	for _, r := range resps {
		var names []string
		var files []slskFile
		for _, f := range r.Files {
			name := strings.ReplaceAll(f.Name, `\`, "/")
			if audioExts[strings.ToLower(path.Ext(name))] {
				names = append(names, name)
				files = append(files, f)
			}
		}
		i, stem := bestFile(t, names)
		if i < 0 {
			continue
		}
		f := files[i]
		dur := float64(f.attr(slskAttrDuration))
		q := fileQuality(names[i], f.Size, dur)
		if br := f.attr(slskAttrBitrate); br > 0 && !q.Lossless {
			q.Kbps = br
		}
		if bd := f.attr(slskAttrBitDepth); bd > 0 {
			q.BitDepth = bd
		}
		if sr := f.attr(slskAttrSampleRate); sr > 0 {
			q.SampleRate = sr
		}
		dir := path.Dir(names[i])
		parts := strings.Split(dir, "/")
		c := Candidate{Source: "soulseek", URL: "soulseek://" + r.User + "/" + names[i], Title: stem,
			Artist: strings.Join(parts[max(0, len(parts)-3):], " "), Album: path.Base(dir), Duration: dur,
			Ref: r.User + "\x00" + f.Name + "\x00" + strconv.FormatInt(f.Size, 10), Quality: q}
		picks = append(picks, pick{c, r, q.Lossless})
	}
	sort.SliceStable(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if a.loss != b.loss {
			return a.loss == lossless
		}
		if busy[a.r.User] != busy[b.r.User] {
			return !busy[a.r.User]
		}
		if a.r.FreeSlot != b.r.FreeSlot {
			return a.r.FreeSlot
		}
		if a.r.Queue != b.r.Queue {
			return a.r.Queue < b.r.Queue
		}
		return a.r.Speed > b.r.Speed
	})
	var out []Candidate
	for i, p := range picks[:min(len(picks), 40)] {
		p.c.Pref = i + 1
		out = append(out, p.c)
	}
	return out
}

func (s *Soulseek) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	parts := strings.SplitN(c.Ref, "\x00", 3)
	if len(parts) != 3 {
		return "", failf("bad Soulseek reference")
	}
	user, file := parts[0], parts[1]
	size, _ := strconv.ParseInt(parts[2], 10, 64)
	if size <= 0 {
		return "", failf("%s's file has no size", user)
	}
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(file, `\`, "/")))
	if len(ext) > 8 {
		ext = ""
	}
	if s.jump != nil && !c.Unsearched { // a track lined up for this user waits for them
		used, limit, waiting := s.c.dlGate.Stats()
		if (used >= limit || s.c.busyUsers()[user]) && s.jump() {
			return "", busyf("its %d download slots are taken (%d tracks waiting); bandwidth is to spare, so later sources go first", limit, waiting)
		}
	}
	dest := filepath.Join(workdir, "soulseek"+ext)
	if err := s.c.Download(ctx, user, file, size, dest, taskFrom(ctx)); err != nil {
		return "", err
	}
	if speed := s.c.lastSpeed(user); s.goodSeeder != nil && speed >= goodSeederSpeed &&
		(c.Quality.Lossless || c.Quality.Kbps >= goodSeederKbps) {
		go s.goodSeeder(user, speed)
	}
	return dest, nil
}

// --- Nicotine+ ------------------------------------------------------------------------------

// nicotineConfigPath is where Nicotine+ keeps its settings.
func nicotineConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "nicotine", "config")
}

var (
	rePyPair   = regexp.MustCompile(`\(\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")\s*,\s*(?:'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)")`)
	rePyString = regexp.MustCompile(`'((?:[^'\\]|\\.)*)'|"((?:[^"\\]|\\.)*)"`)
	rePyInts   = regexp.MustCompile(`\d+`)
)

func pyUnescape(s string) string {
	if u, err := strconv.Unquote(`"` + strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), `\'`, `'`) + `"`); err == nil {
		return u
	}
	return s
}

// importNicotine reads your Nicotine+ settings: account, port, shared folders and upload
// limits. Only reads; Nicotine+'s files are left as they are.
func ImportNicotine(into SoulseekConfig) (SoulseekConfig, error) {
	data, err := os.ReadFile(nicotineConfigPath())
	if err != nil {
		return into, fmt.Errorf("no Nicotine+ settings found (%v)", err)
	}
	sections := map[string]map[string]string{}
	cur := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "[") && strings.HasSuffix(strings.TrimSpace(line), "]") {
			cur = strings.Trim(strings.TrimSpace(line), "[]")
			sections[cur] = map[string]string{}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && cur != "" {
			sections[cur][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	srv, tr := sections["server"], sections["transfers"]
	c := into
	if v := srv["login"]; v != "" {
		c.Username = v
	}
	if v := srv["passw"]; v != "" {
		c.Password = v
	}
	if nums := rePyInts.FindAllString(srv["portrange"], -1); len(nums) > 0 {
		if p, _ := strconv.Atoi(nums[0]); p >= 1024 {
			c.Port = p
		}
	}
	if v := srv["upnp"]; v != "" {
		c.NoUPnP = v == "False"
	}
	if host := srv["server"]; host != "" {
		strs := rePyString.FindStringSubmatch(host)
		nums := rePyInts.FindAllString(host, -1)
		if len(strs) > 0 && len(nums) > 0 {
			if h := FirstNonEmpty(strs[1], strs[2]); h != "" && h+":"+nums[len(nums)-1] != SlskDefaultServer {
				c.Server = h + ":" + nums[len(nums)-1]
			}
		}
	}
	var list func(v string) []string = func(v string) []string {
		var out []string
		for _, m := range rePyString.FindAllStringSubmatch(v, -1) {
			if s := pyUnescape(FirstNonEmpty(m[1], m[2])); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	if v, ok := srv["banlist"]; ok {
		c.Banned = list(v)
	}
	if v, ok := srv["autojoin"]; ok {
		c.Rooms = list(v)
	}
	if v, ok := tr["shared"]; ok {
		var shares []string
		for _, m := range rePyPair.FindAllStringSubmatch(v, -1) {
			name, p := pyUnescape(FirstNonEmpty(m[1], m[2])), pyUnescape(FirstNonEmpty(m[3], m[4]))
			if p == "" {
				continue
			}
			if name != "" && name != filepath.Base(p) {
				shares = append(shares, name+"="+p)
			} else {
				shares = append(shares, p)
			}
		}
		c.Shares = shares
	}
	if tr["useupslots"] == "True" {
		if n, err := strconv.Atoi(tr["uploadslots"]); err == nil && n > 0 {
			c.UploadSlots = min(n, 20)
		}
	}
	switch tr["use_upload_speed_limit"] {
	case "primary":
		c.UploadKBps, _ = strconv.Atoi(tr["uploadlimit"])
	case "alternative":
		c.UploadKBps, _ = strconv.Atoi(tr["uploadlimitalt"])
	case "unlimited":
		c.UploadKBps = 0
	}
	if n, err := strconv.Atoi(tr["queuelimit"]); err == nil && n > 0 {
		c.QueueMB = n
	}
	if n, err := strconv.Atoi(tr["filelimit"]); err == nil && n > 0 {
		c.QueueFiles = n
	}
	if v := sections["userinfo"]["descr"]; v != "" {
		if strs := rePyString.FindStringSubmatch(v); len(strs) > 0 {
			c.Description = pyUnescape(FirstNonEmpty(strs[1], strs[2]))
		} else {
			c.Description = v
		}
	}
	return c, nil
}
