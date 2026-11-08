package core

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSortParseFilename(t *testing.T) {
	cases := []struct {
		stem, artist, album, title string
		disc, track                int
	}{
		{"01 - Papercut", "", "", "Papercut", 0, 1},
		{"01. Papercut", "", "", "Papercut", 0, 1},
		{"01 Papercut", "", "", "Papercut", 0, 1},
		{"1-03 Cure for the Itch", "", "", "Cure for the Itch", 1, 3},
		{"Linkin Park - In The End", "Linkin Park", "", "In The End", 0, 0},
		{"Linkin Park - In The End (Official Video)", "Linkin Park", "", "In The End", 0, 0},
		{"Linkin Park - In the End [128kbps]", "Linkin Park", "", "In the End", 0, 0},
		{"Linkin Park - Hybrid Theory - 08 - In the End", "Linkin Park", "Hybrid Theory", "In the End", 0, 8},
		{"Deftones - 03. Change", "Deftones", "", "Change", 0, 3},
		{"05 - Deftones - Change", "Deftones", "", "Change", 0, 5},
		{"01-linkin_park-in_the_end-ftd", "Linkin Park", "", "In The End", 0, 1},
		{"99 Problems", "", "", "99 Problems", 0, 0},
		{"50 Cent - In Da Club", "50 Cent", "", "In Da Club", 0, 0},
		{"Artist - 1999", "Artist", "", "1999", 0, 0},
	}
	for _, c := range cases {
		h := sortParseFilename(c.stem)
		if h.artist != c.artist || h.album != c.album || h.title != c.title || h.disc != c.disc || h.track != c.track {
			t.Errorf("%q: got %+v", c.stem, h)
		}
	}
}

func TestSortParseFolders(t *testing.T) {
	cases := []struct {
		dirs                        []string
		artist, album, year, parent string
		disc                        int
	}{
		{[]string{"Linkin Park", "2000 - Hybrid Theory"}, "", "Hybrid Theory", "2000", "Linkin Park", 0},
		{[]string{"Deftones - White Pony (2000) [FLAC]"}, "Deftones", "White Pony", "2000", "", 0},
		{[]string{"Smashing Pumpkins", "Mellon Collie", "CD2"}, "", "Mellon Collie", "", "Smashing Pumpkins", 2},
		{[]string{"Downloads"}, "", "", "", "", 0},
		{[]string{"Linkin Park", "Meteora [320kbps]"}, "", "Meteora", "", "Linkin Park", 0},
		{[]string{"Music", "Hybrid Theory"}, "", "Hybrid Theory", "", "", 0},
	}
	for _, c := range cases {
		h := sortParseFolders(c.dirs)
		if h.artist != c.artist || h.album != c.album || h.year != c.year || h.parentArtist != c.parent || h.disc != c.disc {
			t.Errorf("%v: got %+v", c.dirs, h)
		}
	}
	if h := sortParseFolders([]string{"Deftones", "Singles"}); !h.singles || h.parentArtist != "Deftones" || h.album != "" {
		t.Errorf("singles: %+v", h)
	}
}

func TestSortKeysAndNames(t *testing.T) {
	eq := func(a, b string) {
		if a != b {
			t.Errorf("%q != %q", a, b)
		}
	}
	ne := func(a, b string) {
		if a == b {
			t.Errorf("both %q", a)
		}
	}
	eq(sortArtistKey("The Red Hot Chili Peppers"), sortArtistKey("red hot chili peppers"))
	eq(sortArtistKey("Linkin Park feat. Jay-Z"), sortArtistKey("Linkin Park"))
	eq(sortTitleKey("In the End (Remastered 2011)"), sortTitleKey("In The End"))
	eq(sortTitleKey("Song - 2009 Remaster"), sortTitleKey("Song"))
	eq(sortTitleKey("In the End (feat. Someone)"), sortTitleKey("In the End"))
	ne(sortTitleKey("In the End (Live)"), sortTitleKey("In the End"))
	ne(sortTitleKey("One (Extended Mix)"), sortTitleKey("One"))
	eq(sortAlbumKey("In the End - Single"), sortAlbumKey("In The End"))
	if !sortSameArtist("Linkin Park & Jay-Z", "Linkin Park") || !sortSameArtist("The Doors", "Doors") {
		t.Error("the same artist wasn't recognized")
	}
	if sortSameArtist("Pink", "Pink Floyd") || sortSameArtist("Pink", "Pinky") {
		t.Error("different artists taken for the same")
	}
	for _, junk := range []string{"Unknown Artist", "Track 05", "-", "www.example.com", "<unknown>"} {
		if !sortIsPlaceholder(junk) {
			t.Errorf("%q isn't a placeholder", junk)
		}
	}
	if sortIsPlaceholder("Hybrid Theory") {
		t.Error("a real album is a placeholder")
	}
	eq(sortSafeName("AC/DC", "Unknown"), "AC-DC")
	eq(sortSafeName("Album: Part 2?", "Unknown"), "Album - Part 2")
	eq(sortSafeName("...And Justice for All", "Unknown"), "…And Justice for All")
	eq(sortSafeName("CON", "Unknown"), "_CON")
	eq(sortSafeName("   ", "Unknown"), "Unknown")
	if len(sortSafeName(strings.Repeat("x", 500), "U")) > 200 {
		t.Error("too long")
	}
	eq(sortStripEdition("Hybrid Theory (Bonus Edition)"), "Hybrid Theory")
	eq(sortStripEdition("Meteora (20th Anniversary Edition) [Remastered]"), "Meteora")
	eq(sortStripEdition("In the End - Single"), "In the End")
}

// makeSortAudio writes a test tone with tags.
func makeSortAudio(t *testing.T, path string, seconds float64, bitrate string, tags map[string]string, freq int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if freq == 0 {
		freq = 440
	}
	args := []string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%g", freq, seconds)}
	if bitrate != "" {
		args = append(args, "-b:a", bitrate)
	}
	for k, v := range tags {
		args = append(args, "-metadata", k+"="+v)
	}
	if out, err := exec.Command("ffmpeg", append(args, path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
}

func sortTree(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func sortTestLibrary(t *testing.T) (string, string) {
	NeedFFmpeg(t)
	base := t.TempDir()
	lib := filepath.Join(base, "Music")
	lp := func(title, track string) map[string]string {
		return map[string]string{"artist": "Linkin Park", "album": "Hybrid Theory", "title": title, "track": track}
	}
	makeSortAudio(t, lib+"/Linkin Park/Hybrid Theory/01 Papercut.flac", 20, "", lp("Papercut", "1"), 0)
	makeSortAudio(t, lib+"/Linkin Park/Hybrid Theory/08 In the End.flac", 30, "", lp("In the End", "8/12"), 0)
	os.WriteFile(lib+"/Linkin Park/Hybrid Theory/cover.jpg", []byte("jpeg"), 0o644)
	// Lower-quality copies of In the End: a tagged 320, an untagged 128 named by a downloader.
	makeSortAudio(t, lib+"/Downloads/Linkin Park - In The End.mp3", 30, "320k", map[string]string{"artist": "Linkin Park", "title": "In The End"}, 0)
	makeSortAudio(t, lib+"/Downloads/Linkin Park - In the End [128kbps].mp3", 30, "128k", nil, 0)
	// Same artist and title but twice as long: an extended cut, which must be kept.
	makeSortAudio(t, lib+"/Downloads/In the End.mp3", 60, "320k", map[string]string{"artist": "Linkin Park", "title": "In the End"}, 500)
	// Untagged, but its folder tells the album.
	makeSortAudio(t, lib+"/Deftones - White Pony (2000) [FLAC]/Deftones - Change (In the House of Flies).mp3", 25, "192k", nil, 0)
	// A single, by release type, and a various-artists compilation.
	makeSortAudio(t, lib+"/misc/rhcp.ogg", 22, "", map[string]string{"artist": "Red Hot Chili Peppers", "title": "Under the Bridge", "album": "Under the Bridge", "RELEASETYPE": "single"}, 0)
	makeSortAudio(t, lib+"/Now 45/01.mp3", 18, "192k", map[string]string{"artist": "Somebody", "title": "Some Song", "album": "Now 45", "album_artist": "Various Artists", "track": "1"}, 0)
	// A byte-for-byte copy of Papercut lying around elsewhere.
	data, err := os.ReadFile(lib + "/Linkin Park/Hybrid Theory/01 Papercut.flac")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(lib+"/backup", 0o755)
	os.WriteFile(lib+"/backup/papercut copy.flac", data, 0o644)
	return base, lib
}

func sortOptionsFor(lib, dest string) SortOptions {
	return SortOptions{Sources: []string{lib}, Dest: dest, DuplicatesDir: filepath.Join(filepath.Dir(lib), "Duplicates"), Offline: true, Tolerance: 3}
}

func sortMakeTestPlan(t *testing.T, o SortOptions, cacheDir string) *sortPlan {
	t.Helper()
	if err := o.check(); err != nil {
		t.Fatal(err)
	}
	p, err := sortMakePlan(context.Background(), o, cacheDir, &Tools{FFmpeg: "ffmpeg", FFprobe: "ffprobe"}, nil, func(context.CancelFunc) {}, func(string, int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noProgress(string, int, int) {}

func TestSortPlanApplyUndo(t *testing.T) {
	base, lib := sortTestLibrary(t)
	before := sortTree(lib)
	dir := filepath.Join(base, "plan")
	p := sortMakeTestPlan(t, sortOptionsFor(lib, lib), base)
	if err := p.save(dir); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sortTree(lib), before) {
		t.Fatal("planning must not touch the library")
	}
	res, moved, err := sortApply(context.Background(), p, dir, noProgress)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProblemCount != 0 {
		t.Fatalf("problems: %v", res.Problems)
	}
	want := []string{
		"Deftones/White Pony/Change (In the House of Flies).mp3",
		"Linkin Park/Hybrid Theory/01 - Papercut.flac",
		"Linkin Park/Hybrid Theory/08 - In the End.flac",
		"Linkin Park/Hybrid Theory/cover.jpg",
		"Linkin Park/Singles/In the End.mp3", // the 60-second extended cut survives
		"Red Hot Chili Peppers/Singles/Under the Bridge.ogg",
		"Various Artists/Now 45/01 - Somebody - Some Song.mp3",
	}
	if got := sortTree(lib); !slices.Equal(got, want) {
		t.Errorf("library after sorting:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	dups := filepath.Join(base, "Duplicates")
	gotDups := sortTree(dups)
	if !slices.Contains(gotDups, sortReportName) || len(gotDups) != 4 {
		t.Errorf("duplicates: %v", gotDups)
	}
	report, _ := os.ReadFile(filepath.Join(dups, sortReportName))
	for _, s := range []string{"KEPT", "SUPERSEDED", "lower quality", "identical copy"} {
		if !strings.Contains(string(report), s) {
			t.Errorf("the report doesn't say %q", s)
		}
	}
	if len(moved) == 0 {
		t.Error("no moves reported")
	}
	// Planning again on a sorted library changes nothing.
	again := sortMakeTestPlan(t, sortOptionsFor(lib, lib), base)
	if len(again.Moves) != 0 {
		t.Errorf("a second plan wants to move: %+v", again.Moves)
	}
	// Undo puts everything back as it was, and the empty folders with it.
	if _, _, err := sortUndo(context.Background(), p, dir, noProgress); err != nil {
		t.Fatal(err)
	}
	if got := sortTree(lib); !slices.Equal(got, before) {
		t.Errorf("after undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if _, err := os.Stat(dups); err == nil {
		t.Error("the duplicates folder stayed behind")
	}
}

func TestSortCopyLeavesOriginals(t *testing.T) {
	base, lib := sortTestLibrary(t)
	before := sortTree(lib)
	dest := filepath.Join(base, "Sorted")
	o := sortOptionsFor(lib, dest)
	o.Copy = true
	p := sortMakeTestPlan(t, o, base)
	dir := filepath.Join(base, "plan")
	p.save(dir)
	if _, _, err := sortApply(context.Background(), p, dir, noProgress); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sortTree(lib), before) {
		t.Error("copying changed the originals")
	}
	if !slices.Contains(sortTree(dest), "Linkin Park/Hybrid Theory/08 - In the End.flac") {
		t.Errorf("sorted: %v", sortTree(dest))
	}
}

func TestSortNeverOverwrites(t *testing.T) {
	base, lib := sortTestLibrary(t)
	dest := filepath.Join(base, "Sorted")
	p := sortMakeTestPlan(t, sortOptionsFor(lib, dest), base)
	dir := filepath.Join(base, "plan")
	p.save(dir)
	// Something appears where a file is going, after planning.
	var m *SortMove
	for i := range p.Moves {
		if p.Moves[i].Kind == "track" {
			m = &p.Moves[i]
			break
		}
	}
	os.MkdirAll(filepath.Dir(m.Dst), 0o755)
	os.WriteFile(m.Dst, []byte("something else"), 0o644)
	res, _, err := sortApply(context.Background(), p, dir, noProgress)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(m.Dst); string(data) != "something else" {
		t.Error("an existing file was overwritten")
	}
	if _, err := os.Stat(m.Src); err != nil {
		t.Error("the file whose target was taken is gone")
	}
	if res.ProblemCount != 1 || !strings.Contains(res.Problems[0], "target already exists") {
		t.Errorf("problems: %d %v", res.ProblemCount, res.Problems)
	}
}

func TestSortSkipsFilesChangedSincePlanning(t *testing.T) {
	base, lib := sortTestLibrary(t)
	p := sortMakeTestPlan(t, sortOptionsFor(lib, filepath.Join(base, "Sorted")), base)
	dir := filepath.Join(base, "plan")
	p.save(dir)
	src := p.Moves[0].Src
	os.WriteFile(src, []byte("changed"), 0o644)
	res, _, err := sortApply(context.Background(), p, dir, noProgress)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProblemCount != 1 || !strings.Contains(res.Problems[0], "changed since planning") {
		t.Errorf("%v", res.Problems)
	}
	if data, _ := os.ReadFile(src); string(data) != "changed" {
		t.Error("the changed file was moved")
	}
}

// An interrupted apply continues where it stopped.
func TestSortApplyContinues(t *testing.T) {
	base, lib := sortTestLibrary(t)
	dest := filepath.Join(base, "Sorted")
	p := sortMakeTestPlan(t, sortOptionsFor(lib, dest), base)
	dir := filepath.Join(base, "plan")
	p.save(dir)
	ctx, cancel := context.WithCancel(context.Background())
	first := true
	stop := func(string, int, int) {
		if first {
			first = false
			cancel()
		}
	}
	res, _, err := sortApply(ctx, p, dir, stop)
	if err != nil || !res.Stopped {
		t.Fatalf("stopped %v, err %v", res.Stopped, err)
	}
	res, _, err = sortApply(context.Background(), p, dir, noProgress)
	if err != nil || res.ProblemCount != 0 {
		t.Fatalf("continuing: %v %v", err, res.Problems)
	}
	if !slices.Contains(sortTree(dest), "Linkin Park/Hybrid Theory/08 - In the End.flac") {
		t.Errorf("sorted: %v", sortTree(dest))
	}
}

func sortItemFor(path string, a sortMeta, dur float64, lossless bool, kbps int) *sortItem {
	return &sortItem{a: &sortAudio{Path: path, Root: "/m", Size: 1000, Duration: dur, Lossless: lossless, Kbps: kbps, Format: "X"}, m: a}
}

func TestSortAssignRules(t *testing.T) {
	o := SortOptions{Dest: "/lib", DuplicatesDir: "/dup"}
	target := func(items ...*sortItem) map[*sortItem]string {
		sortInferAlbumArtists(items)
		out, _ := sortAssign(items, &o)
		return out
	}
	// A lone title track without a release type counts as a single...
	a := sortItemFor("/m/a.mp3", sortMeta{artist: "A", album: "Song", title: "Song"}, 200, false, 320)
	if got := target(a)[a]; got != "/lib/A/Singles/Song.mp3" {
		t.Errorf("got %s", got)
	}
	// ...but stays in its album when something says it's an album.
	b := sortItemFor("/m/b.mp3", sortMeta{artist: "A", album: "Song", title: "Song", releaseType: "album", track: 1}, 200, false, 320)
	if got := target(b)[b]; got != "/lib/A/Song/01 - Song.mp3" {
		t.Errorf("got %s", got)
	}
	// Different spellings share one folder.
	x := sortItemFor("/m/x.mp3", sortMeta{artist: "Red Hot Chili Peppers", title: "One"}, 200, false, 320)
	y := sortItemFor("/m/y.mp3", sortMeta{artist: "red hot chili peppers", title: "Two"}, 200, false, 320)
	z := sortItemFor("/m/z.mp3", sortMeta{artist: "The Red Hot Chili Peppers", title: "Three"}, 200, false, 320)
	got := target(x, y, z)
	if filepath.Dir(got[x]) != filepath.Dir(got[y]) || filepath.Dir(got[y]) != filepath.Dir(got[z]) {
		t.Errorf("%v", got)
	}
	// Multi-disc numbering.
	d1 := sortItemFor("/m/d1.mp3", sortMeta{artist: "A", album: "Long", title: "One", track: 1, disc: 1}, 200, false, 320)
	d2 := sortItemFor("/m/d2.mp3", sortMeta{artist: "A", album: "Long", title: "Two", track: 1, disc: 2}, 200, false, 320)
	got = target(d1, d2)
	if got[d1] != "/lib/A/Long/1-01 - One.mp3" || got[d2] != "/lib/A/Long/2-01 - Two.mp3" {
		t.Errorf("%v %v", got[d1], got[d2])
	}
}

func TestSortKeepsBestAndDifferentCuts(t *testing.T) {
	m := sortMeta{artist: "A", title: "Song"}
	flac := sortItemFor("/m/a.flac", m, 200, true, 900)
	mp3 := sortItemFor("/m/a.mp3", m, 201, false, 320)
	long := sortItemFor("/m/b.mp3", m, 400, false, 320)
	groups := sortFindDuplicates(context.Background(), []*sortItem{flac, mp3, long}, 3, false, noProgress)
	if len(groups) != 1 || len(groups[flac]) != 1 || mp3.dupOf != flac || long.dupOf != nil {
		t.Errorf("groups %v; mp3 of %v; long of %v", groups, mp3.dupOf, long.dupOf)
	}
}

func TestSortPlanSurvivesARestart(t *testing.T) {
	base, lib := sortTestLibrary(t)
	p := sortMakeTestPlan(t, sortOptionsFor(lib, filepath.Join(base, "Sorted")), base)
	dir := filepath.Join(base, "plan")
	if err := p.save(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "plan.json"))
	var back sortPlan
	if err := json.Unmarshal(data, &back); err != nil || len(back.Moves) != len(p.Moves) {
		t.Fatalf("%v, %d moves", err, len(back.Moves))
	}
}

// Files needing the web are asked about after everything else is known, the ones asked about
// before cost nothing, and the lookups can be skipped.
func TestSortLookupsSkipAndMemory(t *testing.T) {
	id := &sortIdentifier{path: filepath.Join(t.TempDir(), "l.json"), cache: map[string]*sortMatch{}, failures: map[string]int{}, disabled: map[string]bool{}}
	mk := func(artist, title string) *sortResolving {
		return sortPrepare(&sortAudio{Path: "/m/x/" + title + ".mp3", Root: "/m", Duration: 100, Tags: sortTags{Artist: artist, Title: title}})
	}
	known, unknown := mk("Band", "Known"), mk("Band", "Unknown")
	key := func(r *sortResolving) string {
		return "|" + sortArtistKey(r.m.artist) + "|" + sortTitleKey(r.m.title) + "|25"
	}
	id.cache["musicbrainz"+key(known)] = &sortMatch{Source: "musicbrainz", Album: "An Album"}
	if !id.answered(known.a, &known.m, "") || id.answered(unknown.a, &unknown.m, "") {
		t.Fatal("answered is wrong")
	}
	var skip context.CancelFunc
	var stages []string
	notes := sortLookups(context.Background(), id, []*sortResolving{known, unknown}, func(f context.CancelFunc) {
		if f != nil {
			f() // skipped right away, before any question goes out
			skip = f
		}
	}, func(s string, _, total int) { stages = append(stages, fmt.Sprintf("%s/%d", s, total)) })
	if known.m.album != "An Album" {
		t.Errorf("remembered answer not used: %+v", known.m)
	}
	if skip == nil || len(notes) != 1 || !strings.Contains(notes[0], "1 files") {
		t.Errorf("notes %q, stages %q", notes, stages)
	}
	if id.requests != 0 {
		t.Errorf("%d requests", id.requests)
	}
}

// A song already filed in Artist/Singles isn't looked up again; one in an album-less folder is.
func TestSortSinglesFolderIsSettled(t *testing.T) {
	tags := sortTags{Artist: "Band", Title: "Song"}
	if r := sortPrepare(&sortAudio{Path: "/m/Band/Singles/Song.mp3", Root: "/m", Tags: tags}); r.wantsWeb() {
		t.Error("a file in Singles asks the web")
	}
	if r := sortPrepare(&sortAudio{Path: "/m/dump/Song.mp3", Root: "/m", Tags: tags}); !r.wantsWeb() {
		t.Error("a file without an album doesn't ask the web")
	}
}

// sortFind sees a file reachable from two roots, one through a link, once.
func TestSortFindThroughLinkedRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.MkdirAll(filepath.Join(real, "a"), 0o755)
	os.WriteFile(filepath.Join(real, "a", "x.mp3"), []byte("x"), 0o644)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	found, _, err := sortFind(context.Background(), []string{real, link}, nil, func(int) {})
	if err != nil || len(found) != 1 {
		t.Errorf("%d found, %v", len(found), err)
	}
}

func init() { sortSettle = 0 } // the tests' files were all written a moment ago

func TestSortLeavesUnfinishedDownloadsAlone(t *testing.T) {
	root := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	whole := write("Whole Song.flac")
	write("Incomplete.flac") // only a song's name, and a whole file
	for _, name := range []string{"song.flac.part", "song2.flac", "song2.flac.aria2", "song3.temp.m4a",
		"INCOMPLETEabc123def456song4.flac", ".hidden.flac", "song5.flac", "song5.flac.!qB"} {
		write(name)
	}
	found, partial, err := sortFind(context.Background(), []string{root}, nil, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range found {
		names = append(names, filepath.Base(a.Path))
	}
	sort.Strings(names)
	if got := strings.Join(names, ", "); got != "Incomplete.flac, Whole Song.flac" {
		t.Fatalf("found %s", got)
	}
	if partial != 5 { // not the .part itself: that one's no audio file to begin with
		t.Errorf("partial = %d", partial)
	}

	var check sortPartialCheck
	if check.unfinished(whole) {
		t.Error("a whole file counted as unfinished")
	}
	// A download that started after the plan was made.
	write("Whole Song.flac.crdownload")
	check = sortPartialCheck{}
	if !check.unfinished(whole) {
		t.Error("a file with a partial-download marker beside it wasn't noticed")
	}
}

func TestSortSkipsFilesStillBeingWritten(t *testing.T) {
	old := sortSettle
	sortSettle = time.Hour
	defer func() { sortSettle = old }()
	dir := t.TempDir()
	p := filepath.Join(dir, "growing.flac")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if !sortStillWritten(st) {
		t.Error("a file written just now counted as settled")
	}
	long := time.Now().Add(-2 * time.Hour)
	os.Chtimes(p, long, long)
	st, _ = os.Stat(p)
	if sortStillWritten(st) {
		t.Error("an old file counted as still being written")
	}
}
