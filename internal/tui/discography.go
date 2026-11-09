package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"universal-music-downloader/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Picking from an artist's discography, and removing playlists.

type discoRow struct {
	rel     core.Release
	on      bool
	open    bool
	loading bool
	tracks  []*core.Track
	trackOn []bool
}

// pickDiscography lists what an artist has released: space picks, Enter opens a release to pick
// single songs, s adds what's picked.
func (ui *tui) pickDiscography(r core.MusicResult) {
	const name = "discography"
	t := tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	t.SetBorder(true).SetTitle(" " + tview.Escape(r.Title) + ": asking the catalogs… ")
	var rows []*discoRow
	type line struct{ row, track int }
	var lines []line
	fill := func() {
		sel, _ := t.GetSelection()
		t.Clear()
		headerRow(t, "", "Release", "Kind", "Year", "Songs")
		lines = lines[:0]
		n := 1
		for ri, x := range rows {
			mark := "[ ]"
			if x.on {
				mark = "[x]"
			}
			count := ""
			if x.rel.Tracks > 0 {
				count = fmt.Sprint(x.rel.Tracks)
			}
			if x.loading {
				count = "…"
			}
			arrow := "▸ "
			if x.open {
				arrow = "▾ "
			}
			for c, s := range []string{mark, arrow + tview.Escape(x.rel.Title), x.rel.Kind, x.rel.Year, count} {
				cell := tview.NewTableCell(s)
				if c == 1 {
					cell.SetExpansion(1)
				}
				t.SetCell(n, c, cell)
			}
			lines = append(lines, line{ri, -1})
			n++
			if !x.open {
				continue
			}
			for i, tr := range x.tracks {
				mark := "[ ]"
				if x.trackOn[i] {
					mark = "[x]"
				}
				t.SetCell(n, 0, tview.NewTableCell(mark))
				t.SetCell(n, 1, tview.NewTableCell("    "+tview.Escape(tr.Title)).SetExpansion(1).SetTextColor(tcell.ColorGray))
				t.SetCell(n, 2, tview.NewTableCell(""))
				t.SetCell(n, 3, tview.NewTableCell(core.FmtDuration(tr.Duration())))
				t.SetCell(n, 4, tview.NewTableCell(""))
				lines = append(lines, line{ri, i})
				n++
			}
		}
		t.Select(min(max(sel, 1), max(1, len(lines))), 0)
	}
	ctx, cancel := context.WithCancel(ui.m.Context())
	close := func() { cancel(); ui.closeModal(name) }
	current := func() (line, bool) {
		sel, _ := t.GetSelection()
		if sel >= 1 && sel-1 < len(lines) {
			return lines[sel-1], true
		}
		return line{}, false
	}
	setAll := func(want func(*discoRow) bool) {
		for _, x := range rows {
			x.on = want(x)
			for i := range x.trackOn {
				x.trackOn[i] = x.on
			}
		}
		fill()
	}
	toggleOpen := func(x *discoRow) {
		if x.tracks != nil || x.loading {
			x.open = !x.open
			fill()
			return
		}
		x.loading = true
		fill()
		go func() {
			lctx, c := context.WithTimeout(ctx, 45*time.Second)
			defer c()
			tracks, err := ui.m.ReleaseTracks(lctx, x.rel)
			ui.app.QueueUpdateDraw(func() {
				x.loading = false
				if err != nil {
					ui.message("Discography", err.Error())
				} else {
					x.tracks, x.open, x.rel.Tracks = tracks, true, len(tracks)
					x.trackOn = make([]bool, len(tracks))
					for i := range x.trackOn {
						x.trackOn[i] = x.on
					}
				}
				fill()
			})
		}()
	}
	addPicked := func() {
		var picks []core.ReleasePick
		for _, x := range rows {
			if x.tracks == nil {
				if x.on {
					picks = append(picks, core.ReleasePick{Release: x.rel})
				}
				continue
			}
			var ts []*core.Track
			for i, tr := range x.tracks {
				if x.trackOn[i] {
					ts = append(ts, tr)
				}
			}
			if len(ts) == len(x.tracks) {
				picks = append(picks, core.ReleasePick{Release: x.rel})
			} else if len(ts) > 0 {
				picks = append(picks, core.ReleasePick{Release: x.rel, Tracks: ts})
			}
		}
		if len(picks) == 0 {
			ui.message("Discography", "Nothing is picked.")
			return
		}
		if ui.adding != "" {
			ui.message("Search", "Still adding "+ui.adding+"; one at a time.")
			return
		}
		close()
		ui.adding = r.String()
		ui.addNote = "[yellow]adding " + tview.Escape(r.Title) + "…[-]"
		go func() {
			actx, c := context.WithTimeout(ui.m.Context(), 10*time.Minute)
			defer c()
			pl, n, err := ui.m.AddReleases(actx, r, picks, func(p string) {
				ui.app.QueueUpdateDraw(func() { ui.addNote = "[yellow]" + tview.Escape(p) + "[-]" })
			})
			ui.app.QueueUpdateDraw(func() {
				ui.adding = ""
				if err != nil {
					ui.addNote = "[red]" + tview.Escape(r.String()+": "+err.Error()) + "[-]"
					return
				}
				ui.addNote = fmt.Sprintf("[green]added %s: %d tracks, first in the queue[-]", tview.Escape(pl), n)
			})
		}()
	}
	t.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		l, ok := current()
		switch {
		case ev.Key() == tcell.KeyEscape:
			close()
		case ev.Key() == tcell.KeyEnter:
			if ok && l.track < 0 {
				toggleOpen(rows[l.row])
			}
		case ev.Rune() == ' ':
			if ok {
				x := rows[l.row]
				if l.track >= 0 {
					x.trackOn[l.track] = !x.trackOn[l.track]
					x.on = false
					for _, v := range x.trackOn {
						x.on = x.on || v
					}
				} else {
					x.on = !x.on
					for i := range x.trackOn {
						x.trackOn[i] = x.on
					}
				}
				fill()
			}
		case ev.Rune() == 'a':
			setAll(func(*discoRow) bool { return true })
		case ev.Rune() == 'n':
			setAll(func(*discoRow) bool { return false })
		case ev.Rune() == 'A':
			setAll(func(x *discoRow) bool { return x.rel.Kind == "album" || x.rel.Kind == "ep" })
		case ev.Rune() == 's':
			addPicked()
		default:
			return ev
		}
		return nil
	})
	fill()
	ui.openModal(name, t, 100, 30)
	ui.app.SetFocus(t)
	go func() {
		rs, err := ui.m.ArtistReleases(ctx, r)
		ui.app.QueueUpdateDraw(func() {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				close()
				ui.message("Discography", err.Error())
				return
			}
			for _, x := range rs {
				rows = append(rows, &discoRow{rel: x, on: true})
			}
			t.SetTitle(fmt.Sprintf(" %s: %d releases (space picks · Enter opens · a all · n none · A albums and EPs · s adds · Esc cancels) ", tview.Escape(r.Title), len(rs)))
			fill()
		})
	}()
}

// removePlaylists asks what happens to the playlist files, then removes the playlists.
func (ui *tui) removePlaylists(names []string) {
	const name = "removeplaylists"
	what := names[0]
	if len(names) > 1 {
		what = fmt.Sprintf("%d playlists", len(names))
	}
	v := ui.m.RemovalPreview(names)
	f := tview.NewForm()
	info := "Takes it out of the app and clears its songs from the Queue, unless another playlist has them. Your music files stay on the disk."
	if v.Shared > 0 {
		info += fmt.Sprintf(" Files that also hold other playlists stay: %d.", v.Shared)
	}
	f.AddTextView("", tview.Escape(info), 0, 3, true, false)
	choices := []string{"Ignore it (leave it where it is)", "Move it to another folder", "Delete it"}
	values := []string{"keep", "move", "delete"}
	pick := 0
	moveTo := ""
	fileName := fmt.Sprintf("%d playlist files", len(v.Files))
	if len(v.Files) == 1 {
		fileName = filepath.Base(v.Files[0])
	}
	if len(v.Files) > 0 {
		f.AddDropDown(tview.Escape(fileName), choices, 0, func(_ string, i int) {
			if i >= 0 {
				pick = i
			}
		})
		f.AddInputField("Folder to move it to", "", 60, nil, func(t string) { moveTo = t })
	}
	close := func() { ui.closeModal(name) }
	run := func() {
		close()
		go func() {
			res, err := ui.m.RemovePlaylists(names, values[pick], moveTo)
			ui.app.QueueUpdateDraw(func() {
				ui.playlistMarks = nil
				if err != nil {
					ui.message("Remove", err.Error())
					return
				}
				ui.addNote = fmt.Sprintf("[green]removed %d playlists: %d playlist files deleted, %d moved; music files untouched[-]", res.Playlists, res.Deleted, res.Moved)
				if len(res.Errors) > 0 {
					ui.message("Remove", fmt.Sprintf("%d playlist files had problems; the first: %s", len(res.Errors), res.Errors[0]))
				}
			})
		}()
	}
	f.AddButton("Remove", func() {
		switch {
		case values[pick] == "move" && moveTo == "":
			ui.message("Remove", "Say which folder the playlist file should go to.")
		case values[pick] == "delete":
			ui.confirm(fmt.Sprintf("Delete %s for good? Your music files stay.", tview.Escape(fileName)), "Delete", run)
		default:
			run()
		}
	})
	f.AddButton("Cancel", close)
	f.SetCancelFunc(close)
	f.SetBorder(true).SetTitle(" Remove " + tview.Escape(what) + " ")
	ui.openModal(name, f, 90, 14)
}

// showIgnored lists the removed playlists whose files are still there, to bring back or clear.
func (ui *tui) showIgnored() {
	const name = "ignoredplaylists"
	if len(ui.m.RemovedPlaylists()) == 0 {
		return
	}
	list := tview.NewList().ShowSecondaryText(false)
	close := func() { ui.closeModal(name) }
	var fill func()
	fill = func() {
		list.Clear()
		names := ui.m.RemovedPlaylists()
		if len(names) == 0 {
			close()
			return
		}
		for _, n := range names {
			list.AddItem(tview.Escape(n), "", 0, func() {
				ui.m.RestorePlaylists(n)
				fill()
			})
		}
	}
	fill()
	list.SetDoneFunc(close)
	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Rune() == 'c' {
			ui.confirm(fmt.Sprintf("Clear the list? %d playlists come back.", list.GetItemCount()), "Clear", func() {
				ui.m.RestorePlaylists()
				fill()
			})
			return nil
		}
		return ev
	})
	list.SetBorder(true).SetTitle(" Ignored playlists (Enter brings one back · c clears the list · Esc closes) ")
	ui.openModal(name, list, 90, 20)
}
