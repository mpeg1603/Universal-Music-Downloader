package core

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// What we share on Soulseek: the files of the shared folders (your library and downloads
// folder unless set otherwise), read only. Each folder appears to others under its name
// ("Music\Artist\Album\01 Song.flac"). Search requests arrive several times a second, so
// they're answered from a word index.

type slskShareRoot struct {
	Name, Path string
}

type slskShared struct {
	vpath string // virtual path, backslash-separated
	real  string
	size  int64
	attrs []slskAttr
}

type slskFolder struct {
	vpath string
	files []int32
}

type slskShares struct {
	mu       sync.RWMutex
	roots    []slskShareRoot
	files    []slskShared
	byPath   map[string]int32
	byLower  map[string]int32
	words    map[string][]int32
	folders  []slskFolder
	folderAt map[string]int
	list     []byte // the compressed shared-file list, made when first asked for

	scanMu   sync.Mutex
	scanning bool
	scanned  time.Time
	took     time.Duration
	problem  string
	meta     func(real string) (Quality, float64, bool)
}

func newSlskShares() *slskShares {
	return &slskShares{byPath: map[string]int32{}, byLower: map[string]int32{}, words: map[string][]int32{}, folderAt: map[string]int{}}
}

// shareRoots names the shared folders: by their base name, made unique.
func shareRoots(dirs []string) []slskShareRoot {
	var out []slskShareRoot
	used := map[string]bool{}
	for _, d := range dirs {
		d = CleanPath(d)
		if d == "" {
			continue
		}
		name := filepath.Base(d)
		if name == "" || name == "/" || name == "." {
			name = "Shared"
		}
		base := name
		for i := 2; used[strings.ToLower(name)]; i++ {
			name = base + " " + strconv.Itoa(i)
		}
		used[strings.ToLower(name)] = true
		out = append(out, slskShareRoot{Name: name, Path: d})
	}
	return out
}

var slskSkipNames = map[string]bool{"thumbs.db": true, "desktop.ini": true, "@eadir": true, "#recycle": true, "#snapshot": true}

// scan reads the shared folders again (meta gives what the app knows about a file's audio).
func (s *slskShares) scan(ctx context.Context, roots []slskShareRoot, meta func(string) (Quality, float64, bool)) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	s.mu.Lock()
	s.scanning, s.meta = true, meta
	s.mu.Unlock()
	start := time.Now()
	n := newSlskShares()
	n.roots = roots
	var problems []string
	for _, root := range roots {
		if st, err := os.Stat(root.Path); err != nil || !st.IsDir() {
			problems = append(problems, root.Path+" isn't a readable folder")
			continue
		}
		filepath.WalkDir(root.Path, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}
			name := d.Name()
			if p != root.Path && (strings.HasPrefix(name, ".") || slskSkipNames[strings.ToLower(name)]) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root.Path, p)
			n.addLocked(root.Name+`\`+strings.ReplaceAll(rel, string(filepath.Separator), `\`), p, info.Size(), meta)
			return nil
		})
	}
	if ctx.Err() != nil {
		s.mu.Lock()
		s.scanning = false
		s.mu.Unlock()
		return
	}
	n.sortFolders()
	s.mu.Lock()
	s.roots, s.files, s.byPath, s.byLower, s.words, s.folders, s.folderAt = n.roots, n.files, n.byPath, n.byLower, n.words, n.folders, n.folderAt
	s.list, s.scanning, s.scanned, s.took, s.problem = nil, false, time.Now(), time.Since(start), strings.Join(problems, "; ")
	s.mu.Unlock()
}

// add shares a new file (a finished download) if it's inside a shared folder.
func (s *slskShares) add(real string) {
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, root := range s.roots {
		if !isWithin(real, root.Path) {
			continue
		}
		rel, err := filepath.Rel(root.Path, real)
		if err != nil {
			return
		}
		vpath := root.Name + `\` + strings.ReplaceAll(rel, string(filepath.Separator), `\`)
		if _, ok := s.byPath[vpath]; ok {
			return
		}
		s.addLocked(vpath, real, st.Size(), s.meta)
		s.list = nil
		return
	}
}

func (s *slskShares) addLocked(vpath, real string, size int64, meta func(string) (Quality, float64, bool)) {
	f := slskShared{vpath: vpath, real: real, size: size}
	if meta != nil && audioExts[strings.ToLower(filepath.Ext(real))] {
		if q, dur, ok := meta(real); ok {
			f.attrs = slskAttrsFor(q, dur)
		}
	}
	idx := int32(len(s.files))
	s.files = append(s.files, f)
	s.byPath[vpath] = idx
	s.byLower[strings.ToLower(vpath)] = idx
	seen := map[string]bool{}
	for _, w := range slskWords(vpath) {
		if !seen[w] {
			seen[w] = true
			s.words[w] = append(s.words[w], idx)
		}
	}
	dir := vpath[:strings.LastIndexByte(vpath, '\\')]
	at, ok := s.folderAt[dir]
	if !ok {
		at = len(s.folders)
		s.folderAt[dir] = at
		s.folders = append(s.folders, slskFolder{vpath: dir})
	}
	s.folders[at].files = append(s.folders[at].files, idx)
}

func (s *slskShares) sortFolders() {
	sort.Slice(s.folders, func(i, j int) bool { return s.folders[i].vpath < s.folders[j].vpath })
	for i, f := range s.folders {
		s.folderAt[f.vpath] = i
	}
}

// slskAttrsFor gives a file's attributes as Nicotine+ sends them: duration, sample rate and
// bit depth for lossless files; bitrate, duration and VBR for others.
func slskAttrsFor(q Quality, dur float64) []slskAttr {
	var a []slskAttr
	if q.Lossless || q.BitDepth > 0 {
		if dur > 0 {
			a = append(a, slskAttr{slskAttrDuration, uint32(dur + 0.5)})
		}
		if q.SampleRate > 0 {
			a = append(a, slskAttr{slskAttrSampleRate, uint32(q.SampleRate)})
		}
		if q.BitDepth > 0 {
			a = append(a, slskAttr{slskAttrBitDepth, uint32(q.BitDepth)})
		}
		return a
	}
	if q.Kbps > 0 {
		a = append(a, slskAttr{slskAttrBitrate, uint32(q.Kbps)})
	}
	if dur > 0 {
		a = append(a, slskAttr{slskAttrDuration, uint32(dur + 0.5)})
	}
	if q.Kbps > 0 {
		a = append(a, slskAttr{slskAttrVBR, 0})
	}
	return a
}

// slskWords splits a path or search into lowercase words: punctuation separates them.
func slskWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	})
}

// search finds the shared files matching a search: every word (in the path), none of the
// -excluded ones; *partial words match the ends of words. Paths with phrases the server
// excludes from the network are left out.
func (s *slskShares) search(text string, excluded []string, limit int) []slskFile {
	var incl, excl, partial []string
	for _, w := range strings.Fields(strings.ToLower(text)) {
		switch {
		case strings.HasPrefix(w, "-") && len(w) > 1:
			excl = append(excl, slskWords(w)...)
		case strings.HasPrefix(w, "*") && len(w) > 1:
			partial = append(partial, slskWords(w)...)
		default:
			incl = append(incl, slskWords(w)...)
		}
	}
	if len(incl) == 0 {
		return nil // at least one whole word, as the official clients require
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	lists := make([][]int32, 0, len(incl))
	for _, w := range incl {
		l, ok := s.words[w]
		if !ok {
			return nil
		}
		lists = append(lists, l)
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	var out []slskFile
	for _, idx := range lists[0] {
		if !postingHas(lists[1:], idx) {
			continue
		}
		f := &s.files[idx]
		low := strings.ToLower(f.vpath)
		if slskExcluded(low, excl, partial, excluded) {
			continue
		}
		out = append(out, slskFile{Name: f.vpath, Size: f.size, Attrs: f.attrs})
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func postingHas(lists [][]int32, idx int32) bool {
	for _, l := range lists {
		i := sort.Search(len(l), func(k int) bool { return l[k] >= idx })
		if i >= len(l) || l[i] != idx {
			return false
		}
	}
	return true
}

func slskExcluded(low string, excl, partial, phrases []string) bool {
	if len(excl) > 0 {
		words := map[string]bool{}
		for _, w := range slskWords(low) {
			words[w] = true
		}
		for _, w := range excl {
			if words[w] {
				return true
			}
		}
	}
	for _, p := range partial {
		found := false
		for _, w := range slskWords(low) {
			if strings.HasSuffix(w, p) {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	for _, p := range phrases {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// lookup finds a shared file by the path a user asked for (Soulseek NS lowercases paths).
func (s *slskShares) lookup(vpath string) (string, int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx, ok := s.byPath[vpath]
	if !ok {
		idx, ok = s.byLower[strings.ToLower(vpath)]
	}
	if !ok {
		return "", 0, false
	}
	f := s.files[idx]
	st, err := os.Stat(f.real)
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, false
	}
	return f.real, st.Size(), true
}

func (s *slskShares) counts() (folders, files int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.folders), len(s.files)
}

type slskShareStatus struct {
	Roots          []slskShareRoot
	Folders, Files int
	Scanning       bool
	Scanned        time.Time
	Took           time.Duration
	Problem        string
}

func (s *slskShares) Status() slskShareStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slskShareStatus{Roots: s.roots, Folders: len(s.folders), Files: len(s.files), Scanning: s.scanning,
		Scanned: s.scanned, Took: s.took, Problem: s.problem}
}

func (s *slskShares) writeFolder(m *slskMsg, f slskFolder) {
	m.str(f.vpath).u32(uint32(len(f.files)))
	for _, idx := range f.files {
		x := s.files[idx]
		writeSlskFile(m, slskFile{Name: x.vpath[strings.LastIndexByte(x.vpath, '\\')+1:], Size: x.size, Attrs: x.attrs})
	}
}

// fileList is the answer to "browse your files": every shared folder, compressed.
func (s *slskShares) fileList() []byte {
	s.mu.RLock()
	if s.list != nil {
		defer s.mu.RUnlock()
		return s.list
	}
	m := &slskMsg{}
	m.u32(uint32(len(s.folders)))
	for _, f := range s.folders {
		s.writeFolder(m, f)
	}
	m.u32(0).u32(0) // unknown, no private folders
	list := slskCompress(m.b)
	s.mu.RUnlock()
	s.mu.Lock()
	s.list = list
	s.mu.Unlock()
	return list
}

// folderContents adds a folder and its subfolders to m (a FolderContentsResponse).
func (s *slskShares) folderContents(m *slskMsg, folder string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	prefix := strings.ToLower(strings.TrimRight(folder, `\`))
	var list []slskFolder
	for _, f := range s.folders {
		low := strings.ToLower(f.vpath)
		if low == prefix || strings.HasPrefix(low, prefix+`\`) {
			list = append(list, f)
		}
	}
	m.u32(uint32(len(list)))
	for _, f := range list {
		s.writeFolder(m, f)
	}
}
