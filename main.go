// spotify-downloader makes a local copy of the songs in your Spotify playlists from other
// sources (Bandcamp free downloads, Internet Archive, Yandex Music, VK, YouTube Music,
// SoundCloud, YouTube, torrents), skipping songs your music library already has in good
// enough quality. It runs as a terminal app where every setting can be changed while it works.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ", ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

const usage = `spotify-downloader: download the songs of your Spotify playlists from other sources,
skipping the ones your music library already has in good enough quality.

Run it without options to open the app; everything is set up and controlled in there.

Options (all optional; the app saves its settings):
  --config FILE      settings file (default ~/.config/spotify-downloader/config.json)
  --data DIR         where the app keeps its state, logs and caches
                     (default ~/.local/share/spotify-downloader)
  --add PATH|URL     add a playlist file, folder, zip or link (repeatable)
  --library DIR      add a library folder to check (repeatable; never written to)
  --out DIR          the downloads folder
  --bandwidth MBPS   bandwidth target and cap in MB/s (0: none)
  --headless         no app: work through the queue, print progress, write the reports, exit
  --check-torrents   test the Jackett/Prowlarr setup step by step, then exit
  -v                 detailed log
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("spotify-downloader", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	configPath := fs.String("config", defaultConfigPath(), "")
	dataDir := fs.String("data", defaultDataDir(), "")
	var add, libs listFlag
	fs.Var(&add, "add", "")
	fs.Var(&libs, "library", "")
	out := fs.String("out", "", "")
	bandwidth := fs.Float64("bandwidth", -1, "")
	headless := fs.Bool("headless", false, "")
	check := fs.Bool("check-torrents", false, "")
	torrentQuery := fs.String("torrent-query", "", "")
	fs.BoolVar(&verbose, "v", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (did you mean --add %s?)\n", fs.Arg(0), fs.Arg(0))
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, paint("31", "Error: ")+err.Error())
		return 1
	}
	*configPath, *dataDir = cleanPath(*configPath), cleanPath(*dataDir)
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return fail(err)
	}
	changed := false
	for _, a := range add {
		if !strings.Contains(a, "://") && !strings.HasPrefix(a, "spotify:") {
			a = cleanPath(a)
		}
		if !containsInput(cfg.Inputs, a) {
			cfg.Inputs = append(cfg.Inputs, InputCfg{Path: a, Enabled: true})
			changed = true
		}
	}
	for _, d := range libs {
		if d = cleanPath(d); !containsStr(cfg.LibraryDirs, d) {
			cfg.LibraryDirs = append(cfg.LibraryDirs, d)
			changed = true
		}
	}
	if *out != "" {
		cfg.OutputDir, changed = *out, true
	}
	if *bandwidth >= 0 {
		cfg.BandwidthMBps, changed = *bandwidth, true
	}
	if err := cfg.validate(); err != nil {
		return fail(err)
	}
	if err := checkDataDir(cfg, *dataDir); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(*configPath); changed || os.IsNotExist(err) {
		if err := saveConfig(*configPath, cfg); err != nil {
			return fail(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(*dataDir, "logs"), 0o755); err != nil {
		return fail(err)
	}
	logPath := filepath.Join(*dataDir, "logs", time.Now().Format("20060102-150405")+".log")
	if f, err := os.Create(logPath); err == nil {
		defer f.Close()
		debugLog = log.New(f, "", log.LstdFlags)
	}
	pruneLogs(filepath.Join(*dataDir, "logs"), 20)

	if *check {
		aria2c, _ := exec.LookPath("aria2c")
		query := *torrentQuery
		if query == "" {
			query = sampleAlbumQuery(filepath.Join(*dataDir, "library.json"))
		}
		if checkTorrents(context.Background(), cfg.Torznab, aria2c, query) {
			return 0
		}
		return 1
	}

	m := newManager(*configPath, *dataDir, cfg)
	if *headless {
		return runHeadless(m, logPath)
	}
	if !isCharDevice(os.Stdin) || !isCharDevice(os.Stdout) {
		fmt.Fprintln(os.Stderr, "Not a terminal: run it in a terminal, or use --headless.")
		return 2
	}
	if err := m.Start(); err != nil {
		return fail(err)
	}
	err = runTUI(m)
	fmt.Println("Stopping…")
	m.Stop()
	if err != nil {
		return fail(err)
	}
	fmt.Printf("Reports: %s\nLog: %s\n", filepath.Join(m.Config().OutputDir, "reports"), logPath)
	return 0
}

func containsInput(list []InputCfg, p string) bool {
	for _, in := range list {
		if in.Path == p {
			return true
		}
	}
	return false
}

// runHeadless works through the queue without the app, printing what happens.
func runHeadless(m *Manager, logPath string) int {
	appLog.onLine = func(l logLine) {
		if !l.Debug || verbose {
			fmt.Println(l.Text)
		}
	}
	var count atomic.Int64
	var waitMu sync.Mutex
	waited := map[string]bool{}
	m.onFinish = func(v JobView) {
		if v.State == JobWaiting { // shown once; it's tried again later
			waitMu.Lock()
			seen := waited[v.Key]
			waited[v.Key] = true
			waitMu.Unlock()
			if seen {
				return
			}
		}
		n := count.Add(1)
		name := v.Track.String()
		switch v.State {
		case JobDone:
			fmt.Printf("[%d] %s %s %s\n", n, paint("32", "✓"), name, paint("2", fmt.Sprintf("← %s · %s", sourceLabels[v.Rec.Source], v.Rec.Quality)))
		case JobWaiting:
			fmt.Printf("[%d] %s %s %s\n", n, paint("33", "⟳"), name, paint("2", "— "+fitWidth(v.Reason, 200)))
		default:
			fmt.Printf("[%d] %s %s %s\n", n, paint("31", "✗"), name, paint("2", "— "+fitWidth(v.Reason, 200)))
		}
	}
	if err := m.Start(); err != nil {
		fmt.Fprintln(os.Stderr, paint("31", "Error: ")+err.Error())
		return 1
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	lastStatus := time.Now()
	idleSince := time.Time{}
loop:
	for {
		select {
		case <-signals:
			fmt.Println(paint("33", "Stopping (tracks in progress continue next time)…"))
			break loop
		case <-tick.C:
		}
		if m.idle() {
			if idleSince.IsZero() {
				idleSince = time.Now()
			} else if time.Since(idleSince) > 3*time.Second {
				break loop
			}
		} else {
			idleSince = time.Time{}
		}
		if time.Since(lastStatus) > 30*time.Second {
			lastStatus = time.Now()
			o := m.Overview()
			fmt.Println(paint("2", fmt.Sprintf("  … %s · ⇣ %s · %d downloading, %d searching", m.summaryLine(),
				fmtRate(o.Own+o.Torrent), o.Counts[JobDownloading], o.Counts[JobSearching])))
		}
	}
	m.Stop()
	fmt.Println(m.summaryLine())
	fmt.Printf("Reports: %s\nLog: %s\n", filepath.Join(m.Config().OutputDir, "reports"), logPath)
	return 0
}

func pruneLogs(dir string, keep int) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	if len(matches) <= keep {
		return
	}
	for _, p := range matches[:len(matches)-keep] { // names sort by time
		os.Remove(p)
	}
}

// sampleAlbumQuery picks an album from the saved library for --check-torrents to search for.
func sampleAlbumQuery(libPath string) string {
	if lib, err := loadLibrary(libPath); err == nil {
		for _, t := range lib.ordered() {
			if t.Album != "" && !strings.EqualFold(t.Album, t.Title) {
				return t.AlbumArtistStr() + " " + t.Album
			}
		}
	}
	return "Kevin MacLeod"
}
