package gui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Sort & merge page: tidies music folders into Artist/Album folders and moves duplicates
// aside. Three steps, like the tool it comes from: make a plan (reads only), look it over,
// apply it. Applied plans can be undone.

type sortTab struct {
	a     *app
	w     *qt.QWidget
	stack *qt.QStackedWidget

	// choosing
	sources  *qt.QListWidget
	dest     *qt.QLineEdit
	dups     *qt.QLineEdit
	mode     []*qt.QPushButton
	online   *qt.QCheckBox
	sidecars *qt.QCheckBox
	content  *qt.QCheckBox
	keep     *qt.QCheckBox
	comps    *qt.QCheckBox
	tol      *qt.QLineEdit
	adv      *qt.QWidget
	advB     *qt.QPushButton
	hist     *dataTable
	histRows []core.SortPlanInfo
	histDel  *qt.QPushButton
	histNote *qt.QLabel

	// working
	stage  *qt.QLabel
	detail *qt.QLabel
	why    *qt.QLabel
	skip   *qt.QPushButton
	bar    *qt.QProgressBar

	// the plan
	sumMain, sumDups, sumFrom *qt.QLabel
	notes                     *qt.QLabel
	segs                      []*qt.QPushButton
	view                      *qt.QStackedWidget
	kinds                     *qt.QComboBox
	find                      *qt.QLineEdit
	moves                     *dataTable
	moveRows                  []core.SortMove
	dupes                     *dataTable
	dupRows                   []sortDupRow
	report                    *qt.QPlainTextEdit
	apply                     *qt.QPushButton

	// done
	doneTitle, doneText *qt.QLabel
	doneUndo            *qt.QPushButton
	lastPlan            string
	shownPlan           string
	asked               bool
}

type sortDupRow struct {
	song, kept, aside, reason string
}

const (
	sortPageChoose = iota
	sortPageBusy
	sortPagePlan
	sortPageDone
)

func newSortTab(a *app) *sortTab {
	t := &sortTab{a: a}
	p := a.newPage("Sort & merge", "Tidy your library and downloads into Artist/Album folders, and move duplicate songs aside.", "sort")
	t.w = p.w
	t.stack = qt.NewQStackedWidget2()
	t.stack.AddWidget(t.buildChoose())
	t.stack.AddWidget(t.buildBusy())
	t.stack.AddWidget(t.buildPlan())
	t.stack.AddWidget(t.buildDone())
	p.box.AddWidget2(t.stack.QWidget, 1)
	t.loadDefaults()
	return t
}

func (t *sortTab) buildChoose() *qt.QWidget {
	a := t.a
	scroll, body, _ := scrolling()
	safe, safeText := banner("banner")
	safeText.SetText("<b>Nothing changes until you say so.</b> Making a plan only reads your files. You look it over, and only pressing Apply moves anything. Nothing is ever deleted or re-encoded, unfinished downloads are left where they are, and you can undo it afterwards.")
	body.AddWidget(safe.QWidget)

	what, wb := card()
	wb.AddWidget(text("What to sort", "h2").QWidget)
	wb.AddWidget(text("Your music folders and your downloads folder, merged into one tidy library.", "hint").QWidget)
	t.sources = qt.NewQListWidget2()
	t.sources.SetMaximumHeight(110)
	wb.AddWidget(t.sources.QWidget)
	row := hbox()
	row.SetSpacing(6)
	row.AddWidget(a.iconButton("plus", "Add a folder", "", "", func() {
		if p := qt.QFileDialog_GetExistingDirectory3(a.win.QWidget, "A folder to sort", ""); p != "" {
			t.sources.AddItem(p)
		}
	}).QWidget)
	row.AddWidget(a.iconButton("x", "Remove", "", "", func() {
		if r := t.sources.CurrentRow(); r >= 0 {
			t.sources.TakeItem(r)
		}
	}).QWidget)
	row.AddStretch()
	row.AddWidget(button("Start over", "ghost", t.loadDefaults).QWidget)
	wb.AddLayout(row.QLayout)
	body.AddWidget(what.QWidget)

	where, xb := card()
	xb.AddWidget(text("Where it goes", "h2").QWidget)
	pick := func(label, hint string, e **qt.QLineEdit) {
		*e = qt.NewQLineEdit2()
		edit := *e
		field := qt.NewQWidget2()
		fr := qt.NewQHBoxLayout(field)
		fr.SetContentsMargins(0, 0, 0, 0)
		fr.SetSpacing(6)
		fr.AddWidget2(edit.QWidget, 1)
		fr.AddWidget(button("Browse…", "", func() {
			if p := qt.QFileDialog_GetExistingDirectory3(a.win.QWidget, label, edit.Text()); p != "" {
				edit.SetText(p)
			}
		}).QWidget)
		settingRow(xb, label, hint, field)
	}
	pick("Sorted library", "The tidy result: Artist/Album/01 - Song. It can be one of the folders being sorted.", &t.dest)
	pick("Duplicates", "Worse copies of songs you have go here, sorted the same way, with a report of which file replaced which.", &t.dups)
	seg, btns := segments([]string{"Move files", "Copy files"}, func(int) {})
	t.mode = btns
	btns[0].SetChecked(true)
	settingRow(xb, "How", "Moving is instant on one disk. Copying leaves the originals where they are, and needs as much free space as the music itself.", seg.QWidget)
	body.AddWidget(where.QWidget)

	t.advB = button("▸  More options", "link", func() {
		on := !t.adv.IsVisible()
		t.adv.SetVisible(on)
		if on {
			t.advB.SetText("▾  More options")
		} else {
			t.advB.SetText("▸  More options")
		}
	})
	body.AddWidget(t.advB.QWidget)
	adv, ab := card()
	t.adv = adv.QWidget
	t.online = qt.NewQCheckBox3("Look up files with incomplete tags online (MusicBrainz, Deezer)")
	t.online.SetChecked(true)
	ab.AddWidget(t.online.QWidget)
	t.sidecars = qt.NewQCheckBox3("Move cover art, logs and cue sheets along with the music")
	t.sidecars.SetChecked(true)
	ab.AddWidget(t.sidecars.QWidget)
	t.content = qt.NewQCheckBox3("Find byte-identical copies too (compares files of the same size)")
	t.content.SetChecked(true)
	ab.AddWidget(t.content.QWidget)
	t.keep = qt.NewQCheckBox3("Keep the original file names instead of “01 - Song”")
	ab.AddWidget(t.keep.QWidget)
	t.comps = qt.NewQCheckBox3("File compilations under each track's artist instead of Various Artists")
	ab.AddWidget(t.comps.QWidget)
	t.tol = qt.NewQLineEdit3("3")
	t.tol.SetMaximumWidth(80)
	settingRow(ab, "Same song if lengths differ by at most (seconds)", "A 7-minute extended cut and the 3-minute original are both kept.", t.tol.QWidget)
	t.adv.Hide()
	body.AddWidget(t.adv)

	go_ := hbox()
	go_.AddStretch()
	go_.AddWidget(a.iconButton("search", "Make a plan", "primary", "Reads your files and works out what would happen. Changes nothing.", t.makePlan).QWidget)
	body.AddLayout(go_.QLayout)

	histCard, hb := card()
	hb.AddWidget(text("Earlier plans", "h2").QWidget)
	t.histNote = text("Nothing yet.", "muted")
	hb.AddWidget(t.histNote.QWidget)
	t.hist = a.newTable("sort-history", column{title: "Made", width: 170}, column{title: "Sorted into", width: 340, flex: true},
		column{title: "Files", width: 76, right: true}, column{title: "State", width: 140})
	t.hist.count = func() int { return len(t.histRows) }
	t.hist.key = func(r int) string { return t.histRows[r].ID }
	t.hist.text = func(r, c int) string {
		x := t.histRows[r]
		switch c {
		case 0:
			return x.Created
		case 1:
			return x.Options.Dest
		case 2:
			return fmtCount(x.Moves)
		case 3:
			switch {
			case x.Partly:
				return "Stopped part-way"
			case x.Applied:
				return "Applied"
			}
			return "Not applied"
		}
		return ""
	}
	t.hist.view.SetMinimumHeight(120)
	t.hist.view.SetMaximumHeight(190)
	hb.AddWidget(t.hist.view.QWidget)
	actions := hbox()
	actions.SetSpacing(6)
	actions.AddWidget(a.iconButton("refresh", "Undo this plan", "", "Put every file back where it was", t.undoSelected).QWidget)
	actions.AddWidget(button("Continue applying", "", t.continueSelected).QWidget)
	actions.AddWidget(a.iconButton("folder", "Open its folder", "", "plan.txt, every move, and the duplicates report", func() {
		if r := t.hist.current(); r >= 0 && r < len(t.histRows) {
			openPath(t.histRows[r].Dir)
		}
	}).QWidget)
	actions.AddStretch()
	t.histDel = a.iconButton("trash", "Delete this plan", "danger", "Forget this plan. An applied plan can't be undone afterwards", t.deleteSelected)
	actions.AddWidget(t.histDel.QWidget)
	actions.AddWidget(button("Delete all older", "ghost", t.deleteOlder).QWidget)
	hb.AddLayout(actions.QLayout)
	body.AddWidget(histCard.QWidget)
	body.AddStretch()
	return scroll.QWidget
}

func (t *sortTab) buildBusy() *qt.QWidget {
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.AddStretch()
	f, cb := card()
	f.SetMaximumWidth(560)
	t.stage = text("", "h2")
	t.stage.SetAlignment(qt.AlignCenter)
	cb.AddWidget(t.stage.QWidget)
	t.bar = qt.NewQProgressBar2()
	t.bar.SetTextVisible(false)
	cb.AddWidget(t.bar.QWidget)
	t.detail = text("", "muted")
	t.detail.SetAlignment(qt.AlignCenter)
	cb.AddWidget(t.detail.QWidget)
	t.why = richText("These files' tags don't say which album they're on. MusicBrainz takes one question a second, so this part is slow the first time. Every answer is remembered, so stopping loses nothing. You can skip the rest: those files are then filed from their tags and names alone.", "hint")
	t.why.SetAlignment(qt.AlignCenter)
	t.why.Hide()
	cb.AddWidget(t.why.QWidget)
	row := hbox()
	row.SetSpacing(8)
	row.AddStretch()
	t.skip = button("Skip the lookups", "", func() { t.a.m.SortSkipLookups(); t.refresh() })
	t.skip.Hide()
	row.AddWidget(t.skip.QWidget)
	row.AddWidget(button("Stop", "", func() { t.a.m.SortCancel() }).QWidget)
	row.AddStretch()
	cb.AddLayout(row.QLayout)
	centre := hbox()
	centre.AddStretch()
	centre.AddWidget2(f.QWidget, 3)
	centre.AddStretch()
	box.AddLayout(centre.QLayout)
	box.AddStretch()
	return w
}

func (t *sortTab) buildPlan() *qt.QWidget {
	a := t.a
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(10)
	f, sb := card()
	sb.SetSpacing(4)
	t.sumMain = richText("", "")
	sb.AddWidget(t.sumMain.QWidget)
	t.sumDups = richText("", "")
	sb.AddWidget(t.sumDups.QWidget)
	t.sumFrom = text("", "muted")
	sb.AddWidget(t.sumFrom.QWidget)
	t.notes = text("", "hint")
	t.notes.Hide()
	sb.AddWidget(t.notes.QWidget)
	box.AddWidget(f.QWidget)

	row := hbox()
	row.SetSpacing(8)
	seg, btns := segments([]string{"Files to move", "Duplicates", "Summary"}, func(i int) { t.view.SetCurrentIndex(i) })
	t.segs = btns
	btns[0].SetChecked(true)
	row.AddWidget(seg.QWidget)
	row.AddStretch()
	t.kinds = qt.NewQComboBox2()
	t.kinds.AddItems([]string{"All files", "Music", "Duplicates", "Cover art and such"})
	compactCombo(t.kinds, 12)
	t.kinds.OnActivated(func(int) { t.fillMoves() })
	row.AddWidget(t.kinds.QWidget)
	t.find = qt.NewQLineEdit2()
	t.find.SetPlaceholderText("Filter by path")
	t.find.SetClearButtonEnabled(true)
	t.find.SetFixedWidth(200)
	t.find.OnTextChanged(func(string) { t.fillMoves() })
	row.AddWidget(t.find.QWidget)
	box.AddLayout(row.QLayout)

	t.moves = a.newTable("sort-moves", column{title: "From", width: 420, flex: true}, column{title: "To", width: 420},
		column{title: "Kind", width: 100}, column{title: "Size", width: 104, right: true})
	t.moves.count = func() int { return len(t.moveRows) }
	t.moves.key = func(r int) string { return t.moveRows[r].Src }
	t.moves.text = func(r, c int) string {
		m := t.moveRows[r]
		switch c {
		case 0:
			return m.Src
		case 1:
			return m.Dst
		case 2:
			return map[string]string{"track": "Music", "duplicate": "Duplicate", "sidecar": "Cover etc."}[m.Kind]
		case 3:
			return core.FmtBytes(m.Size)
		}
		return ""
	}
	t.moves.num = func(r, c int) (float64, bool) {
		if c == 3 {
			return float64(t.moveRows[r].Size), true
		}
		return 0, false
	}
	t.dupes = a.newTable("sort-dupes", column{title: "Song", width: 300, flex: true}, column{title: "Kept", width: 150},
		column{title: "Moved aside", width: 170}, column{title: "Why", width: 240})
	t.dupes.count = func() int { return len(t.dupRows) }
	t.dupes.text = func(r, c int) string {
		d := t.dupRows[r]
		return []string{d.song, d.kept, d.aside, d.reason}[c]
	}
	t.report = qt.NewQPlainTextEdit2()
	setProp(t.report.QWidget, "role", "log")
	t.report.SetReadOnly(true)
	t.view = qt.NewQStackedWidget2()
	t.view.AddWidget(t.moves.view.QWidget)
	t.view.AddWidget(t.dupes.view.QWidget)
	t.view.AddWidget(t.report.QWidget)
	box.AddWidget2(t.view.QWidget, 1)

	acts := hbox()
	acts.SetSpacing(8)
	acts.AddWidget(button("Change something", "", func() {
		a.m.SortDiscard()
		t.refresh()
	}).QWidget)
	acts.AddWidget(a.iconButton("folder", "Open the plan's folder", "", "", func() {
		if v := a.m.SortStatus(); v.Plan != nil {
			openPath(v.Plan.Dir)
		}
	}).QWidget)
	acts.AddStretch()
	t.apply = a.iconButton("check", "Apply this plan", "primary", "", t.confirmApply)
	acts.AddWidget(t.apply.QWidget)
	box.AddLayout(acts.QLayout)
	return w
}

func (t *sortTab) buildDone() *qt.QWidget {
	a := t.a
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.AddStretch()
	t.doneTitle = text("", "title")
	t.doneTitle.SetAlignment(qt.AlignCenter)
	box.AddWidget(t.doneTitle.QWidget)
	t.doneText = richText("", "subtitle")
	t.doneText.SetAlignment(qt.AlignCenter)
	box.AddWidget(t.doneText.QWidget)
	box.AddSpacing(10)
	row := hbox()
	row.SetSpacing(8)
	row.AddStretch()
	t.doneUndo = a.iconButton("refresh", "Undo", "", "Put every file back where it was", func() {
		if t.lastPlan != "" && a.confirm("Put everything back?", "Every file goes back where it was, and the folders it left are made again.") {
			t.try(a.m.SortUndo(t.lastPlan))
		}
	})
	row.AddWidget(t.doneUndo.QWidget)
	row.AddWidget(button("Done", "primary", func() {
		a.m.SortDiscard()
		t.refresh()
	}).QWidget)
	row.AddStretch()
	box.AddLayout(row.QLayout)
	box.AddStretch()
	return w
}

func (t *sortTab) try(err error) {
	if err != nil {
		t.a.message("That didn't work", err.Error())
	}
	t.refresh()
}

func (t *sortTab) loadDefaults() {
	o := t.a.m.SortDefaults()
	t.sources.Clear()
	for _, s := range o.Sources {
		t.sources.AddItem(s)
	}
	t.dest.SetText(o.Dest)
	t.dups.SetText(o.DuplicatesDir)
}

func (t *sortTab) makePlan() {
	var sources []string
	for i := range t.sources.Count() {
		sources = append(sources, t.sources.Item(i).Text())
	}
	tol, err := strconv.ParseFloat(strings.TrimSpace(t.tol.Text()), 64)
	if err != nil || tol < 0 {
		t.a.message("That can't be used", "The length difference has to be a number of seconds.")
		return
	}
	t.try(t.a.m.SortPlan(core.SortOptions{Sources: sources, Dest: strings.TrimSpace(t.dest.Text()), DuplicatesDir: strings.TrimSpace(t.dups.Text()),
		Copy: t.mode[1].IsChecked(), Offline: !t.online.IsChecked(), Tolerance: tol, ArtistComps: t.comps.IsChecked(), KeepFilenames: t.keep.IsChecked(),
		NoSidecars: !t.sidecars.IsChecked(), NoContentCheck: !t.content.IsChecked()}))
}

func (t *sortTab) confirmApply() {
	v := t.a.m.SortStatus()
	if v.Plan == nil {
		return
	}
	s := v.Plan.Summary
	verb := "Move"
	if v.Plan.Options.Copy {
		verb = "Copy"
	}
	total := s.MoveTracks + s.MoveDups + s.MoveSidecars
	if total == 0 {
		t.a.message("Nothing to do", "Everything is already where it belongs.")
		return
	}
	if !t.a.confirm(verb+" "+fmtCount(total)+" files?", fmt.Sprintf(
		"Music goes to %s, and %s duplicates to %s.\n\nDownloads pause while this runs. Nothing is deleted, nothing is overwritten, and you can undo it afterwards.",
		v.Plan.Options.Dest, fmtCount(s.MoveDups), v.Plan.Options.DuplicatesDir)) {
		return
	}
	t.lastPlan = v.Plan.ID
	t.try(t.a.m.SortApply(""))
}

func (t *sortTab) undoSelected() {
	r := t.hist.current()
	if r < 0 || r >= len(t.histRows) {
		t.a.message("Pick a plan", "Select the plan to undo in the list first.")
		return
	}
	if !t.histRows[r].Applied {
		t.a.message("Nothing to undo", "That plan wasn't applied.")
		return
	}
	if t.a.confirm("Put everything back?", "Every file that plan moved goes back where it was.") {
		t.lastPlan = t.histRows[r].ID
		t.try(t.a.m.SortUndo(t.histRows[r].ID))
	}
}

func (t *sortTab) deleteSelected() {
	r := t.hist.current()
	if r < 0 || r >= len(t.histRows) {
		t.a.message("Choose a plan", "Pick the plan to delete in the list first.")
		return
	}
	x := t.histRows[r]
	body := "The plan and its reports go. Your music files aren't touched."
	if x.Applied {
		body += "\n\nThis plan was applied: without it, what it moved can't be put back by the app."
	}
	if t.a.confirm("Delete the plan of "+x.Created+"?", body) {
		t.removePlans([]string{x.ID})
	}
}

// deleteOlder forgets every plan but the newest.
func (t *sortTab) deleteOlder() {
	if len(t.histRows) < 2 {
		t.a.message("Nothing older", "There's only one plan, or none.")
		return
	}
	var ids []string
	applied := 0
	for _, x := range t.histRows[1:] {
		ids = append(ids, x.ID)
		if x.Applied {
			applied++
		}
	}
	body := fmt.Sprintf("%d older plans go; the newest stays. Your music files aren't touched.", len(ids))
	if applied > 0 {
		body += fmt.Sprintf("\n\n%d of them were applied: what those moved can't be put back by the app afterwards.", applied)
	}
	if t.a.confirm("Delete the older plans?", body) {
		t.removePlans(ids)
	}
}

func (t *sortTab) removePlans(ids []string) {
	a := t.a
	go func() {
		n, err := a.m.SortDeletePlans(ids)
		mainDo(func() {
			if err != nil {
				a.message("That didn't work", err.Error())
			} else {
				a.toast(fmt.Sprintf("Deleted %d plans.", n))
			}
			t.refresh()
		})
	}()
}

func (t *sortTab) continueSelected() {
	r := t.hist.current()
	if r < 0 || r >= len(t.histRows) {
		t.a.message("Pick a plan", "Select the plan to continue in the list first.")
		return
	}
	if t.a.confirm("Carry on with this plan?", "Files that changed since it was made are left alone.") {
		t.lastPlan = t.histRows[r].ID
		t.try(t.a.m.SortApply(t.histRows[r].ID))
	}
}

func (t *sortTab) fillMoves() {
	kind := []string{"", "track", "duplicate", "sidecar"}[max(0, t.kinds.CurrentIndex())]
	t.moveRows = t.a.m.SortMoves(kind, t.find.Text())
	t.moves.reload()
}

// refresh shows the step Sort & merge is at.
func (t *sortTab) refresh() {
	v := t.a.m.SortStatus()
	switch {
	case v.Busy:
		t.stack.SetCurrentIndex(sortPageBusy)
		t.stage.SetText(v.Stage + "…")
		if v.Total > 0 {
			t.bar.SetRange(0, v.Total)
			t.bar.SetValue(v.Done)
		} else {
			t.bar.SetRange(0, 0) // keeps moving: how long isn't known yet
		}
		t.detail.SetText(sortProgressText(v))
		t.why.SetVisible(v.CanSkip)
		t.skip.SetVisible(v.CanSkip)
	case v.Result != nil:
		t.showResult(v)
	case v.Plan != nil:
		t.showPlan(v.Plan)
		t.stack.SetCurrentIndex(sortPagePlan)
	default:
		t.shownPlan = ""
		t.stack.SetCurrentIndex(sortPageChoose)
		t.histRows = t.a.m.SortHistory()
		t.hist.reload()
		t.histNote.SetVisible(len(t.histRows) == 0)
		t.hist.view.SetVisible(len(t.histRows) > 0)
		if v.Err != "" {
			t.a.message("Sort & merge", v.Err)
			t.a.m.SortDiscard()
		}
	}
}

// sortProgressText says how far along a stage is: "312 of 1,204 · about 14m left".
func sortProgressText(v core.SortView) string {
	switch {
	case v.Total > 0 && v.Left > 0:
		return fmt.Sprintf("%s of %s · about %s left", fmtCount(v.Done), fmtCount(v.Total), core.FmtWait(v.Left))
	case v.Total > 0:
		return fmtCount(v.Done) + " of " + fmtCount(v.Total)
	case v.Done > 0:
		return fmtCount(v.Done) + " so far"
	}
	return ""
}

func (t *sortTab) showPlan(p *core.SortPlanView) {
	if t.shownPlan == p.ID {
		return
	}
	t.shownPlan = p.ID
	t.lastPlan = p.ID
	s := p.Summary
	verb := "moved"
	if p.Options.Copy {
		verb = "copied"
	}
	many := func(n int, word string) string {
		if n == 1 {
			return fmtCount(n) + " " + word
		}
		return fmtCount(n) + " " + word + "s"
	}
	t.sumMain.SetText(fmt.Sprintf("<b>%s</b> (%s) become <b>%s</b>, <b>%s</b> and %s. %s will be %s, %s already in place.",
		many(s.Files, "file"), core.FmtBytes(s.Bytes), many(s.Artists, "artist"), many(s.Albums, "album"), many(s.Singles, "single"),
		fmtCount(s.MoveTracks+s.MoveSidecars), verb, fmtCount(s.InPlace)))
	t.sumDups.SetText(fmt.Sprintf("<b>%s</b> %s extra copies: %s (%s) %s to the duplicates folder.", many(s.DupSongs, "song"), map[bool]string{true: "has", false: "have"}[s.DupSongs == 1],
		many(s.DupFiles, "file"), core.FmtBytes(s.DupBytes), map[bool]string{true: "goes", false: "go"}[s.DupFiles == 1]))
	t.sumFrom.SetText(fmt.Sprintf("Details came from tags for %s, file and folder names for %s, the web for %s; %s unknown, %s unreadable.",
		fmtCount(s.FromTags), fmtCount(s.FromNames), fmtCount(s.FromWeb), fmtCount(s.Unknown), fmtCount(s.Unreadable)))
	t.notes.SetText(strings.Join(s.Notes, " "))
	t.notes.SetVisible(len(s.Notes) > 0)
	t.report.SetPlainText(p.Text)
	t.fillMoves()
	t.dupRows = t.dupRows[:0]
	for _, d := range t.a.m.SortDuplicates() {
		for _, x := range d.Superseded {
			t.dupRows = append(t.dupRows, sortDupRow{d.Artist + " - " + d.Title, d.Keeper.Quality, x.Quality, x.Reason})
		}
	}
	t.dupes.reload()
}

func (t *sortTab) showResult(v core.SortView) {
	r := v.Result
	t.stack.SetCurrentIndex(sortPageDone)
	word := "moved or copied"
	if v.ResultWas == "undo" {
		word = "put back"
	}
	switch {
	case r.Stopped:
		t.doneTitle.SetText("Stopped part-way")
	case r.ProblemCount > 0:
		t.doneTitle.SetText("Done, with some files left alone")
	default:
		t.doneTitle.SetText("All done")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s files %s.", fmtCount(r.Done), word)
	if r.Stopped {
		b.WriteString(" You can carry on from Earlier plans.")
	}
	if r.ProblemCount > 0 {
		fmt.Fprintf(&b, "<br>%s files couldn't be moved and stayed where they were:<br>", fmtCount(r.ProblemCount))
		for _, p := range r.Problems {
			b.WriteString(htmlEscape(p) + "<br>")
		}
	}
	if r.ReportPath != "" {
		b.WriteString("<br>The report of which file replaced which is in " + htmlEscape(r.ReportPath))
	}
	t.doneText.SetText(b.String())
	t.doneUndo.SetVisible(v.ResultWas == "apply")
}
