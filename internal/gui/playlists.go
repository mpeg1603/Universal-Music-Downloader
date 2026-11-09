package gui

import (
	"fmt"
	"path/filepath"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Playlists page: the playlists the queue is made from, and the files and links they came
// from.

type playlistsTab struct {
	a      *app
	w      *qt.QWidget
	list   *rowList
	rows   []core.PlaylistStatus // the category being shown
	all    []core.PlaylistStatus
	inputs *qt.QListWidget
	inRows []core.InputStatus
	stack  *qt.QStackedWidget

	show, onoff, up, down, remove *qt.QPushButton
	selAll                        *qt.QPushButton
	inToggle, inRemove, restore   *qt.QPushButton
	segs                          []*qt.QPushButton
	note                          *qt.QLabel
	finished                      bool // showing the finished ones
	loading, again                bool
	removing                      bool
	reread                        *throttle
}

func newPlaylistsTab(a *app) *playlistsTab {
	t := &playlistsTab{a: a}
	p := a.newPage("Playlists", "Bring in whole playlists and the app works through them, skipping what you already have.", "playlists")
	t.w = p.w
	p.actions.AddWidget(a.iconButton("file", "Add a file", "primary", "A CSV from Exportify or a similar tool, a zip of them, Spotify's data export, or a text list", t.addFile).QWidget)
	p.actions.AddWidget(a.iconButton("folder", "Add a folder", "", "A folder of playlist files", t.addFolder).QWidget)
	p.actions.AddWidget(a.iconButton("link", "Add a link", "", "A playlist, album or song on Spotify, Deezer, Apple Music, Bandcamp, YouTube, SoundCloud and many other sites", t.addLink).QWidget)

	e := a.newEmpty("playlist", "No playlists yet", "Export your Spotify playlists with exportify.net and add the file here, or add a public Spotify link. Text files with one song per line work too.")
	e.row.AddWidget(a.iconButton("external", "Open exportify.net", "primary", "", func() { core.OpenBrowser("https://exportify.net") }).QWidget)
	e.row.AddWidget(button("How does this work?", "", func() { a.showHelp("playlists") }).QWidget)
	e.done()

	t.reread = newThrottle(t.w.QObject, 1500, t.refresh)
	t.list = newRowList(60, func() int { return len(t.rows) }, t.draw)
	t.list.view.SetSelectionMode(qt.QAbstractItemView__ExtendedSelection)
	t.list.onSelect(func(int) { t.updateButtons() })
	t.list.onActivate(t.showTracks)

	body := qt.NewQWidget2()
	bb := qt.NewQVBoxLayout(body)
	bb.SetContentsMargins(0, 0, 0, 0)
	bb.SetSpacing(10)

	seg, segs := segments([]string{"In progress", "Finished playlists"}, func(i int) {
		t.finished = i == 1
		t.showCategory()
	})
	t.segs = segs
	segs[0].SetChecked(true)
	segRow := hbox()
	segRow.AddWidget(seg.QWidget)
	segRow.AddStretch()
	bb.AddLayout(segRow.QLayout)
	t.note = text("", "hint")
	t.note.Hide()
	bb.AddWidget(t.note.QWidget)

	frame, fb := card()
	fb.SetContentsMargins(8, 8, 8, 8)
	fb.AddWidget2(t.list.view.QWidget, 1)
	bb.AddWidget2(frame.QWidget, 1)

	acts := hbox()
	acts.SetSpacing(6)
	t.show = a.iconButton("music", "Show songs", "", "See this playlist in Downloads", t.showTracks)
	t.onoff = button("Turn off", "", t.toggle)
	t.onoff.SetToolTip("A playlist that's off stays here, but its songs leave the line")
	t.up = a.iconButton("arrow-up", "Higher priority", "", "Goes before other playlists", func() { t.prio(1) })
	t.down = a.iconButton("arrow-down", "Lower priority", "", "Goes after other playlists", func() { t.prio(-1) })
	t.selAll = button("Select all", "ghost", func() { t.list.view.SelectAll() })
	t.selAll.SetToolTip("Ctrl+click or Shift+click picks several")
	t.remove = a.iconButton("trash", "Remove", "danger", "Take the selected playlists out of the app and the Downloads tab. Music files are never touched", t.removeSelected)
	for _, b := range []*qt.QPushButton{t.show, t.onoff, t.up, t.down} {
		acts.AddWidget(b.QWidget)
	}
	acts.AddStretch()
	acts.AddWidget(t.selAll.QWidget)
	acts.AddWidget(t.remove.QWidget)
	bb.AddLayout(acts.QLayout)

	// the files and links the playlists come from
	in, ib := card()
	ib.SetContentsMargins(14, 10, 14, 10)
	head := hbox()
	head.SetSpacing(6)
	head.AddWidget2(text("Files and links", "h2").QWidget, 1)
	t.inToggle = button("Turn off", "", t.toggleInput)
	t.inRemove = a.iconButton("trash", "Remove", "danger", "Stop using this file (the file itself isn't touched)", t.removeInput)
	head.AddWidget(t.inToggle.QWidget)
	head.AddWidget(t.inRemove.QWidget)
	t.restore = button("Ignored playlists", "ghost", t.showIgnored)
	t.restore.Hide()
	head.AddWidget(t.restore.QWidget)
	head.AddWidget(a.iconButton("refresh", "Read again", "", "Read the files and links now (changed files are noticed by themselves)", func() {
		a.m.Reimport()
		a.toast("Reading your playlist files again.")
	}).QWidget)
	ib.AddLayout(head.QLayout)
	t.inputs = qt.NewQListWidget2()
	t.inputs.SetMaximumHeight(110)
	t.inputs.OnCurrentRowChanged(func(int) { t.updateButtons() })
	ib.AddWidget(t.inputs.QWidget)
	bb.AddWidget(in.QWidget)

	t.stack = qt.NewQStackedWidget2()
	t.stack.AddWidget(e.w)
	t.stack.AddWidget(body)
	p.box.AddWidget2(t.stack.QWidget, 1)
	t.refresh()
	return t
}

func (t *playlistsTab) draw(c *rowCtx) {
	if c.row >= len(t.rows) {
		return
	}
	v := t.rows[c.row]
	f := drawFonts
	p := c.p
	color, titleColor := th.Accent, th.Text
	if !v.Enabled {
		color, titleColor = th.Text3, th.Text3
	}
	drawBadge(p, c.x+10, c.y+(c.h-32)/2, 32, "playlist", color, th.Raised)
	right := 190
	x := c.x + 54
	w := c.w - 54 - right - 24
	name := v.Name
	if !v.Enabled {
		name += "  (off)"
	}
	if v.Priority != 0 {
		name += fmt.Sprintf("  ·  priority %+d", v.Priority)
	}
	drawText(p, x, c.y+9, w, 20, qt.AlignLeft|qt.AlignVCenter, name, f.bold, f.mBold, titleColor)
	detail := fmt.Sprintf("%d songs  ·  %d downloaded  ·  %d in your library", v.Tracks, v.Done, v.InLib)
	if v.Failed > 0 {
		detail += fmt.Sprintf("  ·  %d not found", v.Failed)
	}
	drawText(p, x, c.y+31, w, 18, qt.AlignLeft|qt.AlignVCenter, detail, f.small, f.mSmall, th.Text2)
	frac := 0.0
	if v.Tracks > 0 {
		frac = float64(v.Done+v.InLib) / float64(v.Tracks)
	}
	rx := c.x + c.w - right - 14
	drawText(p, rx, c.y+10, right, 18, qt.AlignRight|qt.AlignVCenter, fmt.Sprintf("%.0f%%", frac*100), f.small, f.mSmall, th.Text2)
	drawBar(p, rx, c.y+35, right, 5, frac, color)
}

func (t *playlistsTab) try(err error) {
	if err != nil {
		t.a.message("That didn't work", err.Error())
	}
	t.refresh()
}

func (t *playlistsTab) addFile() {
	p := qt.QFileDialog_GetOpenFileName4(t.a.win.QWidget, "Add a playlist file", "", "Playlists (*.csv *.zip *.txt *.json);;All files (*)")
	if p != "" {
		t.try(t.a.m.AddInput(p))
	}
}

func (t *playlistsTab) addFolder() {
	if p := qt.QFileDialog_GetExistingDirectory3(t.a.win.QWidget, "Add a folder of playlist files", ""); p != "" {
		t.try(t.a.m.AddInput(p))
	}
}

func (t *playlistsTab) addLink() {
	if p, ok := t.a.ask("Add a link", "A playlist, album or song, on Spotify, Deezer, Apple Music, Bandcamp, YouTube, SoundCloud or another site:", ""); ok && p != "" {
		t.try(t.a.m.AddInput(p))
		t.a.toast("Reading that link. Its songs appear here in a moment.")
	}
}

func (t *playlistsTab) selected() *core.PlaylistStatus {
	if r := t.list.current(); r >= 0 && r < len(t.rows) {
		return &t.rows[r]
	}
	return nil
}

func (t *playlistsTab) selectedInput() *core.InputStatus {
	if r := t.inputs.CurrentRow(); r >= 0 && r < len(t.inRows) {
		return &t.inRows[r]
	}
	return nil
}

func (t *playlistsTab) showTracks() {
	if p := t.selected(); p != nil {
		t.a.queue.showPlaylist(p.Name)
	}
}

func (t *playlistsTab) toggle() {
	if p := t.selected(); p != nil {
		t.try(t.a.m.SetPlaylist(p.Name, !p.Enabled, 0))
	}
}

func (t *playlistsTab) prio(delta int) {
	if p := t.selected(); p != nil {
		t.try(t.a.m.SetPlaylist(p.Name, p.Enabled, delta))
	}
}

// selectedNames are the playlists selected, one or many.
func (t *playlistsTab) selectedNames() []string {
	var out []string
	for _, idx := range t.list.view.SelectionModel().SelectedRows() {
		if r := idx.Row(); r >= 0 && r < len(t.rows) {
			out = append(out, t.rows[r].Name)
		}
	}
	return out
}

// removeSelected asks what should happen to the playlist files, then takes the playlists out.
func (t *playlistsTab) removeSelected() {
	names := t.selectedNames()
	if len(names) == 0 || t.removing {
		return
	}
	go func() {
		v := t.a.m.RemovalPreview(names)
		mainDo(func() { t.askRemoval(names, v) })
	}()
}

func (t *playlistsTab) askRemoval(names []string, v core.RemovalPreview) {
	a := t.a
	d := qt.NewQDialog(a.win.QWidget)
	what := names[0]
	if len(names) > 1 {
		what = fmt.Sprintf("%d playlists", len(names))
	}
	d.SetWindowTitle("Remove " + what)
	height := 280
	if len(v.Files) > 0 {
		height += 110
	}
	if v.Shared > 0 {
		height += 50
	}
	d.Resize(520, height)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 16, 20, 14)
	box.SetSpacing(10)
	box.AddWidget(text("Remove "+what+"?", "h1").QWidget)
	intro := "It leaves the app and its songs leave the Downloads tab, unless another playlist also has them."
	if len(names) > 1 {
		intro = "They leave the app and their songs leave the Downloads tab, unless another playlist also has them."
	}
	box.AddWidget(text(intro, "subtitle").QWidget)
	box.AddWidget(text("Your music files stay on the disk.", "").QWidget)
	if v.Shared > 0 {
		note := fmt.Sprintf("%s also hold other playlists, so they stay as they are.", counted(v.Shared, "playlist file"))
		if v.Shared == 1 {
			note = "1 playlist file also holds other playlists, so it stays as it is."
		}
		box.AddWidget(text(note, "subtitle").QWidget)
	}
	one := len(v.Files) == 1
	pick := func(single, many string) string {
		if one {
			return single
		}
		return many
	}
	fileName := fmt.Sprintf("%s playlist files", fmtCount(len(v.Files)))
	if one {
		fileName = filepath.Base(v.Files[0])
	}
	keep := qt.NewQRadioButton3(pick("Ignore it: leave it where it is", "Ignore them: leave them where they are"))
	mv := qt.NewQRadioButton3(pick("Move it to another folder:", "Move them to another folder:"))
	del := qt.NewQRadioButton3(pick("Delete it", "Delete them"))
	keep.SetChecked(true)
	dest := qt.NewQLineEdit2()
	dest.SetPlaceholderText("Choose a folder")
	dest.SetEnabled(false)
	browse := button("Choose…", "", func() {
		if p := qt.QFileDialog_GetExistingDirectory3(d.QWidget, "Move the playlist files to", dest.Text()); p != "" {
			dest.SetText(p)
			mv.SetChecked(true)
		}
	})
	browse.SetEnabled(false)
	mv.OnToggled(func(on bool) {
		dest.SetEnabled(on)
		browse.SetEnabled(on)
	})
	if len(v.Files) > 0 {
		box.AddWidget(text("What should happen to "+fileName+"?", "").QWidget)
		box.AddWidget(keep.QWidget)
		box.AddWidget(mv.QWidget)
		row := hbox()
		row.SetSpacing(6)
		row.AddWidget2(dest.QWidget, 1)
		row.AddWidget(browse.QWidget)
		box.AddLayout(row.QLayout)
		box.AddWidget(del.QWidget)
	}
	box.AddStretch()
	btns := hbox()
	btns.SetSpacing(8)
	btns.AddStretch()
	btns.AddWidget(button("Cancel", "", func() { d.Reject() }).QWidget)
	go_ := button("Remove", "danger", func() {
		if mv.IsChecked() && dest.Text() == "" {
			a.message("Choose a folder", "Pick where the playlist files should go, or choose another option.")
			return
		}
		d.Accept()
	})
	btns.AddWidget(go_.QWidget)
	box.AddLayout(btns.QLayout)
	if d.Exec() != int(qt.QDialog__Accepted) {
		return
	}
	files, moveTo := "keep", ""
	switch {
	case del.IsChecked():
		if !a.confirm("Delete "+fileName+"?", "This can't be undone, and the playlist can't be brought back afterwards. Your music files stay on the disk.") {
			return
		}
		files = "delete"
	case mv.IsChecked():
		files, moveTo = "move", dest.Text()
	}
	t.removing = true
	t.remove.SetEnabled(false)
	go func() {
		res, err := a.m.RemovePlaylists(names, files, moveTo)
		mainDo(func() {
			t.removing = false
			t.updateButtons()
			if err != nil {
				a.message("That didn't work", err.Error())
				return
			}
			msg := "Removed " + what + "."
			switch {
			case res.Deleted > 0:
				msg = fmt.Sprintf("Removed %s and deleted %s.", what, fileName)
			case res.Moved > 0:
				msg = fmt.Sprintf("Removed %s and moved %s.", what, fileName)
			}
			if len(res.Errors) > 0 {
				a.message("Some playlist files couldn't be handled", fmt.Sprintf("%s\n\n%d files had problems; see Activity.", res.Errors[0], len(res.Errors)))
			}
			a.toast(msg)
			t.refresh()
		})
	}()
}

// showIgnored lists the removed playlists whose files are still there, to bring back or clear.
func (t *playlistsTab) showIgnored() {
	a := t.a
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Ignored playlists")
	d.Resize(480, 420)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 16, 20, 14)
	box.SetSpacing(10)
	box.AddWidget(text("Ignored playlists", "h1").QWidget)
	box.AddWidget(text("You removed these, and the app skips them when it reads their files. Taking one off this list brings it back and puts its songs back in line.", "subtitle").QWidget)
	list := qt.NewQListWidget2()
	list.SetSelectionMode(qt.QAbstractItemView__ExtendedSelection)
	box.AddWidget2(list.QWidget, 1)
	fill := func() {
		list.Clear()
		names := a.m.RemovedPlaylists()
		for _, n := range names {
			list.AddItem(n)
		}
		if len(names) == 0 {
			d.Accept()
		}
	}
	btns := hbox()
	btns.SetSpacing(8)
	back := button("Bring back selected", "", func() {
		var names []string
		for _, it := range list.SelectedItems() {
			names = append(names, it.Text())
		}
		if len(names) > 0 {
			a.m.RestorePlaylists(names...)
			a.toast(fmt.Sprintf("Brought back %s.", counted(len(names), "playlist")))
			fill()
		}
	})
	back.SetEnabled(false)
	list.OnItemSelectionChanged(func() { back.SetEnabled(len(list.SelectedItems()) > 0) })
	btns.AddWidget(back.QWidget)
	btns.AddWidget(button("Clear the list", "", func() {
		n := list.Count()
		if a.confirm("Clear the ignored list?", fmt.Sprintf("%s come back and their songs go back in line.", counted(n, "playlist"))) {
			a.m.RestorePlaylists()
			a.toast(fmt.Sprintf("Brought back %s.", counted(n, "playlist")))
			fill()
		}
	}).QWidget)
	btns.AddStretch()
	btns.AddWidget(button("Close", "", func() { d.Reject() }).QWidget)
	box.AddLayout(btns.QLayout)
	fill()
	d.Exec()
	t.refresh()
}

// counted is "1 playlist", "2 playlists".
func counted(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmtCount(n) + " " + noun + "s"
}

func (t *playlistsTab) toggleInput() {
	if in := t.selectedInput(); in != nil {
		t.try(t.a.m.SetInputEnabled(in.Path, !in.Enabled))
	}
}

func (t *playlistsTab) removeInput() {
	if in := t.selectedInput(); in != nil && t.a.confirm("Remove this file?", in.Path+"\n\nThe app stops using it. The file itself isn't touched.") {
		t.try(t.a.m.RemoveInput(in.Path))
	}
}

func (t *playlistsTab) updateButtons() {
	p := t.selected()
	picked := t.selectedNames()
	on := p != nil && len(picked) == 1
	for _, b := range []*qt.QPushButton{t.show, t.onoff, t.up, t.down} {
		b.SetEnabled(on)
	}
	t.up.SetVisible(!t.finished) // nothing left to put first
	t.down.SetVisible(!t.finished)
	t.remove.SetEnabled(len(picked) > 0 && !t.removing)
	if len(picked) > 1 {
		t.remove.SetText(fmt.Sprintf("Remove %d", len(picked)))
	} else {
		t.remove.SetText("Remove")
	}
	t.selAll.SetEnabled(len(t.rows) > 0)
	if on && !p.Enabled {
		t.onoff.SetText("Turn on")
	} else {
		t.onoff.SetText("Turn off")
	}
	in := t.selectedInput()
	t.inToggle.SetEnabled(in != nil)
	t.inRemove.SetEnabled(in != nil)
	if in != nil && !in.Enabled {
		t.inToggle.SetText("Turn on")
	} else {
		t.inToggle.SetText("Turn off")
	}
}

// showCategory shows the playlists still being worked on, or the finished ones.
func (t *playlistsTab) showCategory() {
	sel := ""
	if p := t.selected(); p != nil {
		sel = p.Name
	}
	var open, finished []core.PlaylistStatus
	for _, p := range t.all {
		if p.Finished() {
			finished = append(finished, p)
		} else {
			open = append(open, p)
		}
	}
	rows := open
	if t.finished {
		rows = finished
	}
	if len(rows) != len(t.rows) {
		t.list.reset(func() { t.rows = rows })
	} else {
		t.rows = rows
		t.list.redraw()
	}
	for i, p := range t.rows {
		if p.Name == sel && t.list.current() != i {
			t.list.selectRow(i)
		}
	}
	t.segs[0].SetText(fmt.Sprintf("In progress (%s)", fmtCount(len(open))))
	t.segs[1].SetText(fmt.Sprintf("Finished playlists (%s)", fmtCount(len(finished))))
	switch {
	case len(rows) > 0 || len(t.all) == 0:
		t.note.Hide()
	case t.finished:
		t.note.SetText("Playlists move here once every song is downloaded, in your library, or couldn't be found.")
		t.note.Show()
	default:
		t.note.SetText("Every playlist has finished. They're under Finished playlists.")
		t.note.Show()
	}
	t.updateButtons()
}

// refresh reads the playlists and files again, away from the window's thread.
func (t *playlistsTab) refresh() {
	if t.loading {
		t.again = true
		return
	}
	t.loading = true
	go func() {
		rows, inputs, removed := t.a.m.Playlists(), t.a.m.Inputs(), len(t.a.m.RemovedPlaylists())
		mainthread.Start(func() {
			t.restore.SetVisible(removed > 0)
			t.restore.SetText(fmt.Sprintf("Ignored playlists (%d)", removed))
			t.loaded(rows, inputs)
		})
	}()
}

func (t *playlistsTab) loaded(rows []core.PlaylistStatus, inputs []core.InputStatus) {
	t.loading = false
	if t.again {
		t.again = false
		t.refresh()
		return
	}
	t.all = rows
	t.showCategory()
	inRow := t.inputs.CurrentRow()
	same := len(inputs) == len(t.inRows)
	for i := 0; same && i < len(inputs); i++ {
		same = inputs[i] == t.inRows[i]
	}
	if !same {
		t.inRows = inputs
		t.inputs.Clear()
		for _, in := range t.inRows {
			s := in.Path
			if !in.Enabled {
				s = "(off)  " + s
			}
			if in.Err != "" {
				s += "  —  " + in.Err
			}
			t.inputs.AddItem(s)
		}
		if inRow >= 0 && inRow < len(t.inRows) {
			t.inputs.SetCurrentRow(inRow)
		}
	}
	if len(t.all) == 0 && len(t.inRows) == 0 {
		t.stack.SetCurrentIndex(0)
	} else {
		t.stack.SetCurrentIndex(1)
	}
	t.updateButtons()
}
