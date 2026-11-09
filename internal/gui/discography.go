package gui

import (
	"context"
	"fmt"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// An artist's discography, before adding it: everything the catalogs list, to pick from a whole
// release or single songs at a time.

type pickRow struct {
	item    *qt.QTreeWidgetItem
	rel     core.Release
	tracks  []*core.Track
	loading bool
	loaded  bool
}

type discographyPicker struct {
	a     *app
	d     *qt.QDialog
	tree  *qt.QTreeWidget
	note  *qt.QLabel
	rows  []*pickRow
	add   *qt.QPushButton
	ready bool
}

func (t *searchTab) chooseFrom(r core.MusicResult, b *qt.QPushButton) {
	a := t.a
	if t.adding != "" {
		a.toast("Still adding " + t.adding + ". One moment.")
		return
	}
	p := &discographyPicker{a: a, d: qt.NewQDialog(a.win.QWidget)}
	d := p.d
	d.SetWindowTitle(r.Title + ": discography")
	d.Resize(760, 620)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 16, 20, 14)
	box.SetSpacing(10)
	box.AddWidget(text(r.Title, "h1").QWidget)
	p.note = text("Asking the catalogs what's out there…", "subtitle")
	box.AddWidget(p.note.QWidget)
	p.tree = qt.NewQTreeWidget2()
	p.tree.SetHeaderLabels([]string{"Release", "Kind", "Year", "Songs"})
	p.tree.SetColumnWidth(0, 440)
	p.tree.SetColumnWidth(1, 80)
	p.tree.SetColumnWidth(2, 70)
	p.tree.OnItemExpanded(func(item *qt.QTreeWidgetItem) { p.loadTracks(item) })
	box.AddWidget2(p.tree.QWidget, 1)
	row := hbox()
	row.SetSpacing(6)
	row.AddWidget(button("Select all", "ghost", func() { p.setAll(func(*pickRow) bool { return true }) }).QWidget)
	row.AddWidget(button("Select none", "ghost", func() { p.setAll(func(*pickRow) bool { return false }) }).QWidget)
	row.AddWidget(button("Albums and EPs only", "ghost", func() {
		p.setAll(func(x *pickRow) bool { return x.rel.Kind == "album" || x.rel.Kind == "ep" })
	}).QWidget)
	row.AddStretch()
	row.AddWidget(button("Cancel", "", func() { d.Reject() }).QWidget)
	p.add = a.iconButton("plus", "Add selected", "primary", "", func() { d.Accept() })
	p.add.SetEnabled(false)
	row.AddWidget(p.add.QWidget)
	box.AddLayout(row.QLayout)
	ctx, cancel := context.WithCancel(a.m.Context())
	defer cancel()
	go func() {
		rs, err := a.m.ArtistReleases(ctx, r)
		mainthread.Start(func() {
			if ctx.Err() != nil {
				return
			}
			p.fill(rs, err)
		})
	}()
	if d.Exec() != int(qt.QDialog__Accepted) {
		return
	}
	picks := p.picks()
	if len(picks) == 0 {
		return
	}
	t.adding = r.String()
	b.SetEnabled(false)
	b.SetText("Adding…")
	go func() {
		ctx, cancel := context.WithTimeout(a.m.Context(), 10*time.Minute)
		defer cancel()
		_, n, err := a.m.AddReleases(ctx, r, picks, func(s string) { mainthread.Start(func() { t.setNote(s) }) })
		mainthread.Start(func() {
			t.adding = ""
			t.setNote("")
			b.SetEnabled(true)
			b.SetText("See discography…")
			if err != nil {
				a.message("Couldn't add that", err.Error())
				return
			}
			a.toast(fmt.Sprintf("Added %d songs to the front of your downloads.", n))
			a.queue.refresh(false)
		})
	}()
}

func (p *discographyPicker) fill(rs []core.Release, err error) {
	if err != nil {
		p.note.SetText("Couldn't read the discography: " + err.Error())
		return
	}
	kinds := map[string]string{"album": "Album", "ep": "EP", "single": "Single", "other": "Other"}
	for i, r := range rs {
		count := ""
		if r.Tracks > 0 {
			count = fmt.Sprint(r.Tracks)
		}
		item := qt.NewQTreeWidgetItem2([]string{r.Title, kinds[r.Kind], r.Year, count})
		item.SetFlags(item.Flags() | qt.ItemIsUserCheckable | qt.ItemIsAutoTristate)
		item.SetCheckState(0, qt.Checked)
		item.SetChildIndicatorPolicy(qt.QTreeWidgetItem__ShowIndicator)
		item.SetData(0, int(qt.UserRole), qt.NewQVariant4(i))
		p.tree.AddTopLevelItem(item)
		p.rows = append(p.rows, &pickRow{item: item, rel: r})
	}
	p.note.SetText(fmt.Sprintf("%d releases. Open one to pick single songs. Each song is added once.", len(rs)))
	p.add.SetEnabled(len(rs) > 0)
}

func (p *discographyPicker) rowOf(item *qt.QTreeWidgetItem) *pickRow {
	if i := item.Data(0, int(qt.UserRole)).ToInt(); i >= 0 && i < len(p.rows) && item.Parent() == nil {
		return p.rows[i]
	}
	return nil
}

// loadTracks fills an opened release in with its songs.
func (p *discographyPicker) loadTracks(item *qt.QTreeWidgetItem) {
	row := p.rowOf(item)
	if row == nil || row.loading || row.loaded {
		return
	}
	row.loading = true
	item.SetText(3, "…")
	rel := row.rel
	go func() {
		ctx, cancel := context.WithTimeout(p.a.m.Context(), 45*time.Second)
		defer cancel()
		tracks, err := p.a.m.ReleaseTracks(ctx, rel)
		mainthread.Start(func() {
			row.loading, row.loaded = false, err == nil
			if err != nil {
				item.SetText(3, "?")
				p.note.SetText("Couldn't read " + rel.Title + ": " + err.Error())
				return
			}
			state := item.CheckState(0)
			row.tracks = tracks
			item.SetText(3, fmt.Sprint(len(tracks)))
			for _, tr := range tracks {
				child := qt.NewQTreeWidgetItem7(item, []string{tr.Title, core.FmtDuration(tr.Duration()), "", ""})
				child.SetFlags(child.Flags() | qt.ItemIsUserCheckable)
				if state == qt.Unchecked {
					child.SetCheckState(0, qt.Unchecked)
				} else {
					child.SetCheckState(0, qt.Checked)
				}
			}
		})
	}()
}

func (p *discographyPicker) setAll(want func(*pickRow) bool) {
	for _, r := range p.rows {
		state := qt.Unchecked
		if want(r) {
			state = qt.Checked
		}
		r.item.SetCheckState(0, state)
	}
}

// picks is what's checked: a release not opened counts as a whole, one opened by its songs.
func (p *discographyPicker) picks() []core.ReleasePick {
	var out []core.ReleasePick
	for _, r := range p.rows {
		switch st := r.item.CheckState(0); {
		case st == qt.Unchecked:
		case !r.loaded || st == qt.Checked:
			out = append(out, core.ReleasePick{Release: r.rel})
		default:
			var tracks []*core.Track
			for i, tr := range r.tracks {
				if i < r.item.ChildCount() && r.item.Child(i).CheckState(0) == qt.Checked {
					tracks = append(tracks, tr)
				}
			}
			if len(tracks) > 0 {
				out = append(out, core.ReleasePick{Release: r.rel, Tracks: tracks})
			}
		}
	}
	return out
}
