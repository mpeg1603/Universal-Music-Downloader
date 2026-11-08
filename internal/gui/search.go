package gui

import (
	"context"
	"fmt"
	"sort"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Search tab: find music by name and add it to the queue (a track, an album, an artist's
// discography); below, the playlist files and the playlists the queue is made from.

type searchTab struct {
	a                          *app
	w                          *qt.QWidget
	find, artist, album, track *qt.QLineEdit
	year                       *qt.QLineEdit
	go_                        *qt.QPushButton // "go" is taken
	results                    *qt.QTreeWidget
	note                       *qt.QLabel
	found                      []core.MusicResult
	busy                       bool
	adding                     string
	inputs                     *qt.QTableWidget
	inputRows                  []core.InputStatus
	lists                      *qt.QTableWidget
	listRows                   []core.PlaylistStatus
	lower                      *qt.QSplitter // playlist files and playlists
}

var sectionTitles = map[string]string{"track": "♪ Tracks", "album": "◉ Albums", "artist": "★ Artists (the whole discography)"}

func newSearchTab(a *app) *searchTab {
	t := &searchTab{a: a, w: qt.NewQWidget2()}
	box := qt.NewQVBoxLayout(t.w)
	box.SetContentsMargins(0, 4, 0, 0)

	t.find = qt.NewQLineEdit2()
	t.find.SetPlaceholderText("Artist - Title · Artist - Album · Artist · or tags: artist: album: track: year:")
	t.go_ = qt.NewQPushButton3("Search")
	row := qt.NewQHBoxLayout2()
	row.AddWidget2(t.find.QWidget, 1)
	row.AddWidget(t.go_.QWidget)
	box.AddLayout(row.QLayout)

	fields := qt.NewQHBoxLayout2()
	mk := func(label, hint string) *qt.QLineEdit {
		e := qt.NewQLineEdit2()
		e.SetPlaceholderText(hint)
		e.SetClearButtonEnabled(true)
		l := qt.NewQLabel3(label)
		fields.AddWidget(l.QWidget)
		fields.AddWidget2(e.QWidget, 1)
		e.OnReturnPressed(t.search)
		return e
	}
	t.artist, t.album, t.track = mk("Artist", "any"), mk("Album", "any"), mk("Track", "any")
	t.year = mk("Year", "1997 or 1995-1999")
	t.year.SetMaximumWidth(150)
	box.AddLayout(fields.QLayout)
	t.find.OnReturnPressed(t.search)
	t.go_.OnClicked(t.search)

	t.note = qt.NewQLabel3("Double-click (or Enter on) a result: a track is queued; an album brings all its tracks; an artist the whole discography. They go first.")
	t.note.SetWordWrap(true)
	box.AddWidget(t.note.QWidget)

	split := qt.NewQSplitter3(qt.Vertical)
	t.results = qt.NewQTreeWidget2()
	t.results.SetColumnCount(6)
	t.results.SetHeaderLabels([]string{"Title", "Artist", "Album", "Year", "Length / tracks", "Fits"})
	t.results.SetRootIsDecorated(false)
	t.results.SetAlternatingRowColors(true)
	for i, w := range []int{320, 200, 200, 60, 110} {
		t.results.SetColumnWidth(i, w)
	}
	t.results.OnItemActivated(func(item *qt.QTreeWidgetItem, _ int) { t.activate(item) })
	split.AddWidget(t.results.QWidget)

	// Playlist files and playlists side by side on a wide window, one above the other on a
	// narrow one.
	lower := qt.NewQSplitter3(qt.Horizontal)
	lower.AddWidget(t.buildInputs())
	lower.AddWidget(t.buildLists())
	lower.SetStretchFactor(0, 2)
	lower.SetStretchFactor(1, 3)
	t.lower = lower
	split.AddWidget(lower.QWidget)
	split.SetStretchFactor(0, 3)
	split.SetStretchFactor(1, 2)
	box.AddWidget2(split.QWidget, 1)
	t.refresh()
	return t
}

// search looks up what was typed in the catalog (Deezer), in the background.
func (t *searchTab) search() {
	text := core.MusicQuery(t.find.Text(), t.artist.Text(), t.album.Text(), t.track.Text(), t.year.Text())
	if text == "" || t.busy {
		return
	}
	t.busy = true
	t.go_.SetEnabled(false)
	t.note.SetText("Searching for " + text + "…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 30*time.Second)
		defer cancel()
		res, err := t.a.m.SearchMusic(ctx, text)
		mainthread.Start(func() {
			t.busy = false
			t.go_.SetEnabled(true)
			if err != nil {
				t.note.SetText("The music catalog (Deezer) didn't answer: " + err.Error())
				return
			}
			t.found = res
			t.fill()
			t.note.SetText(fmt.Sprintf("%d results for %q. Double-click or Enter adds: a track, the whole album, or the artist's discography.", len(res), text))
		})
	}()
}

// fill lists the results under a heading per kind, best first; the kind whose best result
// fits best comes first.
func (t *searchTab) fill() {
	t.results.Clear()
	best := map[string]float64{}
	for _, r := range t.found {
		if _, ok := best[r.Kind]; !ok {
			best[r.Kind] = r.Score
		}
	}
	kinds := []string{"track", "album", "artist"}
	sort.SliceStable(kinds, func(i, j int) bool { return best[kinds[i]] > best[kinds[j]] })
	colors := map[string]string{"track": "#29a", "album": "#b80", "artist": "#a3a"}
	for _, kind := range kinds {
		if _, ok := best[kind]; !ok {
			continue
		}
		head := qt.NewQTreeWidgetItem2([]string{sectionTitles[kind]})
		head.SetFirstColumnSpanned(false)
		font := head.Font(0)
		font.SetBold(true)
		head.SetFont(0, font)
		head.SetForeground(0, qt.NewQBrush3(qt.NewQColor6(colors[kind])))
		head.SetFlags(qt.ItemIsEnabled)
		t.results.AddTopLevelItem(head)
		for i, r := range t.found {
			if r.Kind != kind {
				continue
			}
			size := ""
			switch r.Kind {
			case "track":
				size = core.FmtDuration(float64(r.Duration))
			case "album":
				size = fmt.Sprintf("%d tracks", r.Count)
			case "artist":
				size = fmt.Sprintf("%d releases", r.Count)
			}
			item := qt.NewQTreeWidgetItem2([]string{"   " + r.Title, r.Artist, r.Album, r.Year, size, fmt.Sprintf("%.0f%%", max(0, r.Score))})
			item.SetData(0, int(qt.UserRole), qt.NewQVariant4(i))
			item.SetForeground(0, qt.NewQBrush3(qt.NewQColor6(colors[kind])))
			t.results.AddTopLevelItem(item)
		}
	}
	if len(t.found) == 0 {
		item := qt.NewQTreeWidgetItem2([]string{"Nothing found: try \"Artist - Title\", only the artist, or fewer fields."})
		item.SetFlags(qt.ItemIsEnabled)
		t.results.AddTopLevelItem(item)
	}
}

// activate adds a result to the queue (an artist's discography after asking).
func (t *searchTab) activate(item *qt.QTreeWidgetItem) {
	v := item.Data(0, int(qt.UserRole))
	if !v.IsValid() {
		return
	}
	i := v.ToInt()
	if i < 0 || i >= len(t.found) {
		return
	}
	r := t.found[i]
	if t.adding != "" {
		t.a.message("Search", "Still adding "+t.adding+"; one at a time.")
		return
	}
	if r.Kind == "artist" && !t.a.confirm("Add a discography",
		fmt.Sprintf("Add %s's whole discography (albums, EPs and singles, each song once; %d releases on Deezer)?", r.Title, r.Count)) {
		return
	}
	t.adding = r.String()
	t.note.SetText("Adding " + r.String() + "…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 10*time.Minute)
		defer cancel()
		name, n, err := t.a.m.AddMusic(ctx, r, func(p string) { mainthread.Start(func() { t.note.SetText(p) }) })
		mainthread.Start(func() {
			t.adding = ""
			if err != nil {
				t.note.SetText(r.String() + ": " + err.Error())
				return
			}
			t.note.SetText(fmt.Sprintf("Added %s: %d tracks, first in the queue.", name, n))
			t.refresh()
		})
	}()
}

// --- playlist files and playlists -----------------------------------------------------------

func (t *searchTab) buildInputs() *qt.QWidget {
	w := qt.NewQGroupBox3("Playlist files")
	box := qt.NewQVBoxLayout(w.QWidget)
	t.inputs = qt.NewQTableWidget2()
	t.inputs.SetColumnCount(3)
	t.inputs.SetHorizontalHeaderLabels([]string{"On", "File or link", "Problem"})
	t.inputs.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.inputs.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.inputs.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	t.inputs.VerticalHeader().Hide()
	t.inputs.HorizontalHeader().SetStretchLastSection(true)
	t.inputs.SetColumnWidth(0, 34)
	t.inputs.SetColumnWidth(1, 300)
	box.AddWidget2(t.inputs.QWidget, 1)
	buttons := qt.NewQHBoxLayout2()
	add := qt.NewQPushButton3("Add file…")
	add.SetToolTip("A CSV (Exportify, TuneMyMusic…), a zip of them, Spotify's data export, or a text list (a track, album or artist per line)")
	add.OnClicked(func() {
		p := qt.QFileDialog_GetOpenFileName4(t.a.win.QWidget, "Add a playlist file", "", "Playlists (*.csv *.zip *.txt *.json);;All files (*)")
		t.try(t.a.m.AddInput(p))
	})
	folder := qt.NewQPushButton3("Add folder…")
	folder.OnClicked(func() {
		if p := qt.QFileDialog_GetExistingDirectory3(t.a.win.QWidget, "Add a folder of playlist files", ""); p != "" {
			t.try(t.a.m.AddInput(p))
		}
	})
	link := qt.NewQPushButton3("Add link…")
	link.SetToolTip("A public Spotify playlist or album link (only its first 100 tracks)")
	link.OnClicked(func() {
		if p, ok := t.a.ask("Add a link", "A public Spotify playlist or album link:", ""); ok {
			t.try(t.a.m.AddInput(p))
		}
	})
	toggle := qt.NewQPushButton3("On / off")
	toggle.OnClicked(func() {
		if in := t.selectedInput(); in != nil {
			t.try(t.a.m.SetInputEnabled(in.Path, !in.Enabled))
		}
	})
	remove := qt.NewQPushButton3("Remove")
	remove.OnClicked(func() {
		if in := t.selectedInput(); in != nil && t.a.confirm("Remove", "Remove "+in.Path+" from the list? (The file itself isn't touched.)") {
			t.try(t.a.m.RemoveInput(in.Path))
		}
	})
	again := qt.NewQPushButton3("Read again")
	again.SetToolTip("Read the files again (after you changed them)")
	again.OnClicked(func() { t.a.m.Reimport() })
	for _, b := range []*qt.QPushButton{add, folder, link, toggle, remove, again} {
		buttons.AddWidget(b.QWidget)
	}
	box.AddLayout(buttons.QLayout)
	return w.QWidget
}

func (t *searchTab) buildLists() *qt.QWidget {
	w := qt.NewQGroupBox3("Playlists")
	box := qt.NewQVBoxLayout(w.QWidget)
	t.lists = qt.NewQTableWidget2()
	headers := []string{"On", "Priority", "Playlist", "Tracks", "Downloaded", "In library", "To do", "Not found"}
	t.lists.SetColumnCount(len(headers))
	t.lists.SetHorizontalHeaderLabels(headers)
	t.lists.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.lists.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.lists.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	t.lists.VerticalHeader().Hide()
	t.lists.HorizontalHeader().SetSectionResizeMode2(2, qt.QHeaderView__Stretch)
	t.lists.SetColumnWidth(0, 34)
	t.lists.SetColumnWidth(1, 60)
	t.lists.OnCellDoubleClicked(func(int, int) { t.showPlaylist() })
	box.AddWidget2(t.lists.QWidget, 1)
	buttons := qt.NewQHBoxLayout2()
	show := qt.NewQPushButton3("Show tracks")
	show.OnClicked(t.showPlaylist)
	toggle := qt.NewQPushButton3("On / off")
	toggle.OnClicked(func() {
		if p := t.selectedList(); p != nil {
			t.try(t.a.m.SetPlaylist(p.Name, !p.Enabled, 0))
		}
	})
	up := qt.NewQPushButton3("Priority +")
	up.SetToolTip("Higher priority goes first")
	up.OnClicked(func() {
		if p := t.selectedList(); p != nil {
			t.try(t.a.m.SetPlaylist(p.Name, p.Enabled, 1))
		}
	})
	down := qt.NewQPushButton3("Priority −")
	down.OnClicked(func() {
		if p := t.selectedList(); p != nil {
			t.try(t.a.m.SetPlaylist(p.Name, p.Enabled, -1))
		}
	})
	remove := qt.NewQPushButton3("Remove")
	remove.SetToolTip("Remove something added from the search (its tracks leave the queue; files already downloaded stay)")
	remove.OnClicked(func() {
		if p := t.selectedList(); p != nil {
			if !core.IsAddition(p.Name) {
				t.a.message("Playlists", "Only what was added from the search can be removed here; a playlist file's playlists go with the file (turn them off instead).")
				return
			}
			if t.a.confirm("Remove", "Remove "+p.Name+"? Its tracks leave the queue; files already downloaded stay.") {
				t.a.m.RemoveAddition(p.Name)
				t.refresh()
			}
		}
	})
	for _, b := range []*qt.QPushButton{show, toggle, up, down, remove} {
		buttons.AddWidget(b.QWidget)
	}
	box.AddLayout(buttons.QLayout)
	return w.QWidget
}

func (t *searchTab) try(err error) {
	if err != nil {
		t.a.message("Can't do that", err.Error())
	}
	t.refresh()
}

func (t *searchTab) selectedInput() *core.InputStatus {
	if r := t.inputs.CurrentRow(); r >= 0 && r < len(t.inputRows) {
		return &t.inputRows[r]
	}
	return nil
}

func (t *searchTab) selectedList() *core.PlaylistStatus {
	if r := t.lists.CurrentRow(); r >= 0 && r < len(t.listRows) {
		return &t.listRows[r]
	}
	return nil
}

// showPlaylist shows the playlist's tracks in the Queue.
func (t *searchTab) showPlaylist() {
	if p := t.selectedList(); p != nil {
		t.a.queue.showPlaylist(p.Name)
		t.a.tabs.SetCurrentIndex(0)
	}
}

func cell(text string) *qt.QTableWidgetItem { return qt.NewQTableWidgetItem2(text) }

func number(n int) *qt.QTableWidgetItem {
	c := qt.NewQTableWidgetItem2(fmt.Sprint(n))
	c.SetTextAlignment(int(qt.AlignRight | qt.AlignVCenter))
	return c
}

// refresh redraws the playlist files and playlists, keeping the selections.
func (t *searchTab) refresh() {
	if narrow := t.w.Width() < 1300; narrow != (t.lower.Orientation() == qt.Vertical) {
		if narrow {
			t.lower.SetOrientation(qt.Vertical)
		} else {
			t.lower.SetOrientation(qt.Horizontal)
		}
	}
	inRow, listRow := t.inputs.CurrentRow(), t.lists.CurrentRow()
	t.inputRows = t.a.m.Inputs()
	t.inputs.SetRowCount(len(t.inputRows))
	for i, in := range t.inputRows {
		on := "✓"
		if !in.Enabled {
			on = "·"
		}
		t.inputs.SetItem(i, 0, cell(on))
		t.inputs.SetItem(i, 1, cell(in.Path))
		t.inputs.SetItem(i, 2, cell(in.Err))
	}
	t.listRows = t.a.m.Playlists()
	t.lists.SetRowCount(len(t.listRows))
	for i, p := range t.listRows {
		on, prio := "✓", ""
		if !p.Enabled {
			on = "·"
		}
		if p.Priority != 0 {
			prio = fmt.Sprintf("%+d", p.Priority)
		}
		t.lists.SetItem(i, 0, cell(on))
		t.lists.SetItem(i, 1, cell(prio))
		t.lists.SetItem(i, 2, cell(p.Name))
		for c, n := range []int{p.Tracks, p.Done, p.InLib, p.Todo, p.Failed} {
			t.lists.SetItem(i, 3+c, number(n))
		}
	}
	if inRow >= 0 && inRow < len(t.inputRows) {
		t.inputs.SelectRow(inRow)
	}
	if listRow >= 0 && listRow < len(t.listRows) {
		t.lists.SelectRow(listRow)
	}
}
