package tui

import (
	"universal-music-downloader/internal/core"

	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The Search tab: find music by name and add it to the queue (a track, an album, an artist's
// discography), and the playlist files and lists the queue is made from.

func (ui *tui) buildSearch() tview.Primitive {
	ui.findField = tview.NewInputField().SetLabel(" Find: ").SetFieldWidth(0).
		SetPlaceholder("artist - title · artist - album · artist · or tags: artist: album: track: year:  (Enter searches)")
	field := func(label string, width int) *tview.InputField {
		f := tview.NewInputField().SetLabel(" " + label + " ").SetFieldWidth(width)
		return f
	}
	ui.fArtist, ui.fAlbum, ui.fTrack, ui.fYear = field("Artist", 0), field("Album", 0), field("Track", 0), field("Year", 11)
	ui.fYear.SetPlaceholder("1997 or 1995-1999")
	ui.resultsTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.resultsTable.SetBorder(true).SetTitle(" Results (Enter adds to the queue) ")
	inputs := []*tview.InputField{ui.findField, ui.fArtist, ui.fAlbum, ui.fTrack, ui.fYear}
	for i, f := range inputs {
		i := i
		f.SetDoneFunc(func(k tcell.Key) {
			switch k {
			case tcell.KeyEnter:
				ui.findMusic(ui.musicQuery())
			case tcell.KeyEscape, tcell.KeyDown:
				ui.app.SetFocus(ui.resultsTable)
			case tcell.KeyTab:
				if i+1 < len(inputs) {
					ui.app.SetFocus(inputs[i+1])
				} else {
					ui.app.SetFocus(ui.resultsTable)
				}
			case tcell.KeyBacktab:
				if i > 0 {
					ui.app.SetFocus(inputs[i-1])
				} else {
					ui.app.SetFocus(ui.playlistsTable)
				}
			}
		})
	}
	ui.resultsTable.SetSelectedFunc(func(row, col int) {
		if row >= 0 && row < len(ui.resultRows) && ui.resultRows[row] >= 0 {
			ui.addMusic(ui.results[ui.resultRows[row]])
		}
	})
	ui.resultsTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(ui.inputsTable)
		case ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(ui.fYear)
		case ev.Rune() == '/':
			ui.app.SetFocus(ui.findField)
		case ev.Key() == tcell.KeyUp:
			if r, _ := ui.resultsTable.GetSelection(); r <= ui.firstResultRow() {
				ui.app.SetFocus(ui.findField)
				return nil
			}
			return ev
		default:
			return ev
		}
		return nil
	})
	ui.buildPlaylistTables()
	lists := tview.NewFlex().
		AddItem(ui.inputsTable, 0, 2, false).
		AddItem(ui.playlistsTable, 0, 3, false)
	ui.findInfo = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	fields := tview.NewFlex().
		AddItem(ui.fArtist, 0, 3, false).
		AddItem(ui.fAlbum, 0, 3, false).
		AddItem(ui.fTrack, 0, 3, false).
		AddItem(ui.fYear, 30, 0, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.findField, 1, 0, false).
		AddItem(fields, 1, 0, false).
		AddItem(ui.findInfo, 1, 0, false).
		AddItem(ui.resultsTable, 0, 1, true).
		AddItem(lists, 0, 1, false)
}

// musicQuery is the Find text with the fields added as tags.
func (ui *tui) musicQuery() string {
	return core.MusicQuery(ui.findField.GetText(), ui.fArtist.GetText(), ui.fAlbum.GetText(), ui.fTrack.GetText(), ui.fYear.GetText())
}

var resultSections = []struct {
	kind, title string
	color       tcell.Color
}{
	{"track", "♪ Tracks", tcell.ColorAqua},
	{"album", "◉ Albums", tcell.ColorYellow},
	{"artist", "★ Artists (Enter: the whole discography)", tcell.ColorFuchsia},
}

func (ui *tui) firstResultRow() int {
	for r, i := range ui.resultRows {
		if i >= 0 {
			return r
		}
	}
	return 0
}

// findMusic searches the catalog in the background.
func (ui *tui) findMusic(text string) {
	text = strings.TrimSpace(text)
	if text == "" || ui.finding {
		return
	}
	ui.finding = true
	ui.resultsTable.SetTitle(" Searching… ")
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.Context(), 30*time.Second)
		defer cancel()
		res, err := ui.m.SearchMusic(ctx, text)
		ui.app.QueueUpdateDraw(func() {
			ui.finding = false
			if err != nil {
				ui.resultsTable.SetTitle(" Results ")
				ui.message("Search", "The music catalog (Deezer) didn't answer: "+err.Error())
				return
			}
			ui.results = res
			ui.fillResults()
			ui.resultsTable.SetTitle(fmt.Sprintf(" %d results for %q (Enter adds: a track, the whole album, or the artist's discography) ", len(res), text))
			if len(res) > 0 {
				ui.resultsTable.Select(ui.firstResultRow(), 0)
				ui.app.SetFocus(ui.resultsTable)
			}
		})
	}()
}

// fillResults lists the results by kind, each kind under its own heading, best first; the
// kind whose best result fits best comes first.
func (ui *tui) fillResults() {
	t := ui.resultsTable
	t.Clear()
	ui.resultRows = ui.resultRows[:0]
	best := map[string]float64{}
	for _, r := range ui.results {
		if _, ok := best[r.Kind]; !ok {
			best[r.Kind] = r.Score // results come best first
		}
	}
	sections := append(resultSections[:0:0], resultSections...)
	sort.SliceStable(sections, func(i, j int) bool { return best[sections[i].kind] > best[sections[j].kind] })
	row := 0
	for _, sec := range sections {
		if _, ok := best[sec.kind]; !ok {
			continue
		}
		head := []string{"[::b]" + sec.title + "[::-]", "Artist", "", "Year", "", "Fits"}
		switch sec.kind {
		case "track":
			head[2], head[4] = "Album", "Length"
		case "album":
			head[2], head[4] = "", "Tracks"
		case "artist":
			head[1], head[4] = "", "Releases"
		}
		for c, h := range head {
			t.SetCell(row, c, tview.NewTableCell(h).SetTextColor(sec.color).SetSelectable(false).SetAttributes(tcell.AttrBold))
		}
		ui.resultRows = append(ui.resultRows, -1)
		row++
		for i, r := range ui.results {
			if r.Kind != sec.kind {
				continue
			}
			size := ""
			switch r.Kind {
			case "track":
				size = core.FmtDuration(float64(r.Duration))
			case "album", "artist":
				size = fmt.Sprint(r.Count)
			}
			cells := []*tview.TableCell{
				tview.NewTableCell("  " + tview.Escape(r.Title)).SetExpansion(2).SetMaxWidth(60).SetTextColor(sec.color),
				tview.NewTableCell(tview.Escape(r.Artist)).SetExpansion(1).SetMaxWidth(40),
				tview.NewTableCell(tview.Escape(r.Album)).SetExpansion(1).SetMaxWidth(40).SetTextColor(tcell.ColorGray),
				tview.NewTableCell(r.Year),
				tview.NewTableCell(size).SetAlign(tview.AlignRight),
				tview.NewTableCell(fmt.Sprintf("%.0f%%", max(0, r.Score))).SetAlign(tview.AlignRight).SetTextColor(tcell.ColorGray),
			}
			for c, cell := range cells {
				t.SetCell(row, c, cell)
			}
			ui.resultRows = append(ui.resultRows, i)
			row++
		}
		t.SetCell(row, 0, tview.NewTableCell("").SetSelectable(false))
		ui.resultRows = append(ui.resultRows, -1)
		row++
	}
	if len(ui.results) == 0 {
		t.SetCell(0, 0, tview.NewTableCell("[gray]nothing found; try \"Artist - Title\", only the artist, or fewer fields[-]").SetSelectable(false))
		ui.resultRows = append(ui.resultRows, -1)
	}
}

// addMusic adds a result to the queue (an artist's discography after asking).
func (ui *tui) addMusic(r core.MusicResult) {
	run := func() {
		if ui.adding != "" {
			ui.message("Search", "Still adding "+ui.adding+"; one at a time.")
			return
		}
		ui.adding = r.String()
		ui.addNote = "[yellow]adding " + tview.Escape(r.String()) + "…[-]"
		go func() {
			ctx, cancel := context.WithTimeout(ui.m.Context(), 10*time.Minute)
			defer cancel()
			name, n, err := ui.m.AddMusic(ctx, r, func(p string) {
				ui.app.QueueUpdateDraw(func() { ui.addNote = "[yellow]" + tview.Escape(p) + "[-]" })
			})
			ui.app.QueueUpdateDraw(func() {
				ui.adding = ""
				if err != nil {
					ui.addNote = "[red]" + tview.Escape(r.String()+": "+err.Error()) + "[-]"
					return
				}
				ui.addNote = fmt.Sprintf("[green]added %s: %d tracks, first in the queue[-]", tview.Escape(name), n)
			})
		}()
	}
	if r.Kind == "artist" {
		ui.confirm(fmt.Sprintf("Add %s's whole discography (albums, EPs and singles, each song once; %d releases on Deezer)?", r.Title, r.Count), "Add", run)
		return
	}
	run()
}

func (ui *tui) refreshSearch() {
	ui.refreshPlaylistTables()
	note := ui.addNote
	if note == "" {
		note = "[gray]Enter on a result: a track is queued; an album brings all its tracks; an artist the whole discography. They go first.[-]"
	}
	ui.findInfo.SetText(" " + note)
}

func (ui *tui) buildPlaylistTables() {
	ui.inputsTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.inputsTable.SetBorder(true).SetTitle(" Playlist files (a adds) ")
	ui.playlistsTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.playlistsTable.SetBorder(true).SetTitle(" Playlists (Enter shows the tracks) ")
	addInput := func() {
		ui.askPath("Add playlists", "A CSV (Exportify, TuneMyMusic…), a folder or zip of them, Spotify's data export (zip), a text file (a track, album or artist per line: \"Artist - Title\", \"Artist - Album\", \"Artist\"), or a public Spotify playlist/album link.",
			false, []string{".csv", ".zip", ".txt", ".json"}, func(p string) {
				if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
					p = core.CleanPath(p)
					if _, err := os.Stat(p); err != nil {
						ui.message("Can't add that", err.Error())
						return
					}
				}
				ui.update(func(c *core.Config) {
					for _, in := range c.Inputs {
						if in.Path == p {
							return
						}
					}
					c.Inputs = append(c.Inputs, core.InputCfg{Path: p, Enabled: true})
				})
			})
	}
	ui.inputsTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := ui.inputsTable.GetSelection()
		var sel *core.InputStatus
		if row >= 1 && row-1 < len(ui.inputs) {
			sel = &ui.inputs[row-1]
		}
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(ui.playlistsTable)
			return nil
		case ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(ui.resultsTable)
			return nil
		case ev.Rune() == '/':
			ui.app.SetFocus(ui.findField)
			return nil
		case ev.Key() == tcell.KeyDelete || ev.Rune() == 'd':
			if sel != nil {
				p := sel.Path
				ui.confirm("Remove "+p+" from the list? (The file itself isn't touched.)", "Remove", func() {
					ui.update(func(c *core.Config) {
						var keep []core.InputCfg
						for _, in := range c.Inputs {
							if in.Path != p {
								keep = append(keep, in)
							}
						}
						c.Inputs = keep
					})
				})
			}
			return nil
		case ev.Rune() == 'a':
			addInput()
			return nil
		case ev.Rune() == ' ':
			if sel != nil {
				p := sel.Path
				ui.update(func(c *core.Config) {
					for i := range c.Inputs {
						if c.Inputs[i].Path == p {
							c.Inputs[i].Enabled = !c.Inputs[i].Enabled
						}
					}
				})
			}
			return nil
		case ev.Rune() == 'R':
			ui.m.Reimport()
			return nil
		}
		return ev
	})
	ui.playlistsTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := ui.playlistsTable.GetSelection()
		sel := ui.playlistAt(row)
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(ui.findField)
			return nil
		case ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(ui.inputsTable)
			return nil
		case ev.Rune() == '/':
			ui.app.SetFocus(ui.findField)
			return nil
		case ev.Key() == tcell.KeyEnter:
			if sel != nil {
				ui.q = core.QueueQuery{Playlist: sel.Name, Sort: ui.q.Sort, Reverse: ui.q.Reverse}
				ui.searchField.SetText("")
				ui.show("queue")
			}
			return nil
		case ev.Rune() == 'd' && sel != nil && core.IsAddition(sel.Name):
			name := sel.Name
			ui.confirm("Remove "+name+" (its tracks leave the queue; files already downloaded stay)?", "Remove", func() {
				ui.m.RemoveAddition(name)
			})
			return nil
		case ev.Rune() == 'a':
			addInput()
			return nil
		case ev.Rune() == 'R':
			ui.m.Reimport()
			return nil
		}
		if sel == nil {
			return ev
		}
		name := sel.Name
		switch ev.Rune() {
		case ' ':
			ui.update(func(c *core.Config) {
				pc := c.Playlists[name]
				pc.Disabled = !pc.Disabled
				c.Playlists[name] = pc
			})
		case '+', '=':
			ui.update(func(c *core.Config) { pc := c.Playlists[name]; pc.Priority++; c.Playlists[name] = pc })
		case '-', '_':
			ui.update(func(c *core.Config) { pc := c.Playlists[name]; pc.Priority--; c.Playlists[name] = pc })
		default:
			return ev
		}
		return nil
	})
}

// playlistAt is the playlist on a row of the table; the finished ones come after a heading.
func (ui *tui) playlistAt(row int) *core.PlaylistStatus {
	i := row - 1
	if len(ui.playlists) > ui.playlistsOpen && row > ui.playlistsOpen+1 {
		i--
	}
	if i < 0 || i >= len(ui.playlists) || row == ui.playlistsOpen+1 && len(ui.playlists) > ui.playlistsOpen {
		return nil
	}
	return &ui.playlists[i]
}

func (ui *tui) refreshPlaylistTables() {
	ui.inputs = ui.m.Inputs()
	t := ui.inputsTable
	t.Clear()
	headerRow(t, "On", "File or link", "Problem")
	for i, in := range ui.inputs {
		on, color := "✓", tcell.ColorGreen
		if !in.Enabled {
			on, color = "·", tcell.ColorGray
		}
		t.SetCell(i+1, 0, tview.NewTableCell(on).SetTextColor(color))
		t.SetCell(i+1, 1, tview.NewTableCell(tview.Escape(in.Path)).SetExpansion(2))
		t.SetCell(i+1, 2, tview.NewTableCell(tview.Escape(in.Err)).SetTextColor(tcell.ColorRed).SetExpansion(1))
	}
	if len(ui.inputs) == 0 {
		t.SetCell(1, 1, tview.NewTableCell("[yellow]a adds playlist CSVs (or a folder of them) and text lists; or find music above[-]").SetSelectable(false))
	}
	var open, finished []core.PlaylistStatus
	for _, s := range ui.m.Playlists() {
		if s.Finished() {
			finished = append(finished, s)
		} else {
			open = append(open, s)
		}
	}
	ui.playlists, ui.playlistsOpen = append(open, finished...), len(open)
	p := ui.playlistsTable
	p.Clear()
	headerRow(p, "On", "Priority", "Playlist", "Tracks", "Downloaded", "In library", "To do", "Not found")
	if len(finished) > 0 {
		p.SetCell(len(open)+1, 2, tview.NewTableCell(fmt.Sprintf("[::b]Finished playlists (%d)[::-]", len(finished))).SetSelectable(false))
		for c := range 8 {
			if c != 2 {
				p.SetCell(len(open)+1, c, tview.NewTableCell("").SetSelectable(false))
			}
		}
	}
	for i, s := range ui.playlists {
		on, color := "✓", tcell.ColorGreen
		if !s.Enabled {
			on, color = "·", tcell.ColorGray
		}
		prio := ""
		if s.Priority != 0 {
			prio = fmt.Sprintf("%+d", s.Priority)
		}
		cells := []string{on, prio, tview.Escape(s.Name), fmt.Sprint(s.Tracks), fmt.Sprint(s.Done), fmt.Sprint(s.InLib), fmt.Sprint(s.Todo), fmt.Sprint(s.Failed)}
		for c, text := range cells {
			cell := tview.NewTableCell(text)
			switch c {
			case 0:
				cell.SetTextColor(color)
			case 2:
				cell.SetExpansion(1)
			default:
				cell.SetAlign(tview.AlignRight)
			}
			row := i + 1
			if i >= len(open) {
				row++ // under the heading
			}
			p.SetCell(row, c, cell)
		}
	}
	if r, _ := ui.inputsTable.GetSelection(); r < 1 && len(ui.inputs) > 0 {
		ui.inputsTable.Select(1, 0)
	}
	if r, _ := ui.playlistsTable.GetSelection(); r < 1 && len(ui.playlists) > 0 {
		first := 1
		if ui.playlistsOpen == 0 { // the first row is the heading
			first = 2
		}
		ui.playlistsTable.Select(first, 0)
	}
}

// --- library ----------------------------------------------------------------------------------
