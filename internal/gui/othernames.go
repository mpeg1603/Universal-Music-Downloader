package gui

import (
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// "Find under other names": a song the sources know by another name than the one in your
// playlist (the artist in another alphabet, the real artist of a "Various Artists" listing, the
// name in brackets as the artist). You pick names to search for it by, from suggestions or
// typed in; the file found is still named and tagged with the song's own.

func (t *queueTab) otherNames() {
	a := t.a
	key := t.paneKey
	if key == "" {
		key = t.selected()
	}
	v, ok := a.m.JobView(key)
	if !ok {
		return
	}
	suggested, chosen := a.m.NameSuggestions(key)
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Find under other names")
	d.Resize(780, 560)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 16, 20, 14)
	box.SetSpacing(10)
	box.AddWidget(text("Find “"+v.Track.ArtistStr()+" - "+v.Track.Title+"” under other names", "h1").QWidget)
	box.AddWidget(text("Sources sometimes list a song under another name: the artist in another alphabet, "+
		"the real artist instead of “Various Artists”, the words in brackets as the artist. "+
		"Tick the names to search for it by, or type one in. Whatever is found is still saved and tagged as “"+
		v.Track.ArtistStr()+" - "+v.Track.Title+"”, and its length still has to fit.", "subtitle").QWidget)

	list := qt.NewQListWidget2()
	var names []core.NameVariant
	addRow := func(n core.NameVariant, on bool) {
		for i, x := range names {
			if strings.EqualFold(x.Artist, n.Artist) && strings.EqualFold(x.Title, n.Title) {
				if on {
					list.Item(i).SetCheckState(qt.Checked)
				}
				return
			}
		}
		label := n.Artist + " – " + n.Title
		if n.Artist == "" {
			label = n.Title + " – by any artist"
		}
		if n.Why != "" {
			label += "   (" + n.Why + ")"
		}
		item := qt.NewQListWidgetItem2(label)
		item.SetFlags(item.Flags() | qt.ItemIsUserCheckable)
		item.SetCheckState(qt.Unchecked)
		if on {
			item.SetCheckState(qt.Checked)
		}
		list.AddItemWithItem(item)
		names = append(names, n)
	}
	for _, n := range chosen {
		addRow(n, true)
	}
	for _, n := range suggested {
		addRow(n, false)
	}
	box.AddWidget2(list.QWidget, 1)

	box.AddWidget(text("Another name", "h2").QWidget)
	own := hbox()
	own.SetSpacing(6)
	artist := qt.NewQLineEdit2()
	artist.SetPlaceholderText("Artist (empty: any artist)")
	title := qt.NewQLineEdit2()
	title.SetPlaceholderText("Title")
	title.SetText(v.Track.Title)
	title.SetCursorPosition(0)
	own.AddWidget2(artist.QWidget, 1)
	own.AddWidget2(title.QWidget, 1)
	own.AddWidget(a.iconButton("plus", "Add", "", "Add this name to the list, ticked", func() {
		if tt := strings.TrimSpace(title.Text()); tt != "" {
			addRow(core.NameVariant{Artist: strings.TrimSpace(artist.Text()), Title: tt}, true)
			artist.SetText("")
		}
	}).QWidget)
	box.AddLayout(own.QLayout)

	row := hbox()
	row.SetSpacing(6)
	ownName := false
	if len(chosen) > 0 {
		row.AddWidget(button("Search by its own name again", "ghost", func() { ownName = true; d.Accept() }).QWidget)
	}
	row.AddStretch()
	row.AddWidget(button("Cancel", "", func() { d.Reject() }).QWidget)
	row.AddWidget(a.iconButton("search", "Search", "primary", "Search every source again by the ticked names", func() { d.Accept() }).QWidget)
	box.AddLayout(row.QLayout)

	if d.Exec() != int(qt.QDialog__Accepted) {
		return
	}
	var picked []core.NameVariant
	if !ownName {
		for i, n := range names {
			if list.Item(i).CheckState() == qt.Checked {
				picked = append(picked, n)
			}
		}
		// What's typed in counts even without pressing Add.
		if tt := strings.TrimSpace(title.Text()); strings.TrimSpace(artist.Text()) != "" && tt != "" {
			n := core.NameVariant{Artist: strings.TrimSpace(artist.Text()), Title: tt}
			dup := false
			for _, p := range picked {
				dup = dup || strings.EqualFold(p.Artist, n.Artist) && strings.EqualFold(p.Title, n.Title)
			}
			if !dup {
				picked = append(picked, n)
			}
		}
		if len(picked) == 0 {
			a.toast("No name ticked; nothing changed.")
			return
		}
	}
	if err := a.m.SearchUnderOtherNames(key, picked); err != nil {
		a.message("Couldn't search under those names", err.Error())
		return
	}
	if ownName {
		a.toast("Searching for it by its own name again.")
	} else {
		a.toast("Searching for it under the names you picked.")
	}
	t.refresh(false)
	t.fillPane()
}
