package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Sort & merge, reading: finding audio files, their tags and stream details. Read-only.

// sortExts are the audio files Sort & merge looks at.
var sortExts = setOf(".mp3", ".flac", ".m4a", ".aac", ".alac", ".ogg", ".oga", ".opus", ".wma", ".wav", ".aif", ".aiff",
	".aifc", ".ape", ".wv", ".mpc", ".tta", ".ofr", ".dsf", ".dff", ".spx", ".mp4", ".m4b")

// sortAudio is an audio file as found on disk.
type sortAudio struct {
	Path, Root  string  `json:"-"`
	Size, Mtime int64   `json:"-"`
	Format      string  `json:"f,omitempty"`
	Lossless    bool    `json:"l,omitempty"`
	Kbps        int     `json:"b,omitempty"`
	SampleRate  int     `json:"r,omitempty"`
	Bits        int     `json:"d,omitempty"`
	Duration    float64 `json:"s,omitempty"`
	Tags        sortTags
	Err         string `json:"e,omitempty"`
}

type sortTags struct {
	Artist, AlbumArtist, Album, Title string `json:",omitempty"`
	Track, Disc, DiscTotal, Date      string `json:",omitempty"`
	ReleaseType, Compilation          string `json:",omitempty"`
}

// quality orders files: lossless beats lossy; then resolution, or bitrate.
func (a *sortAudio) quality() [2]int {
	if a.Lossless {
		return [2]int{1, max(a.Bits, 16) * max(a.SampleRate, 44100)}
	}
	return [2]int{0, a.Kbps}
}

func (a *sortAudio) qualityLabel() string {
	if a.Lossless {
		detail := []string{}
		if a.Bits > 0 {
			detail = append(detail, fmt.Sprintf("%dbit", a.Bits))
		}
		if a.SampleRate > 0 {
			detail = append(detail, strconv.FormatFloat(float64(a.SampleRate)/1000, 'g', -1, 64)+"kHz")
		}
		return strings.TrimSpace(a.Format + " " + strings.Join(detail, "/"))
	}
	if a.Kbps > 0 {
		return fmt.Sprintf("%s %dkbps", a.Format, a.Kbps)
	}
	return FirstNonEmpty(a.Format, "unknown format")
}

var sortCodecNames = map[string]string{"mp3": "MP3", "flac": "FLAC", "aac": "AAC", "alac": "ALAC", "opus": "Opus", "vorbis": "Vorbis",
	"wav": "WAV", "wavpack": "WavPack", "ape": "APE", "tta": "TTA", "wmav2": "WMA", "wmav1": "WMA", "mp2": "MP2"}

// sortFind lists the audio files under the roots: hidden folders and symlinks are skipped, and
// a file reachable from two roots counts once.
func sortFind(ctx context.Context, roots, exclude []string, progress func(int)) ([]*sortAudio, error) {
	excluded := map[string]bool{}
	for _, e := range exclude {
		excluded[realPath(e)] = true
	}
	seen := map[string]bool{}
	var found []*sortAudio
	for _, root := range roots {
		root = CleanPath(root)
		realRoot := realPath(root)
		// The walk doesn't follow links, so below the root a path is already its real path.
		resolved := func(p string) string { return filepath.Join(realRoot, strings.TrimPrefix(p, root)) }
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if p != root && (strings.HasPrefix(name, ".") || excluded[resolved(p)]) {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, "._") || !sortExts[strings.ToLower(filepath.Ext(name))] || !d.Type().IsRegular() {
				return nil
			}
			real := resolved(p)
			if seen[real] {
				return nil
			}
			seen[real] = true
			found = append(found, &sortAudio{Path: p, Root: root})
			if len(found)%1000 == 0 {
				progress(len(found))
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return found, nil
}

// realPath follows symlinks in a path that exists.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// sortTagCache remembers what files contain, by the file's identity, so a second plan (or
// one after files were moved) doesn't read them again.
type sortTagCache struct {
	path string
	mu   sync.Mutex
	old  map[string]*sortAudio
	f    *os.File
	w    *bufio.Writer
}

type sortCacheLine struct {
	K string `json:"k"`
	sortAudio
}

func openSortTagCache(path string) *sortTagCache {
	c := &sortTagCache{path: path, old: map[string]*sortAudio{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var l sortCacheLine
			if json.Unmarshal(sc.Bytes(), &l) == nil && l.K != "" {
				v := l.sortAudio
				c.old[l.K] = &v
			}
		}
		f.Close()
	}
	return c
}

func (c *sortTagCache) put(key string, a *sortAudio) {
	data, err := json.Marshal(sortCacheLine{K: key, sortAudio: *a})
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f == nil {
		os.MkdirAll(filepath.Dir(c.path), 0o755)
		f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		c.f, c.w = f, bufio.NewWriterSize(f, 1<<16)
	}
	c.w.Write(data)
	c.w.WriteByte('\n')
}

func (c *sortTagCache) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil {
		c.w.Flush()
		c.f.Close()
		c.f = nil
	}
	c.old = nil
}

// sortRead fills in each file's details, reading only what's new or changed.
func sortRead(ctx context.Context, files []*sortAudio, cache *sortTagCache, tools *Tools, progress func(done int)) {
	var next, done atomic.Int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(files) {
					return
				}
				a := files[i]
				st, err := os.Stat(a.Path)
				if err != nil {
					a.Err = err.Error()
				} else {
					a.Size, a.Mtime = st.Size(), st.ModTime().UnixNano()
					key := fileIdentity(st)
					if c := cache.old[key]; c != nil {
						a.Format, a.Lossless, a.Kbps, a.SampleRate, a.Bits, a.Duration, a.Tags, a.Err = c.Format, c.Lossless, c.Kbps, c.SampleRate, c.Bits, c.Duration, c.Tags, c.Err
					} else {
						sortReadFile(ctx, a, tools)
						cache.put(key, a)
					}
				}
				if n := done.Add(1); n%200 == 0 {
					progress(int(n))
				}
			}
		}()
	}
	wg.Wait()
	progress(len(files))
}

func sortReadFile(ctx context.Context, a *sortAudio, tools *Tools) {
	info, err := readTags(a.Path)
	if err != nil {
		var probeErr error
		if info, probeErr = probeTags(ctx, tools, a.Path); probeErr != nil {
			a.Err = "couldn't read it: " + probeErr.Error()
			return
		}
	}
	q := info.Q
	a.Format = FirstNonEmpty(sortCodecNames[q.Codec], strings.ToUpper(q.Codec))
	if strings.HasPrefix(q.Codec, "pcm") {
		a.Format = "WAV"
	}
	a.Lossless, a.Kbps, a.SampleRate, a.Bits, a.Duration = q.Lossless, q.Kbps, q.SampleRate, q.BitDepth, info.Duration
	a.Tags = sortTags{Artist: FirstNonEmpty(info.FirstArtist, info.Artist), AlbumArtist: FirstNonEmpty(info.FirstAlbumArtist, info.AlbumArtist), Album: info.Album, Title: info.Title,
		Disc: info.Disc, DiscTotal: info.DiscTotal, Date: info.Date, ReleaseType: info.ReleaseType, Compilation: info.Compilation}
	if info.TrackNumber > 0 {
		a.Tags.Track = strconv.Itoa(info.TrackNumber)
	}
}

// sortMeta is the description of a track that's worked out for it.
type sortMeta struct {
	artist, albumArtist, album, title string
	track, disc, discTotal            int
	year, releaseType                 string
	single, compilation               bool
	trackFromTags                     bool
	fromName, fromWeb                 bool
	web                               string
}

var (
	reSortNumberPair = regexp.MustCompile(`^\s*(\d+)(?:\s*(?:/|of)\s*(\d+))?`)
	reSortYearTag    = regexp.MustCompile(`\b(1[89]\d\d|20\d\d)\b`)
)

func sortNumberPair(v string) (int, int) {
	m := reSortNumberPair.FindStringSubmatch(v)
	if m == nil {
		return 0, 0
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a, b
}

func sortMetaFromTags(t sortTags) sortMeta {
	var m sortMeta
	clean := func(v string) string {
		v = strings.TrimSpace(v)
		if v != "" && !sortIsPlaceholder(v) {
			return v
		}
		return ""
	}
	m.artist, m.albumArtist, m.album, m.title = clean(t.Artist), clean(t.AlbumArtist), clean(t.Album), clean(t.Title)
	m.track, _ = sortNumberPair(t.Track)
	m.trackFromTags = m.track > 0
	var total int
	m.disc, total = sortNumberPair(t.Disc)
	if m.discTotal = total; m.discTotal == 0 {
		m.discTotal, _ = sortNumberPair(t.DiscTotal)
	}
	if y := reSortYearTag.FindStringSubmatch(t.Date); y != nil {
		m.year = y[1]
	}
	m.releaseType = strings.ToLower(t.ReleaseType)
	m.single = strings.Contains(m.releaseType, "single")
	flagged := strings.ToLower(strings.TrimSpace(t.Compilation))
	m.compilation = sortIsVarious(m.albumArtist) || ((flagged == "1" || flagged == "true" || flagged == "yes") && m.albumArtist == "")
	return m
}

func (m *sortMeta) fillFromFilename(stem string) {
	name := sortParseFilename(stem)
	artist, title := name.artist, name.title
	// "Title - Artist" instead of "Artist - Title": recognizable when one half matches a tag.
	if artist != "" && title != "" {
		if (m.artist != "" && sortSameArtist(title, m.artist) && !sortSameArtist(artist, m.artist)) ||
			(m.title != "" && sortFold(artist, false) == sortFold(m.title, false)) {
			artist, title = title, artist
		}
	}
	if sortIsPlaceholder(FirstNonEmpty(artist, "-")) {
		artist = ""
	}
	if sortIsPlaceholder(FirstNonEmpty(title, "-")) {
		title = ""
	}
	if m.artist == "" && artist != "" {
		m.artist, m.fromName = artist, true
	}
	if m.title == "" && title != "" {
		m.title, m.fromName = title, true
	}
	if m.album == "" && name.album != "" && sortSameArtist(artist, m.artist) {
		m.album, m.fromName = name.album, true
	}
	if m.track == 0 && name.track > 0 {
		m.track = name.track
	}
	if m.disc == 0 && name.disc > 0 {
		m.disc = name.disc
	}
}

// sortResolve works out artist, album and title: from the tags, then the names of the file and
// its folders, then the web; each step only fills what's still missing.
func sortResolve(ctx context.Context, a *sortAudio, id *sortIdentifier) sortMeta {
	r := sortPrepare(a)
	if id != nil && r.wantsWeb() {
		r.web(ctx, id)
	}
	return r.finish()
}

// sortResolving is a file on its way to a description. The steps are apart because the web is
// slow: everything else is done for all the files first.
type sortResolving struct {
	a      *sortAudio
	m      sortMeta
	stem   string
	dirs   []string
	folder sortFolderHint
}

// sortPrepare reads what the tags and the names of the file and its folders say.
func sortPrepare(a *sortAudio) *sortResolving {
	m := sortMetaFromTags(a.Tags)
	stem := strings.TrimSuffix(filepath.Base(a.Path), filepath.Ext(a.Path))
	m.fillFromFilename(stem)

	var dirs []string
	if rel, err := filepath.Rel(a.Root, filepath.Dir(a.Path)); err == nil {
		for _, d := range strings.Split(rel, string(filepath.Separator)) {
			if d != "." && d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	folder := sortParseFolders(dirs)
	// A folder name is only trusted for the album when it agrees with the artist we already have.
	if m.artist != "" && m.album == "" && !m.single && folder.album != "" &&
		(sortSameArtist(folder.artist, m.artist) || sortSameArtist(folder.parentArtist, m.artist)) {
		m.album, m.fromName = folder.album, true
		m.year = FirstNonEmpty(m.year, folder.year)
	}
	if m.album != "" && m.disc == 0 {
		m.disc = folder.disc
	}
	return &sortResolving{a: a, m: m, stem: stem, dirs: dirs, folder: folder}
}

// wantsWeb: something's missing. A song already in an Artist/Singles folder isn't: that's
// where it was filed before, and there's no use asking about all of them again.
func (r *sortResolving) wantsWeb() bool {
	m := &r.m
	return m.artist == "" || m.title == "" || (m.album == "" && !m.single && !r.folder.singles)
}

func (r *sortResolving) nameQuery() string {
	name := sortParseFilename(r.stem)
	var query []string
	for _, s := range []string{name.artist, name.album, name.title} {
		if s != "" {
			query = append(query, s)
		}
	}
	return strings.Join(query, " ")
}

func (r *sortResolving) web(ctx context.Context, id *sortIdentifier) {
	if match := id.lookup(ctx, r.a, &r.m, r.nameQuery()); match != nil {
		r.m.applyMatch(match)
	}
}

// finish is what's left when the tags, names and the web have had their say.
func (r *sortResolving) finish() sortMeta {
	m, folder, dirs := r.m, r.folder, r.dirs
	if m.artist == "" {
		// Last resort: "Artist - Album/", "Artist/2001 - Album/", or exactly <root>/Artist/Album/
		// and <root>/Artist/Singles/ (so sorting again keeps untagged files in place).
		candidate := folder.artist
		if candidate == "" && (folder.albumLike || folder.singles || len(dirs) == 2) {
			candidate = folder.parentArtist
		}
		if candidate != "" {
			m.artist, m.fromName = candidate, true
			if !m.single && !folder.singles {
				m.album = FirstNonEmpty(m.album, folder.album)
				m.year = FirstNonEmpty(m.year, folder.year)
			}
		}
	}
	return m
}

func (m *sortMeta) applyMatch(x *sortMatch) {
	if m.artist == "" && x.Artist != "" {
		m.artist, m.fromWeb, m.web = x.Artist, true, x.Source
	}
	if m.title == "" && x.Title != "" {
		m.title, m.fromWeb, m.web = x.Title, true, x.Source
	}
	if m.album != "" || m.single {
		return
	}
	if x.Single {
		m.single, m.releaseType, m.fromWeb, m.web = true, "single", true, x.Source
	} else if x.Album != "" {
		m.releaseType, m.album, m.fromWeb, m.web = "album", x.Album, true, x.Source
		m.albumArtist = FirstNonEmpty(m.albumArtist, x.AlbumArtist)
		m.year = FirstNonEmpty(m.year, x.Year)
		if !m.trackFromTags { // a number from the file name belongs to some other release
			m.track, m.disc = x.Track, x.Disc
		}
	}
}
