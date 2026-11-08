package core

import (
	"sort"
	"strings"
)

// The queue as a frontend lists it: filtered, searched and sorted.

// QueueOption is a filter or sort order a frontend offers.
type QueueOption struct{ Name, Label string }

var QueueFilters = []QueueOption{
	{"all", "all tracks"},
	{"todo", "to do (queued, in progress or retried later)"},
	{"active", "in progress (searching, waiting or downloading)"},
	{"downloading", "downloading now, data coming in"},
	{"searching", "searching now"},
	{"waiting", "waiting (slot, queue, peers or a retry)"},
	{"queued", "queued, not started yet"},
	{"prioritized", "moved to the front (p)"},
	{"retry", "to be retried later"},
	{"done", "downloaded"},
	{"lossless", "downloaded in lossless quality"},
	{"lossy", "downloaded in lossy quality"},
	{"upgrades", "downloaded to replace a worse library copy"},
	{"library", "already in your library"},
	{"below", "in your library below the quality threshold"},
	{"failed", "not found"},
	{"cancelled", "cancelled"},
	{"search", "added from the Search tab"},
	{"upgrading", "library upgrades"},
}

var QueueSorts = []QueueOption{
	{"playlist", "playlist order"},
	{"state", "state (in progress first)"},
	{"artist", "artist"},
	{"title", "title"},
	{"album", "album"},
	{"length", "length"},
	{"quality", "quality (downloaded or library copy)"},
	{"source", "source"},
	{"progress", "progress"},
	{"priority", "priority"},
	{"updated", "last finished"},
}

// QueueQuery picks and orders tracks.
type QueueQuery struct {
	Filter   string // a QueueFilters name ("" = all)
	Source   string // only tracks downloaded from, or being fetched from, this source
	Playlist string // only this playlist's tracks
	Text     string // only tracks whose artist, title or album contains this
	Sort     string // a QueueSorts name ("" = playlist order)
	Reverse  bool
}

// jobBrief is what filtering and sorting look at: a small copy of a job, so that listing the
// queue doesn't copy every track's whole state.
type jobBrief struct {
	key        string
	track      *Track
	playlists  []string
	state      JobState
	moving     bool // downloading, and not just waiting for something (see Task.waiting)
	source     string
	recSource  string
	recUpdated string
	recQuality Quality
	libQuality Quality
	hasLib     bool
	recLib     bool // better than the library copy
	needBetter bool
	bumped     bool
	prio       int
	idx        int
	progress   float64
}

func (j *Job) brief() jobBrief {
	j.mu.Lock()
	defer j.mu.Unlock()
	b := jobBrief{key: j.Track.Key, track: j.Track, playlists: j.playlists, state: j.state, source: j.source,
		needBetter: j.needBetter, bumped: j.bump > 0, prio: j.prio, idx: j.idx, progress: -1}
	if j.rec != nil {
		b.recSource, b.recUpdated = j.rec.Source, j.rec.Updated
		if j.state == JobDone {
			b.recQuality, b.recLib = j.rec.Quality, j.rec.LibPath != ""
		}
	}
	if j.state == JobDone {
		b.progress = 2
	}
	if len(j.lib) > 0 {
		b.hasLib, b.libQuality = true, j.lib[0].Q
	}
	switch {
	case j.state == JobInLibrary:
		b.progress = 2
	case j.state.Active():
		t := j.task.view()
		if t.Total > 0 {
			b.progress = float64(t.Done) / float64(t.Total)
		}
		b.moving = j.state == JobDownloading && !t.Waiting
	}
	return b
}

func (b *jobBrief) quality() Quality {
	switch {
	case b.state == JobDone:
		return b.recQuality
	case b.hasLib:
		return b.libQuality
	}
	return Quality{}
}

// matches tells whether a track belongs under a filter.
func (b *jobBrief) matches(f string) bool {
	todo := b.state == JobQueued || b.state.Active() || b.state == JobWaiting
	switch f {
	case "active":
		return b.state.Active() || b.state == JobWaiting
	case "downloading":
		return b.moving
	case "searching":
		return b.state == JobSearching
	case "todo":
		return todo
	case "queued":
		return b.state == JobQueued
	case "prioritized":
		return b.bumped && todo
	case "waiting":
		return b.state == JobReady || b.state == JobWaiting || b.state == JobDownloading && !b.moving
	case "retry":
		return b.state == JobWaiting
	case "done":
		return b.state == JobDone
	case "lossless":
		return b.state == JobDone && b.recQuality.Lossless
	case "lossy":
		return b.state == JobDone && !b.recQuality.Lossless
	case "upgrades":
		return b.state == JobDone && b.recLib
	case "library":
		return b.state == JobInLibrary
	case "below":
		return b.needBetter && b.hasLib && b.state != JobDone
	case "failed":
		return b.state == JobFailed
	case "cancelled":
		return b.state == JobCancelled
	case "search", "upgrading":
		for _, p := range b.playlists {
			if f == "search" && IsAddition(p) && !IsUpgrade(p) || f == "upgrading" && IsUpgrade(p) {
				return true
			}
		}
		return false
	}
	return true
}

// count adds a track to the counts of the filters it's under, and of the source it came from.
func (b *jobBrief) count(out map[string]*int, bySource map[string]int) {
	add := func(f string) { *out[f]++ }
	add("all")
	for _, f := range [...]string{"todo", "active", "downloading", "searching", "queued", "prioritized", "waiting", "retry", "done",
		"lossless", "lossy", "upgrades", "library", "below", "failed", "cancelled"} {
		if b.matches(f) {
			add(f)
		}
	}
	if len(b.playlists) > 0 {
		for _, f := range [...]string{"search", "upgrading"} {
			if b.matches(f) {
				add(f)
			}
		}
	}
	if b.state == JobDone && b.recSource != "" {
		bySource[b.recSource]++
	}
}

func (m *Manager) briefs() []jobBrief {
	m.jobsMu.Lock()
	list := m.order
	m.jobsMu.Unlock()
	out := make([]jobBrief, len(list))
	for i, j := range list {
		out[i] = j.brief()
	}
	return out
}

// QueueKeys lists the tracks q asks for, as keys (JobView gets a track's details), and how many
// tracks are under each filter ("from:<source>" for what came from each source). Listing and
// counting everything takes one pass, so a frontend with a big queue asks for just this and
// reads only the rows it's showing.
func (m *Manager) QueueKeys(q QueueQuery) ([]string, map[string]int) {
	all := m.briefs()
	storage := make([]int, len(QueueFilters))
	refs := make(map[string]*int, len(QueueFilters))
	for i, f := range QueueFilters {
		refs[f.Name] = &storage[i]
	}
	bySource := map[string]int{}
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	rows := make([]int32, 0, len(all))
	for i := range all {
		b := &all[i]
		b.count(refs, bySource)
		if q.Filter != "" && !b.matches(q.Filter) {
			continue
		}
		if q.Playlist != "" && !ContainsString(b.playlists, q.Playlist) {
			continue
		}
		if q.Source != "" && b.recSource != q.Source && b.source != q.Source {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(b.track.String()+" "+b.track.Album), needle) {
			continue
		}
		rows = append(rows, int32(i))
	}
	sortBriefs(all, rows, q.Sort, q.Reverse)
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = all[r].key
	}
	counts := make(map[string]int, len(refs)+len(bySource))
	for name, p := range refs {
		counts[name] = *p
	}
	for src, n := range bySource {
		counts["from:"+src] = n
	}
	return keys, counts
}

// Queue lists the tracks q asks for, with everything about them.
func (m *Manager) Queue(q QueueQuery) []JobView {
	keys, _ := m.QueueKeys(q)
	out := make([]JobView, 0, len(keys))
	for _, k := range keys {
		if v, ok := m.JobView(k); ok {
			out = append(out, v)
		}
	}
	return out
}

// QueueCounts counts the tracks under each filter.
func (m *Manager) QueueCounts() map[string]int {
	_, counts := m.QueueKeys(QueueQuery{Filter: "none"})
	return counts
}

// sortKey is what one track is ordered by.
type sortKey struct {
	a, b string
	x, y float64
}

// sortBriefs orders rows (indexes into all); ties keep playlist order (reversed too, with
// reverse). What it compares is worked out once per track, not once per comparison.
func sortBriefs(all []jobBrief, rows []int32, by string, reverse bool) {
	if by == "" || by == "playlist" {
		// already in playlist order
		if reverse {
			for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
		return
	}
	keys := make([]sortKey, len(all))
	lower := strings.ToLower
	newest := false
	for _, r := range rows {
		b, k := &all[r], &keys[r]
		t := b.track
		switch by {
		case "artist":
			k.a, k.b, k.x = lower(t.ArtistStr()), lower(t.Album), float64(t.TrackNumber)
		case "title":
			k.a = lower(t.Title)
		case "album":
			k.a, k.x = lower(t.Album), float64(t.DiscNumber*1000+t.TrackNumber)
		case "length":
			k.x = float64(t.DurationMs)
		case "quality":
			k.x = -b.quality().Score() // best first
		case "source":
			k.a = lower(FirstNonEmpty(b.recSource, b.source, "~"))
		case "state":
			k.x = float64(stateOrder[b.state])
		case "progress":
			k.x = -b.progress
		case "priority":
			if !b.bumped {
				k.x = 1
			}
			k.y = -float64(b.prio)
		case "updated":
			k.a, newest = lower(b.recUpdated), true
		}
	}
	compare := func(i, j int32) int {
		x, y := &keys[i], &keys[j]
		c := strings.Compare(x.a, y.a)
		if newest {
			c = -c
		}
		if c == 0 {
			c = strings.Compare(x.b, y.b)
		}
		if c == 0 {
			c = num0(x.x, y.x)
		}
		if c == 0 {
			c = num0(x.y, y.y)
		}
		if c == 0 {
			c = num0(all[i].idx, all[j].idx)
		}
		if reverse {
			return -c
		}
		return c
	}
	sort.Slice(rows, func(i, j int) bool { return compare(rows[i], rows[j]) < 0 })
}

func num0[T int | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
