package core

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSymbolTitles: a title of symbols only ("M83 - *") is compared as it is, since normalize
// leaves nothing of it.
func TestSymbolTitles(t *testing.T) {
	star := MakeTrack("*", []string{"M83"}, "Before the Dawn Heals Us", 165)
	for _, c := range []Candidate{
		{Title: "*", Artist: "M83", ArtistIsCredit: true, Duration: 165},
		{Title: "M83 - *", Artist: "M83", Duration: 165},
		{Title: "* (Official Audio)", Artist: "M83", Duration: 166},
	} {
		if m := evaluate(star, c); m.Reject != "" {
			t.Errorf("%q: %s", c.Title, m.Reject)
		}
	}
	for _, c := range []Candidate{
		{Title: "Outro", Artist: "M83", Duration: 165},
		{Title: "M83 - My Tears Are Becoming A Sea", Artist: "M83", Duration: 165},
		{Title: "**", Artist: "M83", Duration: 165},
	} {
		if m := evaluate(star, c); m.Reject == "" {
			t.Errorf("%q was accepted", c.Title)
		}
	}
	if !titleFits(newTrackForm(star), newFileForm("*")) || titleFits(newTrackForm(star), newFileForm("Outro")) {
		t.Error("library check of a symbol title")
	}
	if q := songQueries(star, 3); len(q) < 2 || q[1] != "M83 Before the Dawn Heals Us *" {
		t.Errorf("queries: %q", q)
	}
}

func hasName(list []NameVariant, artist, title string) bool {
	for _, v := range list {
		if strings.EqualFold(v.Artist, artist) && strings.EqualFold(v.Title, title) {
			return true
		}
	}
	return false
}

// TestNameSuggestions: the names a song might go by elsewhere.
func TestNameSuggestions(t *testing.T) {
	s := nameSuggestions(MakeTrack("На заре", []string{"Alyans"}, "", 0))
	if !hasName(s, "Алянс", "На заре") || !hasName(s, "Alyans", "Na zare") {
		t.Errorf("Alyans: %+v", s)
	}
	s = nameSuggestions(MakeTrack("Kosmicheskij Pinbol", []string{"Barbitura"}, "", 0))
	if !hasName(s, "Барбитура", "Космический Пинбол") || !hasName(s, "Барбитура", "Kosmicheskij Pinbol") || !hasName(s, "Barbitura", "Космический Пинбол") {
		t.Errorf("Barbitura: %+v", s)
	}
	s = nameSuggestions(MakeTrack("Acid In My Mind", []string{"Various Artists"}, "", 0))
	if len(s) == 0 || s[0].Artist != "" || s[0].Title != "Acid In My Mind" {
		t.Errorf("Various Artists: %+v", s)
	}
	s = nameSuggestions(MakeTrack("Memo Boy (brian is the most beautiful)", []string{"143Pey", "Patrick Bateman", "Walter White"}, "", 0))
	if !hasName(s, "brian is the most beautiful", "Memo Boy") || !hasName(s, "Patrick Bateman", "Memo Boy") {
		t.Errorf("Memo Boy: %+v", s)
	}
	// English names aren't written in Cyrillic.
	s = nameSuggestions(MakeTrack("Pretend. Release. The Close", []string{"Emarosa"}, "", 0))
	for _, v := range s {
		if hasCyrillic(v.Artist + v.Title) {
			t.Errorf("Emarosa: %+v", v)
		}
	}
	if !looksTransliterated("Diingo Shkola ne nuzhna") || looksTransliterated("Daft Punk Get Lucky") || looksTransliterated("I Wanna Be Yours") {
		t.Error("looksTransliterated")
	}
	// Version words in brackets aren't names.
	s = nameSuggestions(MakeTrack("Strobe (Radio Edit)", []string{"deadmau5"}, "", 0))
	if hasName(s, "Radio Edit", "Strobe") {
		t.Errorf("Strobe: %+v", s)
	}
	// Found under another name: any artist only with a close title.
	any := NameVariant{Title: "Acid In My Mind"}.track(MakeTrack("Acid In My Mind", []string{"Various Artists"}, "", 300))
	if m := evaluate(any, Candidate{Title: "Acid In My Mind", Artist: "Sterling Moss", Duration: 301}); m.Reject != "" {
		t.Errorf("any artist: %s", m.Reject)
	}
	if m := evaluate(any, Candidate{Title: "Acid In My Head", Artist: "Someone", Duration: 300}); m.Reject == "" {
		t.Error("any artist took a different title")
	}
}

// TestSearchUnderOtherNames: a song not found under its own name is found under the one you
// give, and still tagged with its own.
func TestSearchUnderOtherNames(t *testing.T) {
	tools := NeedFFmpeg(t)
	acid := MakeTrack("Acid In My Mind", []string{"Various Artists"}, "Acid Classics", 200)
	src := &nameSource{T: t, by: map[string][]Candidate{"Sterling Moss": Found("fake", "Acid In My Mind", "Sterling Moss", 200)}}
	m := TestManager(t, tools, []Source{src}, TestLibrary(acid), nil)
	out := RunManager(t, m)
	if out[acid.Key].State != JobFailed {
		t.Fatalf("found under its own name: %+v", out[acid.Key])
	}
	sugg, chosen := m.NameSuggestions(acid.Key)
	if len(sugg) == 0 || len(chosen) != 0 {
		t.Fatalf("suggestions %+v, chosen %+v", sugg, chosen)
	}
	if err := m.SearchUnderOtherNames(acid.Key, []NameVariant{{Artist: "Sterling Moss", Title: "Acid In My Mind"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m)
	v, _ := m.JobView(acid.Key)
	if v.State != JobDone || len(v.OtherNames) != 1 {
		t.Fatalf("under another name: %+v", v)
	}
	if tags := ffprobeTags(t, v.Rec.Path); tags["artist"] != "Various Artists" || tags["title"] != "Acid In My Mind" {
		t.Errorf("tagged %v", tags)
	}
	if _, chosen := m.NameSuggestions(acid.Key); len(chosen) != 1 || m.names.get(acid.Key)[0].Artist != "Sterling Moss" {
		t.Errorf("not kept: %+v", chosen)
	}
}

// nameSource has results for whoever is searched for as the artist.
type nameSource struct {
	T  testing.TB
	by map[string][]Candidate
}

func (s *nameSource) Name() string  { return "fake" }
func (s *nameSource) Label() string { return "Fake" }
func (s *nameSource) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	return s.by[tr.Artists[0]], nil
}
func (s *nameSource) Download(ctx context.Context, c Candidate, tr *Track, workdir string) (string, error) {
	return (&FakeSource{T: s.T}).Download(ctx, c, tr, workdir)
}

// albumFake is a source with whole albums: one album search finds the tracks it has.
type albumFake struct {
	FakeSource
	has          map[string]bool // the album's tracks it has, by key
	albumSearchs atomic.Int32
	mu           sync.Mutex
	hits         map[string][]Candidate
}

func (a *albumFake) searchAlbum(ctx context.Context, tracks []*Track) (int, error) {
	a.albumSearchs.Add(1)
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, t := range tracks {
		if a.has[t.Key] {
			a.hits[t.Key] = a.Results[t.Key]
			n++
		}
	}
	return n, nil
}

func (a *albumFake) holds(t *Track) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.hits[t.Key]
	return ok
}

func (a *albumFake) Search(ctx context.Context, tr *Track) ([]Candidate, error) {
	a.mu.Lock()
	c, ok := a.hits[tr.Key]
	delete(a.hits, tr.Key)
	a.mu.Unlock()
	if ok {
		return c, nil
	}
	return a.FakeSource.Search(ctx, tr)
}

// TestAlbumFirst: the tracks of an album still to do are looked for with one album search,
// before any track's own searches; only the ones it didn't find search on their own.
func TestAlbumFirst(t *testing.T) {
	tools := NeedFFmpeg(t)
	var tracks []*Track
	results := map[string][]Candidate{}
	for i, title := range []string{"One", "Two", "Three", "Four", "Five"} {
		tr := MakeTrack(title, []string{"Band"}, "The Album", 30)
		tr.TrackNumber, tr.TotalTracks = i+1, 5
		tracks = append(tracks, tr)
		results[tr.Key] = Found("albums", title, "Band", 30)
	}
	single := MakeTrack("Single", []string{"Band"}, "Single", 30)
	results[single.Key] = Found("albums", "Single", "Band", 30)
	first := &FakeSource{SrcName: "first", T: t}
	albums := &albumFake{FakeSource: FakeSource{SrcName: "albums", Results: results, T: t}, hits: map[string][]Candidate{},
		has: map[string]bool{tracks[0].Key: true, tracks[1].Key: true, tracks[2].Key: true, tracks[3].Key: true}}
	m := TestManager(t, tools, []Source{first, albums}, TestLibrary(append(tracks, single)...), func(c *Config) { c.SearchWorkers = 3 })
	out := RunManager(t, m)
	for _, tr := range append(tracks, single) {
		if out[tr.Key].State != JobDone {
			t.Errorf("%s: %+v", tr.Title, out[tr.Key])
		}
	}
	if n := albums.albumSearchs.Load(); n != 1 {
		t.Errorf("%d album searches, want 1", n)
	}
	// Track five (not in the album found) and the single searched on their own, everywhere.
	if n := first.searched.Load(); n != 2 {
		t.Errorf("the first source was searched %d times, want 2", n)
	}
	if n := albums.searched.Load(); n != 2 {
		t.Errorf("the album source was searched by track %d times, want 2", n)
	}
	if albumKey(single) != "" || albumKey(tracks[0]) == "" || !wantAlbum(2, 4) || wantAlbum(2, 13) {
		t.Error("albumKey / wantAlbum")
	}
}

// TestLinkedSongs: a song added as a link isn't searched for on the link's own site, and one
// that wasn't found before its link came is searched for again.
func TestLinkedSongs(t *testing.T) {
	tools := NeedFFmpeg(t)
	tr := MakeTrack("Школа Не Нужна", []string{"Diingo"}, "", 165)
	tr.SourceURL = "https://www.youtube.com/watch?v=qZzPIkMPJI0"
	yt := &FakeSource{SrcName: "youtube", T: t}
	ytm := &FakeSource{SrcName: "youtube_music", T: t}
	sc := &FakeSource{SrcName: "soundcloud", T: t}
	m := TestManager(t, tools, []Source{yt, ytm, sc}, TestLibrary(tr), nil)
	m.ytdlpBin = "" // nothing can fetch the link here: it's searched for elsewhere
	RunManager(t, m)
	if yt.searched.Load() != 0 || ytm.searched.Load() != 0 || sc.searched.Load() != 1 {
		t.Errorf("searched: YouTube %d, YouTube Music %d, SoundCloud %d", yt.searched.Load(), ytm.searched.Load(), sc.searched.Load())
	}
	j := &Job{Track: tr}
	m.initJob(j, Record{Status: "failed", Reason: "YouTube: no match"})
	if j.state != JobQueued {
		t.Errorf("a failed song with a new link: %s", j.state)
	}
	j = &Job{Track: tr}
	m.initJob(j, Record{Status: "failed", Reason: linkLabelPrefix + "YouTube): video unavailable"})
	if j.state != JobFailed {
		t.Errorf("a failed song whose link was tried: %s", j.state)
	}
}

// TestSoulseekAlbumFromOneUser: an album search hands each track the file of the user whose
// folder has the most of the album, even when someone faster has one of the songs.
func TestSoulseekAlbumFromOneUser(t *testing.T) {
	c := &slskClient{budget: newSlskSearchBudget(15*time.Second, 120), answers: &slskSearchCache{}}
	s := newSoulseekSource(c, "flac")
	var tracks []*Track
	for i, title := range []string{"One", "Two", "Three"} {
		tr := MakeTrack(title, []string{"Band"}, "The Album", 0)
		tr.TrackNumber = i + 1
		tracks = append(tracks, tr)
	}
	file := func(dir, name string) slskFile { return slskFile{Name: dir + `\` + name + ".flac", Size: 30 << 20} }
	resps := []slskResponse{
		{User: "fast", FreeSlot: true, Speed: 5 << 20, Files: []slskFile{file(`Music\Band\The Album`, "02 Two")}},
		{User: "whole", FreeSlot: true, Speed: 1 << 20, Files: []slskFile{
			file(`Band - The Album`, "01 One"), file(`Band - The Album`, "02 Two"), file(`Band - The Album`, "03 Three")}},
	}
	e, mine := c.answers.get(slskQuery(slskAlbumQuery(tracks[0])))
	if !mine {
		t.Fatal("answers already kept")
	}
	c.answers.finish(e, resps, nil)
	n, err := s.searchAlbum(context.Background(), tracks)
	if err != nil || n != 3 {
		t.Fatalf("found %d (%v)", n, err)
	}
	for _, tr := range tracks {
		if !s.holds(tr) {
			t.Fatalf("%s isn't held", tr.Title)
		}
		cands, _ := s.Search(context.Background(), tr)
		accepted, _ := rank(tr, cands)
		sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].Cand.Pref < accepted[j].Cand.Pref })
		if len(accepted) == 0 || !strings.HasPrefix(accepted[0].Cand.Ref, "whole\x00") || !accepted[0].Cand.Unsearched {
			t.Errorf("%s: %+v", tr.Title, accepted)
		}
	}
}
