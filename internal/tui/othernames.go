package tui

import (
	"fmt"
	"strings"

	"universal-music-downloader/internal/core"

	"github.com/rivo/tview"
)

// otherNames asks which other names to search for a track by (see core.NameVariant): the
// artist in another alphabet, the real artist of a "Various Artists" listing, and so on.
func (ui *tui) otherNames(key string) {
	v, ok := ui.m.JobView(key)
	if !ok {
		return
	}
	suggested, chosen := ui.m.NameSuggestions(key)
	name := "othernames"
	form := tview.NewForm()
	form.AddTextView("", tview.Escape("Sources sometimes list a song under another name. Tick the names to search for it by, or type one in "+
		"(empty artist: any artist, if the title and length fit). It's still saved and tagged as “"+v.Track.String()+"”."), 0, 3, true, false)
	var names []core.NameVariant
	var on []bool
	add := func(n core.NameVariant, checked bool) {
		for _, x := range names {
			if strings.EqualFold(x.Artist, n.Artist) && strings.EqualFold(x.Title, n.Title) {
				return
			}
		}
		label := n.Artist + " – " + n.Title
		if n.Artist == "" {
			label = n.Title + " – by any artist"
		}
		if n.Why != "" {
			label += " (" + n.Why + ")"
		}
		i := len(names)
		names, on = append(names, n), append(on, checked)
		form.AddCheckbox(tview.Escape(label), checked, func(c bool) { on[i] = c })
	}
	for _, n := range chosen {
		add(n, true)
	}
	for _, n := range suggested {
		add(n, false)
	}
	artist := tview.NewInputField().SetLabel("Another name: artist ").SetFieldWidth(0)
	title := tview.NewInputField().SetLabel("title ").SetText(v.Track.Title).SetFieldWidth(0)
	form.AddFormItem(artist).AddFormItem(title)
	done := func(own bool) {
		ui.closeModal(name)
		var picked []core.NameVariant
		if !own {
			for i, n := range names {
				if on[i] {
					picked = append(picked, n)
				}
			}
			if a, t := strings.TrimSpace(artist.GetText()), strings.TrimSpace(title.GetText()); a != "" && t != "" {
				picked = append(picked, core.NameVariant{Artist: a, Title: t})
			}
			if len(picked) == 0 {
				core.Logf("%s: no name ticked; nothing changed", v.Track)
				return
			}
		}
		if err := ui.m.SearchUnderOtherNames(key, picked); err != nil {
			ui.confirm(err.Error(), "OK", func() {})
		}
		ui.refresh()
	}
	form.AddButton("Search", func() { done(false) })
	if len(chosen) > 0 {
		form.AddButton("Own name again", func() { done(true) })
	}
	form.AddButton("Cancel", func() { ui.closeModal(name) })
	form.SetCancelFunc(func() { ui.closeModal(name) })
	form.SetBorder(true).SetTitle(fmt.Sprintf(" Find “%s” under other names (space ticks · Tab moves · Esc closes) ", tview.Escape(v.Track.String())))
	ui.openModal(name, form, 110, len(names)*2+14)
}
