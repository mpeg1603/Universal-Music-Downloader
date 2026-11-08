package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sort & merge: sorts music folders into Artist/Album folders and moves duplicate tracks into a
// separate folder, sorted the same way, with a report of which file superseded which.
//
//	<library>/<Artist>/<Album>/<NN - Title>.ext
//	<library>/<Artist>/Singles/<Title>.ext          (singles, or album unknown)
//	<library>/Various Artists/<Album>/<NN - Artist - Title>.ext
//	<duplicates>/<Artist>/<Album>/...               (superseded copies, same layout)
//
// Planning only reads. Only applying a plan changes anything, nothing is ever deleted or
// re-encoded, and an applied plan can be undone.

const (
	sortSingles       = "Singles"
	sortVariousName   = "Various Artists"
	sortUnknownArtist = "Unknown Artist"
)

var reSortAlbumType = regexp.MustCompile(`\b(?:album|ep)\b`)

var sortSidecars = setOf(".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".cue", ".log", ".nfo", ".txt", ".pdf", ".m3u", ".m3u8",
	".accurip", ".md5", ".sfv", ".ffp")

// SortOptions say what to sort, and where to.
type SortOptions struct {
	Sources        []string `json:"sources"`
	Dest           string   `json:"dest"`
	DuplicatesDir  string   `json:"duplicates_dir"`
	Copy           bool     `json:"copy,omitempty"`
	Offline        bool     `json:"offline,omitempty"`
	Tolerance      float64  `json:"tolerance,omitempty"`    // seconds two files may differ and still be one recording
	ArtistComps    bool     `json:"artist_comps,omitempty"` // various-artists albums go under each track's artist
	KeepFilenames  bool     `json:"keep_filenames,omitempty"`
	NoSidecars     bool     `json:"no_sidecars,omitempty"` // leave cover art, logs and cue sheets where they are
	NoContentCheck bool     `json:"no_content_check,omitempty"`
}

// SortMove is one file to move or copy: Kind is track, duplicate or sidecar.
type SortMove struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	Kind  string `json:"kind"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

type sortDupEntry struct {
	Src      string  `json:"src"`
	Dst      string  `json:"dst"`
	Quality  string  `json:"quality"`
	Duration float64 `json:"duration"`
	Size     int64   `json:"size"`
	Reason   string  `json:"reason,omitempty"`
}

// SortDuplicate is a song with extra copies: the one kept and those moved aside.
type SortDuplicate struct {
	Artist     string         `json:"artist"`
	Title      string         `json:"title"`
	Keeper     sortDupEntry   `json:"keeper"`
	Superseded []sortDupEntry `json:"superseded"`
}

type sortPlan struct {
	Format     int             `json:"format"`
	ID         string          `json:"id"`
	Created    string          `json:"created"`
	Options    SortOptions     `json:"options"`
	Moves      []SortMove      `json:"moves"`
	Duplicates []SortDuplicate `json:"duplicates"`
	Summary    SortSummary     `json:"summary"`
}

// SortSummary is what a plan comes to.
type SortSummary struct {
	Files, Unreadable                  int
	Bytes                              int64
	FromTags, FromNames, FromWeb       int
	Unknown                            int
	Artists, Albums, Singles           int
	DupSongs, DupFiles                 int
	DupBytes                           int64
	MoveTracks, MoveDups, MoveSidecars int
	InPlace                            int
	Notes                              []string
}

type sortItem struct {
	a         *sortAudio
	m         sortMeta
	dupOf     *sortItem
	identical bool
	keepName  bool
}

func (it *sortItem) rank() [6]int {
	q := it.a.quality()
	t := it.a.Tags
	tagged := 0
	for _, s := range []string{t.Artist, t.Title, t.Album, t.Track} {
		if s != "" {
			tagged++
		}
	}
	onAlbum := 0
	if it.m.album != "" && !it.m.single && !it.m.compilation {
		onAlbum = 1
	}
	return [6]int{q[0], q[1], tagged, onAlbum, int(it.a.Size), -len(it.a.Path)}
}

func rankLess(a, b [6]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func sortBest(group []*sortItem) *sortItem {
	best := group[0]
	for _, it := range group[1:] {
		if rankLess(best.rank(), it.rank()) {
			best = it
		}
	}
	return best
}

// sortFindDuplicates sets dupOf on every superseded file and returns who supersedes what. Two
// files are one recording if they're byte-for-byte identical, or share artist and title and
// differ in length by at most the tolerance: so a 7-minute extended cut and the 3-minute
// original are both kept.
func sortFindDuplicates(ctx context.Context, items []*sortItem, tolerance float64, content bool, progress func(string, int, int)) map[*sortItem][]*sortItem {
	loser := map[*sortItem]*sortItem{}
	if content {
		for _, group := range sortIdenticalGroups(ctx, items, progress) {
			keeper := sortBest(group)
			for _, it := range group {
				if it != keeper {
					loser[it] = keeper
					it.identical = true
				}
			}
		}
	}
	bySong := map[[2]string][]*sortItem{}
	for _, it := range items {
		if _, gone := loser[it]; !gone && it.m.artist != "" && it.m.title != "" && it.a.Duration > 0 {
			k := [2]string{sortArtistKey(it.m.artist), sortTitleKey(it.m.title)}
			bySong[k] = append(bySong[k], it)
		}
	}
	for _, members := range bySong {
		if len(members) < 2 {
			continue
		}
		sort.Slice(members, func(i, j int) bool {
			if members[i].a.Duration != members[j].a.Duration {
				return members[i].a.Duration < members[j].a.Duration
			}
			return members[i].a.Path < members[j].a.Path
		})
		// Lengths are measured from the shortest of a cluster, so they can't creep upward.
		var cluster []*sortItem
		flush := func() {
			if len(cluster) > 1 {
				keeper := sortBest(cluster)
				for _, it := range cluster {
					if it != keeper {
						loser[it] = keeper
					}
				}
			}
		}
		for _, it := range members {
			if len(cluster) > 0 && it.a.Duration-cluster[0].a.Duration > tolerance {
				flush()
				cluster = nil
			}
			cluster = append(cluster, it)
		}
		flush()
	}
	groups := map[*sortItem][]*sortItem{}
	for it, winner := range loser {
		for { // an identical copy's keeper may itself have lost on quality
			next, ok := loser[winner]
			if !ok {
				break
			}
			winner = next
			it.identical = false
		}
		it.dupOf = winner
		groups[winner] = append(groups[winner], it)
	}
	for _, members := range groups {
		sort.Slice(members, func(i, j int) bool { return members[i].a.Path < members[j].a.Path })
	}
	return groups
}

func sortIdenticalGroups(ctx context.Context, items []*sortItem, progress func(string, int, int)) [][]*sortItem {
	bySize := map[int64][]*sortItem{}
	for _, it := range items {
		if it.a.Size > 0 {
			bySize[it.a.Size] = append(bySize[it.a.Size], it)
		}
	}
	var candidates [][]*sortItem
	total := 0
	for _, g := range bySize {
		if len(g) > 1 {
			candidates = append(candidates, g)
			total += len(g)
		}
	}
	var groups [][]*sortItem
	done := 0
	for _, same := range candidates {
		byQuick := map[[32]byte][]*sortItem{}
		for _, it := range same {
			if ctx.Err() != nil {
				return nil
			}
			if d, ok := sortDigest(it.a.Path, true); ok {
				byQuick[d] = append(byQuick[d], it)
			}
			done++
			if done%50 == 0 {
				progress("Comparing files of the same size", done, total)
			}
		}
		for _, maybe := range byQuick {
			if len(maybe) < 2 {
				continue
			}
			byFull := map[[32]byte][]*sortItem{}
			for _, it := range maybe {
				if d, ok := sortDigest(it.a.Path, false); ok {
					byFull[d] = append(byFull[d], it)
				}
			}
			for _, g := range byFull {
				if len(g) > 1 {
					groups = append(groups, g)
				}
			}
		}
	}
	return groups
}

// sortDigest hashes a whole file, or (quick) its first and last 256 KB.
func sortDigest(path string, quick bool) (out [32]byte, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return out, false
	}
	defer f.Close()
	h := sha256.New()
	const chunk = 256 * 1024
	if quick {
		if _, err := io.CopyN(h, f, chunk); err != nil && err != io.EOF {
			return out, false
		}
		end, _ := f.Seek(0, io.SeekEnd)
		if end > 2*chunk {
			f.Seek(-chunk, io.SeekEnd)
			if _, err := io.CopyN(h, f, chunk); err != nil && err != io.EOF {
				return out, false
			}
		}
	} else if _, err := io.Copy(h, f); err != nil {
		return out, false
	}
	h.Sum(out[:0])
	return out, true
}

// sortInferAlbumArtists fills in a missing album artist for albums whose tracks sit together in
// one folder: tracks credited to "A" and "A & B" on one album belong in A's folder, and an
// album where no artist has a majority is a compilation. Only when the album makes up (nearly)
// the whole folder: a Downloads folder holding several "Greatest Hits" tracks isn't one album.
func sortInferAlbumArtists(items []*sortItem) {
	perDir := map[string]int{}
	for _, it := range items {
		perDir[filepath.Dir(it.a.Path)]++
	}
	groups := map[[2]string][]*sortItem{}
	for _, it := range items {
		m := &it.m
		if m.album != "" && m.artist != "" && m.albumArtist == "" && !m.compilation && !m.single {
			k := [2]string{filepath.Dir(it.a.Path), sortAlbumKey(m.album)}
			groups[k] = append(groups[k], it)
		}
	}
	for k, members := range groups {
		artists := map[string]bool{}
		for _, it := range members {
			artists[sortArtistKey(it.m.artist)] = true
		}
		if len(members) < 2 || len(artists) == 1 || float64(len(members)) < 0.8*float64(perDir[k[0]]) {
			continue
		}
		lead := make(map[*sortItem]string, len(members))
		count := map[string]int{}
		for _, it := range members {
			l := sortFold(sortLeadArtist(it.m.artist), true)
			lead[it] = l
			count[l]++
		}
		top, most := "", 0
		for l, n := range count {
			if n > most || (n == most && l < top) {
				top, most = l, n
			}
		}
		if most*2 >= len(members) {
			names := map[string]int{}
			name, most := "", 0
			for _, it := range members {
				if lead[it] == top {
					names[sortPrimaryArtist(it.m.artist)]++
				}
			}
			for _, it := range members { // of equally common names, the first one met
				if n := sortPrimaryArtist(it.m.artist); lead[it] == top && names[n] > most {
					name, most = n, names[n]
				}
			}
			for _, it := range members {
				if lead[it] == top {
					it.m.albumArtist = name
				}
			}
		} else if len(count) >= 3 {
			seen, unique := map[[2]int]bool{}, true
			for _, it := range members {
				if it.m.track > 0 {
					n := [2]int{it.m.disc, it.m.track}
					unique = unique && !seen[n]
					seen[n] = true
				}
			}
			if unique { // one release has no repeated track numbers
				for _, it := range members {
					it.m.compilation = true
				}
			}
		}
	}
}

// sortPickName is the most common spelling; on a tie, mixed case over ALL CAPS or all lower.
func sortPickName(spellings map[string]int) string {
	best, bestN, bestMixed := "", -1, false
	for s, n := range spellings {
		mixed := !allOneCase(s)
		if n > bestN || (n == bestN && (mixed && !bestMixed || (mixed == bestMixed && s > best))) {
			best, bestN, bestMixed = s, n, mixed
		}
	}
	return best
}

// allOneCase: it has letters that have case, and they're all lower case or all upper.
func allOneCase(s string) bool {
	return s != strings.ToUpper(s) && s == strings.ToLower(s) || s != strings.ToLower(s) && s == strings.ToUpper(s)
}

func sortFolderArtist(it *sortItem, o *SortOptions) string {
	m := &it.m
	if m.compilation && m.album != "" && !o.ArtistComps {
		return sortVariousName
	}
	if m.albumArtist != "" && !sortIsVarious(m.albumArtist) {
		return m.albumArtist
	}
	if m.artist != "" {
		return sortPrimaryArtist(m.artist)
	}
	return sortUnknownArtist
}

// sortAssign decides where every file goes.
func sortAssign(items []*sortItem, o *SortOptions) (map[*sortItem]string, map[string]bool) {
	artistOf := make(map[*sortItem]string, len(items))
	spellings := map[string]map[string]int{}
	for _, it := range items {
		name := sortFolderArtist(it, o)
		artistOf[it] = name
		k := sortArtistKey(name)
		if spellings[k] == nil {
			spellings[k] = map[string]int{}
		}
		spellings[k][name]++
	}
	// One folder per artist even if tags spell it differently.
	artistDir := make(map[string]string, len(spellings))
	for k, names := range spellings {
		artistDir[k] = sortSafeName(sortPickName(names), sortUnknownArtist)
	}
	albums := map[[2]string][]*sortItem{}
	for _, it := range items {
		if it.m.album != "" && !it.m.single {
			k := [2]string{sortArtistKey(artistOf[it]), sortAlbumKey(it.m.album)}
			albums[k] = append(albums[k], it)
		}
	}
	albumDir := map[[2]string]string{}
	multiDisc := map[[2]string]bool{}
	for k, members := range albums {
		if sortSingleLike(k[1], members) {
			continue
		}
		names := map[string]int{}
		discs := map[int]bool{}
		for _, it := range members {
			names[it.m.album]++
			if it.m.disc > 0 {
				discs[it.m.disc] = true
			}
			multiDisc[k] = multiDisc[k] || it.m.discTotal > 1
		}
		name := sortSafeName(sortPickName(names), "Unknown")
		if sortFold(name, false) == sortFold(sortSingles, false) {
			name += " (Album)"
		}
		albumDir[k] = name
		multiDisc[k] = multiDisc[k] || len(discs) > 1
	}
	preferred := make(map[*sortItem]string, len(items))
	for _, it := range items {
		aKey := sortArtistKey(artistOf[it])
		key := [2]string{}
		if it.m.album != "" && !it.m.single {
			key = [2]string{aKey, sortAlbumKey(it.m.album)}
		}
		folder, ok := albumDir[key]
		if !ok {
			folder = sortSingles
		}
		root := o.Dest
		if it.dupOf != nil {
			root = o.DuplicatesDir
		}
		name := sortFileName(it, folder, multiDisc[key], artistOf[it] == sortVariousName, o.KeepFilenames)
		preferred[it] = filepath.Join(root, artistDir[aKey], folder, name)
	}
	// Files already where they belong claim their paths first, so sorting again changes nothing.
	ordered := append([]*sortItem(nil), items...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := preferred[ordered[i]] == ordered[i].a.Path, preferred[ordered[j]] == ordered[j].a.Path
		if a != b {
			return a
		}
		return ordered[i].a.Path < ordered[j].a.Path
	})
	claimed := make(map[string]bool, len(items))
	targets := make(map[*sortItem]string, len(items))
	for _, it := range ordered {
		targets[it] = sortClaim(preferred[it], it.a.Path, claimed)
	}
	return targets, claimed
}

// sortSingleLike: an "album" that's really a single: "Song - Single", or every track named like
// it. A release type from the tags or the web overrides the guess.
func sortSingleLike(key string, members []*sortItem) bool {
	titled := 0
	same := 0
	for _, it := range members {
		if sortIsSingleTitle(it.m.album) {
			return true
		}
	}
	for _, it := range members {
		if reSortAlbumType.MatchString(it.m.releaseType) {
			return false
		}
		if it.m.title != "" {
			titled++
			if sortTitleKey(it.m.title) == key {
				same++
			}
		}
	}
	return titled > 0 && titled == same
}

func sortFileName(it *sortItem, folder string, multiDisc, various, keep bool) string {
	ext := filepath.Ext(it.a.Path)
	stem := strings.TrimSuffix(filepath.Base(it.a.Path), ext)
	ext = strings.ToLower(ext)
	if keep || it.keepName || it.m.title == "" {
		return sortSafeName(stem, "Untitled") + ext
	}
	name := it.m.title
	if folder != sortSingles {
		if various && it.m.artist != "" {
			name = it.m.artist + " - " + name
		}
		if it.m.track > 0 {
			number := fmt.Sprintf("%02d", it.m.track)
			if multiDisc && it.m.disc > 0 {
				number = fmt.Sprintf("%d-%s", it.m.disc, number)
			}
			name = number + " - " + name
		}
	}
	return sortSafeName(name, "Untitled") + ext
}

// sortClaim reserves path, or "name (2).ext" and so on if taken. Never an existing file.
func sortClaim(path, src string, claimed map[string]bool) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for n := 1; ; n++ {
		candidate := path
		if n > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", base, n, ext)
		}
		key := strings.ToLower(candidate) // stay safe on case-insensitive disks
		if candidate == src {
			claimed[key] = true
			return candidate
		}
		if !claimed[key] {
			if _, err := os.Lstat(candidate); err != nil {
				claimed[key] = true
				return candidate
			}
		}
	}
}

// sortSidecarMoves: cover art, logs and cue sheets follow the audio when a whole folder goes to
// one album folder.
func sortSidecarMoves(items []*sortItem, targets map[*sortItem]string, claimed map[string]bool) []SortMove {
	byDir := map[string][]*sortItem{}
	for _, it := range items {
		d := filepath.Dir(it.a.Path)
		byDir[d] = append(byDir[d], it)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var moves []SortMove
	for _, dir := range dirs {
		members := byDir[dir]
		var kept []*sortItem
		for _, it := range members {
			if it.dupOf == nil {
				kept = append(kept, it)
			}
		}
		if len(kept) == 0 {
			kept = members
		}
		dest, one := "", true
		for _, it := range kept {
			d := filepath.Dir(targets[it])
			if dest != "" && d != dest {
				one = false
				break
			}
			dest = d
		}
		if !one || dest == dir {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") || !sortSidecars[strings.ToLower(filepath.Ext(e.Name()))] || !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			src := filepath.Join(dir, e.Name())
			moves = append(moves, SortMove{src, sortClaim(filepath.Join(dest, e.Name()), src, claimed), "sidecar", info.Size(), info.ModTime().UnixNano()})
		}
	}
	return moves
}

func sortReason(it, keeper *sortItem) string {
	if it.identical && it.a.quality() == keeper.a.quality() {
		return "identical copy"
	}
	q, k := it.a.quality(), keeper.a.quality()
	if q[0] < k[0] || (q[0] == k[0] && q[1] < k[1]) {
		return "lower quality"
	}
	return "same quality; the kept copy has fuller tags or sits on the original album"
}

func sortEntry(it *sortItem, targets map[*sortItem]string) sortDupEntry {
	return sortDupEntry{Src: it.a.Path, Dst: targets[it], Quality: it.a.qualityLabel(), Duration: float64(int(it.a.Duration*10)) / 10, Size: it.a.Size}
}

// sortBuild decides everything from what was read, without changing a thing.
func sortBuild(items []*sortItem, dups map[*sortItem][]*sortItem, o SortOptions) *sortPlan {
	sortInferAlbumArtists(items)
	for _, it := range items {
		_, err := os.Stat(strings.TrimSuffix(it.a.Path, filepath.Ext(it.a.Path)) + ".cue")
		it.keepName = err == nil
	}
	targets, claimed := sortAssign(items, &o)
	p := &sortPlan{Format: 1, ID: time.Now().Format("20060102-150405"), Created: time.Now().Format("2006-01-02 15:04:05"), Options: o}
	s := &p.Summary
	artists, albums := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		s.Files++
		s.Bytes += it.a.Size
		if it.a.Err != "" {
			s.Unreadable++
		}
		switch {
		case it.m.artist == "":
			s.Unknown++
		case it.m.fromWeb:
			s.FromWeb++
		case it.m.fromName:
			s.FromNames++
		default:
			s.FromTags++
		}
		target := targets[it]
		if target == it.a.Path {
			s.InPlace++
		} else {
			kind := "track"
			if it.dupOf != nil {
				kind = "duplicate"
				s.MoveDups++
			} else {
				s.MoveTracks++
			}
			p.Moves = append(p.Moves, SortMove{it.a.Path, target, kind, it.a.Size, it.a.Mtime})
		}
		if it.dupOf != nil {
			s.DupFiles++
			s.DupBytes += it.a.Size
			continue
		}
		if rel, err := filepath.Rel(o.Dest, target); err == nil {
			if parts := strings.Split(rel, string(filepath.Separator)); len(parts) >= 3 {
				artists[parts[0]] = true
				if parts[1] == sortSingles {
					s.Singles++
				} else {
					albums[parts[0]+"\x00"+parts[1]] = true
				}
			}
		}
	}
	s.Artists, s.Albums, s.DupSongs = len(artists), len(albums), len(dups)
	sort.Slice(p.Moves, func(i, j int) bool { return p.Moves[i].Src < p.Moves[j].Src })
	if !o.NoSidecars {
		side := sortSidecarMoves(items, targets, claimed)
		p.Moves = append(p.Moves, side...)
		s.MoveSidecars = len(side)
	}
	type group struct {
		k    *sortItem
		list []*sortItem
	}
	groups := make([]group, 0, len(dups))
	for k, l := range dups {
		groups = append(groups, group{k, l})
	}
	sort.Slice(groups, func(i, j int) bool {
		a, b := groups[i].k, groups[j].k
		if x, y := sortArtistKey(a.m.artist), sortArtistKey(b.m.artist); x != y {
			return x < y
		}
		if x, y := sortTitleKey(a.m.title), sortTitleKey(b.m.title); x != y {
			return x < y
		}
		return a.a.Path < b.a.Path
	})
	for _, g := range groups {
		d := SortDuplicate{Artist: FirstNonEmpty(g.k.m.artist, sortUnknownArtist), Title: FirstNonEmpty(g.k.m.title, filepath.Base(g.k.a.Path)), Keeper: sortEntry(g.k, targets)}
		for _, it := range g.list {
			e := sortEntry(it, targets)
			e.Reason = sortReason(it, g.k)
			d.Superseded = append(d.Superseded, e)
		}
		p.Duplicates = append(p.Duplicates, d)
	}
	return p
}

// save writes the plan and the things worth reading before applying it.
func (p *sortPlan) save(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "plan.json"), data); err != nil {
		return err
	}
	info, _ := json.Marshal(SortPlanInfo{ID: p.ID, Created: p.Created, Moves: len(p.Moves), Options: p.Options})
	if err := writeFileAtomic(filepath.Join(dir, "info.json"), info); err != nil {
		return err
	}
	var moves strings.Builder
	moves.WriteString("kind\tfrom\tto\n")
	for _, m := range p.Moves {
		fmt.Fprintf(&moves, "%s\t%s\t%s\n", m.Kind, m.Src, m.Dst)
	}
	if err := os.WriteFile(filepath.Join(dir, "moves.tsv"), []byte(moves.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "plan.txt"), []byte(p.text()), 0o644); err != nil {
		return err
	}
	if len(p.Duplicates) > 0 {
		return os.WriteFile(filepath.Join(dir, "duplicates_report.preview.txt"), []byte(p.duplicatesReport(nil)), 0o644)
	}
	return nil
}

func sortSize(n int64) string { return FmtBytes(n) }

func (p *sortPlan) text() string {
	s, o := &p.Summary, &p.Options
	verb := "moved"
	if o.Copy {
		verb = "copied"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Sort & merge plan, made %s\n\nFolders:            %s\nSorted library:     %s\nDuplicates folder:  %s\nFiles are %s; nothing is deleted or re-encoded.\n\n",
		p.Created, strings.Join(o.Sources, ", "), o.Dest, o.DuplicatesDir, verb)
	fmt.Fprintf(&b, "Audio files found:  %d (%s)\n  unreadable:       %d\nWhere the artist/album/title came from:\n  tags alone:              %d\n  file and folder names:   %d\n  the web:                 %d\n  still unknown:           %d\n\n",
		s.Files, sortSize(s.Bytes), s.Unreadable, s.FromTags, s.FromNames, s.FromWeb, s.Unknown)
	fmt.Fprintf(&b, "Library after sorting: %d artists, %d albums, %d tracks in Singles\nDuplicates: %d songs have extra copies; %d files (%s) go to the duplicates folder\n\n",
		s.Artists, s.Albums, s.Singles, s.DupSongs, s.DupFiles, sortSize(s.DupBytes))
	fmt.Fprintf(&b, "Files to be %s:\n  tracks:           %d\n  duplicates:       %d\n  cover art etc.:   %d\n  already in place: %d\n", verb, s.MoveTracks, s.MoveDups, s.MoveSidecars, s.InPlace)
	for _, n := range s.Notes {
		b.WriteString("\n" + n + "\n")
	}
	return b.String()
}

func (p *sortPlan) duplicatesReport(problems map[string]string) string {
	total := 0
	for _, g := range p.Duplicates {
		total += len(g.Superseded)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Universal Music Downloader: duplicates report\nPlanned %s\n\nThe sorted library is in %s.\nThis folder holds %d superseded copies of %d songs. For each song the\nbest-quality copy (lossless > higher bitrate > lower bitrate) was kept in the library.\nCopies only count as the same recording when artist, title AND length (within a few seconds) match,\nso different cuts such as extended or radio edits are never listed here.\n\n",
		p.Created, p.Options.Dest, total, len(p.Duplicates))
	line := func(label string, e sortDupEntry) {
		fmt.Fprintf(&b, "  %-12s  %s, %s\n                %s\n", label, e.Quality, FmtDuration(e.Duration), e.Dst)
		if e.Dst != e.Src {
			fmt.Fprintf(&b, "                (was %s)\n", e.Src)
		}
		if why := problems[e.Src]; why != "" {
			fmt.Fprintf(&b, "                NOT MOVED: %s\n", why)
		}
	}
	for i, g := range p.Duplicates {
		fmt.Fprintf(&b, "[%d] %s - %s\n", i+1, g.Artist, g.Title)
		line("KEPT", g.Keeper)
		for _, d := range g.Superseded {
			line("SUPERSEDED", d)
			fmt.Fprintf(&b, "                reason: %s\n", d.Reason)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// sortEach runs do for 0..n-1 on a few workers, until ctx ends.
func sortEach(ctx context.Context, n, workers int, do func(i int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				do(i)
			}
		}()
	}
	wg.Wait()
}

// sortLookups fills in from the web what's missing. What's been asked before is known at once;
// the rest goes out one question at a time (MusicBrainz takes one a second), and can be skipped
// with the function handed to skippable. It returns what to tell about it.
func sortLookups(ctx context.Context, id *sortIdentifier, all []*sortResolving, skippable func(context.CancelFunc), progress func(string, int, int)) []string {
	var ask []*sortResolving
	for _, r := range all {
		if !r.wantsWeb() {
			continue
		}
		if id.answered(r.a, &r.m, r.nameQuery()) {
			r.web(ctx, id)
		} else {
			ask = append(ask, r)
		}
	}
	if len(ask) == 0 {
		return nil
	}
	web, skip := context.WithCancel(ctx)
	defer skip()
	skippable(skip)
	defer skippable(nil)
	const stage = "Looking up files with incomplete tags"
	progress(stage, 0, len(ask))
	var done atomic.Int64
	sortEach(web, len(ask), 4, func(i int) {
		ask[i].web(web, id)
		if web.Err() == nil {
			progress(stage, int(done.Add(1)), len(ask))
		}
	})
	if left := len(ask) - int(done.Load()); left > 0 && ctx.Err() == nil {
		return []string{fmt.Sprintf("The online lookups were skipped: %d files were filed from their tags and names alone. Planning again carries on with them.", left)}
	}
	return nil
}

// sortMakePlan does the whole reading and deciding.
func sortMakePlan(ctx context.Context, o SortOptions, cacheDir string, tools *Tools, dz *Deezer, skippable func(context.CancelFunc), progress func(string, int, int)) (*sortPlan, error) {
	progress("Looking for audio files", 0, 0)
	exclude := []string{}
	if o.DuplicatesDir != "" {
		exclude = append(exclude, o.DuplicatesDir)
	}
	files, err := sortFind(ctx, o.Sources, exclude, func(n int) { progress("Looking for audio files", n, 0) })
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	cache := openSortTagCache(filepath.Join(cacheDir, "sort-tags.jsonl"))
	sortRead(ctx, files, cache, tools, func(n int) { progress("Reading tags", n, len(files)) })
	cache.close()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var notes []string
	if o.Offline {
		notes = append(notes, "The web wasn't used: files with incomplete tags weren't looked up.")
	}
	progress("Working out artist, album and title", 0, len(files))
	resolving := make([]*sortResolving, len(files))
	var prepared atomic.Int64
	sortEach(ctx, len(files), 4, func(i int) {
		resolving[i] = sortPrepare(files[i])
		if n := prepared.Add(1); n%1000 == 0 {
			progress("Working out artist, album and title", int(n), len(files))
		}
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !o.Offline {
		id := newSortIdentifier(filepath.Join(cacheDir, "sort-lookups.json"), dz)
		defer id.save()
		notes = append(notes, sortLookups(ctx, id, resolving, skippable, progress)...)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		id.mu.Lock()
		for name := range id.disabled {
			notes = append(notes, name+" kept failing and was skipped part-way; planning again later retries only what's missing.")
		}
		id.mu.Unlock()
	}
	items := make([]*sortItem, len(files))
	for i, r := range resolving {
		items[i] = &sortItem{a: files[i], m: r.finish()}
	}
	progress("Looking for duplicates", 0, 0)
	dups := sortFindDuplicates(ctx, items, o.Tolerance, !o.NoContentCheck, progress)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	progress("Deciding where everything goes", 0, 0)
	p := sortBuild(items, dups, o)
	p.Summary.Notes = notes
	return p, nil
}
