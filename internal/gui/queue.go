package gui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Queue tab: every track, with what's happening to it. The table reads its rows from a
// list kept here (a model, so even 70,000 tracks draw only what's visible).

var queueColumns = []string{"", "Track", "Album", "Status", "Quality", "Playlist"}

type queueTab struct {
	a       *app
	w       *qt.QWidget
	table   *qt.QTableView
	model   *qt.QAbstractTableModel
	filter  *qt.QComboBox
	sort    *qt.QComboBox
	reverse *qt.QCheckBox
	find    *qt.QLineEdit
	info    *qt.QLabel

	q    core.QueueQuery
	rows []core.JobView

	lastFull time.Time  // the last time every row was read
	later    *qt.QTimer // a full read put off (they're at most once a second)
}

func newQueueTab(a *app) *queueTab {
	t := &queueTab{a: a, w: qt.NewQWidget2()}
	box := qt.NewQVBoxLayout(t.w)
	box.SetContentsMargins(0, 4, 0, 0)

	bar := qt.NewQHBoxLayout2()
	t.filter = qt.NewQComboBox2()
	t.filter.SetToolTip("Which tracks to show (F)")
	for _, f := range core.QueueFilters {
		t.filter.AddItem(f.Label)
	}
	t.filter.OnCurrentIndexChanged(func(i int) {
		if i >= 0 && i < len(core.QueueFilters) {
			t.q.Filter = core.QueueFilters[i].Name
			if t.q.Filter == "all" {
				t.q.Filter = ""
			}
			t.refresh(true)
		}
	})
	t.sort = qt.NewQComboBox2()
	t.sort.SetToolTip("Order (O)")
	for _, s := range core.QueueSorts {
		t.sort.AddItem("Sort: " + s.Label)
	}
	t.sort.OnCurrentIndexChanged(func(i int) {
		if i >= 0 && i < len(core.QueueSorts) {
			t.q.Sort = core.QueueSorts[i].Name
			t.refresh(true)
		}
	})
	t.reverse = qt.NewQCheckBox3("Reverse")
	t.reverse.OnToggled(func(on bool) { t.q.Reverse = on; t.refresh(true) })
	t.find = qt.NewQLineEdit2()
	t.find.SetPlaceholderText("Find: artist, title, album (/)")
	t.find.SetClearButtonEnabled(true)
	t.find.OnTextChanged(func(s string) { t.q.Text = strings.TrimSpace(s); t.refresh(true) })
	bar.AddWidget(t.filter.QWidget)
	bar.AddWidget(t.sort.QWidget)
	bar.AddWidget(t.reverse.QWidget)
	bar.AddWidget2(t.find.QWidget, 1)
	box.AddLayout(bar.QLayout)

	t.model = qt.NewQAbstractTableModel()
	t.model.OnRowCount(func(parent *qt.QModelIndex) int {
		if parent.IsValid() {
			return 0
		}
		return len(t.rows)
	})
	t.model.OnColumnCount(func(parent *qt.QModelIndex) int { return len(queueColumns) })
	t.model.OnHeaderData(func(super func(int, qt.Orientation, int) *qt.QVariant, section int, o qt.Orientation, role int) *qt.QVariant {
		if o == qt.Horizontal && role == int(qt.DisplayRole) && section < len(queueColumns) {
			return qt.NewQVariant14(queueColumns[section])
		}
		return super(section, o, role)
	})
	t.model.OnData(t.data)

	t.table = qt.NewQTableView2()
	t.table.SetModel(t.model.QAbstractItemModel)
	t.table.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.table.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.table.SetAlternatingRowColors(true)
	t.table.SetShowGrid(false)
	t.table.SetWordWrap(false)
	t.table.VerticalHeader().Hide()
	t.table.VerticalHeader().SetDefaultSectionSize(22)
	h := t.table.HorizontalHeader()
	// The track takes what's left; the others keep their width (sizing them to their contents
	// would measure every row).
	h.SetStretchLastSection(false)
	for i, w := range []int{26, 300, 170, 320, 110, 140} {
		t.table.SetColumnWidth(i, w)
		h.SetSectionResizeMode2(i, qt.QHeaderView__Interactive)
	}
	h.SetSectionResizeMode2(0, qt.QHeaderView__Fixed)
	h.SetSectionResizeMode2(1, qt.QHeaderView__Stretch)
	t.table.OnDoubleClicked(func(*qt.QModelIndex) { t.details() })
	t.table.SetContextMenuPolicy(qt.CustomContextMenu)
	t.table.OnCustomContextMenuRequested(func(pos *qt.QPoint) { t.menu(t.table.Viewport().MapToGlobalWithQPoint(pos)) })
	box.AddWidget2(t.table.QWidget, 1)

	t.info = qt.NewQLabel2()
	box.AddWidget(t.info.QWidget)

	// The terminal app's keys work here too.
	keys := map[string]func(){
		"Return": t.details, "C": func() { t.act("cancel") }, "R": func() { t.act("restart") },
		"N": func() { t.act("next") }, "Shift+R": t.retryAll, "/": func() { t.find.SetFocus() },
		"F": func() { t.filter.ShowPopup() }, "O": func() { t.sort.ShowPopup() },
		"Shift+O": func() { t.reverse.Toggle() },
	}
	for k, f := range keys {
		f := f
		sc := qt.NewQShortcut2(qt.NewQKeySequence2(k), t.table.QObject)
		sc.SetContext(qt.WidgetShortcut)
		sc.OnActivated(f)
	}
	t.refresh(true)
	return t
}

// data is what the table shows in a cell.
func (t *queueTab) data(idx *qt.QModelIndex, role int) *qt.QVariant {
	r, c := idx.Row(), idx.Column()
	if r < 0 || r >= len(t.rows) {
		return qt.NewQVariant()
	}
	v := t.rows[r]
	switch qt.ItemDataRole(role) {
	case qt.DisplayRole:
		switch c {
		case 0:
			icon, _ := stateIcon(v)
			return qt.NewQVariant14(icon)
		case 1:
			return qt.NewQVariant14(v.Track.String())
		case 2:
			return qt.NewQVariant14(v.Track.Album)
		case 3:
			return qt.NewQVariant14(statusText(v))
		case 4:
			return qt.NewQVariant14(qualityText(v))
		case 5:
			p := ""
			if len(v.Playlists) > 0 {
				p = v.Playlists[0]
				if len(v.Playlists) > 1 {
					p += fmt.Sprintf(" +%d", len(v.Playlists)-1)
				}
			}
			return qt.NewQVariant14(p)
		}
	case qt.ForegroundRole:
		if c == 0 || c == 3 {
			_, color := stateIcon(v)
			if color != "" {
				return qt.NewQColor6(color).ToQVariant()
			}
		}
	case qt.ToolTipRole:
		if c == 3 && v.Reason != "" {
			return qt.NewQVariant14(v.Reason)
		}
	}
	return qt.NewQVariant()
}

// stateIcon is a track's state as a symbol, and its color ("" for the default).
func stateIcon(v core.JobView) (string, string) {
	switch v.State {
	case core.JobDownloading:
		return "↓", "#29a"
	case core.JobSearching:
		return "⌕", "#b90"
	case core.JobReady:
		return "…", "#b90"
	case core.JobWaiting:
		return "⟳", "#d70"
	case core.JobDone:
		return "✓", "#3a3"
	case core.JobInLibrary:
		return "◆", "#38c"
	case core.JobFailed:
		return "✗", "#c33"
	case core.JobCancelled:
		return "⊘", "#888"
	}
	if v.Bumped {
		return "▲", ""
	}
	return "·", "#888"
}

func statusText(v core.JobView) string {
	switch v.State {
	case core.JobSearching, core.JobReady, core.JobDownloading:
		t := v.Task
		s := core.FirstNonEmpty(t.Stage, v.State.String())
		if t.Total > 0 {
			s = fmt.Sprintf("%3.0f%% %s", 100*float64(t.Done)/float64(t.Total), core.FmtBytePair(t.Done, t.Total))
			if t.Speed > 0 {
				s += " " + core.FmtRate(t.Speed)
			}
		}
		if t.Note != "" {
			s += " · " + t.Note
		}
		return s
	case core.JobDone:
		s := "← " + core.FirstNonEmpty(core.SourceLabels[v.Rec.Source], v.Rec.Source)
		if v.Rec.LibPath != "" {
			s += " (better than your library copy)"
		}
		return s
	case core.JobInLibrary:
		if v.Lib != nil {
			return "in your library: " + filepath.Base(v.Lib.Path)
		}
		return "in your library"
	case core.JobWaiting:
		return fmt.Sprintf("retry %s · %s", v.RetryAt.Format("15:04"), v.Reason)
	case core.JobFailed:
		return v.Reason
	case core.JobCancelled:
		return "cancelled"
	case core.JobQueued:
		if v.NeedBetter && v.Lib != nil {
			return "queued · only if better than your " + v.Lib.Q.String()
		}
		if v.Bumped {
			return "queued · next up"
		}
	}
	return v.State.String()
}

func qualityText(v core.JobView) string {
	switch {
	case v.State == core.JobDone:
		return v.Rec.Quality.String()
	case v.Lib != nil:
		return "have " + v.Lib.Q.String()
	}
	return ""
}

// changed reads every row again when the queue changed: right away for a new filter or order
// (reset), otherwise at most once a second.
func (t *queueTab) changed() {
	if wait := time.Second - time.Since(t.lastFull); wait > 0 {
		if t.later == nil {
			t.later = qt.NewQTimer2(t.w.QObject)
			t.later.SetSingleShot(true)
			t.later.OnTimeout(func() { t.refresh(false) })
		}
		if !t.later.IsActive() {
			t.later.Start(int(wait.Milliseconds()) + 1)
		}
		return
	}
	t.refresh(false)
}

// refresh reads the rows again; reset: the filter or order changed (the selection may go).
func (t *queueTab) refresh(reset bool) {
	t.lastFull = time.Now()
	sel := t.selected()
	rows := t.a.m.Queue(t.q)
	if reset || len(rows) != len(t.rows) {
		t.model.BeginResetModel()
		t.rows = rows
		t.model.EndResetModel()
	} else {
		t.rows = rows
		t.redrawVisible()
	}
	if sel != "" {
		for i, v := range t.rows {
			if v.Key == sel {
				t.table.SelectRow(i)
				break
			}
		}
	}
	counts := t.a.m.QueueCounts()
	for i, f := range core.QueueFilters {
		t.filter.SetItemText(i, fmt.Sprintf("%s (%d)", f.Label, counts[f.Name]))
	}
	info := fmt.Sprintf("%d tracks shown", len(t.rows))
	if len(t.a.m.Config().Inputs) == 0 && len(t.rows) == 0 && t.q.Filter == "" && t.q.Text == "" {
		info = "Nothing queued yet: the Search tab finds music; playlist files are added there too."
	}
	t.info.SetText(info + "   ·   Enter details · C cancel · R restart · N do next · Shift+R retry everything not found")
}

// tick redraws the visible tracks that are in progress (progress moves without events).
func (t *queueTab) tick() {
	if t.a.tabs.CurrentIndex() != 0 || len(t.rows) == 0 {
		return
	}
	first, last := t.visible()
	for i := first; i <= last; i++ {
		if t.rows[i].State.Active() {
			if v, ok := t.a.m.JobView(t.rows[i].Key); ok {
				t.rows[i] = v
			}
		}
	}
	t.redrawVisible()
}

// visible is the range of rows on screen.
func (t *queueTab) visible() (int, int) {
	first := max(0, t.table.RowAt(0))
	last := t.table.RowAt(t.table.Viewport().Height())
	if last < 0 {
		last = len(t.rows) - 1
	}
	return first, min(last, len(t.rows)-1)
}

func (t *queueTab) redrawVisible() {
	if len(t.rows) == 0 {
		return
	}
	first, last := t.visible()
	t.model.DataChanged(t.model.Index(first, 0, qt.NewQModelIndex()), t.model.Index(last, len(queueColumns)-1, qt.NewQModelIndex()))
}

func (t *queueTab) selected() string {
	rows := t.table.SelectionModel().SelectedRows()
	if len(rows) == 0 {
		return ""
	}
	if r := rows[0].Row(); r >= 0 && r < len(t.rows) {
		return t.rows[r].Key
	}
	return ""
}

func (t *queueTab) act(what string) {
	key := t.selected()
	if key == "" {
		return
	}
	switch what {
	case "cancel":
		t.a.m.Cancel(key)
	case "restart":
		t.a.m.Restart(key)
	case "next":
		t.a.m.Prioritize(key)
	}
	t.refresh(false)
}

func (t *queueTab) retryAll() {
	n := t.a.m.RetryFailed(false)
	t.a.message("Retry", fmt.Sprintf("%d tracks that weren't found are queued again.", n))
}

func (t *queueTab) menu(at *qt.QPoint) {
	if t.selected() == "" {
		return
	}
	m := qt.NewQMenu2()
	m.AddActionWithText("Details…").OnTriggered(t.details)
	m.AddSeparator()
	m.AddActionWithText("Do next").OnTriggered(func() { t.act("next") })
	m.AddActionWithText("Restart (search again; download again if done)").OnTriggered(func() { t.act("restart") })
	m.AddActionWithText("Cancel").OnTriggered(func() { t.act("cancel") })
	m.ExecWithPos(at)
}

// details shows everything about the selected track: every source tried and why it didn't work.
func (t *queueTab) details() {
	key := t.selected()
	if key == "" {
		return
	}
	v, ok := t.a.m.JobView(key)
	if !ok {
		return
	}
	tr := v.Track
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s", tr.Title, tr.ArtistStr())
	if tr.Album != "" {
		fmt.Fprintf(&b, " · %s", tr.Album)
	}
	if tr.DurationMs > 0 {
		fmt.Fprintf(&b, " · %s", core.FmtDuration(tr.Duration()))
	}
	fmt.Fprintf(&b, "\nPlaylists: %s\n\n", strings.Join(v.Playlists, ", "))
	icon, _ := stateIcon(v)
	fmt.Fprintf(&b, "%s %s\n%s\n", icon, v.State, statusText(v))
	if v.State == core.JobDone {
		r := v.Rec
		fmt.Fprintf(&b, "\nFile: %s\nQuality: %s · from %s %s\n", r.Path, r.Quality, core.SourceLabels[r.Source], r.SourceURL)
		if r.LibPath != "" {
			fmt.Fprintf(&b, "Better than your library copy %s (%s), which was left untouched.\n", r.LibPath, r.LibQuality)
		}
	}
	if v.LibCount > 0 && v.Lib != nil {
		fmt.Fprintf(&b, "\nIn your library (%d): %s · %s\n", v.LibCount, v.Lib.Path, v.Lib.Q)
	}
	reasons := v.Reasons
	if len(reasons) == 0 && v.Reason != "" {
		reasons = strings.Split(v.Reason, "; ")
	}
	if len(reasons) > 0 {
		b.WriteString("\nSources tried:\n")
		for _, r := range reasons {
			fmt.Fprintf(&b, " • %s\n", r)
		}
	}
	d := qt.NewQDialog(t.a.win.QWidget)
	d.SetWindowTitle(tr.String())
	d.Resize(760, 460)
	l := qt.NewQVBoxLayout(d.QWidget)
	text := qt.NewQPlainTextEdit3(b.String())
	text.SetReadOnly(true)
	l.AddWidget(text.QWidget)
	buttons := qt.NewQDialogButtonBox4(qt.QDialogButtonBox__Close)
	buttons.OnRejected(func() { d.Close() })
	l.AddWidget(buttons.QWidget)
	d.Exec()
}
