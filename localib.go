package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Your existing music library: folders that are scanned (read only, never written to) so
// that songs you already have in good enough quality are not downloaded again. The scan is
// cached, so later scans only read new or changed files.

type LibFile struct {
	Path        string  `json:"p"`
	Size        int64   `json:"s"`
	Mod         int64   `json:"m"`
	Title       string  `json:"t,omitempty"`
	Artist      string  `json:"a,omitempty"`
	AlbumArtist string  `json:"aa,omitempty"`
	Album       string  `json:"al,omitempty"`
	ISRC        string  `json:"i,omitempty"`
	Duration    float64 `json:"d,omitempty"`
	Q           Quality `json:"q"`
	Guessed     bool    `json:"g,omitempty"` // no usable tags: title/artist taken from the path
}

type LibScanStatus struct {
	Scanning     bool
	Done, Total  int
	Files        int
	LastScan     time.Time
	Took         time.Duration
	Err          string
	Unreadable   int
	ReadFromTags int
}

type LibIndex struct {
	cachePath string
	tools     *Tools

	mu       sync.RWMutex
	files    map[string]*LibFile
	byArtist map[string][]*LibFile
	byTitle  map[string][]*LibFile
	byISRC   map[string][]*LibFile

	status   LibScanStatus
	statusMu sync.Mutex
	scanMu   sync.Mutex // one scan at a time
	done     atomic.Int64
}

func newLibIndex(cachePath string, tools *Tools) *LibIndex {
	ix := &LibIndex{cachePath: cachePath, tools: tools}
	ix.files = map[string]*LibFile{}
	if data, err := os.ReadFile(cachePath); err == nil {
		var list []*LibFile
		if json.Unmarshal(data, &list) == nil {
			for _, f := range list {
				ix.files[f.Path] = f
			}
		}
	}
	ix.build(nil)
	return ix
}

func (ix *LibIndex) Status() LibScanStatus {
	ix.statusMu.Lock()
	defer ix.statusMu.Unlock()
	s := ix.status
	if s.Scanning {
		s.Done = int(ix.done.Load())
	}
	return s
}

func (ix *LibIndex) setStatus(f func(*LibScanStatus)) {
	ix.statusMu.Lock()
	f(&ix.status)
	ix.statusMu.Unlock()
}

// splitArtists splits a tag's artist field into names: "A; B", "A / B", "A feat. B".
var reArtistSep = regexp.MustCompile(`(?i)\s*(?:;|/|\\|,|&|\bfeat\.?|\bft\.|\bfeaturing\b|\bwith\b|\sx\s|\svs\.?\s)\s*`)

func artistKeys(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(v string) {
		if n := normalize(v); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	add(s)
	for _, p := range reArtistSep.Split(s, -1) {
		add(p)
	}
	return out
}

func libTitleKey(title string) string { return cleanTitle(baseTitle(title), true) }

func (ix *LibIndex) build(roots []string) {
	byArtist, byTitle, byISRC := map[string][]*LibFile{}, map[string][]*LibFile{}, map[string][]*LibFile{}
	for _, f := range ix.files {
		if roots != nil && !underAny(f.Path, roots) {
			continue
		}
		for _, k := range append(artistKeys(f.Artist), artistKeys(f.AlbumArtist)...) {
			byArtist[k] = append(byArtist[k], f)
		}
		if k := libTitleKey(f.Title); k != "" {
			byTitle[k] = append(byTitle[k], f)
		}
		if f.ISRC != "" {
			k := strings.ToUpper(strings.TrimSpace(f.ISRC))
			byISRC[k] = append(byISRC[k], f)
		}
	}
	ix.byArtist, ix.byTitle, ix.byISRC = byArtist, byTitle, byISRC
}

func underAny(p string, roots []string) bool {
	for _, r := range roots {
		if isWithin(p, r) {
			return true
		}
	}
	return false
}

// isWithin reports whether p is dir itself or inside it.
func isWithin(p, dir string) bool {
	if p == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Scan reads the library folders. Unchanged files are taken from the cache.
func (ix *LibIndex) Scan(ctx context.Context, roots []string) error {
	ix.scanMu.Lock()
	defer ix.scanMu.Unlock()
	start := time.Now()
	ix.done.Store(0)
	ix.setStatus(func(s *LibScanStatus) { s.Scanning, s.Err, s.Total, s.Done = true, "", 0, 0 })

	type entry struct {
		path      string
		size, mod int64
	}
	var found []entry
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				debugf("library scan: %v", err)
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if p != root && strings.HasPrefix(d.Name(), ".") {
					return fs.SkipDir
				}
				return nil
			}
			if !audioExts[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			found = append(found, entry{p, info.Size(), info.ModTime().Unix()})
			if len(found)%500 == 0 {
				ix.setStatus(func(s *LibScanStatus) { s.Total = len(found) })
			}
			return nil
		})
		if err != nil && ctx.Err() == nil {
			ix.setStatus(func(s *LibScanStatus) { s.Err = fmt.Sprintf("%s: %v", root, err) })
		}
	}
	if ctx.Err() != nil {
		ix.setStatus(func(s *LibScanStatus) { s.Scanning = false })
		return ctx.Err()
	}
	ix.setStatus(func(s *LibScanStatus) { s.Total = len(found) })

	ix.mu.RLock()
	old := ix.files
	ix.mu.RUnlock()
	result := make([]*LibFile, len(found))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var unreadable, fromTags atomic.Int64
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				e := found[i]
				if f := old[e.path]; f != nil && f.Size == e.size && f.Mod == e.mod {
					result[i] = f
				} else {
					result[i] = ix.readFile(ctx, e.path, e.size, e.mod)
					if result[i].Guessed && !result[i].Q.known() {
						unreadable.Add(1)
					}
				}
				if !result[i].Guessed {
					fromTags.Add(1)
				}
				ix.done.Add(1)
			}
		}()
	}
feed:
	for i := range found {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		ix.setStatus(func(s *LibScanStatus) { s.Scanning = false })
		return ctx.Err()
	}

	files := make(map[string]*LibFile, len(result))
	list := make([]*LibFile, 0, len(result))
	for _, f := range result {
		if f != nil {
			files[f.Path] = f
			list = append(list, f)
		}
	}
	ix.mu.Lock()
	ix.files = files
	ix.build(roots)
	ix.mu.Unlock()
	if data, err := json.Marshal(list); err == nil {
		if err := writeFileAtomic(ix.cachePath, data); err != nil {
			debugf("saving the library index: %v", err)
		}
	}
	took := time.Since(start)
	ix.setStatus(func(s *LibScanStatus) {
		s.Scanning, s.Files, s.LastScan, s.Took = false, len(files), time.Now(), took
		s.Unreadable, s.ReadFromTags = int(unreadable.Load()), int(fromTags.Load())
	})
	return nil
}

func (ix *LibIndex) readFile(ctx context.Context, p string, size, mod int64) *LibFile {
	f := &LibFile{Path: p, Size: size, Mod: mod}
	info, err := readTags(p)
	if err != nil && ix.tools != nil && ix.tools.FFprobe != "" {
		if !errors.Is(err, errUnsupported) {
			debugf("library: reading %s natively failed (%v); asking ffprobe", p, err)
		}
		info, err = probeTags(ctx, ix.tools, p)
	}
	if err == nil {
		f.Title, f.Artist, f.AlbumArtist, f.Album, f.ISRC = info.Title, info.Artist, info.AlbumArtist, info.Album, info.ISRC
		f.Duration, f.Q = info.Duration, info.Q
	} else {
		debugf("library: can't read %s: %v", p, err)
	}
	if f.Title == "" || (f.Artist == "" && f.AlbumArtist == "") {
		guessFromPath(f)
	}
	return f
}

var (
	reFileTrackNum = regexp.MustCompile(`^\s*(?:\d{1,2}[-.])?\d{1,3}\s*(?:[-._)]\s*|\s+)`)
	reYearPrefix   = regexp.MustCompile(`^\s*[(\[]?(?:19|20)\d{2}[)\]]?\s*(?:-\s*)?`)
	reYear         = regexp.MustCompile(`^\s*[(\[]?(?:19|20)\d{2}[)\]]?\s*$`)
)

// guessFromPath fills title/artist/album from "Artist/Album/01 - Title.ext" or "Artist - Title.ext".
func guessFromPath(f *LibFile) {
	f.Guessed = true
	stem := strings.TrimSuffix(filepath.Base(f.Path), filepath.Ext(f.Path))
	stem = reFileTrackNum.ReplaceAllString(stem, "")
	dir := filepath.Dir(f.Path)
	album, artist := filepath.Base(dir), filepath.Base(filepath.Dir(dir))
	if parts := reSpacedDash.Split(stem, 2); len(parts) == 2 {
		if f.Artist == "" {
			f.Artist = strings.TrimSpace(parts[0])
		}
		stem = parts[1]
	}
	if f.Title == "" {
		f.Title = strings.TrimSpace(stem)
	}
	if f.Album == "" {
		f.Album = strings.TrimSpace(reYearPrefix.ReplaceAllString(album, ""))
	}
	if f.Artist == "" && f.AlbumArtist == "" {
		if parts := reSpacedDash.Split(album, 2); len(parts) == 2 && !reYear.MatchString(parts[0]) { // "Artist - Album" folders
			f.Artist = parts[0]
		} else {
			f.Artist = artist
		}
	}
}

// probeTags reads tags and stream details with ffprobe (for formats not read natively).
func probeTags(ctx context.Context, tools *Tools, p string) (*tagInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := command(ctx, tools.FFprobe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,bits_per_raw_sample,bits_per_sample,bit_rate,sample_fmt:format=duration,bit_rate:format_tags:stream_tags",
		"-of", "json", p).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var data ffprobeOutput
	if err := json.Unmarshal(out, &data); err != nil || len(data.Streams) == 0 {
		return nil, errors.New("no audio stream")
	}
	info := &tagInfo{}
	tags := map[string]string{}
	for _, src := range []map[string]string{data.Streams[0].Tags, data.Format.Tags} {
		for k, v := range src {
			if _, ok := tags[strings.ToLower(k)]; !ok {
				tags[strings.ToLower(k)] = v
			}
		}
	}
	info.Title, info.Artist, info.Album = tags["title"], tags["artist"], tags["album"]
	info.AlbumArtist = firstNonEmpty(tags["album_artist"], tags["albumartist"], tags["album artist"])
	info.ISRC = firstNonEmpty(tags["isrc"], tags["tsrc"])
	info.TrackNumber = leadingInt(tags["track"])
	info.Duration, info.Q = data.quality()
	return info, nil
}

type ffprobeOutput struct {
	Streams []struct {
		CodecName        string            `json:"codec_name"`
		SampleRate       string            `json:"sample_rate"`
		BitsPerRawSample string            `json:"bits_per_raw_sample"`
		BitsPerSample    int               `json:"bits_per_sample"`
		BitRate          string            `json:"bit_rate"`
		SampleFmt        string            `json:"sample_fmt"`
		Tags             map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string            `json:"duration"`
		BitRate  string            `json:"bit_rate"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

func (d *ffprobeOutput) quality() (float64, Quality) {
	s := d.Streams[0]
	duration, _ := strconv.ParseFloat(d.Format.Duration, 64)
	rate, _ := strconv.Atoi(s.SampleRate)
	depth, _ := strconv.Atoi(s.BitsPerRawSample)
	if depth == 0 {
		depth = s.BitsPerSample
	}
	if depth == 0 {
		switch s.SampleFmt {
		case "s16", "s16p":
			depth = 16
		case "s32", "s32p":
			depth = 24 // FLAC/ALAC decode 24-bit audio as s32
		}
	}
	bps, _ := strconv.Atoi(s.BitRate)
	if bps == 0 {
		bps, _ = strconv.Atoi(d.Format.BitRate)
	}
	q := newQuality(s.CodecName, bps/1000, rate, depth)
	if !q.Lossless {
		q.BitDepth = 0
	}
	if q.Codec == "opus" {
		q.SampleRate = 0
	}
	return duration, q
}

// Match finds the library's copies of a track, best quality first.
func (ix *LibIndex) Match(t *Track) []*LibFile {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	seen := map[*LibFile]bool{}
	var cands []*LibFile
	add := func(list []*LibFile) {
		for _, f := range list {
			if !seen[f] {
				seen[f] = true
				cands = append(cands, f)
			}
		}
	}
	isrc := strings.ToUpper(strings.TrimSpace(t.ISRC))
	if isrc != "" {
		add(ix.byISRC[isrc])
	}
	for _, a := range append(append([]string{}, t.Artists...), t.AlbumArtist) {
		for _, k := range artistKeys(a) {
			add(ix.byArtist[k])
		}
	}
	add(ix.byTitle[libTitleKey(t.Title)])
	var out []*LibFile
	for _, f := range cands {
		if libMatches(t, f, isrc) {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Q.Score() > out[j].Q.Score() })
	return out
}

func libMatches(t *Track, f *LibFile, isrc string) bool {
	dur := f.Duration
	if f.ISRC != "" && isrc != "" && strings.EqualFold(strings.TrimSpace(f.ISRC), isrc) {
		// The same recording; only a very different length (a cut or a mislabelled file) says no.
		return dur == 0 || t.DurationMs == 0 || abs(dur-t.Duration()) <= max(20, t.Duration()*0.1)
	}
	c := Candidate{Title: f.Title, Artist: firstNonEmpty(f.Artist, f.AlbumArtist), Album: f.Album, Duration: dur, ArtistIsCredit: true}
	m := evaluate(t, c)
	if m.Reject != "" && f.AlbumArtist != "" && f.Artist != "" && strings.HasPrefix(m.Reject, "different artist") {
		c.Artist = f.AlbumArtist
		m = evaluate(t, c)
	}
	return m.Reject == ""
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// Size is how many files are indexed.
// lookup returns what the scan found about the file at p (nil: not in the library).
func (ix *LibIndex) lookup(p string) *LibFile {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.files[p]
}

func (ix *LibIndex) Size() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.files)
}
