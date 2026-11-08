package tui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The Sort tab: tidy your library and downloads into Artist/Album folders and move duplicates
// aside. Make a plan (reads only), look it over, apply it; applied plans can be undone.

type sortUI struct {
	info  *tview.TextView
	table *tview.Table
	rows  *sortContent
	seen  string // the plan the table shows
	asked bool
}

type sortContent struct {
	tview.TableContentReadOnly
	mode  string // history, moves or dupes
	hist  []core.SortPlanInfo
	moves []core.SortMove
	dupes []sortDupe
	kind  int
	text  string
}

type sortDupe struct {
	song, kept, aside, reason string
}

var sortKinds = []struct{ name, label string }{{"", "all files"}, {"track", "music"}, {"duplicate", "duplicates"}, {"sidecar", "cover art and such"}}

func (c *sortContent) columns() []string {
	switch c.mode {
	case "moves":
		return []string{"Kind", "From", "To", "Size"}
	case "dupes":
		return []string{"Song", "Kept", "Moved aside", "Why"}
	}
	return []string{"Made", "Sorted into", "Files", "State"}
}

func (c *sortContent) GetColumnCount() int { return 4 }

func (c *sortContent) GetRowCount() int {
	switch c.mode {
	case "moves":
		return len(c.moves) + 1
	case "dupes":
		return len(c.dupes) + 1
	}
	return len(c.hist) + 1
}

func (c *sortContent) GetCell(row, col int) *tview.TableCell {
	if row < 0 || col < 0 || col >= 4 || row >= c.GetRowCount() {
		return nil
	}
	if row == 0 {
		return tview.NewTableCell(c.columns()[col]).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow)
	}
	text := ""
	expand := 0
	switch c.mode {
	case "moves":
		m := c.moves[row-1]
		text = []string{m.Kind, m.Src, m.Dst, core.FmtBytes(m.Size)}[col]
		if col == 1 || col == 2 {
			expand = 1
		}
	case "dupes":
		d := c.dupes[row-1]
		text = []string{d.song, d.kept, d.aside, d.reason}[col]
		if col == 0 {
			expand = 1
		}
	default:
		h := c.hist[row-1]
		state := "not applied"
		if h.Partly {
			state = "stopped part-way"
		} else if h.Applied {
			state = "applied"
		}
		text = []string{h.Created, h.Options.Dest, strconv.Itoa(h.Moves), state}[col]
		if col == 1 {
			expand = 1
		}
	}
	return tview.NewTableCell(tview.Escape(text)).SetExpansion(expand).SetMaxWidth(70)
}

func (ui *tui) buildSort() tview.Primitive {
	s := &sortUI{rows: &sortContent{}}
	ui.st = s
	s.info = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	s.info.SetBorder(true).SetTitle(" Sort & merge ")
	s.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetContent(s.rows)
	s.table.SetBorder(true)
	s.table.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.table.GetSelection()
		switch ev.Rune() {
		case 's':
			ui.sortForm()
		case 'a':
			ui.sortConfirmApply()
		case 'x':
			ui.m.SortCancel()
		case 'd':
			ui.m.SortDiscard()
			s.seen = ""
			ui.refreshSort()
		case 'u':
			ui.sortHistoryDo(row, true)
		case 'C':
			ui.sortHistoryDo(row, false)
		case 'f':
			if s.rows.mode == "moves" {
				s.rows.kind = (s.rows.kind + 1) % len(sortKinds)
				ui.sortFillMoves()
			}
		case 'D':
			if s.rows.mode == "moves" {
				s.rows.mode = "dupes"
			} else if s.rows.mode == "dupes" {
				s.rows.mode = "moves"
			}
			s.table.Select(1, 0)
			ui.refreshSort()
		case '/':
			if s.rows.mode == "moves" {
				ui.askText("Filter by path", "Only moves whose path contains this", s.rows.text, func(v string) {
					s.rows.text = v
					ui.sortFillMoves()
				})
			}
		default:
			return ev
		}
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.info, 9, 0, false).
		AddItem(s.table, 0, 1, true)
}

func (ui *tui) sortFillMoves() {
	s := ui.st
	s.rows.moves = ui.m.SortMoves(sortKinds[s.rows.kind].name, s.rows.text)
	s.table.Select(min(1, len(s.rows.moves)), 0)
	ui.refreshSort()
}

// sortForm asks what to sort, and where to.
func (ui *tui) sortForm() {
	o := ui.m.SortDefaults()
	sources := strings.Join(o.Sources, "; ")
	dest, dups := o.Dest, o.DuplicatesDir
	copyFiles, offline, keep, comps := false, false, false, false
	sidecars, content := true, true
	tol := "3"
	f := tview.NewForm()
	f.AddInputField("Folders to sort (; between them)", sources, 0, nil, func(t string) { sources = t })
	f.AddInputField("Sorted library", dest, 0, nil, func(t string) { dest = t })
	f.AddInputField("Duplicates folder", dups, 0, nil, func(t string) { dups = t })
	f.AddCheckbox("Copy instead of move (leaves the originals)", copyFiles, func(v bool) { copyFiles = v })
	f.AddCheckbox("No web lookups for incomplete tags", offline, func(v bool) { offline = v })
	f.AddCheckbox("Move cover art, logs and cue sheets too", sidecars, func(v bool) { sidecars = v })
	f.AddCheckbox("Find byte-identical copies", content, func(v bool) { content = v })
	f.AddCheckbox("Keep the original file names", keep, func(v bool) { keep = v })
	f.AddCheckbox("Compilations under each track's artist", comps, func(v bool) { comps = v })
	f.AddInputField("Same song if lengths differ by at most (s)", tol, 8, nil, func(t string) { tol = t })
	f.AddButton("Make a plan", func() {
		t, err := strconv.ParseFloat(strings.TrimSpace(tol), 64)
		if err != nil || t < 0 {
			ui.message("Sort & merge", "The length difference has to be a number of seconds.")
			return
		}
		ui.closeModal("sortform")
		if err := ui.m.SortPlan(core.SortOptions{Sources: core.SplitBy(sources, ";"), Dest: strings.TrimSpace(dest), DuplicatesDir: strings.TrimSpace(dups), Copy: copyFiles,
			Offline: offline, Tolerance: t, ArtistComps: comps, KeepFilenames: keep, NoSidecars: !sidecars, NoContentCheck: !content}); err != nil {
			ui.message("Sort & merge", err.Error())
		}
		ui.st.seen = ""
		ui.refreshSort()
	})
	f.AddButton("Cancel", func() { ui.closeModal("sortform") })
	f.SetCancelFunc(func() { ui.closeModal("sortform") })
	f.SetBorder(true).SetTitle(" Make a plan: this only reads your files ")
	ui.openModal("sortform", f, 96, 25)
}

func (ui *tui) sortConfirmApply() {
	v := ui.m.SortStatus()
	if v.Plan == nil {
		return
	}
	s := v.Plan.Summary
	total := s.MoveTracks + s.MoveDups + s.MoveSidecars
	if total == 0 {
		ui.message("Sort & merge", "Everything is already where it belongs.")
		return
	}
	verb := "Move"
	if v.Plan.Options.Copy {
		verb = "Copy"
	}
	ui.confirm(fmt.Sprintf("%s %d files into %s? Downloads pause meanwhile. Nothing is deleted or overwritten, and it can be undone.", verb, total, v.Plan.Options.Dest), "Apply", func() {
		if err := ui.m.SortApply(""); err != nil {
			ui.message("Sort & merge", err.Error())
		}
	})
}

func (ui *tui) sortHistoryDo(row int, undo bool) {
	s := ui.st
	if s.rows.mode != "history" || row < 1 || row > len(s.rows.hist) {
		return
	}
	h := s.rows.hist[row-1]
	switch {
	case undo && !h.Applied:
		ui.message("Sort & merge", "That plan wasn't applied.")
	case undo:
		ui.confirm(fmt.Sprintf("Put back every file the plan of %s moved?", h.Created), "Undo", func() {
			if err := ui.m.SortUndo(h.ID); err != nil {
				ui.message("Sort & merge", err.Error())
			}
		})
	default:
		ui.confirm("Carry on with this plan? Files that changed since it was made are left alone.", "Continue", func() {
			if err := ui.m.SortApply(h.ID); err != nil {
				ui.message("Sort & merge", err.Error())
			}
		})
	}
}

func (ui *tui) refreshSort() {
	s := ui.st
	v := ui.m.SortStatus()
	var b strings.Builder
	switch {
	case v.Busy:
		fmt.Fprintf(&b, "[yellow]%s…[-]", v.Stage)
		if v.Total > 0 {
			fmt.Fprintf(&b, "  %d of %d  [aqua]%s[-]", v.Done, v.Total, tviewBar(float64(v.Done)/float64(v.Total), 30))
		} else if v.Done > 0 {
			fmt.Fprintf(&b, "  %d", v.Done)
		}
		b.WriteString("\n\n[::d]x stops[::-]")
	case v.Result != nil:
		r := v.Result
		word := "moved or copied"
		if v.ResultWas == "undo" {
			word = "put back"
		}
		fmt.Fprintf(&b, "[green]%d files %s.[-]", r.Done, word)
		if r.Stopped {
			b.WriteString(" [yellow]Stopped part-way: C on the plan below carries on.[-]")
		}
		if r.ProblemCount > 0 {
			fmt.Fprintf(&b, "\n[yellow]%d files stayed where they were:[-]", r.ProblemCount)
			for i, p := range r.Problems {
				if i < 3 {
					b.WriteString("\n  " + tview.Escape(p))
				}
			}
		}
		if r.ReportPath != "" {
			b.WriteString("\nReport: " + tview.Escape(r.ReportPath))
		}
		b.WriteString("\n\n[::d]d: done[::-]")
	case v.Plan != nil:
		p := v.Plan.Summary
		fmt.Fprintf(&b, "[::b]%d files[::-] (%s) become [::b]%d artists, %d albums[::-] and %d singles.\n", p.Files, core.FmtBytes(p.Bytes), p.Artists, p.Albums, p.Singles)
		fmt.Fprintf(&b, "To move: %d music, %d duplicates, %d cover art and such; %d already in place.\n", p.MoveTracks, p.MoveDups, p.MoveSidecars, p.InPlace)
		fmt.Fprintf(&b, "[::b]%d songs[::-] have extra copies: %d files (%s) go to %s.\n", p.DupSongs, p.DupFiles, core.FmtBytes(p.DupBytes), tview.Escape(v.Plan.Options.DuplicatesDir))
		fmt.Fprintf(&b, "Details from tags %d, names %d, the web %d; unknown %d; unreadable %d.\n", p.FromTags, p.FromNames, p.FromWeb, p.Unknown, p.Unreadable)
		if len(p.Notes) > 0 {
			b.WriteString("[yellow]" + tview.Escape(strings.Join(p.Notes, " ")) + "[-]\n")
		}
		fmt.Fprintf(&b, "Plan and full lists: %s\n[::b]Nothing has changed yet.[::-] a applies · d drops it · D switches moves/duplicates", tview.Escape(v.Plan.Dir))
	default:
		b.WriteString("Tidies your library and downloads into Artist/Album folders and moves duplicate songs aside, with a report of which file replaced which.\n\n")
		b.WriteString("[::b]s[::-] makes a plan: that only reads your files. You look it over, and only applying it moves anything. Nothing is ever deleted or re-encoded, and an applied plan can be undone.")
		if v.Err != "" {
			b.WriteString("\n\n[red]" + tview.Escape(v.Err) + "[-]")
		}
	}
	s.info.SetText(b.String())
	if v.Busy {
		return
	}
	switch {
	case v.Plan != nil && v.Result == nil:
		if s.seen != v.Plan.ID {
			s.seen = v.Plan.ID
			s.rows.mode, s.rows.kind, s.rows.text = "moves", 0, ""
			s.rows.moves = ui.m.SortMoves("", "")
			s.rows.dupes = s.rows.dupes[:0]
			for _, d := range ui.m.SortDuplicates() {
				for _, x := range d.Superseded {
					s.rows.dupes = append(s.rows.dupes, sortDupe{d.Artist + " - " + d.Title, d.Keeper.Quality, x.Quality, x.Reason})
				}
			}
			s.table.Select(min(1, len(s.rows.moves)), 0)
			s.table.ScrollToBeginning()
		}
		if s.rows.mode == "moves" {
			title := fmt.Sprintf(" %d moves: %s (f) ", len(s.rows.moves), sortKinds[s.rows.kind].label)
			if s.rows.text != "" {
				title += "· matching " + tview.Escape(s.rows.text) + " (/) "
			}
			s.table.SetTitle(title)
		} else {
			s.table.SetTitle(fmt.Sprintf(" %d duplicate files (D: back to the moves) ", len(s.rows.dupes)))
		}
	default:
		s.seen = ""
		s.rows.mode = "history"
		s.rows.hist = ui.m.SortHistory()
		s.table.SetTitle(" Earlier plans (u undoes · C carries on with one) ")
	}
}
