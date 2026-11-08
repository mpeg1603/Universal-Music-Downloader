package tui

import (
	"universal-music-downloader/internal/core"

	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type queueContent struct {
	tview.TableContentReadOnly
	m     core.Backend
	keys  []string
	views map[int]core.JobView // the rows drawn lately; the others aren't read at all
}

// view reads a row's track when it's first drawn.
func (q *queueContent) view(row int) (core.JobView, bool) {
	if v, ok := q.views[row]; ok {
		return v, true
	}
	if q.views == nil || len(q.views) > 300 {
		q.views = map[int]core.JobView{}
	}
	v, ok := q.m.JobView(q.keys[row])
	if ok {
		q.views[row] = v
	}
	return v, ok
}

var queueHeaders = []string{"", "Track", "Album", "Status", "Quality", "Playlist"}

func (q *queueContent) GetRowCount() int { return len(q.keys) + 1 }

func (q *queueContent) GetColumnCount() int { return len(queueHeaders) }

// GetCell must accept positions outside the table: tview's mouse handler asks for row or
// column -1 when a click lands below the last row or right of the last column.
func (q *queueContent) GetCell(row, col int) *tview.TableCell {
	if row < 0 || row > len(q.keys) || col < 0 || col >= len(queueHeaders) {
		return nil
	}
	if row == 0 {
		return tview.NewTableCell(queueHeaders[col]).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow)
	}
	v, ok := q.view(row - 1)
	if !ok {
		return nil
	}
	switch col {
	case 0:
		icon, color := stateIcon(v.State)
		if v.Bumped && v.State == core.JobQueued {
			icon = "▲"
		}
		return tview.NewTableCell(icon).SetTextColor(color)
	case 1:
		return tview.NewTableCell(tview.Escape(v.Track.String())).SetExpansion(3).SetMaxWidth(60)
	case 2:
		return tview.NewTableCell(tview.Escape(v.Track.Album)).SetExpansion(1).SetMaxWidth(30).SetTextColor(tcell.ColorGray)
	case 3:
		return tview.NewTableCell(statusText(v)).SetExpansion(3).SetMaxWidth(70)
	case 4:
		return tview.NewTableCell(tview.Escape(qualityText(v))).SetMaxWidth(18)
	case 5:
		p := ""
		if len(v.Playlists) > 0 {
			p = v.Playlists[0]
			if len(v.Playlists) > 1 {
				p += fmt.Sprintf(" +%d", len(v.Playlists)-1)
			}
		}
		return tview.NewTableCell(tview.Escape(p)).SetMaxWidth(24).SetTextColor(tcell.ColorGray)
	}
	return nil
}

func stateIcon(s core.JobState) (string, tcell.Color) {
	switch s {
	case core.JobDownloading:
		return "↓", tcell.ColorAqua
	case core.JobSearching:
		return "⌕", tcell.ColorYellow
	case core.JobReady:
		return "…", tcell.ColorYellow
	case core.JobWaiting:
		return "⟳", tcell.ColorOrange
	case core.JobDone:
		return "✓", tcell.ColorGreen
	case core.JobInLibrary:
		return "◆", tcell.ColorDodgerBlue
	case core.JobFailed:
		return "✗", tcell.ColorRed
	case core.JobCancelled:
		return "⊘", tcell.ColorGray
	}
	return "·", tcell.ColorGray
}

func statusText(v core.JobView) string {
	esc := tview.Escape
	switch v.State {
	case core.JobSearching, core.JobReady, core.JobDownloading:
		t := v.Task
		s := core.FirstNonEmpty(t.Stage, v.State.String())
		if t.Total > 0 {
			frac := float64(t.Done) / float64(t.Total)
			s = fmt.Sprintf("[aqua]%s[-] %3.0f%% %s", tviewBar(frac, 12), 100*frac, core.FmtBytePair(t.Done, t.Total))
			if t.Speed > 0 {
				s += " " + core.FmtRate(t.Speed)
			}
			if t.Note != "" {
				s += " · " + esc(t.Note)
			}
			return s
		}
		if t.Note != "" {
			s += " · " + t.Note
		}
		return esc(s)
	case core.JobDone:
		s := "← " + core.FirstNonEmpty(core.SourceLabels[v.Rec.Source], v.Rec.Source)
		if v.Rec.LibPath != "" {
			s += " (better than your library copy)"
		}
		return esc(s)
	case core.JobInLibrary:
		if v.Lib != nil {
			return "[dodgerblue]in your library[-] " + esc(filepath.Base(v.Lib.Path))
		}
		return "in your library"
	case core.JobWaiting:
		return fmt.Sprintf("[orange]retry %s[-] · %s", v.RetryAt.Format("15:04"), esc(v.Reason))
	case core.JobFailed:
		return "[red]" + esc(v.Reason) + "[-]"
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

func tviewBar(frac float64, width int) string {
	frac = max(0, min(1, frac))
	full := int(frac * float64(width))
	return strings.Repeat("━", full) + "[gray]" + strings.Repeat("─", width-full) + "[aqua]"
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

func (ui *tui) buildQueue() tview.Primitive {
	ui.queueRows = &queueContent{m: ui.m}
	ui.queueTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetContent(ui.queueRows)
	ui.queueTable.SetSelectionChangedFunc(func(row, col int) {
		if row >= 1 && row-1 < len(ui.queueRows.keys) {
			ui.selected = ui.queueRows.keys[row-1]
		}
	})
	ui.queueTable.SetSelectedFunc(func(row, col int) { ui.showDetails() })
	ui.queueTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			if ui.q.Text != "" || ui.q.Playlist != "" || ui.q.Filter != "" || ui.q.Source != "" {
				ui.q = core.QueueQuery{Sort: ui.q.Sort, Reverse: ui.q.Reverse}
				ui.searchField.SetText("")
				ui.refresh()
			}
			return nil
		}
		if ev.Key() != tcell.KeyRune {
			return ev
		}
		switch ev.Rune() {
		case 'c':
			if ui.selected != "" {
				ui.m.Cancel(ui.selected)
			}
		case 'r':
			if ui.selected != "" {
				ui.m.Restart(ui.selected)
			}
		case 'p':
			if ui.selected != "" {
				ui.m.Prioritize(ui.selected)
			}
		case 'R':
			ui.confirm("Search again for every track that wasn't found (and those waiting to be retried)?", "Retry", func() {
				n := ui.m.RetryFailed(false)
				core.Logf("Retrying %d tracks", n)
			})
		case 'f':
			ui.pickFilter()
		case 'o':
			ui.pickSort()
		case 'O':
			ui.q.Reverse = !ui.q.Reverse
		case '/':
			ui.app.SetFocus(ui.searchField)
		default:
			return ev
		}
		ui.refresh()
		return nil
	})
	ui.queueInfo = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	ui.searchField = tview.NewInputField().SetLabel("/ ").SetFieldWidth(30).SetPlaceholder("search artist, title, album")
	ui.searchField.SetChangedFunc(func(text string) {
		ui.q.Text = text
		ui.refreshQueue()
	})
	ui.searchField.SetDoneFunc(func(k tcell.Key) { ui.app.SetFocus(ui.queueTable) })
	top := tview.NewFlex().AddItem(ui.queueInfo, 0, 1, false).AddItem(ui.searchField, 34, 0, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(top, 1, 0, false).AddItem(ui.queueTable, 0, 1, true)
}

func (ui *tui) refreshQueue() {
	// The order is read again every couple of seconds, or at once for a new filter; the rows
	// on screen are read at every drawing.
	if ui.q != ui.queueAsked || time.Since(ui.queueRead) > 2*time.Second {
		ui.queueRows.keys, ui.queueCounts = ui.m.QueueKeys(ui.q)
		ui.queueAsked, ui.queueRead = ui.q, time.Now()
	}
	ui.queueRows.views = nil
	rows := ui.queueRows.keys
	filter, sortBy := "all tracks", "playlist order"
	for _, f := range core.QueueFilters {
		if f.Name == ui.q.Filter {
			filter = f.Label
		}
	}
	for _, o := range core.QueueSorts {
		if o.Name == ui.q.Sort {
			sortBy = o.Label
		}
	}
	if ui.q.Reverse {
		sortBy += ", reversed"
	}
	info := fmt.Sprintf(" [::b]%s[::-] (f) · %d tracks · sorted by %s (o, O reverses)", filter, len(rows), sortBy)
	if ui.q.Source != "" {
		info += " · from [::b]" + core.SourceLabels[ui.q.Source] + "[::-]"
	}
	if ui.q.Playlist != "" {
		info += " · playlist [::b]" + tview.Escape(ui.q.Playlist) + "[::-]"
	}
	if ui.q.Filter != "" || ui.q.Source != "" || ui.q.Playlist != "" || ui.q.Text != "" {
		info += " · Esc shows all"
	}
	if len(ui.m.Config().Inputs) == 0 && ui.queueCounts["all"] == 0 {
		info = " [yellow]Nothing queued yet: press 2 for Search to find music or add your playlist files[-]"
	}
	ui.queueInfo.SetText(info)
	// Keep the selected track selected as rows move.
	row, _ := ui.queueTable.GetSelection()
	want := -1
	for i, k := range rows {
		if k == ui.selected {
			want = i + 1
			break
		}
	}
	switch {
	case want > 0 && want != row:
		ui.queueTable.Select(want, 0)
	case want < 0 && len(rows) > 0:
		ui.queueTable.Select(min(max(row, 1), len(rows)), 0)
	}
}

func (ui *tui) showDetails() {
	key := ui.selected
	if key == "" {
		return
	}
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	render := func() {
		v, ok := ui.m.JobView(key)
		if !ok {
			return
		}
		view.SetText(detailsText(v))
	}
	render()
	name := "details"
	view.SetBorder(true).SetTitle(" Track (c cancel · r restart · p do next · Esc close) ")
	view.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape || ev.Key() == tcell.KeyEnter {
			ui.closeModal(name)
			return nil
		}
		switch ev.Rune() {
		case 'c':
			ui.m.Cancel(key)
		case 'r':
			ui.m.Restart(key)
		case 'p':
			ui.m.Prioritize(key)
		case 'q':
			ui.closeModal(name)
			return nil
		default:
			return ev
		}
		time.AfterFunc(200*time.Millisecond, func() { ui.app.QueueUpdateDraw(render) })
		return nil
	})
	ui.openModal(name, view, 110, 26)
}

func detailsText(v core.JobView) string {
	e := tview.Escape
	t := v.Track
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]%s[::-]\n%s", e(t.Title), e(t.ArtistStr()))
	if t.Album != "" {
		fmt.Fprintf(&b, " · %s", e(t.Album))
	}
	if t.DurationMs > 0 {
		fmt.Fprintf(&b, " · %s", core.FmtDuration(t.Duration()))
	}
	b.WriteString("\n")
	if t.SpotifyURL != "" {
		fmt.Fprintf(&b, "[gray]%s[-]\n", e(t.SpotifyURL))
	}
	fmt.Fprintf(&b, "Playlists: %s\n\n", e(strings.Join(v.Playlists, ", ")))
	icon, _ := stateIcon(v.State)
	fmt.Fprintf(&b, "[::b]%s %s[::-]", icon, v.State)
	if v.Source != "" && v.State.Active() {
		fmt.Fprintf(&b, " · %s", core.SourceLabels[v.Source])
	}
	b.WriteString("\n")
	if v.State.Active() {
		fmt.Fprintf(&b, "%s\n", statusText(v))
	}
	if v.State == core.JobDone {
		r := v.Rec
		fmt.Fprintf(&b, "File: %s\nQuality: %s · from %s %s\n", e(r.Path), r.Quality, core.SourceLabels[r.Source], e(r.SourceURL))
		if r.LibPath != "" {
			fmt.Fprintf(&b, "Better than your library copy %s (%s), which was left untouched.\n", e(r.LibPath), r.LibQuality)
		}
	}
	if v.State == core.JobWaiting {
		fmt.Fprintf(&b, "Trying again at %s\n", v.RetryAt.Format("15:04"))
	}
	if v.LibCount > 0 && v.Lib != nil {
		fmt.Fprintf(&b, "\n[dodgerblue]In your library[-] (%d cop%s): %s · %s\n", v.LibCount, map[bool]string{true: "y", false: "ies"}[v.LibCount == 1], e(v.Lib.Path), v.Lib.Q)
		if v.NeedBetter && v.State != core.JobInLibrary {
			b.WriteString("Below your quality threshold: downloaded only if a source has it in better quality.\n")
		}
	}
	reasons := v.Reasons
	if len(reasons) == 0 && v.Reason != "" {
		reasons = strings.Split(v.Reason, "; ")
	}
	if len(reasons) > 0 {
		b.WriteString("\n[::b]Sources tried[::-]\n")
		for _, r := range reasons {
			fmt.Fprintf(&b, " • %s\n", e(r))
		}
	}
	return b.String()
}

// --- playlists ----------------------------------------------------------------------------------

// pickFilter lists the filters with how many tracks each has, and the sources tracks came from.
func (ui *tui) pickFilter() {
	counts := ui.m.QueueCounts()
	list := tview.NewList().ShowSecondaryText(false)
	cur := 0
	for _, f := range core.QueueFilters {
		f := f
		if f.Name == ui.q.Filter || (f.Name == "all" && ui.q.Filter == "" && ui.q.Source == "") {
			cur = list.GetItemCount()
		}
		list.AddItem(fmt.Sprintf("%-48s %6d", f.Label, counts[f.Name]), "", 0, func() {
			ui.closeModal("filter")
			ui.q.Filter, ui.q.Source = f.Name, ""
			if f.Name == "all" {
				ui.q.Filter = ""
			}
			ui.refresh()
		})
	}
	bySource := map[string]int{}
	for k, n := range counts {
		if name, ok := strings.CutPrefix(k, "from:"); ok {
			bySource[name] = n
		}
	}
	var srcs []string
	for k := range bySource {
		srcs = append(srcs, k)
	}
	sort.Slice(srcs, func(i, j int) bool { return bySource[srcs[i]] > bySource[srcs[j]] })
	for _, name := range srcs {
		name := name
		if name == ui.q.Source {
			cur = list.GetItemCount()
		}
		list.AddItem(fmt.Sprintf("%-48s %6d", "downloaded from "+core.SourceLabels[name], bySource[name]), "", 0, func() {
			ui.closeModal("filter")
			ui.q.Filter, ui.q.Source = "done", name
			ui.refresh()
		})
	}
	list.SetCurrentItem(cur)
	list.SetDoneFunc(func() { ui.closeModal("filter") })
	list.SetBorder(true).SetTitle(" Show (Enter picks · Esc closes) ")
	ui.openModal("filter", list, 64, min(list.GetItemCount()+2, 26))
}

func (ui *tui) pickSort() {
	list := tview.NewList().ShowSecondaryText(false)
	cur := 0
	for i, o := range core.QueueSorts {
		o := o
		if o.Name == ui.q.Sort || (ui.q.Sort == "" && i == 0) {
			cur = i
		}
		list.AddItem(o.Label, "", 0, func() {
			ui.closeModal("sort")
			ui.q.Sort = o.Name
			ui.refresh()
		})
	}
	rev := "reverse the order (O): off"
	if ui.q.Reverse {
		rev = "reverse the order (O): on"
	}
	list.AddItem(rev, "", 'r', func() {
		ui.closeModal("sort")
		ui.q.Reverse = !ui.q.Reverse
		ui.refresh()
	})
	list.SetCurrentItem(cur)
	list.SetDoneFunc(func() { ui.closeModal("sort") })
	list.SetBorder(true).SetTitle(" Sort by (Enter picks · Esc closes) ")
	ui.openModal("sort", list, 52, len(core.QueueSorts)+3)
}
