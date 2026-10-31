// spotify-downloader makes a local copy of your Spotify library (your playlists and Liked
// Songs) from other sources: free Bandcamp downloads, licensed Internet Archive items,
// YouTube Music, SoundCloud, YouTube and, as a last resort, torrents. It needs no Spotify
// Premium or developer app: the track list comes from Spotify's own data export or from
// CSV exports, text files or public playlist links.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ", ") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

var (
	debugLog *log.Logger
	verbose  bool
	color    = isTerminal(os.Stdout)
)

func debugf(format string, args ...any) {
	if debugLog != nil {
		debugLog.Printf(format, args...)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "  · "+format+"\n", args...)
	}
}

// logf prints a message for the user (and keeps it in the log file).
func logf(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	if debugLog != nil {
		debugLog.Printf(format, args...)
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
}

func paint(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

const usage = `spotify-downloader: download the songs in your Spotify playlists and Liked Songs
from other sources, then list what was downloaded, by whom, and what couldn't be found.

Usage:
  spotify-downloader --from my_spotify_data.zip        first run: read your library and download
  spotify-downloader                                   later runs: continue / retry
  spotify-downloader --from my_spotify_data.zip --list  just show the playlists that were found

Where the track list comes from (no Spotify Premium or developer app needed):
  --from PATH|URL        Repeatable. Any of:
                           Spotify's data export (spotify.com → Account → Privacy settings →
                             Download your data → "Account data"): the zip or its folder
                           CSV files from Exportify, TuneMyMusic etc. (a file, folder or zip)
                           a .txt file with one "Artist - Title" per line
                           a public playlist/album link (only its first 100 tracks)
                         The list is remembered, so later runs don't need --from.
  --list                 show the playlists found (with track counts) and exit
  --playlist NAME        only this playlist (repeatable; "Liked Songs" is one too)
  --exclude-playlist NAME  skip this playlist (repeatable), e.g. ones you follow but didn't make
  --no-liked             skip Liked Songs

Output:
  --out DIR              music folder (default ~/Music/Spotify); reports and .m3u8
                         playlists go in it as well
  --format FMT           mp3 (default), opus, m4a, flac, or original (no re-encoding)
  --template T           file layout (default "{album_artist}/{album}/{tracknum} - {title}");
                         fields: {artist} {artists} {album_artist} {album} {title} {tracknum} {year}

Downloading:
  --sources LIST         tried in this order (default bandcamp,archive,youtube_music,soundcloud,youtube)
  --workers N            tracks processed in parallel (default 3)
  --limit N              only process N tracks (handy for a first test)
  --retry-failed         also retry tracks that found no match last time
  --no-enrich            don't look up missing lengths/covers on Deezer
  --cookies FILE         cookies.txt for yt-dlp
  --cookies-from-browser BROWSER   let yt-dlp use your browser's YouTube login (helps with bot checks)
  --yt-dlp PATH          use this yt-dlp instead of the automatically downloaded one

Torrents (last resort, for tracks nothing else had; needs aria2c):
  --torznab URL          Torznab feed of your Jackett or Prowlarr (or set TORZNAB_URL)
  --torznab-key KEY      its API key (or set TORZNAB_KEY)
  --torrent-timeout D    give up on a torrent after this long (default 30m)

Other:
  --report-only          only rewrite the reports and playlists
  -v                     verbose output
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("spotify-downloader", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var from, only, exclude listFlag
	fs.Var(&from, "from", "")
	fs.Var(&only, "playlist", "")
	fs.Var(&exclude, "exclude-playlist", "")
	home, _ := os.UserHomeDir()
	out := fs.String("out", filepath.Join(home, "Music", "Spotify"), "")
	format := fs.String("format", "mp3", "")
	template := fs.String("template", defaultTemplate, "")
	sourcesFlag := fs.String("sources", strings.Join(defaultSources, ","), "")
	workers := fs.Int("workers", 3, "")
	limit := fs.Int("limit", 0, "")
	retryFailed := fs.Bool("retry-failed", false, "")
	noLiked := fs.Bool("no-liked", false, "")
	noEnrich := fs.Bool("no-enrich", false, "")
	list := fs.Bool("list", false, "")
	reportOnly := fs.Bool("report-only", false, "")
	cookies := fs.String("cookies", "", "")
	cookiesFromBrowser := fs.String("cookies-from-browser", "", "")
	ytdlpPath := fs.String("yt-dlp", "", "")
	torznabURL := fs.String("torznab", os.Getenv("TORZNAB_URL"), "")
	torznabKey := fs.String("torznab-key", os.Getenv("TORZNAB_KEY"), "")
	torrentTimeout := fs.Duration("torrent-timeout", 30*time.Minute, "")
	fs.BoolVar(&verbose, "v", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (did you mean --from %s?)\n", fs.Arg(0), fs.Arg(0))
		return 2
	}
	validFormat := false
	for _, f := range formats {
		validFormat = validFormat || f == *format
	}
	if !validFormat {
		fmt.Fprintf(os.Stderr, "--format must be one of %s\n", strings.Join(formats, ", "))
		return 2
	}

	musicDir, _ := filepath.Abs(expandHome(*out))
	stateDir := filepath.Join(musicDir, ".spotify-downloader")
	if err := os.MkdirAll(filepath.Join(stateDir, "logs"), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	logPath := filepath.Join(stateDir, "logs", time.Now().Format("20060102-150405")+".log")
	if f, err := os.Create(logPath); err == nil {
		defer f.Close()
		debugLog = log.New(f, "", log.LstdFlags)
	} else {
		debugLog = log.New(io.Discard, "", 0)
	}

	// First Ctrl-C: finish the tracks in progress, then write the reports. Second: stop now.
	ctx, kill := context.WithCancel(context.Background())
	defer kill()
	stop := make(chan struct{})
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt)
	go func() {
		<-signals
		fmt.Println(paint("33", "\nStopping after the tracks in progress (Ctrl-C again to stop immediately)…"))
		close(stop)
		<-signals
		kill()
	}()

	fail := func(err error) int {
		debugLog.Printf("fatal: %v", err)
		fmt.Fprintln(os.Stderr, paint("31", "Error: ")+err.Error())
		return 1
	}

	// The track list.
	libPath := filepath.Join(stateDir, "library.json")
	var full *Library
	if len(from) > 0 {
		lib := newLibrary()
		lib.Inputs = from
		logf("Reading your track list…")
		for _, src := range from {
			if err := importInput(ctx, lib, src, logf); err != nil {
				return fail(err)
			}
		}
		if len(lib.Tracks) == 0 {
			return fail(fmt.Errorf("no tracks found in %s", strings.Join(from, ", ")))
		}
		if old, err := loadLibrary(libPath); err == nil {
			for k, t := range lib.Tracks { // keep earlier Deezer lookups
				if o := old.Tracks[k]; o != nil && o.Enriched {
					t.fillFrom(o)
					t.DeezerArtistURL, t.Enriched = firstNonEmpty(t.DeezerArtistURL, o.DeezerArtistURL), true
				}
			}
		}
		if err := lib.save(libPath); err != nil {
			return fail(err)
		}
		full = lib
	} else {
		var err error
		if full, err = loadLibrary(libPath); err != nil {
			fmt.Fprint(os.Stderr, "No track list yet: pass --from with your Spotify data export, a CSV, a text file or a playlist link.\n\n"+usage)
			return 2
		}
	}
	lib := full.filtered(only, exclude, *noLiked)

	if *list {
		fmt.Printf("%d unique tracks in %d playlists:\n", len(lib.Tracks), len(lib.Playlists))
		for _, p := range lib.Playlists {
			fmt.Printf("  %5d  %s\n", len(p.Keys), p.Name)
		}
		if len(lib.Skipped) > 0 {
			fmt.Printf("(%d podcast episodes and other non-songs will be skipped)\n", len(lib.Skipped))
		}
		fmt.Println("\nTip: Spotify's export also lists playlists you only follow; leave those out with --exclude-playlist NAME.")
		return 0
	}

	state, err := openState(filepath.Join(stateDir, "state.jsonl"))
	if err != nil {
		return fail(err)
	}
	defer state.close()
	writeAll := func() {
		recs := state.all()
		rep := buildReport(lib, recs, musicDir)
		files, err := writeReports(rep, filepath.Join(musicDir, "reports"))
		if err != nil {
			fail(err)
			return
		}
		n, _ := writePlaylists(lib, recs, musicDir)
		fmt.Printf("\n%s tracks downloaded by %s artists, %s entries not downloaded\n",
			paint("1", fmt.Sprint(len(rep.Downloaded))), paint("1", fmt.Sprint(len(rep.Artists))), paint("1", fmt.Sprint(len(rep.Failed))))
		for i, label := range []string{"Artists:   ", "Downloaded:", "Failed:    "} {
			fmt.Printf("  %s %s\n", label, files[i])
		}
		fmt.Printf("  Playlists:  %s (%d .m3u8 files)\n", filepath.Join(musicDir, "Playlists"), n)
	}
	if *reportOnly {
		writeAll()
		return 0
	}

	// Tools and sources.
	sourceNames := strings.Split(strings.ReplaceAll(*sourcesFlag, " ", ""), ",")
	tools, err := findTools(ctx, *ytdlpPath, defaultCacheDir(), usesYtDlp(sourceNames), logf)
	if err != nil {
		return fail(err)
	}
	sources, err := buildSources(sourceNames, *format, filepath.Join(stateDir, "cache"), newYtDlp(tools, *cookies, *cookiesFromBrowser))
	if err != nil {
		return fail(err)
	}
	var torrents *TorrentFetcher
	if *torznabURL != "" {
		if tools.Aria2c == "" {
			logf("Torrents are configured but aria2c isn't installed (e.g. `sudo pacman -S aria2`); skipping torrents.")
		} else {
			torrents = newTorrentFetcher(*torznabURL, *torznabKey, tools, *torrentTimeout)
		}
	}
	defer os.RemoveAll(filepath.Join(stateDir, "cache"))
	defer os.RemoveAll(filepath.Join(stateDir, "tmp"))

	if !*noEnrich {
		enrichLibrary(ctx, lib, logf)
		full.save(libPath)
	}

	engine := newEngine(Settings{
		MusicDir: musicDir, Format: *format, Template: *template, Workers: *workers, Limit: *limit,
		RetryFailed: *retryFailed, TriesPerSource: 2, MaxOutages: 5,
	}, sources, state, tools, filepath.Join(stateDir, "tmp"))

	todo := engine.pending(lib)
	recs := state.all()
	done, notFound := 0, 0
	for k := range lib.Tracks {
		if r, ok := recs[k]; ok && r.Status == "downloaded" && engine.fileExists(r.Path) {
			done++
		} else if ok && r.Status == "failed" {
			notFound++
		}
	}
	summary := fmt.Sprintf("%d to process · %d already downloaded", len(todo), done)
	if notFound > 0 && !*retryFailed {
		summary += fmt.Sprintf(" · %d not found last time (add --retry-failed to try again)", notFound)
	}
	logf("%s", summary)
	var labels []string
	for _, s := range sources {
		labels = append(labels, s.Label())
	}
	if torrents != nil {
		labels = append(labels, "Torrent (last resort)")
	}
	logf("Sources, in order: %s", strings.Join(labels, ", "))

	n := 0
	total := len(todo)
	final := map[string]string{} // track key -> last outcome
	var unresolved []*Track
	show := func(o Outcome) {
		n++
		prefix := fmt.Sprintf("[%*d/%d]", len(fmt.Sprint(total)), n, total)
		if o.Status == "downloaded" {
			fmt.Printf("%s %s %s %s\n", prefix, paint("32", "✓"), o.Track, paint("2", "← "+o.Detail))
			return
		}
		detail := o.Detail
		if len(detail) > 200 {
			detail = detail[:197] + "…"
		}
		fmt.Printf("%s %s %s %s\n", prefix, paint("31", "✗"), o.Track, paint("2", detail))
	}
	engine.run(ctx, stop, todo, func(o Outcome) {
		if o.Status == "aborted" {
			return
		}
		show(o)
		final[o.Track.Key] = o.Status
		if o.Status != "downloaded" {
			unresolved = append(unresolved, o.Track)
		}
	})
	if torrents != nil && len(unresolved) > 0 && ctx.Err() == nil {
		select {
		case <-stop:
		default:
			logf("\nTrying torrents for %d tracks…", len(unresolved))
			n, total = 0, len(unresolved)
			engine.runTorrents(ctx, stop, torrents, unresolved, func(o Outcome) {
				show(o)
				if o.Status == "downloaded" {
					final[o.Track.Key] = o.Status
				}
			})
		}
	}
	counts := map[string]int{}
	for _, st := range final {
		counts[st]++
	}
	if total > 0 {
		fmt.Printf("\n%s, %s\n", paint("32", fmt.Sprintf("%d downloaded", counts["downloaded"])),
			paint("31", fmt.Sprintf("%d not downloaded", counts["failed"]+counts["error"])))
		if counts["error"] > 0 {
			fmt.Printf("(%d of those because a source was unreachable; they're retried on the next run)\n", counts["error"])
		}
	}
	full.save(libPath) // keeps details learned while downloading
	writeAll()
	fmt.Printf("  Log:        %s\n", logPath)
	return 0
}
