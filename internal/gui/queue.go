package gui

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Downloads page: every track, with what's happening to it. The list reads its rows from
// a slice kept here and draws only what's visible, so even 70,000 tracks are instant. Several
// songs can be selected at once (Shift, Ctrl, Ctrl+A); the actions work on all of them.

// The filters with a button of their own; the rest are under "More".
var mainFilters = []string{"all", "active", "downloading", "searching", "waiting", "queued", "done", "library", "failed"}

var chipWords = map[string]string{"all": "All", "active": "In progress", "downloading": "Downloading", "searching": "Searching",
	"waiting": "Waiting", "queued": "Queued", "done": "Done", "library": "In library", "failed": "Not found"}

var chipTips = map[string]string{"all": "Every song", "active": "Everything being worked on: searching, waiting and downloading together",
	"downloading": "Data is coming in for these right now", "searching": "Being looked for right now",
	"waiting": "Found, and waiting for a free slot, a place in someone's queue, peers, or a retry", "queued": "Their turn hasn't come yet",
	"done": "Downloaded to your downloads folder", "library": "Already in your library", "failed": "No source had them"}

type queueTab struct {
	a    *app
	w    *qt.QWidget
	page *page
	list *rowList

	chips    map[string]*qt.QPushButton
	more     *qt.QComboBox
	moreKeys []string
	sort     *qt.QComboBox
	reverse  *qt.QPushButton
	find     *qt.QLineEdit
	retry    *qt.QPushButton
	progress *qt.QProgressBar
	notice   *qt.QFrame
	noticeT  *qt.QLabel
	stack    *qt.QStackedWidget
	empty    *emptyState
	nomatch  *emptyState

	// the details pane
	pane                   *qt.QFrame
	dTitle, dArtist, dBody *qt.QLabel
	dState                 *qt.QLabel
	dNext, dAgain, dCancel *qt.QPushButton
	dOpen, dOther          *qt.QPushButton
	paneKey                string   // the song shown…
	paneKeys               []string // …or the songs, when several are selected

	q     core.QueueQuery
	keys  []string             // the tracks shown, in order
	views map[int]core.JobView // the rows drawn lately; the others aren't read at all

	loading, again, wantReset bool
	moreSources               []string // the "came from" filters offered under More
	reread                    *throttle
}

func newQueueTab(a *app) *queueTab {
	t := &queueTab{a: a, chips: map[string]*qt.QPushButton{}}
	p := a.newPage("Downloads", "", "downloads")
	t.page, t.w = p, p.w

	t.find = qt.NewQLineEdit2()
	t.find.SetPlaceholderText("Filter by name")
	t.find.SetToolTip("Show only songs with this artist, title or album (/)")
	t.find.SetClearButtonEnabled(true)
	t.find.SetFixedWidth(150)
	t.find.OnTextChanged(func(s string) { t.q.Text = strings.TrimSpace(s); t.refresh(true) })
	p.actions.AddWidget(t.find.QWidget)
	t.sort = qt.NewQComboBox2()
	t.sort.SetToolTip("The order of the list (O)")
	compactCombo(t.sort, 12)
	for _, s := range core.QueueSorts {
		t.sort.AddItem(sortWords[s.Name])
	}
	t.sort.OnCurrentIndexChanged(func(i int) {
		if i >= 0 && i < len(core.QueueSorts) {
			t.q.Sort = core.QueueSorts[i].Name
			t.refresh(true)
		}
	})
	p.actions.AddWidget(t.sort.QWidget)
	t.reverse = a.iconButton("sort", "", "ghost", "Reverse the order (Shift+O)", func() {
		t.q.Reverse = !t.q.Reverse
		t.reverse.SetChecked(t.q.Reverse)
		t.refresh(true)
	})
	t.reverse.SetCheckable(true)
	p.actions.AddWidget(t.reverse.QWidget)
	t.retry = a.iconButton("refresh", "Try again", "", "Search again for every song that wasn't found (Shift+R)", t.retryAll)
	t.retry.Hide()
	p.actions.AddWidget(t.retry.QWidget)
	p.actions.AddWidget(a.iconButton("folder", "", "ghost", "Open your downloads folder", func() { openPath(a.m.Config().OutputDir) }).QWidget)

	t.progress = qt.NewQProgressBar2()
	t.progress.SetTextVisible(false)
	t.progress.SetRange(0, 1000)
	p.box.AddWidget(t.progress.QWidget)

	t.notice = qt.NewQFrame2()
	setProp(t.notice.QWidget, "role", "banner")
	nb := qt.NewQHBoxLayout(t.notice.QWidget)
	nb.SetContentsMargins(14, 7, 8, 7)
	t.noticeT = text("", "")
	nb.AddWidget2(t.noticeT.QWidget, 1)
	nb.AddWidget(button("Show everything", "link", t.clearFilters).QWidget)
	t.notice.Hide()
	p.box.AddWidget(t.notice.QWidget)

	// the filters, and everything else that narrows or orders the list
	bar, done := hbox(), hbox() // what's under way, and what's over with
	bar.SetSpacing(6)
	done.SetSpacing(6)
	group := qt.NewQButtonGroup2(t.w.QObject)
	group.SetExclusive(true)
	for _, name := range mainFilters {
		name := name
		b := button(chipWords[name], "chip", func() { t.setFilter(name) })
		b.SetCheckable(true)
		b.SetSizePolicy2(qt.QSizePolicy__Fixed, qt.QSizePolicy__Fixed)
		group.AddButton(b.QAbstractButton)
		t.chips[name] = b
		if name == "done" || name == "library" || name == "failed" {
			done.AddWidget(b.QWidget)
		} else {
			bar.AddWidget(b.QWidget)
		}
	}
	t.chips["all"].SetChecked(true)
	t.more = qt.NewQComboBox2()
	t.more.SetToolTip("More ways to narrow the list (F)")
	compactCombo(t.more, 4)
	t.more.AddItem("More…")
	t.moreKeys = []string{""}
	for _, f := range core.QueueFilters {
		if !core.ContainsString(mainFilters, f.Name) {
			t.more.AddItem(filterWords[f.Name])
			t.moreKeys = append(t.moreKeys, f.Name)
		}
	}
	t.more.OnActivated(func(i int) {
		if i <= 0 {
			return
		}
		switch {
		case i < len(t.moreKeys):
			t.q.Filter, t.q.Source = t.moreKeys[i], ""
		case i-len(t.moreKeys) < len(t.moreSources):
			t.q.Filter, t.q.Source = "done", t.moreSources[i-len(t.moreKeys)]
		default:
			return
		}
		group.SetExclusive(false)
		for _, b := range t.chips {
			b.SetChecked(false)
		}
		group.SetExclusive(true)
		t.refresh(true)
	})
	done.AddWidget(t.more.QWidget)
	done.AddStretch()
	bar.AddStretch()
	p.box.AddLayout(bar.QLayout)
	p.box.AddLayout(done.QLayout)

	t.views = map[int]core.JobView{}
	t.reread = newThrottle(t.w.QObject, 1000, func() { t.refresh(false) })
	t.list = newRowList(queueRowHeight, func() int { return len(t.keys) }, t.draw)
	t.list.multi()
	t.list.onSelect(func(int) { t.showPane() })
	t.list.onActivate(t.showPane)
	t.list.onMenu(t.menu)
	t.buildEmpty()

	t.stack = qt.NewQStackedWidget2()
	t.stack.AddWidget(t.list.view.QWidget)
	t.stack.AddWidget(t.empty.w)
	t.stack.AddWidget(t.nomatch.w)

	main := hbox()
	main.SetSpacing(14)
	main.AddWidget2(t.stack.QWidget, 1)
	main.AddWidget(t.buildPane())
	p.box.AddLayout2(main.QLayout, 1)

	keys := map[string]func(){
		"Return": t.showPane, "C": func() { t.act("cancel") }, "R": func() { t.act("restart") },
		"N": func() { t.act("next") }, "P": func() { t.act("next") }, "Shift+R": t.retryAll, "/": func() { t.find.SetFocus() },
		"A": t.otherNames,
		"F": func() { t.more.ShowPopup() }, "O": func() { t.sort.ShowPopup() },
		"Shift+O": func() { t.reverse.Click() }, "Escape": t.clearFilters,
	}
	for k, f := range keys {
		sc := qt.NewQShortcut2(qt.NewQKeySequence2(k), t.list.view.QObject)
		sc.SetContext(qt.WidgetShortcut)
		sc.OnActivated(f)
	}
	t.refresh(true)
	return t
}

func (t *queueTab) buildEmpty() {
	a := t.a
	e := a.newEmpty("music", "Nothing to download yet", "Search for a song, album or artist, or bring in your playlists. Whatever you add shows up here while it downloads.")
	e.row.AddWidget(a.iconButton("search", "Find music", "primary", "", func() { a.show(pageFind) }).QWidget)
	e.row.AddWidget(a.iconButton("playlist", "Add playlists", "", "", func() { a.show(pagePlaylists) }).QWidget)
	e.done()
	t.empty = e
	n := a.newEmpty("search", "Nothing matches", "No songs here fit what you've chosen.")
	n.row.AddWidget(button("Show everything", "", t.clearFilters).QWidget)
	n.done()
	t.nomatch = n
}

func (t *queueTab) buildPane() *qt.QWidget {
	a := t.a
	f := qt.NewQFrame2()
	setProp(f.QWidget, "role", "card")
	f.SetFixedWidth(320)
	box := qt.NewQVBoxLayout(f.QWidget)
	box.SetContentsMargins(16, 12, 12, 14)
	box.SetSpacing(8)
	head := hbox()
	state := qt.NewQLabel3("")
	setProp(state.QWidget, "role", "pill")
	setProp(state.QWidget, "pill", "off")
	state.SetSizePolicy2(qt.QSizePolicy__Maximum, qt.QSizePolicy__Fixed)
	head.AddWidget(state.QWidget)
	head.AddStretch()
	head.AddWidget(a.iconButton("x", "", "ghost", "Close (Esc)", t.hidePane).QWidget)
	box.AddLayout(head.QLayout)
	t.dState = state
	t.dTitle = text("", "h1")
	box.AddWidget(t.dTitle.QWidget)
	t.dArtist = text("", "subtitle")
	box.AddWidget(t.dArtist.QWidget)

	row := qt.NewQGridLayout2()
	row.SetSpacing(6)
	t.dNext = a.iconButton("arrow-up", "Do next", "", "Move to the front of the line (N)", func() { t.act("next") })
	t.dAgain = a.iconButton("refresh", "Try again", "", "Search from the first source again; a finished song is downloaded again (R)", func() { t.act("restart") })
	t.dCancel = a.iconButton("x", "Cancel", "danger", "Stop working on this song (C)", func() { t.act("cancel") })
	t.dOpen = a.iconButton("folder", "Show in folder", "", "Open the folder with this file", t.openFile)
	row.AddWidget2(t.dNext.QWidget, 0, 0)
	row.AddWidget2(t.dAgain.QWidget, 0, 1)
	row.AddWidget2(t.dCancel.QWidget, 1, 0)
	row.AddWidget2(t.dOpen.QWidget, 1, 1)
	box.AddLayout(row.QLayout)
	t.dOther = a.iconButton("search", "Find under other names…", "", "Search for it as another artist or title: the artist in another alphabet, the real artist of a “Various Artists” listing, … (A)", t.otherNames)
	box.AddWidget(t.dOther.QWidget)
	box.AddWidget(rule().QWidget)

	t.dBody = richText("", "hint")
	t.dBody.SetAlignment(qt.AlignTop | qt.AlignLeft)
	t.dBody.SetTextInteractionFlags(qt.TextBrowserInteraction)
	scroll, inner, _ := scrolling()
	inner.SetContentsMargins(0, 0, 4, 0)
	inner.AddWidget(t.dBody.QWidget)
	inner.AddStretch()
	box.AddWidget2(scroll.QWidget, 1)
	t.pane = f
	f.Hide()
	return f.QWidget
}

// --- drawing a row ------------------------------------------------------------------------------

const queueRowHeight = 58

// viewAt is a row's track, read when it's first drawn.
func (t *queueTab) viewAt(row int) (core.JobView, bool) {
	if v, ok := t.views[row]; ok {
		return v, true
	}
	if row < 0 || row >= len(t.keys) {
		return core.JobView{}, false
	}
	v, ok := t.a.m.JobView(t.keys[row])
	if ok {
		if len(t.views) > 200 {
			clear(t.views)
		}
		t.views[row] = v
	}
	return v, ok
}

func (t *queueTab) draw(c *rowCtx) {
	v, ok := t.viewAt(c.row)
	if !ok {
		return
	}
	f := drawFonts
	p := c.p
	icon, color, soft := stateVisual(v)
	drawBadge(p, c.x+10, c.y+(c.h-32)/2, 32, icon, color, soft)
	right := min(270, c.w/3)
	x := c.x + 54
	w := c.w - 54 - right - 22
	tr := v.Track
	drawText(p, x, c.y+9, w, 20, qt.AlignLeft|qt.AlignVCenter, tr.Title, f.bold, f.mBold, th.Text)
	sub := tr.ArtistStr()
	if tr.Album != "" {
		sub += "  ·  " + tr.Album
	}
	drawText(p, x, c.y+30, w, 18, qt.AlignLeft|qt.AlignVCenter, sub, f.small, f.mSmall, th.Text2)

	rx := c.x + c.w - right - 14
	main, detail := statusLines(v)
	drawText(p, rx, c.y+9, right, 20, qt.AlignRight|qt.AlignVCenter, main, f.normal, f.mNormal, statusColor(v))
	if v.State == core.JobDownloading && v.Task.Total > 0 {
		drawBar(p, rx+right-150, c.y+35, 150, 5, float64(v.Task.Done)/float64(v.Task.Total), th.Accent)
	} else if detail != "" {
		drawText(p, rx, c.y+30, right, 18, qt.AlignRight|qt.AlignVCenter, detail, f.small, f.mSmall, th.Text3)
	}
	if c.row < len(t.keys)-1 && !c.selected {
		p.SetPen(col(th.Line))
		p.DrawLine2(c.x+54, c.y+c.h-1, c.x+c.w-14, c.y+c.h-1)
	}
}

// stateVisual is a track's state as an icon and colors.
func stateVisual(v core.JobView) (icon, color, soft string) {
	if v.State == core.JobDownloading && v.Task.Waiting {
		return "clock", th.Warn, th.WarnSoft
	}
	switch v.State {
	case core.JobDownloading:
		return "download", th.Accent, th.AccentSoft
	case core.JobSearching, core.JobReady:
		return "search", th.Info, th.InfoSoft
	case core.JobWaiting:
		return "clock", th.Warn, th.WarnSoft
	case core.JobDone:
		return "check", th.OK, th.OKSoft
	case core.JobInLibrary:
		return "library", th.Info, th.InfoSoft
	case core.JobFailed:
		return "x", th.Bad, th.BadSoft
	case core.JobCancelled:
		return "ban", th.Text3, th.Raised
	}
	if v.Bumped {
		return "arrow-up", th.Accent, th.AccentSoft
	}
	return "dot", th.Text3, th.Raised
}

func statusColor(v core.JobView) string {
	if v.State == core.JobDownloading && v.Task.Waiting {
		return th.Warn
	}
	switch v.State {
	case core.JobDownloading:
		return th.Accent
	case core.JobDone:
		return th.OK
	case core.JobFailed:
		return th.Bad
	case core.JobWaiting:
		return th.Warn
	}
	return th.Text2
}

// statusLines are what a row says about a track, in two lines.
func statusLines(v core.JobView) (string, string) {
	switch v.State {
	case core.JobSearching:
		return core.FirstNonEmpty(capitalized(v.Task.Stage), "Searching…"), v.Task.Note
	case core.JobReady:
		return "Starting…", ""
	case core.JobDownloading:
		t := v.Task
		if t.Total > 0 {
			s := fmt.Sprintf("%.0f%%  ·  %s", 100*float64(t.Done)/float64(t.Total), core.FmtBytes(t.Total))
			if t.Speed > 0 {
				s += "  ·  " + core.FmtRate(t.Speed)
			}
			return s, ""
		}
		return core.FirstNonEmpty(capitalized(t.Stage), "Downloading…"), t.Note
	case core.JobDone:
		detail := v.Rec.Quality.String() + "  ·  " + core.FirstNonEmpty(core.SourceLabels[v.Rec.Source], v.Rec.Source)
		if v.Rec.LibPath != "" {
			return "Downloaded, better than your copy", detail
		}
		return "Downloaded", detail
	case core.JobInLibrary:
		if v.Lib != nil {
			return "In your library", v.Lib.Q.String()
		}
		return "In your library", ""
	case core.JobWaiting:
		return "Will retry at " + v.RetryAt.Format("15:04"), v.Reason
	case core.JobFailed:
		if n := len(v.Reasons); n > 1 {
			return "Not found", fmt.Sprintf("%d sources tried", n)
		}
		return "Not found", v.Reason
	case core.JobCancelled:
		return "Cancelled", ""
	case core.JobQueued:
		switch {
		case v.NeedBetter && v.Lib != nil:
			return "Queued", "only if better than your " + v.Lib.Q.String()
		case v.Bumped:
			return "Up next", ""
		}
		return "Queued", ""
	}
	return capitalized(v.State.String()), ""
}

func capitalized(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// --- the list ------------------------------------------------------------------------------------

func (t *queueTab) focus() { t.list.view.SetFocus() }

// setFilterFromOutside shows one filter's tracks, as the Dashboard's rows ask for.
func (t *queueTab) setFilterFromOutside(name string) {
	if b := t.chips[name]; b != nil {
		b.SetChecked(true)
		t.setFilter(name)
		return
	}
	for i, key := range t.moreKeys {
		if key == name {
			t.more.SetCurrentIndex(i)
			t.q.Filter, t.q.Source = name, ""
			t.chips["all"].SetChecked(false)
			t.refresh(true)
			return
		}
	}
}

func (t *queueTab) setFilter(name string) {
	t.q.Filter, t.q.Source = name, ""
	if name == "all" {
		t.q.Filter = ""
	}
	t.more.SetCurrentIndex(0)
	t.refresh(true)
}

func (t *queueTab) clearFilters() {
	if t.pane.IsVisible() {
		t.hidePane()
		return
	}
	if t.q.Playlist == "" && t.q.Text == "" && t.q.Filter == "" && t.q.Source == "" {
		return
	}
	t.q.Playlist = ""
	t.find.SetText("")
	t.chips["all"].SetChecked(true)
	t.setFilter("all")
}

// changed reads the list again when the queue changed, at most once a second, and only if the
// page is on screen: with a big queue that's work, and showing the page reads it anyway.
func (t *queueTab) changed() {
	if t.a.current() == pageDownloads && t.a.win.IsVisible() {
		t.reread.poke()
	}
}

// refresh reads the list again, away from the window's thread so it never stutters; reset: the
// filter or order changed (the list goes back to the top).
func (t *queueTab) refresh(reset bool) {
	t.wantReset = t.wantReset || reset
	if t.loading {
		t.again = true
		return
	}
	t.loading = true
	q := t.q
	go func() {
		keys, counts := t.a.m.QueueKeys(q)
		mainthread.Start(func() { t.loaded(keys, counts) })
	}()
}

func (t *queueTab) loaded(keys []string, counts map[string]int) {
	t.loading = false
	if t.again { // out of date already: what you asked for meanwhile comes first
		t.again = false
		t.refresh(false)
		return
	}
	sel := t.selected()
	many := t.selectedKeys()
	clear(t.views)
	if t.wantReset || len(keys) != len(t.keys) {
		bar := t.list.view.VerticalScrollBar().Value()
		t.list.reset(func() { t.keys = keys })
		if !t.wantReset {
			t.list.view.VerticalScrollBar().SetValue(bar)
		}
		t.wantReset = false
		t.reselect(sel, many)
	} else {
		old := t.keys
		t.keys = keys
		t.list.redraw()
		moved := false
		for _, r := range t.list.selectedRows() {
			if r >= len(keys) || old[r] != keys[r] {
				moved = true
				break
			}
		}
		if moved {
			t.reselect(sel, many)
		}
	}
	for name, b := range t.chips {
		b.SetText(chipWords[name] + " " + compactCount(counts[name]))
		b.SetToolTip(chipTips[name] + ": " + fmtCount(counts[name]) + " songs")
	}
	t.updateMore(counts)
	t.retry.SetVisible(counts["failed"] > 0)
	if t.q.Playlist != "" {
		t.noticeT.SetText(fmt.Sprintf("Showing the playlist “%s”: %d songs", t.q.Playlist, len(t.keys)))
		t.notice.Show()
	} else {
		t.notice.Hide()
	}
	switch {
	case len(t.keys) > 0:
		t.stack.SetCurrentIndex(0)
	case counts["all"] == 0:
		t.stack.SetCurrentIndex(1)
	default:
		t.stack.SetCurrentIndex(2)
	}
	if t.pane.IsVisible() {
		t.fillPane()
	}
}

// reselect finds the tracks that were selected (key the current one), after the list changed
// under them.
func (t *queueTab) reselect(key string, keys []string) {
	if len(keys) <= 1 {
		if i := slices.Index(t.keys, key); key != "" && i >= 0 {
			t.list.selectRow(i)
		}
		return
	}
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	var rows []int
	cur := -1
	for i, k := range t.keys {
		if want[k] {
			rows = append(rows, i)
		}
		if k == key {
			cur = i
		}
	}
	t.list.selectRows(rows, cur)
}

// updateMore keeps the numbers under More, and offers each source tracks came from.
func (t *queueTab) updateMore(counts map[string]int) {
	var sources []string
	for k := range counts {
		if name, ok := strings.CutPrefix(k, "from:"); ok {
			sources = append(sources, name)
		}
	}
	slices.SortFunc(sources, func(a, b string) int { return counts["from:"+b] - counts["from:"+a] })
	if !slices.Equal(sources, t.moreSources) {
		t.moreSources = sources
		for t.more.Count() > len(t.moreKeys) {
			t.more.RemoveItem(t.more.Count() - 1)
		}
		for _, name := range sources {
			t.more.AddItem("Came from " + core.FirstNonEmpty(core.SourceLabels[name], name))
		}
	}
	for i, key := range t.moreKeys {
		if i > 0 {
			t.more.SetItemText(i, fmt.Sprintf("%s (%d)", filterWords[key], counts[key]))
		}
	}
	for i, name := range sources {
		t.more.SetItemText(len(t.moreKeys)+i, fmt.Sprintf("Came from %s (%d)", core.FirstNonEmpty(core.SourceLabels[name], name), counts["from:"+name]))
	}
}

// tick redraws the page's numbers and the visible tracks in progress.
func (t *queueTab) tick(o core.Overview) {
	c := o.Counts
	done := c[core.JobDone] + c[core.JobInLibrary]
	if o.Total > 0 {
		t.progress.SetValue(done * 1000 / o.Total)
	}
	t.progress.SetVisible(o.Total > 0)
	var parts []string
	switch {
	case o.Total == 0:
		parts = append(parts, "Your songs show up here while they download.")
	default:
		parts = append(parts, fmt.Sprintf("%d of %d songs are ready", done, o.Total))
		if c[core.JobFailed] > 0 {
			parts = append(parts, fmt.Sprintf("%d not found", c[core.JobFailed]))
		}
		if o.Transferring > 0 {
			parts = append(parts, fmt.Sprintf("%d downloading", o.Transferring))
		}
		if o.SrUsed > 0 {
			parts = append(parts, fmt.Sprintf("%d being searched for", o.SrUsed))
		}
		if o.ToCheck > 0 && o.Checked < o.ToCheck {
			parts = append(parts, fmt.Sprintf("checking your library, %d of %d", o.Checked, o.ToCheck))
		}
	}
	t.page.sub.SetText(strings.Join(parts, "  ·  "))
	if t.a.current() != pageDownloads || len(t.keys) == 0 {
		return
	}
	first, last := t.visible()
	moved := false
	for i := first; i <= last; i++ {
		if v, ok := t.viewAt(i); ok && v.State.Active() {
			delete(t.views, i) // read again for this drawing
			moved = true
		}
	}
	if moved {
		t.list.redraw()
	}
	if t.pane.IsVisible() && t.paneKey != "" {
		if v, ok := t.a.m.JobView(t.paneKey); ok && v.State.Active() {
			t.fillPane()
		}
	}
}

// visible is the range of rows on screen.
func (t *queueTab) visible() (int, int) {
	v := t.list.view
	first := max(0, v.VerticalScrollBar().Value()/queueRowHeight)
	last := first + v.Viewport().Height()/queueRowHeight + 1
	return first, min(last, len(t.keys)-1)
}

func (t *queueTab) selected() string {
	if r := t.list.current(); r >= 0 && r < len(t.keys) {
		return t.keys[r]
	}
	return ""
}

// selectedKeys are the songs selected, in the list's order.
func (t *queueTab) selectedKeys() []string {
	rows := t.list.selectedRows()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r < len(t.keys) {
			out = append(out, t.keys[r])
		}
	}
	return out
}

// act does what to every song selected.
func (t *queueTab) act(what string) {
	keys := t.selectedKeys()
	if len(keys) == 0 {
		return
	}
	switch what {
	case "cancel":
		for _, k := range keys {
			t.a.m.Cancel(k)
		}
	case "restart":
		for _, k := range keys {
			t.a.m.Restart(k)
		}
	case "next": // the last one moved up goes first: backwards keeps their order
		for i := len(keys) - 1; i >= 0; i-- {
			t.a.m.Prioritize(keys[i])
		}
	}
	if len(keys) > 1 {
		verb := map[string]string{"cancel": "cancelled", "restart": "searched for again", "next": "moved to the front"}[what]
		t.a.toast(fmt.Sprintf("%d songs %s.", len(keys), verb))
	}
	t.refresh(false)
	t.fillPane()
}

func (t *queueTab) retryAll() {
	n := t.a.m.RetryFailed(false)
	t.a.toast(fmt.Sprintf("%d songs are being searched for again.", n))
	t.refresh(false)
}

func (t *queueTab) menu(at *qt.QPoint) {
	key := t.selected()
	if key == "" {
		return
	}
	v, ok := t.a.m.JobView(key)
	if !ok {
		return
	}
	m := qt.NewQMenu2()
	if n := len(t.selectedKeys()); n > 1 {
		m.AddActionWithText(fmt.Sprintf("Do next (%d songs)", n)).OnTriggered(func() { t.act("next") })
		m.AddActionWithText(fmt.Sprintf("Try again (%d songs)", n)).OnTriggered(func() { t.act("restart") })
		m.AddActionWithText(fmt.Sprintf("Cancel (%d songs)", n)).OnTriggered(func() { t.act("cancel") })
		m.ExecWithPos(at)
		return
	}
	m.AddActionWithText("Details").OnTriggered(t.showPane)
	m.AddSeparator()
	m.AddActionWithText("Do next").OnTriggered(func() { t.act("next") })
	m.AddActionWithText("Try again").OnTriggered(func() { t.act("restart") })
	m.AddActionWithText("Find under other names…").OnTriggered(t.otherNames)
	m.AddActionWithText("Cancel").OnTriggered(func() { t.act("cancel") })
	if v.State == core.JobDone && v.Rec.Path != "" {
		m.AddSeparator()
		m.AddActionWithText("Show in folder").OnTriggered(t.openFile)
	}
	m.ExecWithPos(at)
}

func (t *queueTab) openFile() {
	if v, ok := t.a.m.JobView(t.paneKey); ok {
		switch {
		case v.State == core.JobDone && v.Rec.Path != "":
			openFolder(v.Rec.Path)
		case v.Lib != nil:
			openFolder(v.Lib.Path)
		}
	}
}

// --- the details pane ---------------------------------------------------------------------------

func (t *queueTab) showPane() {
	t.paneKey, t.paneKeys = t.selected(), nil
	if keys := t.selectedKeys(); len(keys) > 1 {
		t.paneKey, t.paneKeys = "", keys
	}
	if t.paneKey == "" && t.paneKeys == nil {
		t.pane.Hide()
		return
	}
	t.pane.Show()
	t.fillPane()
}

func (t *queueTab) hidePane() {
	t.pane.Hide()
	t.paneKey, t.paneKeys = "", nil
	t.list.view.SelectionModel().ClearSelection()
	t.list.view.SetCurrentIndex(rootIndex) // so clicking the same song opens it again
	t.focus()
}

// fillMulti shows the songs selected, when there are several.
func (t *queueTab) fillMulti() {
	e := html.EscapeString
	keys := t.paneKeys
	t.dTitle.SetText(fmt.Sprintf("%d songs selected", len(keys)))
	counts := map[core.JobState]int{}
	var b strings.Builder
	b.WriteString("<ul style='margin-left:-22px'>")
	counted := min(len(keys), 3000) // read again every second while shown: not all of 70,000
	for i, k := range keys[:counted] {
		v, ok := t.a.m.JobView(k)
		if !ok {
			continue
		}
		counts[v.State]++
		if i < 40 {
			fmt.Fprintf(&b, "<li>%s – %s</li>", e(v.Track.ArtistStr()), e(v.Track.Title))
		}
	}
	if len(keys) > 40 {
		fmt.Fprintf(&b, "<li>and %d more</li>", len(keys)-40)
	}
	b.WriteString("</ul><p>Do next, Try again and Cancel work on all of them. Shift or Ctrl with a click changes the selection; Ctrl+A selects every song shown.</p>")
	var parts []string
	for _, s := range []core.JobState{core.JobDownloading, core.JobSearching, core.JobReady, core.JobWaiting, core.JobQueued, core.JobDone, core.JobInLibrary, core.JobFailed, core.JobCancelled} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	if counted < len(keys) {
		parts = append(parts, fmt.Sprintf("(of the first %d)", counted))
	}
	t.dArtist.SetText(strings.Join(parts, " · "))
	setPill(t.dState, "Selection", "info")
	t.dNext.SetVisible(true)
	t.dAgain.SetVisible(true)
	t.dCancel.SetVisible(true)
	t.dOpen.SetVisible(false)
	t.dOther.SetVisible(false)
	t.dBody.SetText(b.String())
}

// fillPane shows everything about a track: every source tried and why it didn't work.
func (t *queueTab) fillPane() {
	if len(t.paneKeys) > 1 {
		t.fillMulti()
		return
	}
	v, ok := t.a.m.JobView(t.paneKey)
	if !ok {
		return
	}
	e := html.EscapeString
	tr := v.Track
	t.dTitle.SetText(tr.Title)
	sub := tr.ArtistStr()
	if tr.Album != "" {
		sub += " · " + tr.Album
	}
	if tr.DurationMs > 0 {
		sub += " · " + core.FmtDuration(tr.Duration())
	}
	t.dArtist.SetText(sub)
	main, _ := statusLines(v)
	kind := map[core.JobState]string{core.JobDone: "ok", core.JobFailed: "bad", core.JobWaiting: "warn", core.JobDownloading: "accent", core.JobSearching: "info", core.JobReady: "info", core.JobInLibrary: "info"}[v.State]
	if v.State == core.JobDownloading && v.Task.Waiting {
		kind = "warn"
	}
	if kind == "" {
		kind = "off"
	}
	setPill(t.dState, firstWords(main, 26), kind)

	active := v.State == core.JobQueued || v.State == core.JobSearching || v.State == core.JobReady || v.State == core.JobDownloading || v.State == core.JobWaiting
	t.dNext.SetVisible(active)
	t.dCancel.SetVisible(active)
	t.dAgain.SetVisible(!active || v.State == core.JobWaiting)
	t.dOpen.SetVisible((v.State == core.JobDone && v.Rec.Path != "") || v.Lib != nil)
	t.dOther.SetVisible(true)

	var b strings.Builder
	if len(v.Playlists) > 0 {
		fmt.Fprintf(&b, "<p><b>From:</b> %s</p>", e(strings.Join(v.Playlists, ", ")))
	}
	if tr.SpotifyURL != "" {
		fmt.Fprintf(&b, "<p><a style='color:%s' href='%s'>Open in Spotify</a></p>", th.Accent, e(tr.SpotifyURL))
	}
	if v.State == core.JobDone {
		r := v.Rec
		fmt.Fprintf(&b, "<p><b>Saved as:</b> %s</p><p><b>Quality:</b> %s<br><b>Came from:</b> %s</p>", e(r.Path), e(r.Quality.String()), e(core.SourceLabels[r.Source]))
		if r.SourceURL != "" && strings.HasPrefix(r.SourceURL, "http") {
			fmt.Fprintf(&b, "<p><a style='color:%s' href='%s'>Open the source page</a></p>", th.Accent, e(r.SourceURL))
		}
		if r.LibPath != "" {
			fmt.Fprintf(&b, "<p>This copy is better than the %s in your library, which was left as it is.</p>", e(r.LibQuality.String()))
		}
	}
	if v.Lib != nil {
		s := "copy"
		if v.LibCount > 1 {
			s = fmt.Sprintf("%d copies; the best", v.LibCount)
		}
		fmt.Fprintf(&b, "<p><b>In your library</b> (%s): %s<br>%s</p>", s, e(v.Lib.Q.String()), e(v.Lib.Path))
		if v.NeedBetter && v.State != core.JobInLibrary {
			b.WriteString("<p>It's below the quality you chose, so it's only downloaded if a source has it in better quality.</p>")
		}
	}
	if v.State == core.JobWaiting {
		fmt.Fprintf(&b, "<p>The app will try again at %s by itself.</p>", v.RetryAt.Format("15:04"))
	}
	if v.State == core.JobDownloading || v.State == core.JobSearching {
		if v.Task.Note != "" {
			fmt.Fprintf(&b, "<p>%s</p>", e(v.Task.Note))
		}
	}
	if len(v.OtherNames) > 0 {
		var names []string
		for _, n := range v.OtherNames {
			names = append(names, e(n.String()))
		}
		fmt.Fprintf(&b, "<p><b>Searched for as:</b> %s (Find under other names changes this)</p>", strings.Join(names, "; "))
	}
	reasons := v.Reasons
	if len(reasons) == 0 && v.Reason != "" {
		reasons = strings.Split(v.Reason, "; ")
	}
	if len(reasons) > 0 {
		b.WriteString("<p><b>What the sources said</b></p><ul style='margin-left:-22px'>")
		for _, r := range reasons {
			fmt.Fprintf(&b, "<li>%s</li>", e(r))
		}
		b.WriteString("</ul>")
	}
	t.dBody.SetText(b.String())
}

func firstWords(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if i := strings.Index(s, "  ·"); i > 0 {
		return s[:i]
	}
	return s[:n] + "…"
}

// showPlaylist shows one playlist's tracks.
func (t *queueTab) showPlaylist(name string) {
	t.q.Playlist = name
	t.q.Filter, t.q.Source = "", ""
	t.chips["all"].SetChecked(true)
	t.refresh(true)
	t.a.show(pageDownloads)
}
