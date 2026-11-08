// universal-music-downloader makes a local copy of the songs in your Spotify playlists from other
// sources (Bandcamp free downloads, Internet Archive, Yandex Music, VK, YouTube Music,
// SoundCloud, YouTube, torrents), skipping songs your music library already has in good
// enough quality. It runs as a terminal app where every setting can be changed while it works.
package main

import (
	"universal-music-downloader/internal/core"
	"universal-music-downloader/internal/tui"

	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// version is set by release builds (-ldflags "-X main.version=v1.2.3").
var version = "dev"

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ", ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

const usage = `Universal Music Downloader: download the songs of your playlists, and music you search
for, from many sources (Bandcamp, Soulseek, torrents, YouTube…), skipping what your music
library already has in good enough quality.

Run it without options to open the app; everything is set up and controlled in there.

Options (all optional; the app saves its settings):
  --config FILE      settings file (default ~/.config/universal-music-downloader/config.json)
  --data DIR         where the app keeps its state, logs and caches
                     (default ~/.local/share/universal-music-downloader)
  --add PATH|URL     add a playlist file, folder, zip or link (repeatable)
  --library DIR      add a library folder to check (repeatable; never written to)
  --out DIR          the downloads folder
  --bandwidth MBPS   bandwidth target and cap in MB/s (0: none)
  --headless         no app: work through the queue, print progress, write the reports, exit
  --check-torrents   test the torrent indexers (built-in, Jackett/Prowlarr) with a search, then exit
  -v                 detailed log
  --version          print the version and exit
`

func main() {
	core.TuneMemory()
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("universal-music-downloader", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	configPath := fs.String("config", core.DefaultConfigPath(), "")
	dataDir := fs.String("data", core.DefaultDataDir(), "")
	var add, libs listFlag
	fs.Var(&add, "add", "")
	fs.Var(&libs, "library", "")
	out := fs.String("out", "", "")
	bandwidth := fs.Float64("bandwidth", -1, "")
	headless := fs.Bool("headless", false, "")
	check := fs.Bool("check-torrents", false, "")
	torrentQuery := fs.String("torrent-query", "", "")
	fs.BoolVar(&core.Verbose, "v", false, "")
	showVersion := fs.Bool("version", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Println("Universal Music Downloader", version)
		return 0
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (did you mean --add %s?)\n", fs.Arg(0), fs.Arg(0))
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, core.Paint("31", "Error: ")+err.Error())
		return 1
	}
	if *configPath == core.DefaultConfigPath() && *dataDir == core.DefaultDataDir() {
		for _, m := range core.MigrateLegacyFolders() {
			fmt.Println("Moved", m)
		}
	}
	*configPath, *dataDir = core.CleanPath(*configPath), core.CleanPath(*dataDir)
	cfg, err := core.LoadConfig(*configPath)
	if err != nil {
		return fail(err)
	}
	changed := false
	for _, a := range add {
		if !strings.Contains(a, "://") && !strings.HasPrefix(a, "spotify:") {
			a = core.CleanPath(a)
		}
		if !containsInput(cfg.Inputs, a) {
			cfg.Inputs = append(cfg.Inputs, core.InputCfg{Path: a, Enabled: true})
			changed = true
		}
	}
	for _, d := range libs {
		if d = core.CleanPath(d); !core.ContainsString(cfg.LibraryDirs, d) {
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
	if err := cfg.Validate(); err != nil {
		return fail(err)
	}
	if err := core.CheckDataDir(cfg, *dataDir); err != nil {
		return fail(err)
	}
	if _, err := os.Stat(*configPath); changed || os.IsNotExist(err) {
		if err := core.SaveConfig(*configPath, cfg); err != nil {
			return fail(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(*dataDir, "logs"), 0o755); err != nil {
		return fail(err)
	}
	logPath := filepath.Join(*dataDir, "logs", time.Now().Format("20060102-150405")+".log")
	if f, err := os.Create(logPath); err == nil {
		defer f.Close()
		core.DebugLog = log.New(f, "", log.LstdFlags)
		debug.SetCrashOutput(f, debug.CrashOptions{}) // should it ever crash, the log says where
	}
	pruneLogs(filepath.Join(*dataDir, "logs"), 20)

	if *check {
		query := *torrentQuery
		if query == "" {
			query = sampleAlbumQuery(filepath.Join(*dataDir, "library.json"))
		}
		if core.CheckTorrents(context.Background(), cfg, *dataDir, query) {
			return 0
		}
		return 1
	}

	m := core.NewManager(*configPath, *dataDir, cfg)
	if *headless {
		return runHeadless(m, logPath)
	}
	if !core.IsCharDevice(os.Stdin) || !core.IsCharDevice(os.Stdout) {
		fmt.Fprintln(os.Stderr, "Not a terminal: run it in a terminal, or use --headless.")
		return 2
	}
	if err := m.Start(); err != nil {
		return fail(err)
	}
	err = tui.Run(m)
	fmt.Println("Stopping…")
	m.Stop()
	if err != nil {
		return fail(err)
	}
	fmt.Printf("Reports: %s\nLog: %s\n", filepath.Join(m.Config().OutputDir, "reports"), logPath)
	return 0
}

func containsInput(list []core.InputCfg, p string) bool {
	for _, in := range list {
		if in.Path == p {
			return true
		}
	}
	return false
}

// runHeadless works through the queue without the app, printing what happens.
func runHeadless(m core.Backend, logPath string) int {
	m.OnLog(func(l core.LogLine) {
		if !l.Debug || core.Verbose {
			fmt.Println(l.Text)
		}
	})
	var count atomic.Int64
	var waitMu sync.Mutex
	waited := map[string]bool{}
	m.OnFinish(func(v core.JobView) {
		if v.State == core.JobWaiting { // shown once; it's tried again later
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
		case core.JobDone:
			fmt.Printf("[%d] %s %s %s\n", n, core.Paint("32", "✓"), name, core.Paint("2", fmt.Sprintf("← %s · %s", core.SourceLabels[v.Rec.Source], v.Rec.Quality)))
		case core.JobWaiting:
			fmt.Printf("[%d] %s %s %s\n", n, core.Paint("33", "⟳"), name, core.Paint("2", "— "+core.FitWidth(v.Reason, 200)))
		default:
			fmt.Printf("[%d] %s %s %s\n", n, core.Paint("31", "✗"), name, core.Paint("2", "— "+core.FitWidth(v.Reason, 200)))
		}
	})
	if err := m.Start(); err != nil {
		fmt.Fprintln(os.Stderr, core.Paint("31", "Error: ")+err.Error())
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
			fmt.Println(core.Paint("33", "Stopping (tracks in progress continue next time)…"))
			break loop
		case <-tick.C:
		}
		if m.Idle() {
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
			fmt.Println(core.Paint("2", fmt.Sprintf("  … %s · ⇣ %s · %d downloading, %d searching", m.SummaryLine(),
				core.FmtRate(o.Own+o.Torrent), o.Counts[core.JobDownloading], o.Counts[core.JobSearching])))
		}
	}
	m.Stop()
	fmt.Println(m.SummaryLine())
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
	if lib, err := core.LoadLibrary(libPath); err == nil {
		for _, t := range lib.Ordered() {
			if t.Album != "" && !strings.EqualFold(t.Album, t.Title) {
				return t.AlbumArtistStr() + " " + t.Album
			}
		}
	}
	return "Kevin MacLeod"
}
