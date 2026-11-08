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
	{"active", "in progress"},
	{"downloading", "downloading now (no searches)"},
	{"searching", "searching now"},
	{"queued", "queued, not started yet"},
	{"prioritized", "moved to the front (p)"},
	{"waiting", "to be retried later"},
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

// MatchesFilter tells whether a track belongs under a filter.
func MatchesFilter(v JobView, f string) bool {
	switch f {
	case "active":
		return v.State.Active()
	case "downloading":
		return v.State == JobDownloading
	case "searching":
		return v.State == JobSearching
	case "todo":
		return v.State == JobQueued || v.State.Active() || v.State == JobWaiting
	case "queued":
		return v.State == JobQueued
	case "prioritized":
		return v.Bumped && (v.State == JobQueued || v.State.Active() || v.State == JobWaiting)
	case "waiting":
		return v.State == JobWaiting
	case "done":
		return v.State == JobDone
	case "lossless":
		return v.State == JobDone && v.Rec.Quality.Lossless
	case "lossy":
		return v.State == JobDone && !v.Rec.Quality.Lossless
	case "upgrades":
		return v.State == JobDone && v.Rec.LibPath != ""
	case "library":
		return v.State == JobInLibrary
	case "below":
		return v.NeedBetter && v.LibCount > 0 && v.State != JobDone
	case "failed":
		return v.State == JobFailed
	case "cancelled":
		return v.State == JobCancelled
	case "search", "upgrading":
		for _, p := range v.Playlists {
			if f == "search" && IsAddition(p) && !IsUpgrade(p) || f == "upgrading" && IsUpgrade(p) {
				return true
			}
		}
		return false
	}
	return true
}

// Queue lists the tracks q asks for.
func (m *Manager) Queue(q QueueQuery) []JobView {
	views := m.Jobs()
	needle := strings.ToLower(strings.TrimSpace(q.Text))
	rows := views[:0]
	for _, v := range views {
		if !MatchesFilter(v, q.Filter) {
			continue
		}
		if q.Playlist != "" && !ContainsString(v.Playlists, q.Playlist) {
			continue
		}
		if q.Source != "" && v.Rec.Source != q.Source && v.Source != q.Source {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(v.Track.String()+" "+v.Track.Album), needle) {
			continue
		}
		rows = append(rows, v)
	}
	SortViews(rows, q.Sort, q.Reverse)
	return rows
}

// QueueCounts counts the tracks under each filter.
func (m *Manager) QueueCounts() map[string]int {
	out := map[string]int{}
	for _, v := range m.Jobs() {
		for _, f := range QueueFilters {
			if MatchesFilter(v, f.Name) {
				out[f.Name]++
			}
		}
	}
	return out
}

func viewQuality(v JobView) Quality {
	if v.State == JobDone {
		return v.Rec.Quality
	}
	if v.Lib != nil {
		return v.Lib.Q
	}
	return Quality{}
}

func viewProgress(v JobView) float64 {
	switch {
	case v.State == JobDone || v.State == JobInLibrary:
		return 2
	case v.Task.Total > 0:
		return float64(v.Task.Done) / float64(v.Task.Total)
	}
	return -1
}

// SortViews orders tracks; ties keep playlist order (reversed too, with reverse).
func SortViews(views []JobView, by string, reverse bool) {
	less := func(x, y JobView) int { // -1, 0, 1
		cmp := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
		num := func(a, b float64) int {
			switch {
			case a < b:
				return -1
			case a > b:
				return 1
			}
			return 0
		}
		switch by {
		case "artist":
			if c := cmp(x.Track.ArtistStr(), y.Track.ArtistStr()); c != 0 {
				return c
			}
			if c := cmp(x.Track.Album, y.Track.Album); c != 0 {
				return c
			}
			return num(float64(x.Track.TrackNumber), float64(y.Track.TrackNumber))
		case "title":
			return cmp(x.Track.Title, y.Track.Title)
		case "album":
			if c := cmp(x.Track.Album, y.Track.Album); c != 0 {
				return c
			}
			return num(float64(x.Track.DiscNumber*1000+x.Track.TrackNumber), float64(y.Track.DiscNumber*1000+y.Track.TrackNumber))
		case "length":
			return num(float64(x.Track.DurationMs), float64(y.Track.DurationMs))
		case "quality":
			return -num(viewQuality(x).Score(), viewQuality(y).Score()) // best first
		case "source":
			return cmp(FirstNonEmpty(x.Rec.Source, x.Source, "~"), FirstNonEmpty(y.Rec.Source, y.Source, "~"))
		case "state":
			return num(float64(stateOrder[x.State]), float64(stateOrder[y.State]))
		case "progress":
			return -num(viewProgress(x), viewProgress(y))
		case "priority":
			if x.Bumped != y.Bumped {
				if x.Bumped {
					return -1
				}
				return 1
			}
			return -num(float64(x.Prio), float64(y.Prio))
		case "updated":
			return -cmp(x.Rec.Updated, y.Rec.Updated) // newest first
		}
		return 0
	}
	sort.SliceStable(views, func(a, b int) bool {
		x, y := views[a], views[b]
		c := less(x, y)
		if c == 0 {
			c = num0(x.idx, y.idx)
		}
		if reverse {
			return c > 0
		}
		return c < 0
	})
}

func num0(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
