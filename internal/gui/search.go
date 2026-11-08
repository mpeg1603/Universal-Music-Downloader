package gui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Find music page: search the catalog by name and add what you want: a song, an album, or
// everything by an artist.

type searchTab struct {
	a                          *app
	w                          *qt.QWidget
	find, artist, album, track *qt.QLineEdit
	year                       *qt.QLineEdit
	go_                        *qt.QPushButton // "go" is taken
	fields                     *qt.QWidget
	note                       *qt.QLabel
	stack                      *qt.QStackedWidget
	results                    *qt.QVBoxLayout
	content                    *qt.QWidget // the current results
	found                      []core.MusicResult
	busy                       bool
	adding                     string
}

var sectionTitles = map[string]string{"track": "Songs", "album": "Albums", "artist": "Artists"}

func newSearchTab(a *app) *searchTab {
	t := &searchTab{a: a}
	p := a.newPage("Find music", "Search for any song, album or artist. Pick what you want and the app gets it for you.", "find")
	t.w = p.w

	t.find = qt.NewQLineEdit2()
	setProp(t.find.QWidget, "role", "bigsearch")
	t.find.SetPlaceholderText("A song, an album or an artist")
	t.find.SetClearButtonEnabled(true)
	t.find.OnReturnPressed(t.search)
	t.go_ = a.iconButton("search", "Search", "primary", "", t.search)
	t.go_.SetMinimumHeight(42)
	row := hbox()
	row.SetSpacing(8)
	row.AddWidget2(t.find.QWidget, 1)
	row.AddWidget(t.go_.QWidget)
	var more *qt.QPushButton
	more = button("More options", "ghost", func() {
		on := !t.fields.IsVisible()
		t.fields.SetVisible(on)
		if on {
			more.SetText("Fewer options")
			t.artist.SetFocus()
		} else {
			more.SetText("More options")
		}
	})
	more.SetMinimumHeight(42)
	row.AddWidget(more.QWidget)
	p.box.AddLayout(row.QLayout)

	t.fields = qt.NewQWidget2()
	fields := qt.NewQHBoxLayout(t.fields)
	fields.SetContentsMargins(0, 0, 0, 0)
	fields.SetSpacing(8)
	mk := func(hint string) *qt.QLineEdit {
		e := qt.NewQLineEdit2()
		e.SetPlaceholderText(hint)
		e.SetClearButtonEnabled(true)
		e.OnReturnPressed(t.search)
		fields.AddWidget2(e.QWidget, 1)
		return e
	}
	t.artist, t.album, t.track = mk("Artist"), mk("Album"), mk("Song")
	t.year = mk("Year, or 1995-1999")
	t.year.SetMaximumWidth(160)
	t.fields.Hide()
	p.box.AddWidget(t.fields)

	t.note = text("", "hint")
	t.note.Hide()
	p.box.AddWidget(t.note.QWidget)

	scroll, box, _ := scrolling()
	t.results = box
	box.SetSpacing(2)
	e := a.newEmpty("search", "What would you like to hear?", "Try an artist, an album, or “artist - song”. Results come from a large music catalog; the music itself comes from your sources.")
	e.done()
	t.stack = qt.NewQStackedWidget2()
	t.stack.AddWidget(e.w)
	t.stack.AddWidget(scroll.QWidget)
	p.box.AddWidget2(t.stack.QWidget, 1)
	a.onTheme(func() {
		if t.content != nil {
			t.fill()
		}
	})
	return t
}

func (t *searchTab) setNote(s string) {
	t.note.SetText(s)
	t.note.SetVisible(s != "")
}

// search looks up what was typed in the catalog, in the background.
func (t *searchTab) search() {
	q := core.MusicQuery(t.find.Text(), t.artist.Text(), t.album.Text(), t.track.Text(), t.year.Text())
	if q == "" || t.busy {
		return
	}
	t.busy = true
	t.go_.SetEnabled(false)
	t.setNote("Searching…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 30*time.Second)
		defer cancel()
		res, err := t.a.m.SearchMusic(ctx, q)
		mainthread.Start(func() {
			t.busy = false
			t.go_.SetEnabled(true)
			if err != nil {
				t.setNote("The catalog didn't answer (" + err.Error() + "). Check your connection and try again.")
				return
			}
			t.found = res
			t.fill()
			if len(res) == 0 {
				t.setNote("Nothing found. Try fewer words, or only the artist's name.")
			} else {
				t.setNote("")
			}
		})
	}()
}

// fill lists the results under a heading per kind; the kind that fits best comes first.
func (t *searchTab) fill() {
	if t.content != nil {
		t.content.Hide()
		t.content.DeleteLater()
	}
	t.content = qt.NewQWidget2()
	box := qt.NewQVBoxLayout(t.content)
	box.SetContentsMargins(0, 0, 0, 12)
	box.SetSpacing(2)
	// The catalog lists artists with nothing released: nothing to add.
	kept := t.found[:0]
	for _, r := range t.found {
		if r.Kind != "artist" || r.Count > 0 {
			kept = append(kept, r)
		}
	}
	t.found = kept
	best := map[string]float64{}
	for _, r := range t.found {
		if _, ok := best[r.Kind]; !ok {
			best[r.Kind] = r.Score
		}
	}
	kinds := []string{"track", "album", "artist"}
	sort.SliceStable(kinds, func(i, j int) bool { return best[kinds[i]] > best[kinds[j]] })
	for _, kind := range kinds {
		if _, ok := best[kind]; !ok {
			continue
		}
		h := text(sectionTitles[kind], "h2")
		h.SetContentsMargins(4, 12, 0, 4)
		box.AddWidget(h.QWidget)
		for i := range t.found {
			if t.found[i].Kind == kind {
				box.AddWidget(t.resultRow(t.found[i]))
			}
		}
	}
	box.AddStretch()
	t.results.AddWidget(t.content)
	t.stack.SetCurrentIndex(1)
}

func (t *searchTab) resultRow(r core.MusicResult) *qt.QWidget {
	a := t.a
	row, box := card()
	box.SetContentsMargins(12, 8, 10, 8)
	lay := hbox()
	lay.SetSpacing(12)
	icon := map[string]string{"track": "music", "album": "album", "artist": "artist"}[r.Kind]
	pic := qt.NewQLabel2()
	pic.SetPixmap(iconPixmap(icon, 22, th.Text3))
	lay.AddWidget(pic.QWidget)
	col := vbox()
	col.SetSpacing(1)
	title := oneLine(r.Title, "")
	title.SetStyleSheet("font-weight: 600;")
	col.AddWidget(title.QWidget)
	var bits []string
	switch r.Kind {
	case "track":
		bits = []string{r.Artist, r.Album, r.Year, core.FmtDuration(float64(r.Duration))}
	case "album":
		bits = []string{r.Artist, r.Year, fmt.Sprintf("%d tracks", r.Count)}
	case "artist":
		bits = []string{fmt.Sprintf("%d releases", r.Count)}
	}
	var keep []string
	for _, b := range bits {
		if b != "" {
			keep = append(keep, b)
		}
	}
	sub := oneLine(strings.Join(keep, "  ·  "), "hint")
	col.AddWidget(sub.QWidget)
	lay.AddLayout2(col.QLayout, 1)
	label := map[string]string{"track": "Add", "album": "Add album", "artist": "Add everything…"}[r.Kind]
	var add *qt.QPushButton
	add = a.iconButton("plus", label, "", "Add to your downloads", func() { t.add(r, add) })
	lay.AddWidget(add.QWidget)
	box.AddLayout(lay.QLayout)
	return row.QWidget
}

// add puts a result in the queue (an artist's whole discography after asking).
func (t *searchTab) add(r core.MusicResult, b *qt.QPushButton) {
	a := t.a
	if t.adding != "" {
		a.toast("Still adding " + t.adding + ". One moment.")
		return
	}
	if r.Kind == "artist" && !a.confirm("Add everything by "+r.Title+"?",
		fmt.Sprintf("This adds all of %s's albums, EPs and singles (%d releases), each song once.", r.Title, r.Count)) {
		return
	}
	t.adding = r.String()
	b.SetEnabled(false)
	b.SetText("Adding…")
	go func() {
		ctx, cancel := context.WithTimeout(a.m.Context(), 10*time.Minute)
		defer cancel()
		_, n, err := a.m.AddMusic(ctx, r, func(p string) { mainthread.Start(func() { t.setNote(p) }) })
		mainthread.Start(func() {
			t.adding = ""
			t.setNote("")
			if err != nil {
				b.SetEnabled(true)
				b.SetText("Try again")
				a.message("Couldn't add that", err.Error())
				return
			}
			b.SetText("Added")
			b.SetIcon(iconOf("check", 16, th.OK))
			word := "songs"
			if n == 1 {
				word = "song"
			}
			a.toast(fmt.Sprintf("Added %d %s to the front of your downloads.", n, word))
			a.queue.refresh(false)
		})
	}()
}
