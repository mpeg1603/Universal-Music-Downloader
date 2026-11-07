package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The Manager runs for as long as the app does. Every track of the enabled playlists is a Job.
// Queued jobs are searched (a few at a time) source by source in priority order; a job that
// found a match waits for a download slot. The number of download slots follows the
// bandwidth target. Everything can be changed while it runs: settings, playlists, sources,
// and each job (cancel, restart, prioritize).

type JobState int

const (
	JobQueued      JobState = iota // waiting to be searched
	JobSearching                   // looking through the sources
	JobReady                       // found, waiting for a download slot
	JobDownloading                 // downloading or converting
	JobWaiting                     // a source was unreachable or paused; tried again later
	JobDone                        // downloaded
	JobInLibrary                   // already in your library in good enough quality
	JobFailed                      // no source had it
	JobCancelled                   // cancelled by you
)

var jobStateNames = map[JobState]string{JobQueued: "queued", JobSearching: "searching", JobReady: "ready",
	JobDownloading: "downloading", JobWaiting: "retry later", JobDone: "downloaded", JobInLibrary: "in library",
	JobFailed: "not found", JobCancelled: "cancelled"}

func (s JobState) String() string { return jobStateNames[s] }

func (s JobState) active() bool { return s == JobSearching || s == JobReady || s == JobDownloading }

type Job struct {
	Track *Track
	idx   int // position in the library

	mu         sync.Mutex
	playlists  []string
	state      JobState
	task       *Task
	source     string // the source being searched or downloaded from
	tried      map[string]bool
	notes      []jobNote // what each source said, kept across rounds (see start)
	reasons    []string  // their texts, for the app
	lastErr    string
	lib        []*LibFile // the library's copies, best first
	needBetter bool       // a library copy exists but is below the threshold
	force      bool       // download even though it's in the library
	rec        Record     // when downloaded
	prio       int
	bump       int64
	cancel     context.CancelFunc
	cancelWhy  string
	gen        int
	retryAt    time.Time
	rounds     int
	pendingOn  string // Waiting for something from you for this source (Bandcamp's emailed link)
	started    time.Time
	converting bool // downloaded, being converted and tagged: a pause lets it finish
}

// jobNote is one source's answer for a job. Skips (paused or busy sources) only matter for
// the round they happened in; answers of sources that were done with stay in the report.
type jobNote struct {
	src  string
	text string
	skip bool
}

type JobView struct {
	Key        string
	Track      *Track
	Playlists  []string
	State      JobState
	Source     string
	Task       taskView
	Reason     string
	Reasons    []string
	Lib        *LibFile
	LibCount   int
	NeedBetter bool
	Rec        Record
	Prio       int
	Bumped     bool
	RetryAt    time.Time
	Started    time.Time
	idx        int
}

func (j *Job) view() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{Key: j.Track.Key, Track: j.Track, Playlists: j.playlists, State: j.state, Source: j.source,
		Reason: j.lastErr, Reasons: append([]string(nil), j.reasons...), LibCount: len(j.lib), NeedBetter: j.needBetter,
		Rec: j.rec, Prio: j.prio, Bumped: j.bump > 0, RetryAt: j.retryAt, Started: j.started, idx: j.idx}
	if len(j.lib) > 0 {
		v.Lib = j.lib[0]
	}
	if j.state.active() {
		v.Task = j.task.view()
	}
	return v
}

type sourceHealth struct {
	fails  int
	level  int
	until  time.Time
	reason string
	paused bool // by you

	searches, found, downloads, dlFails int
}

type Manager struct {
	cfgPath, dataDir string
	cfgMu            sync.RWMutex
	cfg              Config

	ctx    context.Context
	cancel context.CancelFunc

	tools  *Tools
	proxy  *LocalProxy
	index  *LibIndex
	state  *State
	deezer *Deezer

	libMu     sync.RWMutex
	full      *Library
	inputErrs map[string]string
	importing atomic.Bool

	jobsMu sync.Mutex
	jobs   map[string]*Job
	order  []*Job

	srcMu    sync.RWMutex
	sources  []Source
	srcErr   map[string]string
	srcNote  map[string]string // something to know about a working source (e.g. its cookies)
	health   map[string]*sourceHealth
	torrent  *TorrentSource
	torKey   string
	ytdlpBin string
	bcMail   *bandcampMail // Bandcamp's emailed download links
	cookies  *ytCookies    // your YouTube login, for yt-dlp
	cookieID string        // the settings cookies was made from

	searchGate *gate
	dlGate     *gate
	govMu      sync.Mutex // the three below
	dlTarget   int
	satSince   time.Time
	lastGrow   time.Time
	applyMu    sync.Mutex

	pathMu  sync.Mutex
	claimed map[string]string

	paused   atomic.Bool
	ready    atomic.Bool // library scanned and playlists imported: downloading may start
	wakeCh   chan struct{}
	bumpSeq  atomic.Int64
	dirty    atomic.Bool
	scanMu   sync.Mutex
	scanStop context.CancelFunc
	wg       sync.WaitGroup

	checkMu               sync.Mutex // one library check at a time
	checkDone, checkTotal atomic.Int64

	// onFinish is called when a job reaches a final state (for the headless output).
	onFinish func(JobView)
}

func newManager(cfgPath, dataDir string, cfg Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{cfgPath: cfgPath, dataDir: dataDir, cfg: cfg, ctx: ctx, cancel: cancel,
		jobs: map[string]*Job{}, srcErr: map[string]string{}, srcNote: map[string]string{}, health: map[string]*sourceHealth{},
		inputErrs: map[string]string{}, claimed: map[string]string{}, wakeCh: make(chan struct{}, 1),
		searchGate: newGate(cfg.SearchWorkers), dlGate: newGate(cfg.MaxDownloads), deezer: newDeezer(),
		bcMail: newBandcampMail(filepath.Join(dataDir, "bandcamp-emails.json"))}
	m.bcMail.onLink = m.bandcampLinkArrived
	for _, n := range allSources {
		m.health[healthKey(n)] = &sourceHealth{}
	}
	return m
}

// healthKey groups sources that share a fate: YouTube and YouTube Music get bot-checked together.
func healthKey(name string) string {
	if name == "youtube_music" {
		return "youtube"
	}
	return name
}

func (m *Manager) Config() Config {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.cfg.clone()
}

func (m *Manager) wake() {
	select {
	case m.wakeCh <- struct{}{}:
	default:
	}
}

// Start opens the state, finds the tools and starts working in the background.
func (m *Manager) Start() error {
	for _, d := range []string{"logs", "tmp", "cache"} {
		if err := os.MkdirAll(filepath.Join(m.dataDir, d), 0o755); err != nil {
			return err
		}
	}
	os.RemoveAll(filepath.Join(m.dataDir, "tmp"))
	st, err := openState(filepath.Join(m.dataDir, "state.jsonl"))
	if err != nil {
		return err
	}
	m.state = st
	if m.proxy, err = startLocalProxy(); err != nil {
		return fmt.Errorf("starting the local proxy: %w", err)
	}
	cfg := m.Config()
	router.SetInterface(cfg.DirectInterface)
	bw.SetCap(cfg.BandwidthMBps * 1e6)
	m.setTarget(m.initialTarget(cfg))
	tools, err := findBaseTools()
	if err != nil {
		return err
	}
	m.tools = tools
	m.index = newLibIndex(filepath.Join(m.dataDir, "library-index.json"), tools)
	m.index.restrict(cfg.LibraryDirs)
	if old, err := loadLibrary(filepath.Join(m.dataDir, "library.json")); err == nil {
		m.full = old
	} else {
		m.full = newLibrary()
	}
	// Shown right away; setup checks them against the library before anything is downloaded.
	m.makeJobs()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.setup()
	}()
	m.wg.Add(2)
	go func() { defer m.wg.Done(); m.dispatchLoop() }()
	go func() { defer m.wg.Done(); m.governor() }()
	return nil
}

// setup does the slow parts of starting: yt-dlp, sources, playlists, library scan.
func (m *Manager) setup() {
	cfg := m.Config()
	if needsYtDlp(cfg) {
		m.ensureYtDlp(cfg)
	}
	m.rebuildSources()
	m.importInputs()
	m.scanLibrary(false)
	m.ready.Store(true)
	logf("Ready: %s", m.summaryLine())
	m.wake()
}

func needsYtDlp(cfg Config) bool {
	for _, s := range cfg.Sources {
		if s.Enabled && (s.Name == "youtube" || s.Name == "youtube_music" || s.Name == "soundcloud") {
			return true
		}
	}
	return false
}

func (m *Manager) ensureYtDlp(cfg Config) {
	if cfg.YtDlpPath != "" {
		m.srcMu.Lock()
		m.ytdlpBin = expandHome(cfg.YtDlpPath)
		m.srcMu.Unlock()
		return
	}
	bin, err := ensureYtDlp(m.ctx, filepath.Join(defaultCacheDir(), "bin"), logf)
	if err != nil {
		if p, ok := lookPath("yt-dlp"); ok {
			logf("Couldn't fetch the latest yt-dlp (%v); using %s", err, p)
			bin = p
		} else {
			logf("yt-dlp is needed for YouTube/SoundCloud and couldn't be downloaded: %v", err)
		}
	}
	m.srcMu.Lock()
	m.ytdlpBin = bin
	m.srcMu.Unlock()
}

// Stop cancels everything in progress (those tracks are picked up again next time), writes
// the reports and closes the state.
func (m *Manager) Stop() {
	m.jobsMu.Lock()
	for _, j := range m.order {
		j.mu.Lock()
		if j.cancel != nil && j.cancelWhy == "" {
			j.cancelWhy = "stop"
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	m.cancel()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	m.srcMu.Lock()
	if m.torrent != nil {
		m.torrent.Close()
	}
	m.srcMu.Unlock()
	if m.proxy != nil {
		m.proxy.Close()
	}
	m.saveLibrary()
	m.writeReports()
	if m.state != nil {
		m.state.close()
	}
	os.RemoveAll(filepath.Join(m.dataDir, "tmp"))
}

// --- settings -------------------------------------------------------------------------------

// UpdateConfig changes the settings and applies the change right away.
func (m *Manager) UpdateConfig(change func(*Config)) error {
	m.cfgMu.Lock()
	old := m.cfg.clone()
	c := m.cfg.clone()
	change(&c)
	if err := c.validate(); err != nil {
		m.cfgMu.Unlock()
		return err
	}
	if err := checkDataDir(c, m.dataDir); err != nil {
		m.cfgMu.Unlock()
		return err
	}
	m.cfg = c
	err := saveConfig(m.cfgPath, c)
	m.cfgMu.Unlock()
	if err != nil {
		logf("Couldn't save the settings: %v", err)
	}
	go m.apply(old, c)
	return nil
}

// apply puts a settings change into effect. (old, c) tell what changed; the values set are
// always the current ones, so changes made in quick succession end up right whatever order
// their apply calls run in.
func (m *Manager) apply(old, c Config) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	cur := m.Config()
	if old.BandwidthMBps != c.BandwidthMBps || old.MaxDownloads != c.MaxDownloads {
		bw.SetCap(cur.BandwidthMBps * 1e6)
		m.setTarget(m.initialTarget(cur))
		if old.BandwidthMBps != c.BandwidthMBps {
			logf("Bandwidth: %s", describeCap(cur.BandwidthMBps))
		}
	}
	if old.SearchWorkers != c.SearchWorkers {
		m.searchGate.setLimit(cur.SearchWorkers)
	}
	if old.DirectInterface != c.DirectInterface {
		router.SetInterface(cur.DirectInterface)
	}
	if sourcesChanged(old, c) {
		if needsYtDlp(c) && (m.ytdlpPath() == "" || old.YtDlpPath != c.YtDlpPath) {
			m.ensureYtDlp(c)
		}
		m.rebuildSources()
	}
	if strings.Join(old.LibraryDirs, "\x00") != strings.Join(c.LibraryDirs, "\x00") {
		m.index.restrict(c.LibraryDirs)
		m.scanLibrary(true)
	} else if old.Threshold != c.Threshold {
		m.reevaluateLibrary()
	}
	if inputsChanged(old, c) {
		m.importInputs()
	} else if playlistsChanged(old, c) {
		m.rebuildJobs()
	}
	m.wake()
}

func describeCap(mbps float64) string {
	if mbps <= 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%.1f MB/s", mbps)
}

func (m *Manager) setTarget(n int) {
	m.govMu.Lock()
	m.dlTarget = n
	m.govMu.Unlock()
	m.dlGate.setLimit(n)
}

func (m *Manager) initialTarget(c Config) int {
	if c.BandwidthMBps <= 0 {
		return c.MaxDownloads
	}
	return min(2, c.MaxDownloads)
}

func sourcesChanged(a, b Config) bool {
	ja, jb := fmt.Sprint(a.Sources, a.Torznab, a.Cookies, a.CookiesFromBrowser, a.YtDlpPath, a.VKToken,
		a.YandexToken, a.Bandcamp, a.Format, a.TorrentTimeout, a.IndexerTimeout, a.YouTubePace, a.DirectInterface, a.Slskd),
		fmt.Sprint(b.Sources, b.Torznab, b.Cookies, b.CookiesFromBrowser, b.YtDlpPath, b.VKToken,
			b.YandexToken, b.Bandcamp, b.Format, b.TorrentTimeout, b.IndexerTimeout, b.YouTubePace, b.DirectInterface, b.Slskd)
	return ja != jb
}

func inputsChanged(a, b Config) bool { return fmt.Sprint(a.Inputs) != fmt.Sprint(b.Inputs) }

func playlistsChanged(a, b Config) bool { return fmt.Sprint(a.Playlists) != fmt.Sprint(b.Playlists) }

// --- sources --------------------------------------------------------------------------------

func (m *Manager) ytdlpPath() string {
	m.srcMu.RLock()
	defer m.srcMu.RUnlock()
	return m.ytdlpBin
}

// rebuildSources makes the sources from the settings. Jobs in progress keep the ones they have.
func (m *Manager) rebuildSources() {
	cfg := m.Config()
	cacheDir := filepath.Join(m.dataDir, "cache")
	errs := map[string]string{}
	var list []Source
	m.setupTorrentIfNeeded(cfg)
	m.srcMu.RLock()
	torrent, torErr := m.torrent, m.srcErr["torrent"]
	bin := m.ytdlpBin
	m.srcMu.RUnlock()
	tools := *m.tools
	tools.YtDlp = bin
	pace := time.Duration(cfg.YouTubePace * float64(time.Second))
	cookies := m.youtubeCookies(cfg, bin)
	for _, sc := range cfg.Sources {
		if !sc.Enabled {
			continue
		}
		route := normalizeRoute(sc.Route)
		yt := func() *YtDlp {
			return newYtDlp(&tools, cookies, m.proxy.URL(route))
		}
		switch sc.Name {
		case "bandcamp":
			var mail *bandcampMail
			if cfg.Bandcamp.Email != "" {
				f, problem := cfg.Bandcamp.fetcher()
				m.bcMail.configure(cfg.Bandcamp, route, f, problem)
				mail = m.bcMail
			}
			list = append(list, newBandcamp(cacheDir, cfg.Format, route, mail))
		case "archive":
			list = append(list, newArchive(cfg.Format, route))
		case "youtube_music":
			if bin == "" {
				errs[sc.Name] = "yt-dlp isn't available"
				continue
			}
			list = append(list, &YouTubeMusic{ytdlp: yt().paced(youtubePacer, pace), http: newRoutedHTTP("YouTube Music", 0, route)})
		case "youtube":
			if bin == "" {
				errs[sc.Name] = "yt-dlp isn't available"
				continue
			}
			list = append(list, &YouTube{ytdlp: yt().paced(youtubePacer, pace), http: newRoutedHTTP("YouTube", 0, route)})
		case "soundcloud":
			if bin == "" {
				errs[sc.Name] = "yt-dlp isn't available"
				continue
			}
			list = append(list, &SoundCloud{ytdlp: yt()})
		case "torrent":
			if torrent == nil {
				errs[sc.Name] = firstNonEmpty(torErr, "not set up")
				continue
			}
			list = append(list, torrent)
		case "vk":
			if cfg.VKToken == "" {
				errs[sc.Name] = "needs your VK token (Sources → VK)"
				continue
			}
			list = append(list, newVK(cfg.VKToken, route))
		case "yandex":
			if cfg.YandexToken == "" {
				errs[sc.Name] = "needs your Yandex Music token (Sources → Yandex Music)"
				continue
			}
			list = append(list, newYandex(cfg.YandexToken, route))
		case "audius":
			list = append(list, newAudius(route))
		case "slskd":
			if !cfg.Slskd.configured() {
				errs[sc.Name] = "needs your slskd address and API key (Sources → Soulseek)"
				continue
			}
			list = append(list, newSlskd(cfg.Slskd, cfg.Format, route))
		}
	}
	m.srcMu.Lock()
	m.sources = list
	for k, v := range errs {
		m.srcErr[k] = v
	}
	for _, n := range allSources {
		if _, bad := errs[n]; !bad && n != "torrent" {
			delete(m.srcErr, n)
		}
	}
	m.srcMu.Unlock()
	var labels []string
	for _, s := range list {
		labels = append(labels, s.Label())
	}
	logf("Sources, in order: %s", strings.Join(labels, ", "))
	m.wake()
}

// youtubeCookies returns the cookies yt-dlp gets, made anew when their settings changed; a new
// one is checked for a YouTube login in the background (the Sources tab shows what's wrong).
func (m *Manager) youtubeCookies(cfg Config, bin string) *ytCookies {
	id := strings.Join([]string{cfg.Cookies, cfg.CookiesFromBrowser, bin}, "\x00")
	m.srcMu.Lock()
	defer m.srcMu.Unlock()
	if m.cookieID == id {
		return m.cookies
	}
	m.cookieID = id
	m.cookies = newYtCookies(cfg.Cookies, cfg.CookiesFromBrowser, bin, filepath.Join(m.dataDir, "tmp", "cookies"))
	delete(m.srcNote, "youtube")
	delete(m.srcNote, "youtube_music")
	if c := m.cookies; c != nil {
		if cfg.Cookies != "" && cfg.CookiesFromBrowser != "" {
			logf("YouTube: using %s; the cookies file %s is not used while a browser is set", c.describe(), cfg.Cookies)
		}
		go func() {
			ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
			defer cancel()
			problem := c.check(ctx)
			m.srcMu.Lock()
			if m.cookies == c {
				m.srcNote["youtube"], m.srcNote["youtube_music"] = problem, problem
			}
			m.srcMu.Unlock()
			if problem != "" {
				logf("YouTube cookies: %s", problem)
			} else {
				logf("YouTube: logged in with %s", c.describe())
			}
		}()
	}
	return m.cookies
}

func (m *Manager) setupTorrentIfNeeded(cfg Config) {
	sc := cfg.source("torrent")
	key := fmt.Sprint(cfg.Torznab, cfg.TorrentTimeout, cfg.IndexerTimeout, cfg.Format)
	m.srcMu.RLock()
	same := m.torKey == key && (m.torrent != nil || m.srcErr["torrent"] != "")
	m.srcMu.RUnlock()
	if !sc.Enabled || same {
		return
	}
	m.srcMu.Lock()
	if m.torrent != nil {
		m.torrent.Close()
		m.torrent = nil
	}
	m.torKey = key
	delete(m.srcErr, "torrent")
	m.srcMu.Unlock()
	if !cfg.Torznab.configured() {
		m.setSrcErr("torrent", "needs your Jackett or Prowlarr address and API key (Sources → Torrent)")
		return
	}
	if m.tools.Aria2c == "" {
		m.setSrcErr("torrent", "the aria2c command wasn't found; install the aria2 package")
		return
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	feeds, err := resolveFeeds(ctx, cfg.Torznab, time.Duration(cfg.IndexerTimeout))
	if err != nil {
		m.setSrcErr("torrent", err.Error())
		logf("Torrent: %v", err)
		return
	}
	src := newTorrentSource(feeds, m.tools.Aria2c, filepath.Join(m.dataDir, "cache"), cfg.Format,
		time.Duration(cfg.TorrentTimeout), time.Duration(cfg.IndexerTimeout))
	m.srcMu.Lock()
	m.torrent = src
	m.srcMu.Unlock()
	logf("Torrent: %d indexers", len(feeds))
}

func (m *Manager) setSrcErr(name, msg string) {
	m.srcMu.Lock()
	m.srcErr[name] = msg
	m.srcMu.Unlock()
}

func (m *Manager) currentSources() []Source {
	m.srcMu.RLock()
	defer m.srcMu.RUnlock()
	return append([]Source(nil), m.sources...)
}

// healthOf returns a source's health record; m.srcMu must be held (for writing).
func (m *Manager) healthOf(name string) *sourceHealth {
	k := healthKey(name)
	h := m.health[k]
	if h == nil {
		h = &sourceHealth{}
		m.health[k] = h
	}
	return h
}

// cooling reports whether a source is paused (by you, or after being blocked) and until when.
func (m *Manager) cooling(name string) (bool, time.Time, string) {
	m.srcMu.Lock()
	defer m.srcMu.Unlock()
	h := m.healthOf(name)
	if h.paused {
		return true, time.Time{}, "paused by you"
	}
	if time.Now().Before(h.until) {
		return true, h.until, h.reason
	}
	return false, time.Time{}, ""
}

var backoffSteps = []time.Duration{2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

// recordResult keeps track of how a source is doing. A blocked source (bot check, rate limit,
// captcha) pauses for a while, longer each time; one that keeps failing to respond, too.
func (m *Manager) recordResult(src Source, err error, search bool) {
	m.srcMu.Lock()
	defer m.srcMu.Unlock()
	h := m.healthOf(src.Name())
	if search {
		h.searches++
	} else if err == nil {
		h.downloads++
	} else {
		h.dlFails++
	}
	switch {
	case err == nil:
		h.fails = 0
		if h.level > 0 && time.Since(h.until) > 30*time.Minute {
			h.level--
		}
	case isBlocked(err):
		if time.Now().Before(h.until) {
			return // already paused
		}
		d := backoffSteps[min(h.level, len(backoffSteps)-1)]
		h.level++
		h.until, h.reason = time.Now().Add(d), err.Error()
		logf("%s: %s; pausing it for %s", src.Label(), err, fmtWait(d))
	case isUnavailable(err):
		if time.Now().Before(h.until) {
			return // already paused: requests that were under way don't make it longer
		}
		h.fails++
		if h.fails >= 5 {
			d := backoffSteps[min(h.level, len(backoffSteps)-1)]
			h.level++
			h.fails = 0
			h.until, h.reason = time.Now().Add(d), err.Error()
			logf("%s failed to respond 5 times in a row (%v); pausing it for %s", src.Label(), err, fmtWait(d))
		}
	}
}

// --- scheduling ------------------------------------------------------------------------------

func (m *Manager) dispatchLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.wakeCh:
		case <-tick.C:
			m.requeueWaiting()
		}
		m.dispatch()
	}
}

func (m *Manager) readyMax() int {
	_, limit, _ := m.dlGate.stats()
	return limit*2 + 2
}

func (m *Manager) dispatch() {
	if m.paused.Load() || !m.ready.Load() || m.ctx.Err() != nil {
		return
	}
	_, limit, _ := m.searchGate.stats()
	// Jobs counted as searching include ones about to take a search slot.
	for m.countState(JobSearching) < limit && m.countState(JobReady) < m.readyMax() {
		j := m.nextQueued()
		if j == nil {
			return
		}
		m.start(j)
	}
}

func (m *Manager) countState(s JobState) int {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	n := 0
	for _, j := range m.order {
		j.mu.Lock()
		if j.state == s {
			n++
		}
		j.mu.Unlock()
	}
	return n
}

// nextQueued picks the queued job to search next: ones you prioritized, then by playlist
// priority, then in playlist order.
func (m *Manager) nextQueued() *Job {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	var best *Job
	var bb int64
	var bp, bi int
	for _, j := range m.order {
		j.mu.Lock()
		ok := j.state == JobQueued
		b, p, i := j.bump, j.prio, j.idx
		j.mu.Unlock()
		if !ok {
			continue
		}
		if best == nil || b > bb || (b == bb && (p > bp || (p == bp && i < bi))) {
			best, bb, bp, bi = j, b, p, i
		}
	}
	return best
}

func (j *Job) priority() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return int64(j.prio)*1_000_000_000 + j.bump*1000
}

func (m *Manager) start(j *Job) {
	ctx, cancel := context.WithCancel(m.ctx)
	j.mu.Lock()
	j.gen++
	gen := j.gen
	j.state, j.cancel, j.cancelWhy, j.started = JobSearching, cancel, "", time.Now()
	j.task = &Task{Track: j.Track, started: time.Now()}
	if j.tried == nil {
		j.tried = map[string]bool{}
	}
	var keep []jobNote
	for _, n := range j.notes {
		if j.tried[n.src] && !n.skip {
			keep = append(keep, n)
		}
	}
	j.notes, j.reasons = keep, nil
	for _, n := range keep {
		j.reasons = append(j.reasons, n.text)
	}
	task := j.task
	j.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		res := m.process(withTask(ctx, task), j, task, gen)
		m.finish(j, gen, res)
	}()
}

type result struct {
	state     JobState
	reason    string
	transient bool
	counted   bool // a source failed (not just paused or busy): counts toward giving up
	retryAt   time.Time
	pendingOn string // Waiting on you, not on a source: no give-up count
	rec       Record
	aborted   bool
}

func (j *Job) setState(gen int, s JobState, source string) {
	j.mu.Lock()
	if j.gen == gen {
		j.state, j.source = s, source
	}
	j.mu.Unlock()
}

func (j *Job) addNote(src, text string, skip bool) {
	j.mu.Lock()
	j.notes = append(j.notes, jobNote{src, text, skip})
	j.reasons = append(j.reasons, text)
	j.mu.Unlock()
}

// noteText is the report of everything the sources said.
func (j *Job) noteText() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var parts []string
	for _, n := range j.notes {
		parts = append(parts, n.text)
	}
	return strings.Join(parts, "; ")
}

// process takes one job through the sources until it's downloaded or none is left.
func (m *Manager) process(ctx context.Context, j *Job, task *Task, gen int) result {
	j.mu.Lock()
	t := j.Track
	j.mu.Unlock()
	cfg := m.Config()
	workdir := filepath.Join(m.dataDir, "tmp", fmt.Sprintf("%s.%d", t.Key, gen))
	os.RemoveAll(workdir)
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return result{state: JobFailed, reason: err.Error(), transient: true}
	}
	defer os.RemoveAll(workdir)

	if cfg.Enrich && needsEnrichment(t) {
		// On a copy: the app shows the track meanwhile.
		task.setStage("looking up details on Deezer")
		c := *t
		if err := m.deezer.enrich(ctx, &c); err == nil || !isUnavailable(err) {
			c.Enriched = true
			t = &c
			j.mu.Lock()
			j.Track = t
			j.mu.Unlock()
			m.libMu.Lock()
			if m.full.Tracks[t.Key] != nil {
				m.full.Tracks[t.Key] = t
			}
			m.libMu.Unlock()
			m.dirty.Store(true)
		}
	}
	j.mu.Lock()
	needBetter := j.needBetter && !j.force
	var libQ Quality
	if len(j.lib) > 0 {
		libQ = j.lib[0].Q
	}
	j.mu.Unlock()

	transient, counted := false, false
	var retryAt time.Time
	skipped := map[string]bool{}
	failed := map[string]bool{} // candidates that didn't work out, not to be tried again in this run
	var name string
	note := func(r string) { j.addNote(name, r, false) }
	for {
		if ctx.Err() != nil {
			return result{aborted: true}
		}
		src := m.nextSource(j, needBetter, libQ, &transient, &retryAt, skipped)
		if src == nil {
			break
		}
		var label string
		name, label = src.Name(), src.Label()
		j.setState(gen, JobSearching, name)
		task.setStage("waiting to search " + label)
		if err := m.searchGate.acquire(ctx, j.priority()+1); err != nil {
			return result{aborted: true}
		}
		if cool, _, _ := m.cooling(name); cool { // paused while this job waited for its turn
			m.searchGate.release()
			continue
		}
		task.setStage("searching " + label)
		cands, err := src.Search(ctx, t)
		m.searchGate.release()
		if ctx.Err() != nil {
			return result{aborted: true}
		}
		m.recordResult(src, err, true)
		if err != nil {
			note(label + ": " + err.Error())
			if isUnavailable(err) {
				transient, counted = true, true
				m.markTried(j, name, false)
			} else {
				m.markTried(j, name, true)
			}
			continue
		}
		accepted, rejected := rank(t, cands)
		if needBetter {
			var keep []Match
			for _, mt := range accepted {
				if mightBeBetter(mt.Cand.Quality, libQ) {
					keep = append(keep, mt)
				}
			}
			if len(keep) == 0 && len(accepted) > 0 {
				note(fmt.Sprintf("%s: only %s, not better than your library copy (%s)", label, accepted[0].Cand.Quality, libQ))
				m.markTried(j, name, true)
				continue
			}
			accepted = keep
		}
		debugf("%s / %s: %d accepted, %d rejected", t, name, len(accepted), len(rejected))
		if len(accepted) == 0 {
			note(label + ": " + describeRejection(rejected))
			m.markTried(j, name, true)
			continue
		}
		m.srcMu.Lock()
		m.healthOf(name).found++
		m.srcMu.Unlock()
		srcTransient, again := false, false
		for _, mt := range accepted[:min(len(accepted), max(1, cfg.TriesPerSrc))] {
			ck := mt.Cand.Source + "\x00" + mt.Cand.URL + "\x00" + mt.Cand.Ref
			if failed[ck] {
				continue
			}
			j.setState(gen, JobReady, name)
			task.setStage("waiting for a download slot")
			m.wake()
			if err := m.dlGate.acquire(ctx, j.priority()); err != nil {
				return result{aborted: true}
			}
			var once sync.Once
			release := func() { once.Do(m.dlGate.release) }
			task.mu.Lock()
			task.onPark = release
			task.mu.Unlock()
			j.setState(gen, JobDownloading, name)
			task.setStage("downloading from " + label)
			if ts, ok := src.(*TorrentSource); ok {
				m.fetchAlong(ts, j, mt.Cand)
			}
			raw, err := src.Download(ctx, mt.Cand, t, workdir)
			release()
			m.wake()
			var rec Record
			if err == nil && ctx.Err() == nil {
				task.setStage("converting")
				j.setConverting(true)
				rec, err = m.finalize(ctx, j, raw, mt, workdir, cfg)
				j.setConverting(false)
			}
			if err == nil && rec.Status == "downloaded" { // done, even if stopped meanwhile
				m.recordResult(src, nil, false)
				debugf("%s <- %s %s (score %.0f)", t, name, mt.Cand.URL, mt.Score)
				return result{state: JobDone, rec: rec}
			}
			if ctx.Err() != nil {
				return result{aborted: true}
			}
			if wait, ok := isPending(err); ok {
				note(label + ": " + err.Error())
				return result{state: JobWaiting, reason: label + ": " + err.Error(), transient: true,
					retryAt: time.Now().Add(wait), pendingOn: name}
			}
			m.recordResult(src, err, false)
			note(label + ": " + err.Error())
			if isUnavailable(err) {
				srcTransient, transient, counted = true, true, true
				break
			}
			failed[ck] = true
			if mt.Cand.Unsearched { // a shortcut that didn't work out: search properly
				again = true
				break
			}
		}
		if !again {
			m.markTried(j, name, !srcTransient)
		}
	}
	reason := j.noteText()
	if reason == "" {
		reason = "no sources enabled"
		if len(m.currentSources()) > 0 {
			reason = "no source left to try (r searches all of them again)"
		}
	}
	if transient {
		return result{state: JobWaiting, reason: reason, transient: true, counted: counted, retryAt: retryAt}
	}
	return result{state: JobFailed, reason: reason}
}

func (j *Job) setConverting(v bool) {
	j.mu.Lock()
	j.converting = v
	j.mu.Unlock()
}

func (m *Manager) markTried(j *Job, name string, done bool) {
	if done {
		j.mu.Lock()
		j.tried[name] = true
		j.mu.Unlock()
	}
}

// busyLimit: a source whose requests would wait longer than this for their turn (its pace
// after rate limits, see rateLimiter) is skipped for now, like a paused one, instead of
// holding up searches.
const busyLimit = 20 * time.Second

// nextSource returns the first source in priority order the job hasn't been through yet,
// noting the ones skipped because they're paused or busy.
func (m *Manager) nextSource(j *Job, needBetter bool, libQ Quality, transient *bool, retryAt *time.Time, skipped map[string]bool) Source {
	j.mu.Lock()
	tried := make(map[string]bool, len(j.tried))
	for k, v := range j.tried {
		tried[k] = v
	}
	j.mu.Unlock()
	for _, src := range m.currentSources() {
		name := src.Name()
		if tried[name] {
			continue
		}
		if needBetter {
			if c, ok := src.(qualityCapper); ok && !better(c.MaxQuality(), libQ) {
				j.addNote(name, fmt.Sprintf("%s: offers at most %s, not better than your library copy (%s)", src.Label(), c.MaxQuality(), libQ), false)
				m.markTried(j, name, true)
				continue
			}
		}
		later := func(until time.Time) {
			if !until.IsZero() && (retryAt.IsZero() || until.Before(*retryAt)) {
				*retryAt = until
			}
			skipped[src.Label()] = true
			*transient = true
		}
		if cool, until, why := m.cooling(name); cool {
			if !skipped[src.Label()] {
				when := "for now"
				if !until.IsZero() {
					when = "until " + until.Format("15:04")
				}
				j.addNote(name, fmt.Sprintf("%s: paused %s (%s)", src.Label(), when, why), true)
				later(until)
			}
			*transient = true
			continue
		}
		if b, ok := src.(interface{ busy() time.Duration }); ok {
			if wait := b.busy(); wait > busyLimit || skipped[src.Label()] {
				if !skipped[src.Label()] {
					j.addNote(name, fmt.Sprintf("%s: busy (requests wait %s for their turn); tried again later", src.Label(), fmtWait(wait)), true)
					later(time.Now().Add(wait))
				}
				continue
			}
		}
		return src
	}
	return nil
}

func (m *Manager) finish(j *Job, gen int, res result) {
	j.mu.Lock()
	if j.gen != gen { // restarted or removed meanwhile
		j.mu.Unlock()
		return
	}
	why := j.cancelWhy
	j.cancel, j.cancelWhy = nil, ""
	key := j.Track.Key
	switch {
	case res.aborted && why == "cancel":
		j.state, j.lastErr = JobCancelled, "cancelled by you"
		j.mu.Unlock()
		m.state.markCancelled(key)
	case res.aborted && why == "pause": // first in line when you resume
		j.state, j.bump = JobQueued, m.bumpSeq.Add(1)
		j.mu.Unlock()
	case res.aborted:
		j.state = JobQueued // app stopping, or restarted
		j.mu.Unlock()
	case res.state == JobDone:
		j.state, j.rec, j.lastErr, j.rounds = JobDone, res.rec, "", 0
		j.mu.Unlock()
	case res.state == JobWaiting && res.pendingOn != "":
		j.state, j.lastErr, j.retryAt, j.pendingOn = JobWaiting, res.reason, res.retryAt, res.pendingOn
		j.mu.Unlock()
		m.state.markFailed(key, res.reason, true)
	case res.state == JobWaiting:
		// Only rounds in which a source failed count toward giving up: one that just waits
		// for a paused source waits as long as it takes.
		if res.counted {
			j.rounds++
		}
		if j.rounds > 8 {
			j.state, j.lastErr = JobFailed, res.reason+" (gave up after several tries; restart it to try again)"
			j.mu.Unlock()
			m.state.markFailed(key, res.reason, true)
			break
		}
		wait := time.Duration(math.Min(float64(5*time.Minute)*math.Pow(2, float64(max(j.rounds, 1)-1)), float64(time.Hour)))
		at := time.Now().Add(wait)
		if !res.retryAt.IsZero() && res.retryAt.Before(at) {
			// Spread out: the tracks waiting for a source shouldn't all come back the same second.
			at = res.retryAt.Add(5*time.Second + time.Duration(rand.Int64N(int64(2*time.Minute))))
		}
		j.state, j.lastErr, j.retryAt, j.pendingOn = JobWaiting, res.reason, at, ""
		j.mu.Unlock()
		m.state.markFailed(key, res.reason, true)
	default:
		j.state, j.lastErr = JobFailed, res.reason
		j.mu.Unlock()
		m.state.markFailed(key, res.reason, false)
	}
	m.dropTorrentHint(key)
	m.dirty.Store(true)
	if m.onFinish != nil && !res.aborted {
		m.onFinish(j.view())
	}
	m.wake()
}

// fetchAlong looks for other tracks still to be downloaded in the torrent of c, which is about
// to be downloaded for self. They're handed that torrent, so they skip the search and their
// files go into the same download, and they move to the front of the queue to get there in
// time. Only tracks of the same album or by the same artists are looked into: that's what
// torrents hold.
func (m *Manager) fetchAlong(ts *TorrentSource, self *Job, c Candidate) {
	if c.Unsearched {
		return // found along with another track, which looked already
	}
	self.mu.Lock()
	from := self.Track
	self.mu.Unlock()
	type sibling struct {
		j          *Job
		t          *Track
		needBetter bool
		libQ       Quality
	}
	var sibs []sibling
	m.jobsMu.Lock()
	for _, j := range m.order {
		if j == self {
			continue
		}
		j.mu.Lock()
		if !j.tried[ts.Name()] && related(j.Track, from) &&
			(j.state == JobQueued || j.state == JobWaiting && j.pendingOn == "" || j.state == JobSearching && j.source != ts.Name()) {
			s := sibling{j: j, t: j.Track, needBetter: j.needBetter && !j.force}
			if len(j.lib) > 0 {
				s.libQ = j.lib[0].Q
			}
			sibs = append(sibs, s)
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	var tracks []*Track
	for _, s := range sibs {
		if !ts.hinted(s.t.Key) {
			tracks = append(tracks, s.t)
		}
	}
	if len(tracks) == 0 {
		return
	}
	found := ts.alongside(c, from, tracks)
	n := 0
	for _, s := range sibs {
		cand, ok := found[s.t.Key]
		if !ok || s.needBetter && !mightBeBetter(cand.Quality, s.libQ) {
			continue
		}
		ts.hint(s.t.Key, cand)
		s.j.mu.Lock()
		if s.j.state == JobQueued || s.j.state == JobWaiting && s.j.pendingOn == "" {
			s.j.state, s.j.retryAt = JobQueued, time.Time{}
			s.j.bump = m.bumpSeq.Add(1)
		}
		s.j.mu.Unlock()
		debugf("%s: also in “%s” (%s)", s.t, c.Album, cand.Title)
		n++
	}
	if n > 0 {
		logf("Torrent “%s” also has %d other tracks to download; they go into the same download", c.Album, n)
		m.dirty.Store(true)
		m.wake()
	}
}

// related is whether two tracks may well be in the same torrent: same album, or an artist in
// common. (Plain comparisons: this runs over the whole queue.)
func related(a, b *Track) bool {
	if a.Album != "" && strings.EqualFold(a.Album, b.Album) {
		return true
	}
	for _, x := range a.Artists {
		for _, y := range b.Artists {
			if strings.EqualFold(x, y) {
				return true
			}
		}
		if b.AlbumArtist != "" && strings.EqualFold(x, b.AlbumArtist) {
			return true
		}
	}
	return false
}

// dropTorrentHint forgets the torrent a track was handed (fetchAlong) if it didn't use it.
func (m *Manager) dropTorrentHint(key string) {
	for _, src := range m.currentSources() {
		if ts, ok := src.(*TorrentSource); ok {
			ts.dropHint(key)
		}
	}
}

// bandcampLinkArrived lets the tracks waiting for one of Bandcamp's emailed links go on.
func (m *Manager) bandcampLinkArrived() {
	m.jobsMu.Lock()
	n := 0
	for _, j := range m.order {
		j.mu.Lock()
		if j.state == JobWaiting && j.pendingOn == "bandcamp" {
			j.state, j.retryAt, j.pendingOn = JobQueued, time.Time{}, ""
			n++
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	if n > 0 {
		debugf("bandcamp: a download link arrived; %d waiting tracks go on", n)
		m.dirty.Store(true)
		m.wake()
	}
}

// AddBandcampLinks takes Bandcamp download links pasted from its emails (the links, or whole
// emails); the tracks waiting for them go on.
func (m *Manager) AddBandcampLinks(text string) (found, added int, errs []string) {
	ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
	defer cancel()
	found, added, errs = m.bcMail.AddText(ctx, text)
	logf("Bandcamp links pasted: %d found, %d new", found, added)
	for _, e := range errs {
		logf("Bandcamp link %s", e)
	}
	return
}

func (m *Manager) requeueWaiting() {
	now := time.Now()
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	for _, j := range m.order {
		j.mu.Lock()
		if j.state == JobWaiting && !j.retryAt.IsZero() && now.After(j.retryAt) {
			j.state, j.retryAt = JobQueued, time.Time{}
		}
		j.mu.Unlock()
	}
}

// governor adjusts the bandwidth split and the number of downloads once a second, and writes
// the reports now and then.
func (m *Manager) governor() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastAria, lastReport := -1.0, time.Now()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-tick.C:
		}
		m.srcMu.RLock()
		tor := m.torrent
		m.srcMu.RUnlock()
		torSpeed := 0.0
		if tor != nil {
			torSpeed = tor.speed()
		}
		ariaLimit := bw.sample(torSpeed)
		if tor != nil && math.Abs(ariaLimit-lastAria) > max(lastAria*0.05, 32<<10) {
			if tor.setLimit(ariaLimit) {
				lastAria = ariaLimit
			}
		}
		m.adjustDownloads()
		if m.dirty.Load() && time.Since(lastReport) > 2*time.Minute {
			lastReport = time.Now()
			m.dirty.Store(false)
			m.saveLibrary()
			m.writeReports()
		}
	}
}

// adjustDownloads moves the number of parallel downloads toward the bandwidth target: one more
// while the total stays clearly below it and every slot is busy, one fewer once it's been
// saturated for a while (fewer, faster downloads finish tracks sooner).
func (m *Manager) adjustDownloads() {
	cfg := m.Config()
	own, tor, capBps := bw.Speeds()
	used, limit, waiting := m.dlGate.stats()
	m.govMu.Lock()
	defer m.govMu.Unlock()
	if capBps <= 0 {
		if limit != cfg.MaxDownloads {
			m.dlTarget = cfg.MaxDownloads
			m.dlGate.setLimit(m.dlTarget)
		}
		return
	}
	total := own + tor
	switch {
	case total < 0.85*capBps && waiting > 0 && used >= limit && limit < cfg.MaxDownloads && time.Since(m.lastGrow) > 2*time.Second:
		m.dlTarget = limit + 1
		m.lastGrow = time.Now()
		m.satSince = time.Time{}
	case total >= 0.97*capBps && limit > 2:
		if m.satSince.IsZero() {
			m.satSince = time.Now()
		} else if time.Since(m.satSince) > 20*time.Second {
			m.dlTarget = limit - 1
			m.satSince = time.Now()
		}
	default:
		m.satSince = time.Time{}
	}
	if limit > cfg.MaxDownloads {
		m.dlTarget = cfg.MaxDownloads
	}
	if m.dlTarget != limit {
		m.dlGate.setLimit(m.dlTarget)
	}
}

// --- finishing a download ------------------------------------------------------------------

// finalize checks the downloaded file, converts and tags it and moves it into the downloads
// folder (never into the library).
func (m *Manager) finalize(ctx context.Context, j *Job, raw string, mt Match, workdir string, cfg Config) (Record, error) {
	j.mu.Lock()
	tc := *j.Track // gaps are filled on a copy; the app shows the job's track meanwhile
	j.mu.Unlock()
	t, c := &tc, mt.Cand
	codec, length, q, err := probe(ctx, m.tools, raw)
	if err != nil {
		return Record{}, err
	}
	if length > 0 && t.DurationMs > 0 && math.Abs(length-t.Duration()) > durationTolerance(t.Duration()) {
		return Record{}, failf("%s turned out to be %s long instead of %s (preview clip or different cut)",
			c.URL, fmtDuration(length), fmtDuration(t.Duration()))
	}
	if t.Album == "" {
		t.Album = firstNonEmpty(c.Album, t.Title)
	}
	if t.DurationMs == 0 && length > 0 {
		t.DurationMs = int(length * 1000)
	}
	ext := targetExtension(cfg.Format, codec)
	converted := filepath.Join(workdir, "final"+ext)
	cover := ""
	if t.CoverURL != "" {
		cover = coverCache(filepath.Join(m.dataDir, "tmp", "covers")).get(ctx, t.CoverURL)
	}
	if err := convertAndTag(ctx, m.tools, raw, converted, codec, t, cover); err != nil {
		return Record{}, err
	}
	if cfg.Format != "original" {
		if _, _, q2, err := probe(ctx, m.tools, converted); err == nil {
			q = q2
		}
	}
	j.mu.Lock()
	needBetter := j.needBetter && !j.force
	var lib *LibFile
	if len(j.lib) > 0 {
		lib = j.lib[0]
	}
	j.mu.Unlock()
	if needBetter && lib != nil && !better(q, lib.Q) {
		return Record{}, failf("got %s, not better than your library copy (%s)", q, lib.Q)
	}
	dest, err := m.place(converted, relativePath(t, cfg.Template, ext), t.Key, cfg)
	if err != nil {
		return Record{}, err
	}
	rec := Record{Key: t.Key, Status: "downloaded", Path: dest, Quality: q, Source: c.Source, SourceURL: c.URL,
		SourceTitle: c.Title, SourceArtist: c.Artist, SourceArtistURL: c.ArtistURL, SourceAlbum: c.Album, Score: mt.Score}
	if lib != nil {
		rec.LibPath, rec.LibQuality = lib.Path, lib.Q
	}
	m.state.put(rec)
	return rec, nil
}

// place moves a finished file into the downloads folder under rel, numbering it if the name is
// taken. It never replaces an existing file, and refuses to write into a library folder.
func (m *Manager) place(src, rel, key string, cfg Config) (string, error) {
	for _, d := range cfg.LibraryDirs {
		if isWithin(cfg.OutputDir, d) {
			return "", failf("the downloads folder is inside your library; refusing to write there")
		}
	}
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	m.pathMu.Lock()
	defer m.pathMu.Unlock()
	for n := 1; n < 1000; n++ {
		candidate := rel
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
		}
		dest := filepath.Join(cfg.OutputDir, filepath.FromSlash(candidate))
		if underAny(dest, cfg.LibraryDirs) {
			return "", failf("refusing to write into your library (%s)", dest)
		}
		if owner := m.claimed[dest]; owner != "" && owner != key {
			continue
		}
		if owner := m.state.ownerOf(dest); owner != "" && owner != key {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		err := moveNoReplace(src, dest)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		m.claimed[dest] = key
		return dest, nil
	}
	return "", failf("couldn't find a free file name for %s", rel)
}

// moveNoReplace moves src to dst, failing with os.ErrExist if dst exists.
func moveNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return os.Remove(src)
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

var (
	coversMu sync.Mutex
	covers   = map[string]*CoverCache{}
)

func coverCache(dir string) *CoverCache {
	coversMu.Lock()
	defer coversMu.Unlock()
	if covers[dir] == nil {
		covers[dir] = newCoverCache(dir)
	}
	return covers[dir]
}

// --- controls (from the UI) -------------------------------------------------------------------

func (m *Manager) job(key string) *Job {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	return m.jobs[key]
}

// Cancel stops a track; it stays cancelled until you restart it.
func (m *Manager) Cancel(key string) {
	j := m.job(key)
	if j == nil {
		return
	}
	j.mu.Lock()
	switch {
	case j.cancel != nil:
		j.cancelWhy = "cancel"
		j.cancel()
		j.mu.Unlock()
	case j.state == JobQueued || j.state == JobWaiting || j.state == JobFailed:
		j.state, j.lastErr = JobCancelled, "cancelled by you"
		j.mu.Unlock()
		m.state.markCancelled(key)
		m.dropTorrentHint(key)
	default:
		j.mu.Unlock()
	}
	m.wake()
}

// Restart searches for a track again from the first source, ahead of the others. A track
// that's in your library is downloaded anyway; a downloaded one is downloaded again.
func (m *Manager) Restart(key string) {
	j := m.job(key)
	if j == nil {
		return
	}
	j.mu.Lock()
	if j.cancel != nil {
		j.cancelWhy = "restart"
		j.cancel()
		j.cancel = nil
	}
	if j.state == JobInLibrary {
		j.force = true
	}
	j.gen++ // results of the run in progress are ignored
	j.state, j.tried, j.notes, j.reasons, j.lastErr, j.rounds, j.retryAt = JobQueued, map[string]bool{}, nil, nil, "", 0, time.Time{}
	j.bump = m.bumpSeq.Add(1)
	j.mu.Unlock()
	m.wake()
}

// Prioritize moves a queued track to the front.
func (m *Manager) Prioritize(key string) {
	if j := m.job(key); j != nil {
		j.mu.Lock()
		j.bump = m.bumpSeq.Add(1)
		if j.state == JobWaiting {
			j.state, j.retryAt = JobQueued, time.Time{}
		}
		j.mu.Unlock()
		m.wake()
	}
}

// RetryFailed queues every track that wasn't found (or was cancelled) again.
func (m *Manager) RetryFailed(includeCancelled bool) int {
	m.jobsMu.Lock()
	var keys []string
	for _, j := range m.order {
		j.mu.Lock()
		if j.state == JobFailed || j.state == JobWaiting || (includeCancelled && j.state == JobCancelled) {
			keys = append(keys, j.Track.Key)
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	for _, k := range keys {
		j := m.job(k)
		j.mu.Lock()
		j.state, j.tried, j.notes, j.reasons, j.rounds, j.retryAt = JobQueued, map[string]bool{}, nil, nil, 0, time.Time{}
		j.mu.Unlock()
	}
	m.wake()
	return len(keys)
}

// SetPaused pauses (or resumes) everything: searches and downloads in progress stop and go
// back to the front of the queue, where they continue from the source they were at when you
// resume; torrents are paused in aria2c, keeping what they have. Tracks being converted
// finish.
func (m *Manager) SetPaused(p bool) {
	if m.paused.Swap(p) == p {
		return
	}
	m.srcMu.RLock()
	tor := m.torrent
	m.srcMu.RUnlock()
	if !p {
		if tor != nil {
			tor.pause(false)
		}
		logf("Resumed")
		m.wake()
		return
	}
	if tor != nil {
		tor.pause(true)
	}
	n := 0
	m.jobsMu.Lock()
	for _, j := range m.order {
		j.mu.Lock()
		keep := j.converting || j.state == JobDownloading && j.source == "torrent"
		if j.cancel != nil && j.cancelWhy == "" && !keep {
			j.cancelWhy = "pause"
			j.cancel()
			n++
		}
		j.mu.Unlock()
	}
	m.jobsMu.Unlock()
	logf("Paused: %d searches and downloads stopped (they continue first when you resume), torrents paused", n)
	m.wake()
}

func (m *Manager) Paused() bool { return m.paused.Load() }

// PauseSource stops (or resumes) using a source.
func (m *Manager) PauseSource(name string, paused bool) {
	m.srcMu.Lock()
	h := m.healthOf(name)
	h.paused = paused
	if !paused {
		h.until, h.level, h.fails = time.Time{}, 0, 0
	}
	m.srcMu.Unlock()
	m.wake()
}

// --- status for the UI ------------------------------------------------------------------------

func (m *Manager) Jobs() []JobView {
	m.jobsMu.Lock()
	list := append([]*Job(nil), m.order...)
	m.jobsMu.Unlock()
	out := make([]JobView, len(list))
	for i, j := range list {
		out[i] = j.view()
	}
	return out
}

func (m *Manager) JobView(key string) (JobView, bool) {
	if j := m.job(key); j != nil {
		return j.view(), true
	}
	return JobView{}, false
}

type SourceStatus struct {
	Name, Label, Route string
	Enabled            bool
	Problem            string // why it can't be used
	Note               string // something to know while it works (e.g. its cookies have no login)
	Paused             bool
	Until              time.Time
	Reason             string
	Searches, Found    int
	Downloads, DlFails int
}

func (m *Manager) SourceStatus() []SourceStatus {
	cfg := m.Config()
	m.srcMu.Lock()
	defer m.srcMu.Unlock()
	var out []SourceStatus
	for _, sc := range cfg.Sources {
		h := m.healthOf(sc.Name)
		s := SourceStatus{Name: sc.Name, Label: sourceLabels[sc.Name], Route: normalizeRoute(sc.Route), Enabled: sc.Enabled,
			Problem: m.srcErr[sc.Name], Note: m.srcNote[sc.Name], Paused: h.paused, Searches: h.searches, Found: h.found, Downloads: h.downloads, DlFails: h.dlFails}
		if time.Now().Before(h.until) {
			s.Until, s.Reason = h.until, h.reason
		}
		out = append(out, s)
	}
	return out
}

func (m *Manager) TorrentFeeds() []feedStatus {
	m.srcMu.RLock()
	t := m.torrent
	m.srcMu.RUnlock()
	if t == nil {
		return nil
	}
	return t.feedStatus()
}

type Overview struct {
	Counts           map[JobState]int
	Total            int
	Own, Torrent     float64
	Cap              float64
	DlUsed, DlLimit  int
	DlWaiting        int
	SrUsed, SrLimit  int
	Paused, Ready    bool
	Importing        bool
	Checked, ToCheck int // the library check in progress
	Lib              LibScanStatus
	Free             int64
	OutputDir        string
}

func (m *Manager) Overview() Overview {
	o := Overview{Counts: map[JobState]int{}}
	m.jobsMu.Lock()
	for _, j := range m.order {
		j.mu.Lock()
		o.Counts[j.state]++
		j.mu.Unlock()
	}
	o.Total = len(m.order)
	m.jobsMu.Unlock()
	o.Own, o.Torrent, o.Cap = bw.Speeds()
	o.DlUsed, o.DlLimit, o.DlWaiting = m.dlGate.stats()
	o.SrUsed, o.SrLimit, _ = m.searchGate.stats()
	o.Paused, o.Ready, o.Importing = m.paused.Load(), m.ready.Load(), m.importing.Load()
	o.Checked, o.ToCheck = int(m.checkDone.Load()), int(m.checkTotal.Load())
	if m.index != nil {
		o.Lib = m.index.Status()
	}
	o.OutputDir = m.Config().OutputDir
	o.Free = diskFree(nearestDir(o.OutputDir))
	return o
}

func nearestDir(p string) string {
	for p != "" && p != "/" && p != "." {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		p = filepath.Dir(p)
	}
	return "/"
}

func (m *Manager) summaryLine() string {
	o := m.Overview()
	return fmt.Sprintf("%d tracks · %d downloaded · %d in library · %d to do · %d not found",
		o.Total, o.Counts[JobDone], o.Counts[JobInLibrary],
		o.Counts[JobQueued]+o.Counts[JobSearching]+o.Counts[JobReady]+o.Counts[JobDownloading]+o.Counts[JobWaiting],
		o.Counts[JobFailed])
}

// idle reports whether nothing is left to do right now (for the headless mode).
func (m *Manager) idle() bool {
	if !m.ready.Load() || m.importing.Load() {
		return false
	}
	o := m.Overview()
	return o.Counts[JobQueued]+o.Counts[JobSearching]+o.Counts[JobReady]+o.Counts[JobDownloading] == 0
}
