package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The terminal app: every setting and control is in here.

var tabs = []struct{ name, title string }{
	{"queue", "Queue"}, {"playlists", "Playlists"}, {"library", "Library"},
	{"sources", "Sources"}, {"soulseek", "Soulseek"}, {"settings", "Settings"}, {"log", "Log"},
}

var queueFilters = []struct{ name, title string }{
	{"all", "all"}, {"active", "in progress"}, {"todo", "to do"}, {"done", "downloaded"},
	{"library", "in library"}, {"failed", "not found"}, {"cancelled", "cancelled"},
}

type tui struct {
	m    *Manager
	app  *tview.Application
	root *tview.Pages
	body *tview.Pages

	header, tabBar, footer *tview.TextView
	current                string
	modals                 []string
	width, height          int // of the terminal, as last drawn

	// queue
	queueTable  *tview.Table
	queueRows   *queueContent
	queueInfo   *tview.TextView
	searchField *tview.InputField
	filter      int
	search      string
	playlist    string
	sortBy      string
	selected    string

	// playlists
	inputsTable, playlistsTable *tview.Table
	inputs                      []InputStatus
	playlists                   []PlaylistStatus

	// library
	libInfo  *tview.TextView
	libTable *tview.Table

	// sources
	srcTable  *tview.Table
	srcDetail *tview.TextView
	srcs      []SourceStatus

	// soulseek
	sk *slskUI

	// settings
	settings     *tview.Form
	slskFieldIdx int // the first Soulseek field in the settings

	// log
	logView   *tview.TextView
	logSeq    int64
	logFollow bool
}

func runTUI(m *Manager) error {
	ui := &tui{m: m, app: tview.NewApplication(), sortBy: "playlist", logFollow: true}
	ui.build()
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for range tick.C {
			ui.app.QueueUpdateDraw(ui.refresh)
		}
	}()
	// Closing the terminal, or `kill`, quits the way q does: the terminal is put back in order
	// and the tracks in progress continue next time.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		if _, ok := <-signals; ok {
			logf("Quitting (asked by a signal)")
			ui.app.Stop()
		}
	}()
	ui.refresh()
	return ui.app.SetRoot(ui.root, true).EnableMouse(true).EnablePaste(true).Run()
}

func (ui *tui) build() {
	ui.header = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	ui.tabBar = tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWrap(false)
	ui.tabBar.SetHighlightedFunc(func(added, removed, remaining []string) {
		if len(added) > 0 {
			ui.show(added[0])
		}
	})
	ui.footer = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	ui.body = tview.NewPages()
	ui.body.AddPage("queue", ui.buildQueue(), true, false)
	ui.body.AddPage("playlists", ui.buildPlaylists(), true, false)
	ui.body.AddPage("library", ui.buildLibrary(), true, false)
	ui.body.AddPage("sources", ui.buildSources(), true, false)
	ui.body.AddPage("soulseek", ui.buildSoulseek(), true, false)
	ui.body.AddPage("settings", ui.buildSettings(), true, false)
	ui.body.AddPage("log", ui.buildLog(), true, false)
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.header, 2, 0, false).
		AddItem(ui.tabBar, 1, 0, false).
		AddItem(ui.body, 0, 1, true).
		AddItem(ui.footer, 1, 0, false)
	ui.root = tview.NewPages().AddPage("main", main, true, true)
	ui.app.SetInputCapture(ui.globalKeys)
	ui.app.SetBeforeDrawFunc(func(s tcell.Screen) bool {
		ui.width, ui.height = s.Size()
		return false
	})
	ui.app.SetAfterDrawFunc(transparentBackground)
	start := "queue"
	if len(ui.m.Config().Inputs) == 0 {
		start = "playlists"
	}
	ui.show(start)
}

// transparentBackground swaps tview's black background for the terminal's
// default one, so a translucent terminal (e.g. kitty's background_opacity)
// shows through. Done after drawing rather than via tview.Styles because tview
// also uses PrimitiveBackgroundColor as the text color of inverted elements
// (selected table rows, dropdown and autocomplete lists), which would
// otherwise become the terminal's default foreground and turn unreadable.
func transparentBackground(s tcell.Screen) {
	w, h := s.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mainc, combc, style, _ := s.GetContent(x, y)
			if _, bg, _ := style.Decompose(); bg == tcell.ColorBlack {
				s.SetContent(x, y, mainc, combc, style.Background(tcell.ColorDefault))
			}
		}
	}
}

func (ui *tui) show(name string) {
	if ui.current == name {
		return
	}
	ui.current = name
	if name == "settings" {
		ui.fillSettings() // show changes made elsewhere (e.g. the bandwidth keys)
	}
	ui.body.SwitchToPage(name)
	ui.tabBar.Highlight(name)
	ui.focusPage()
	ui.refresh()
}

func (ui *tui) focusPage() {
	switch ui.current {
	case "queue":
		ui.app.SetFocus(ui.queueTable)
	case "playlists":
		ui.app.SetFocus(ui.playlistsTable)
		if len(ui.inputs) == 0 {
			ui.app.SetFocus(ui.inputsTable)
		}
	case "library":
		ui.app.SetFocus(ui.libTable)
	case "sources":
		ui.app.SetFocus(ui.srcTable)
	case "soulseek":
		ui.focusSoulseek()
	case "settings":
		// On the Save button, so number keys still switch tabs; Tab/↑ go into the fields.
		ui.settings.SetFocus(ui.settings.GetFormItemCount())
		ui.app.SetFocus(ui.settings)
	case "log":
		ui.app.SetFocus(ui.logView)
	}
}

func (ui *tui) typing() bool {
	switch ui.app.GetFocus().(type) {
	case *tview.InputField, *tview.DropDown, *tview.TextArea:
		return true
	}
	return false
}

func (ui *tui) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() == tcell.KeyCtrlC {
		ui.confirmQuit()
		return nil
	}
	if len(ui.modals) > 0 {
		return ev
	}
	if k := ev.Key(); k >= tcell.KeyF1 && k < tcell.KeyF1+tcell.Key(len(tabs)) {
		ui.show(tabs[k-tcell.KeyF1].name)
		return nil
	}
	if r := ev.Rune(); ev.Key() == tcell.KeyRune && ev.Modifiers()&tcell.ModAlt != 0 && r >= '1' && r < '1'+rune(len(tabs)) {
		ui.show(tabs[r-'1'].name)
		return nil
	}
	if ui.typing() || ev.Key() != tcell.KeyRune {
		return ev
	}
	switch r := ev.Rune(); {
	case r >= '1' && r < '1'+rune(len(tabs)):
		ui.show(tabs[r-'1'].name)
		return nil
	case r == 'q':
		ui.confirmQuit()
		return nil
	case r == '[' || r == ']':
		ui.nudgeBandwidth(r == ']')
		return nil
	case r == 'P':
		ui.m.SetPaused(!ui.m.Paused())
		return nil
	}
	return ev
}

func (ui *tui) nudgeBandwidth(up bool) {
	c := ui.m.Config()
	v := c.BandwidthMBps
	step := 1.0
	if v < 2 || (v == 2 && !up) {
		step = 0.5
	}
	switch {
	case up:
		v += step
	case v > 0:
		v = max(0, v-step)
	}
	ui.update(func(c *Config) { c.BandwidthMBps = v })
}

func (ui *tui) update(change func(*Config)) bool {
	if err := ui.m.UpdateConfig(change); err != nil {
		ui.message("Can't change that", err.Error())
		return false
	}
	return true
}

func (ui *tui) confirmQuit() {
	if ui.hasModal("quit") {
		return
	}
	o := ui.m.Overview()
	text := "Quit?"
	if n := o.Counts[JobSearching] + o.Counts[JobDownloading] + o.Counts[JobReady]; n > 0 {
		text = fmt.Sprintf("Quit? The %d tracks in progress are stopped and continue next time.", n)
	}
	modal := tview.NewModal().SetText(text).AddButtons([]string{"Quit", "Cancel"}).
		SetDoneFunc(func(i int, label string) {
			ui.closeModal("quit")
			if label == "Quit" {
				ui.app.Stop()
			}
		})
	ui.openModal("quit", modal, 0, 0)
}

// --- modals ----------------------------------------------------------------------------------

func centered(p tview.Primitive, w, h int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, h, 1, true).
			AddItem(nil, 0, 1, false), w, 1, true).
		AddItem(nil, 0, 1, false)
}

// modalLayer is a window over the app: it takes every mouse event, so a click beside the
// window can't reach (and focus) the page under it.
type modalLayer struct{ tview.Primitive }

func (m modalLayer) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	inner := m.Primitive.MouseHandler()
	return func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		_, capture := inner(action, ev, setFocus)
		return true, capture
	}
}

func (ui *tui) openModal(name string, p tview.Primitive, w, h int) {
	if ui.hasModal(name) { // opened twice: replace it
		ui.root.RemovePage(name)
		ui.dropModal(name)
	}
	if w > 0 {
		if ui.width > 0 {
			w, h = min(w, max(20, ui.width-2)), min(h, max(5, ui.height-2))
		}
		p = centered(p, w, h)
	}
	ui.root.AddPage(name, modalLayer{p}, true, true)
	ui.modals = append(ui.modals, name)
	ui.app.SetFocus(p)
}

func (ui *tui) dropModal(name string) {
	for i, m := range ui.modals {
		if m == name {
			ui.modals = append(ui.modals[:i], ui.modals[i+1:]...)
			return
		}
	}
}

func (ui *tui) hasModal(name string) bool {
	for _, m := range ui.modals {
		if m == name {
			return true
		}
	}
	return false
}

func (ui *tui) closeModal(name string) {
	if !ui.hasModal(name) {
		return
	}
	ui.root.RemovePage(name)
	ui.dropModal(name)
	ui.refocus()
	// Closed from inside a key handler, the window's form may still move the focus to one of
	// its (now hidden) buttons afterwards; put it back once the key is handled.
	go ui.app.QueueUpdate(ui.refocus)
}

// refocus puts the focus on the top window, or on the current page if none is open.
func (ui *tui) refocus() {
	if len(ui.modals) == 0 {
		ui.focusPage()
	} else if top, ok := ui.root.GetPage(ui.modals[len(ui.modals)-1]).(modalLayer); ok {
		if f := ui.app.GetFocus(); f == nil || !top.HasFocus() {
			ui.app.SetFocus(top.Primitive)
		}
	}
}

func (ui *tui) message(title, text string) {
	name := "message" + strconv.Itoa(len(ui.modals))
	modal := tview.NewModal().SetText(title + "\n\n" + text).AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) { ui.closeModal(name) })
	ui.openModal(name, modal, 0, 0)
}

func (ui *tui) confirm(text, yes string, f func()) {
	name := "confirm"
	modal := tview.NewModal().SetText(text).AddButtons([]string{yes, "Cancel"}).
		SetDoneFunc(func(i int, label string) {
			ui.closeModal(name)
			if label == yes {
				f()
			}
		})
	ui.openModal(name, modal, 0, 0)
}

// askPath asks for a file or folder path. Suggestions show as you type: ↑/↓ choose one, Tab
// takes it (and, for a folder, lists what's inside), Enter takes it and closes the list; Enter
// with no list open (or on what's already typed) accepts the path.
func (ui *tui) askPath(title, help string, dirsOnly bool, exts []string, done func(string)) {
	name := "path"
	form := tview.NewForm()
	input := tview.NewInputField().SetLabel("Path ").SetFieldWidth(0)
	submit := func() {
		if !ui.hasModal(name) {
			return
		}
		p := strings.TrimSpace(input.GetText())
		if p == "" {
			return
		}
		ui.closeModal(name)
		done(p)
	}
	listOpen := false // whether tview shows the suggestions (it doesn't say)
	input.SetAutocompleteFunc(func(text string) []string {
		list := pathCompletions(text, dirsOnly, exts)
		listOpen = len(list) > 0
		return list
	})
	// Tab with no suggestions showing (closed with Esc) completes like a shell: the one match,
	// or what all matches start with, and shows them.
	input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyEnter:
			if !listOpen { // handled here, so the form doesn't move the focus afterwards
				submit()
				return nil
			}
		case tcell.KeyEscape:
			if listOpen {
				listOpen = false
			}
		case tcell.KeyTab:
			if listOpen {
				return ev
			}
			list := pathCompletions(input.GetText(), dirsOnly, exts)
			if len(list) == 0 {
				return ev // to the OK button
			}
			if p := commonPrefix(list); len(p) > len(input.GetText()) {
				input.SetText(p)
			}
			input.Autocomplete() // safe here: tview isn't holding the field's lock yet
			return nil
		}
		return ev
	})
	// Called by tview while it holds the input field's lock: never call input.Autocomplete()
	// here (it would wait for that lock forever and freeze the app). Changing the text is
	// enough: tview then refreshes the list itself.
	input.SetAutocompletedFunc(func(text string, index, source int) bool {
		switch source {
		case tview.AutocompletedNavigate:
			return false
		case tview.AutocompletedEnter:
			listOpen = false
			if strings.TrimRight(text, `/\`) == strings.TrimRight(input.GetText(), `/\`) {
				go ui.app.QueueUpdateDraw(submit) // already typed out: accept it
				return true
			}
			input.SetText(text)
			return true
		}
		input.SetText(text) // Tab or a click
		closeList := !strings.HasSuffix(text, string(filepath.Separator))
		if closeList {
			listOpen = false
		}
		return closeList
	})
	form.AddFormItem(input)
	form.AddTextView("", help+"\n[::d]Tab completes · ↑↓ pick a suggestion · Enter accepts · Esc closes[::-]", 0, 3, true, false)
	form.AddButton("OK", submit).AddButton("Cancel", func() { ui.closeModal(name) })
	form.SetCancelFunc(func() { ui.closeModal(name) })
	form.SetBorder(true).SetTitle(" " + title + " ")
	ui.openModal(name, form, 90, 12)
}

func commonPrefix(list []string) string {
	if len(list) == 0 {
		return ""
	}
	p := list[0]
	for _, s := range list[1:] {
		for !strings.HasPrefix(s, p) {
			_, size := utf8.DecodeLastRuneInString(p)
			p = p[:len(p)-size]
		}
	}
	return p
}

func pathCompletions(text string, dirsOnly bool, exts []string) []string {
	if text == "" || strings.Contains(text, "://") {
		return nil
	}
	expanded := expandHome(text)
	dir, prefix := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	shownDir := text[:len(text)-len(prefix)]
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(strings.ToLower(n), strings.ToLower(prefix)) || (strings.HasPrefix(n, ".") && !strings.HasPrefix(prefix, ".")) {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, n)); err == nil {
				isDir = st.IsDir()
			}
		}
		if isDir {
			out = append(out, shownDir+n+string(filepath.Separator))
			continue
		}
		if dirsOnly {
			continue
		}
		ok := len(exts) == 0
		for _, x := range exts {
			ok = ok || strings.EqualFold(filepath.Ext(n), x)
		}
		if ok {
			out = append(out, shownDir+n)
		}
	}
	sort.Strings(out)
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

// --- header, tabs, footer ----------------------------------------------------------------

func (ui *tui) refresh() {
	o := ui.m.Overview()
	state := "[green]● running[-]"
	switch {
	case !o.Ready && o.Lib.Scanning:
		state = fmt.Sprintf("[yellow]◌ scanning your library %d/%d[-]", o.Lib.Done, o.Lib.Total)
	case !o.Ready && o.ToCheck > 0:
		state = fmt.Sprintf("[yellow]◌ checking your library for %d/%d tracks[-]", o.Checked, o.ToCheck)
	case !o.Ready && o.Importing:
		state = "[yellow]◌ reading your playlists…[-]"
	case !o.Ready:
		state = "[yellow]◌ starting…[-]"
	case o.Paused:
		state = "[yellow]⏸ paused[-] (P resumes)"
	}
	speed := fmtRate(o.Own + o.Torrent)
	if o.Cap > 0 {
		speed += " of " + fmtRate(o.Cap)
	} else {
		speed += " (no limit)"
	}
	if o.Torrent > 1000 {
		speed += fmt.Sprintf(", torrents %s", fmtRate(o.Torrent))
	}
	todo := o.Counts[JobQueued] + o.Counts[JobSearching] + o.Counts[JobReady] + o.Counts[JobDownloading]
	line1 := fmt.Sprintf(" [::b]spotify-downloader[::-]  %s   ⇣ %s   downloads %d/%d", state, speed, o.DlUsed, o.DlLimit)
	if o.DlWaiting > 0 {
		line1 += fmt.Sprintf(" (+%d ready)", o.DlWaiting)
	}
	line1 += fmt.Sprintf("   searches %d/%d", o.SrUsed, o.SrLimit)
	line2 := fmt.Sprintf(" %d tracks   [green]✓ %d downloaded[-]   [blue]◆ %d in library[-]   · %d to do",
		o.Total, o.Counts[JobDone], o.Counts[JobInLibrary], todo)
	if n := o.Counts[JobWaiting]; n > 0 {
		line2 += fmt.Sprintf("   [yellow]⟳ %d retry later[-]", n)
	}
	if n := o.Counts[JobFailed]; n > 0 {
		line2 += fmt.Sprintf("   [red]✗ %d not found[-]", n)
	}
	if n := o.Counts[JobCancelled]; n > 0 {
		line2 += fmt.Sprintf("   ⊘ %d cancelled", n)
	}
	if o.Free >= 0 {
		disk := "disk " + fmtBytes(o.Free) + " free"
		if o.Free < 2<<30 {
			disk = "[red]" + disk + "[-]"
		}
		line2 += "   " + disk
	}
	ui.header.SetText(line1 + "\n" + line2)

	var tb strings.Builder
	unread := 0
	if c := ui.m.Soulseek(); c != nil {
		unread = c.chat.Unread()
	}
	for i, t := range tabs {
		title := t.title
		if t.name == "soulseek" && unread > 0 {
			title += fmt.Sprintf(" [yellow](%d)[-]", unread)
		}
		fmt.Fprintf(&tb, ` ["%s"][::b]%d[::-] %s[""] `, t.name, i+1, title)
	}
	ui.tabBar.SetText(tb.String())
	ui.tabBar.Highlight(ui.current)

	switch ui.current {
	case "queue":
		ui.refreshQueue()
	case "playlists":
		ui.refreshPlaylists()
	case "library":
		ui.refreshLibrary()
	case "sources":
		ui.refreshSources()
	case "soulseek":
		ui.refreshSoulseek()
	case "log":
		ui.refreshLog()
	}
	ui.footer.SetText(ui.footerText())
}

func (ui *tui) footerText() string {
	common := "  [::d]1-7/Alt+1-7/F1-F7 tabs · [ ] bandwidth −/+ · P pause all · q quit[::-]"
	hints := map[string]string{
		"queue":     " [::b]Enter[::-] details  [::b]c[::-] cancel  [::b]r[::-] restart  [::b]p[::-] do next  [::b]R[::-] retry all failed  [::b]f[::-] filter  [::b]/[::-] search  [::b]o[::-] sort",
		"playlists": " [::b]a[::-] add file/link  [::b]d[::-] remove  [::b]space[::-] on/off  [::b]+ -[::-] priority  [::b]Enter[::-] show tracks  [::b]Tab[::-] switch list  [::b]R[::-] re-read",
		"library":   " [::b]a[::-] add folder  [::b]d[::-] remove  [::b]s[::-] rescan  [::b]t[::-] quality threshold",
		"sources":   " [::b]space[::-] on/off  [::b]K J[::-] move up/down  [::b]Enter[::-] settings  [::b]e[::-] route  [::b]p[::-] pause/resume  [::b]b[::-] paste Bandcamp links",
		"soulseek":  " [::b]Tab[::-] transfers/chats/message  [::b]n[::-] message someone  [::b]j[::-] join a room  [::b]l[::-] leave/close chat  [::b]m[::-] message user  [::b]x[::-] stop upload  [::b]B[::-] ban  [::b]o[::-] port check  [::b]R[::-] reconnect",
		"settings":  " [::b]Tab ↑ ↓[::-] move between fields  [::b]Esc[::-] leave a field  Save applies the changes right away",
		"log":       " [::b]↑ ↓ PgUp PgDn[::-] scroll  [::b]End[::-] follow  [::b]v[::-] details " + onOff(verbose),
	}
	return hints[ui.current] + common
}

func onOff(b bool) string {
	if b {
		return "(on)"
	}
	return "(off)"
}

// --- queue -------------------------------------------------------------------------------------

type queueContent struct {
	tview.TableContentReadOnly
	rows []JobView
}

var queueHeaders = []string{"", "Track", "Album", "Status", "Quality", "Playlist"}

func (q *queueContent) GetRowCount() int    { return len(q.rows) + 1 }
func (q *queueContent) GetColumnCount() int { return len(queueHeaders) }

// GetCell must accept positions outside the table: tview's mouse handler asks for row or
// column -1 when a click lands below the last row or right of the last column.
func (q *queueContent) GetCell(row, col int) *tview.TableCell {
	if row < 0 || row > len(q.rows) || col < 0 || col >= len(queueHeaders) {
		return nil
	}
	if row == 0 {
		return tview.NewTableCell(queueHeaders[col]).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow)
	}
	v := q.rows[row-1]
	switch col {
	case 0:
		icon, color := stateIcon(v.State)
		if v.Bumped && v.State == JobQueued {
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

func stateIcon(s JobState) (string, tcell.Color) {
	switch s {
	case JobDownloading:
		return "↓", tcell.ColorAqua
	case JobSearching:
		return "⌕", tcell.ColorYellow
	case JobReady:
		return "…", tcell.ColorYellow
	case JobWaiting:
		return "⟳", tcell.ColorOrange
	case JobDone:
		return "✓", tcell.ColorGreen
	case JobInLibrary:
		return "◆", tcell.ColorDodgerBlue
	case JobFailed:
		return "✗", tcell.ColorRed
	case JobCancelled:
		return "⊘", tcell.ColorGray
	}
	return "·", tcell.ColorGray
}

func statusText(v JobView) string {
	esc := tview.Escape
	switch v.State {
	case JobSearching, JobReady, JobDownloading:
		t := v.Task
		s := firstNonEmpty(t.Stage, v.State.String())
		if t.Total > 0 {
			frac := float64(t.Done) / float64(t.Total)
			s = fmt.Sprintf("[aqua]%s[-] %3.0f%% %s", tviewBar(frac, 12), 100*frac, fmtBytePair(t.Done, t.Total))
			if t.Speed > 0 {
				s += " " + fmtRate(t.Speed)
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
	case JobDone:
		s := "← " + firstNonEmpty(sourceLabels[v.Rec.Source], v.Rec.Source)
		if v.Rec.LibPath != "" {
			s += " (better than your library copy)"
		}
		return esc(s)
	case JobInLibrary:
		if v.Lib != nil {
			return "[dodgerblue]in your library[-] " + esc(filepath.Base(v.Lib.Path))
		}
		return "in your library"
	case JobWaiting:
		return fmt.Sprintf("[orange]retry %s[-] · %s", v.RetryAt.Format("15:04"), esc(v.Reason))
	case JobFailed:
		return "[red]" + esc(v.Reason) + "[-]"
	case JobCancelled:
		return "cancelled"
	case JobQueued:
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

func qualityText(v JobView) string {
	switch {
	case v.State == JobDone:
		return v.Rec.Quality.String()
	case v.Lib != nil:
		return "have " + v.Lib.Q.String()
	}
	return ""
}

func (ui *tui) buildQueue() tview.Primitive {
	ui.queueRows = &queueContent{}
	ui.queueTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetContent(ui.queueRows)
	ui.queueTable.SetSelectionChangedFunc(func(row, col int) {
		if row >= 1 && row-1 < len(ui.queueRows.rows) {
			ui.selected = ui.queueRows.rows[row-1].Key
		}
	})
	ui.queueTable.SetSelectedFunc(func(row, col int) { ui.showDetails() })
	ui.queueTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			if ui.search != "" || ui.playlist != "" || ui.filter != 0 {
				ui.search, ui.playlist, ui.filter = "", "", 0
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
				logf("Retrying %d tracks", n)
			})
		case 'f':
			ui.filter = (ui.filter + 1) % len(queueFilters)
		case 'F':
			ui.filter = (ui.filter + len(queueFilters) - 1) % len(queueFilters)
		case 'o':
			ui.sortBy = map[string]string{"playlist": "state", "state": "artist", "artist": "playlist"}[ui.sortBy]
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
		ui.search = text
		ui.refreshQueue()
	})
	ui.searchField.SetDoneFunc(func(k tcell.Key) { ui.app.SetFocus(ui.queueTable) })
	top := tview.NewFlex().AddItem(ui.queueInfo, 0, 1, false).AddItem(ui.searchField, 34, 0, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(top, 1, 0, false).AddItem(ui.queueTable, 0, 1, true)
}

func (ui *tui) refreshQueue() {
	views := ui.m.Jobs()
	f := queueFilters[ui.filter].name
	needle := strings.ToLower(strings.TrimSpace(ui.search))
	rows := views[:0]
	for _, v := range views {
		if !matchesFilter(v, f) {
			continue
		}
		if ui.playlist != "" && !containsStr(v.Playlists, ui.playlist) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(v.Track.String()+" "+v.Track.Album), needle) {
			continue
		}
		rows = append(rows, v)
	}
	sortViews(rows, ui.sortBy)
	ui.queueRows.rows = rows
	info := fmt.Sprintf(" showing [::b]%s[::-] (f) · %d tracks · sorted by %s (o)", queueFilters[ui.filter].title, len(rows), ui.sortBy)
	if ui.playlist != "" {
		info += " · playlist [::b]" + tview.Escape(ui.playlist) + "[::-] (Esc clears)"
	}
	if len(ui.m.Config().Inputs) == 0 {
		info = " [yellow]No playlists yet: press 2 for Playlists, then a to add your CSV files[-]"
	}
	ui.queueInfo.SetText(info)
	// Keep the selected track selected as rows move.
	row, _ := ui.queueTable.GetSelection()
	want := -1
	for i, v := range rows {
		if v.Key == ui.selected {
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

func matchesFilter(v JobView, f string) bool {
	switch f {
	case "active":
		return v.State.active()
	case "todo":
		return v.State == JobQueued || v.State.active() || v.State == JobWaiting
	case "done":
		return v.State == JobDone
	case "library":
		return v.State == JobInLibrary
	case "failed":
		return v.State == JobFailed || v.State == JobWaiting
	case "cancelled":
		return v.State == JobCancelled
	}
	return true
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
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

func detailsText(v JobView) string {
	e := tview.Escape
	t := v.Track
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]%s[::-]\n%s", e(t.Title), e(t.ArtistStr()))
	if t.Album != "" {
		fmt.Fprintf(&b, " · %s", e(t.Album))
	}
	if t.DurationMs > 0 {
		fmt.Fprintf(&b, " · %s", fmtDuration(t.Duration()))
	}
	b.WriteString("\n")
	if t.SpotifyURL != "" {
		fmt.Fprintf(&b, "[gray]%s[-]\n", e(t.SpotifyURL))
	}
	fmt.Fprintf(&b, "Playlists: %s\n\n", e(strings.Join(v.Playlists, ", ")))
	icon, _ := stateIcon(v.State)
	fmt.Fprintf(&b, "[::b]%s %s[::-]", icon, v.State)
	if v.Source != "" && v.State.active() {
		fmt.Fprintf(&b, " · %s", sourceLabels[v.Source])
	}
	b.WriteString("\n")
	if v.State.active() {
		fmt.Fprintf(&b, "%s\n", statusText(v))
	}
	if v.State == JobDone {
		r := v.Rec
		fmt.Fprintf(&b, "File: %s\nQuality: %s · from %s %s\n", e(r.Path), r.Quality, sourceLabels[r.Source], e(r.SourceURL))
		if r.LibPath != "" {
			fmt.Fprintf(&b, "Better than your library copy %s (%s), which was left untouched.\n", e(r.LibPath), r.LibQuality)
		}
	}
	if v.State == JobWaiting {
		fmt.Fprintf(&b, "Trying again at %s\n", v.RetryAt.Format("15:04"))
	}
	if v.LibCount > 0 && v.Lib != nil {
		fmt.Fprintf(&b, "\n[dodgerblue]In your library[-] (%d cop%s): %s · %s\n", v.LibCount, map[bool]string{true: "y", false: "ies"}[v.LibCount == 1], e(v.Lib.Path), v.Lib.Q)
		if v.NeedBetter && v.State != JobInLibrary {
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

func (ui *tui) buildPlaylists() tview.Primitive {
	ui.inputsTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.inputsTable.SetBorder(true).SetTitle(" Playlist files (a add · d remove · space on/off · R re-read) ")
	ui.playlistsTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.playlistsTable.SetBorder(true).SetTitle(" Playlists (space on/off · + - priority · Enter show tracks) ")
	addInput := func() {
		ui.askPath("Add playlists", "A CSV (Exportify, TuneMyMusic…), a folder or zip of them, Spotify's data export (zip), a text file of \"Artist - Title\" lines, or a public Spotify playlist/album link.",
			false, []string{".csv", ".zip", ".txt", ".json"}, func(p string) {
				if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
					p = cleanPath(p)
					if _, err := os.Stat(p); err != nil {
						ui.message("Can't add that", err.Error())
						return
					}
				}
				ui.update(func(c *Config) {
					for _, in := range c.Inputs {
						if in.Path == p {
							return
						}
					}
					c.Inputs = append(c.Inputs, InputCfg{Path: p, Enabled: true})
				})
			})
	}
	ui.inputsTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := ui.inputsTable.GetSelection()
		var sel *InputStatus
		if row >= 1 && row-1 < len(ui.inputs) {
			sel = &ui.inputs[row-1]
		}
		switch {
		case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(ui.playlistsTable)
			return nil
		case ev.Key() == tcell.KeyDelete || ev.Rune() == 'd':
			if sel != nil {
				p := sel.Path
				ui.confirm("Remove "+p+" from the list? (The file itself isn't touched.)", "Remove", func() {
					ui.update(func(c *Config) {
						var keep []InputCfg
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
				ui.update(func(c *Config) {
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
		var sel *PlaylistStatus
		if row >= 1 && row-1 < len(ui.playlists) {
			sel = &ui.playlists[row-1]
		}
		switch {
		case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(ui.inputsTable)
			return nil
		case ev.Key() == tcell.KeyEnter:
			if sel != nil {
				ui.playlist, ui.filter, ui.search = sel.Name, 0, ""
				ui.searchField.SetText("")
				ui.show("queue")
			}
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
			ui.update(func(c *Config) {
				pc := c.Playlists[name]
				pc.Disabled = !pc.Disabled
				c.Playlists[name] = pc
			})
		case '+', '=':
			ui.update(func(c *Config) { pc := c.Playlists[name]; pc.Priority++; c.Playlists[name] = pc })
		case '-', '_':
			ui.update(func(c *Config) { pc := c.Playlists[name]; pc.Priority--; c.Playlists[name] = pc })
		default:
			return ev
		}
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.inputsTable, 9, 0, true).
		AddItem(ui.playlistsTable, 0, 1, false)
}

func headerRow(t *tview.Table, cols ...string) {
	for i, c := range cols {
		t.SetCell(0, i, tview.NewTableCell(c).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow))
	}
}

func (ui *tui) refreshPlaylists() {
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
		t.SetCell(1, 1, tview.NewTableCell("[yellow]Press a to add your playlist CSVs (or a folder of them)[-]").SetSelectable(false))
	}
	ui.playlists = ui.m.Playlists()
	p := ui.playlistsTable
	p.Clear()
	headerRow(p, "On", "Priority", "Playlist", "Tracks", "Downloaded", "In library", "To do", "Not found")
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
			p.SetCell(i+1, c, cell)
		}
	}
	if r, _ := ui.inputsTable.GetSelection(); r < 1 && len(ui.inputs) > 0 {
		ui.inputsTable.Select(1, 0)
	}
	if r, _ := ui.playlistsTable.GetSelection(); r < 1 && len(ui.playlists) > 0 {
		ui.playlistsTable.Select(1, 0)
	}
}

// --- library ----------------------------------------------------------------------------------

func (ui *tui) buildLibrary() tview.Primitive {
	ui.libInfo = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	ui.libInfo.SetBorder(true).SetTitle(" Your music library (read only: nothing in it is ever changed) ")
	ui.libTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.libTable.SetBorder(true).SetTitle(" Library folders (a add · d remove · s rescan · t threshold) ")
	ui.libTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		dirs := ui.m.Config().LibraryDirs
		row, _ := ui.libTable.GetSelection()
		switch ev.Rune() {
		case 'a':
			ui.askPath("Add a library folder", "A folder with your music. It is only read, never written to; downloads go to the downloads folder (Settings).",
				true, nil, func(p string) {
					p = cleanPath(p)
					if st, err := os.Stat(p); err != nil || !st.IsDir() {
						ui.message("Can't add that", p+" is not a folder")
						return
					}
					ui.update(func(c *Config) {
						if !containsStr(c.LibraryDirs, p) {
							c.LibraryDirs = append(c.LibraryDirs, p)
						}
					})
				})
		case 'd':
			if row >= 1 && row-1 < len(dirs) {
				d := dirs[row-1]
				ui.confirm("Stop checking "+d+"? (Nothing in it is touched.)", "Remove", func() {
					ui.update(func(c *Config) {
						var keep []string
						for _, x := range c.LibraryDirs {
							if x != d {
								keep = append(keep, x)
							}
						}
						c.LibraryDirs = keep
					})
				})
			}
		case 's':
			ui.m.Rescan()
		case 't':
			ui.pickThreshold()
		default:
			if ev.Key() == tcell.KeyDelete && row >= 1 {
				return tcell.NewEventKey(tcell.KeyRune, 'd', 0)
			}
			return ev
		}
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ui.libInfo, 11, 0, false).AddItem(ui.libTable, 0, 1, true)
}

func (ui *tui) pickThreshold() {
	list := tview.NewList().ShowSecondaryText(false)
	cur := ui.m.Config().Threshold
	for i, t := range thresholds {
		name := string(t.Name)
		label := fmt.Sprintf("%-9s %s", name, t.Label)
		if name == cur {
			label += "  ✓"
		}
		list.AddItem(label, "", 0, func() {
			ui.closeModal("threshold")
			ui.update(func(c *Config) { c.Threshold = name })
		})
		if name == cur {
			list.SetCurrentItem(i)
		}
	}
	list.SetDoneFunc(func() { ui.closeModal("threshold") })
	list.SetBorder(true).SetTitle(" Good enough quality: tracks you have at this quality or better are skipped ")
	ui.openModal("threshold", list, 84, len(thresholds)+2)
}

func (ui *tui) refreshLibrary() {
	c := ui.m.Config()
	st := ui.m.LibraryStatus()
	var b strings.Builder
	th := Threshold(c.Threshold)
	label := ""
	for _, t := range thresholds {
		if t.Name == th {
			label = t.Label
		}
	}
	fmt.Fprintf(&b, "Good enough quality (t): [::b]%s[::-] — %s\n", c.Threshold, label)
	b.WriteString("Tracks you have at this quality or better are skipped. Ones you have in lower quality are downloaded only\n" +
		"if a source has them in clearly better quality; the new file goes to the downloads folder and your copy stays.\n\n")
	switch {
	case len(c.LibraryDirs) == 0:
		b.WriteString("[yellow]No library folders yet: press a to add one.[-]\n")
	case st.Scanning:
		fmt.Fprintf(&b, "[yellow]Scanning: %d of %d files[-]\n", st.Done, st.Total)
	case !st.LastScan.IsZero():
		fmt.Fprintf(&b, "%d audio files, scanned %s (took %s)", st.Files, st.LastScan.Format("15:04"), st.Took.Round(100*time.Millisecond))
		if g := st.Files - st.ReadFromTags; g > 0 {
			fmt.Fprintf(&b, "; %d without usable tags are matched by file name", g)
		}
		b.WriteString("\n")
	default:
		fmt.Fprintf(&b, "%d files known from the last scan; rescanning soon\n", ui.m.index.Size())
	}
	if st.Err != "" {
		fmt.Fprintf(&b, "[red]%s[-]\n", tview.Escape(st.Err))
	}
	fmt.Fprintf(&b, "Downloads go to: %s", tview.Escape(c.OutputDir))
	ui.libInfo.SetText(b.String())
	t := ui.libTable
	t.Clear()
	headerRow(t, "Folder")
	for i, d := range c.LibraryDirs {
		t.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(d)).SetExpansion(1))
	}
	if r, _ := t.GetSelection(); r < 1 && len(c.LibraryDirs) > 0 {
		t.Select(1, 0)
	}
}

// --- sources -----------------------------------------------------------------------------------

func (ui *tui) buildSources() tview.Primitive {
	ui.srcTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.srcTable.SetBorder(true).SetTitle(" Sources, tried top to bottom ")
	ui.srcDetail = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	ui.srcDetail.SetBorder(true)
	ui.srcTable.SetSelectionChangedFunc(func(row, col int) { ui.refreshSourceDetail() })
	ui.srcTable.SetSelectedFunc(func(row, col int) { ui.sourceSettings() })
	ui.srcTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := ui.srcTable.GetSelection()
		if row < 1 || row-1 >= len(ui.srcs) {
			return ev
		}
		s := ui.srcs[row-1]
		move := 0
		switch {
		case ev.Rune() == ' ':
			ui.update(func(c *Config) { c.setSource(s.Name, !c.source(s.Name).Enabled) })
		case ev.Rune() == 'K' || (ev.Key() == tcell.KeyUp && ev.Modifiers()&(tcell.ModShift|tcell.ModAlt) != 0):
			move = -1
		case ev.Rune() == 'J' || (ev.Key() == tcell.KeyDown && ev.Modifiers()&(tcell.ModShift|tcell.ModAlt) != 0):
			move = 1
		case ev.Rune() == 'e':
			ui.pickRoute(s.Name)
		case ev.Rune() == 'p':
			ui.m.PauseSource(s.Name, !s.Paused && s.Until.IsZero())
		case ev.Rune() == 'b':
			ui.pasteBandcampLinks()
		default:
			return ev
		}
		if move != 0 {
			i := row - 1
			j := i + move
			if j >= 0 && j < len(ui.srcs) {
				ui.update(func(c *Config) { c.Sources[i], c.Sources[j] = c.Sources[j], c.Sources[i] })
				ui.srcTable.Select(j+1, 0)
			}
		}
		ui.refresh()
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ui.srcTable, 12, 0, true).AddItem(ui.srcDetail, 0, 1, false)
}

func (ui *tui) refreshSources() {
	ui.srcs = ui.m.SourceStatus()
	t := ui.srcTable
	t.Clear()
	headerRow(t, "#", "On", "Source", "Route", "Status", "Searched", "Found", "Downloaded", "Failed")
	for i, s := range ui.srcs {
		on, color := "✓", tcell.ColorGreen
		if !s.Enabled {
			on, color = "·", tcell.ColorGray
		}
		status, scolor := "ok", tcell.ColorGreen
		switch {
		case !s.Enabled:
			status, scolor = "off", tcell.ColorGray
		case s.Problem != "":
			status, scolor = s.Problem, tcell.ColorRed
		case s.Paused:
			status, scolor = "paused by you (p)", tcell.ColorYellow
		case !s.Until.IsZero():
			status, scolor = "paused until "+s.Until.Format("15:04")+": "+s.Reason, tcell.ColorOrange
		case s.Note != "":
			status, scolor = "ok, but "+s.Note, tcell.ColorYellow
		}
		route := s.Route
		cells := []*tview.TableCell{
			tview.NewTableCell(fmt.Sprint(i + 1)).SetAlign(tview.AlignRight),
			tview.NewTableCell(on).SetTextColor(color),
			tview.NewTableCell(s.Label),
			tview.NewTableCell(tview.Escape(route)).SetMaxWidth(22),
			tview.NewTableCell(tview.Escape(status)).SetTextColor(scolor).SetExpansion(1).SetMaxWidth(70),
			tview.NewTableCell(fmt.Sprint(s.Searches)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.Found)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.Downloads)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.DlFails)).SetAlign(tview.AlignRight),
		}
		for c, cell := range cells {
			t.SetCell(i+1, c, cell)
		}
	}
	if r, _ := t.GetSelection(); r < 1 && len(ui.srcs) > 0 {
		t.Select(1, 0)
	}
	ui.refreshSourceDetail()
}

var sourceHelp = map[string]string{
	"bandcamp": "Free and name-your-price downloads (lossless when offered). For name-your-price releases whose artist asks for an email address, enter your email in the settings: you paste the link Bandcamp sends (b), or it's read from Gmail or an IMAP mailbox.",
	"archive":  "Internet Archive items published under a license (Creative Commons, public domain); live concert tapes are left out.",
	"yandex": "Yandex Music with your account (Plus needed for full tracks; FLAC where available). Get a token: open " + yandexTokenURL +
		" in a browser, log in, and copy the access_token=… part of the address you end up on. Route \"direct\" keeps it off your VPN.",
	"vk":            "VK music with your account's token (MP3 up to 320 kbps). Log in from its settings (Enter) to get a token, or paste one. Route \"direct\" keeps it off your VPN.",
	"youtube_music": "Official \"song\" uploads, found by ISRC and by artist + title (Opus ~130 kbps). If YouTube asks to sign in (bot check), set \"Cookies from browser\" in its settings.",
	"soundcloud":    "Public SoundCloud tracks; 30-second previews are detected and rejected.",
	"youtube":       "Regular YouTube uploads (Opus ~130 kbps). Shares the bot-check pause and cookies with YouTube Music.",
	"torrent":       "Your Jackett or Prowlarr indexers, downloaded with aria2c (only the file needed). Indexers that keep failing are left out for a while instead of slowing every search.",
	"audius":        "Audius, an open platform where artists upload their own music (much independent electronic music). Full tracks as MP3 320; no account needed. Tracks for paying fans only are left out.",
	"soulseek":      "Soulseek, a file-sharing network where people share their music folders, often in FLAC. The client is built in: it needs only your Soulseek account (Enter: Settings → Soulseek; a new name and password make a new account). Searches are paced and only a few files are requested at once, as the network asks. While it's on you share your library (see the Soulseek tab), chat and keep a wishlist: tracks no source had are searched for again now and then.",
}

func (ui *tui) refreshSourceDetail() {
	row, _ := ui.srcTable.GetSelection()
	if row < 1 || row-1 >= len(ui.srcs) {
		return
	}
	s := ui.srcs[row-1]
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]%s[::-] · route: %s\n%s\n", s.Label, tview.Escape(describeRoute(s.Route)), tview.Escape(sourceHelp[s.Name]))
	if s.Problem != "" {
		fmt.Fprintf(&b, "\n[red]%s[-]\n", tview.Escape(s.Problem))
	}
	if s.Note != "" {
		fmt.Fprintf(&b, "\n[yellow]%s[-]\n", tview.Escape(s.Note))
	}
	if s.Reason != "" {
		fmt.Fprintf(&b, "\n[orange]Paused until %s: %s[-] (p resumes it now)\n", s.Until.Format("15:04"), tview.Escape(s.Reason))
	}
	if s.Name == "bandcamp" {
		bc := ui.m.Config().Bandcamp
		b.WriteString("\n[::b]Releases that ask for an email address[::-]\n")
		if bc.Email == "" {
			b.WriteString("skipped: enter your email address in the settings (Enter).\n")
		} else {
			pending, problem := ui.m.bcMail.status()
			from := map[string]string{"paste": "you paste them (b)", "gmail": "read from Gmail (" + firstNonEmpty(bc.GmailAccount, "not signed in") + ")",
				"imap": "read from " + bc.IMAPHost}[bc.mailbox()]
			fmt.Fprintf(&b, "Bandcamp emails the links to %s; %s.\n", tview.Escape(bc.Email), tview.Escape(from))
			if pending > 0 {
				fmt.Fprintf(&b, "[yellow]%d releases waiting for their link[-] (b pastes links from the emails)\n", pending)
			}
			if problem != "" {
				fmt.Fprintf(&b, "[red]Mailbox: %s[-]\n", tview.Escape(problem))
			}
		}
	}
	if s.Name == "torrent" {
		feeds := ui.m.TorrentFeeds()
		if len(feeds) > 0 {
			b.WriteString("\n[::b]Indexers[::-]\n")
		}
		for _, f := range feeds {
			state := "[green]ok[-]"
			if !f.OK {
				state = "[orange]left out until " + f.Until.Format("15:04") + "[-]"
			}
			fmt.Fprintf(&b, " %s  %s · %d searches, %d errors", state, tview.Escape(f.Name), f.Searches, f.Errors)
			if f.Avg > 0 {
				fmt.Fprintf(&b, ", answers in %.1fs", f.Avg.Seconds())
			}
			if f.LastErr != "" {
				fmt.Fprintf(&b, "\n      [gray]last error: %s[-]", tview.Escape(fitWidth(f.LastErr, 140)))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n[gray]Enter: settings · e: route · space: on/off · K/J: move · p: pause/resume · b: paste Bandcamp links[-]")
	ui.srcDetail.SetText(b.String())
	ui.srcDetail.SetTitle(" " + s.Label + " ")
}

func (ui *tui) pickRoute(name string) {
	cur := normalizeRoute(ui.m.Config().source(name).Route)
	list := tview.NewList().ShowSecondaryText(true)
	set := func(r string) {
		ui.update(func(c *Config) {
			for i := range c.Sources {
				if c.Sources[i].Name == name {
					c.Sources[i].Route = r
				}
			}
		})
	}
	list.AddItem("system", "the operating system's routing (your VPN applies)", 0, func() { ui.closeModal("route"); set(routeSystem) })
	list.AddItem("direct", "bound to your physical network interface, bypassing a VPN ("+describeRoute(routeDirect)+")", 0, func() { ui.closeModal("route"); set(routeDirect) })
	list.AddItem("proxy…", "through a SOCKS5 or HTTP proxy", 0, func() {
		ui.closeModal("route")
		ui.askText("Proxy for "+sourceLabels[name], "socks5://user:pass@host:port or http://host:port", strings.TrimPrefix(cur, routeSystem), func(v string) {
			if err := validRoute(v); err != nil {
				ui.message("Can't use that", err.Error())
				return
			}
			set(v)
		})
	})
	switch cur {
	case routeDirect:
		list.SetCurrentItem(1)
	case routeSystem:
	default:
		list.SetCurrentItem(2)
	}
	list.SetDoneFunc(func() { ui.closeModal("route") })
	list.SetBorder(true).SetTitle(" Route for " + sourceLabels[name] + " ")
	ui.openModal("route", list, 90, 8)
}

func (ui *tui) askText(title, help, value string, done func(string)) {
	form := tview.NewForm()
	input := tview.NewInputField().SetLabel("Value ").SetText(value).SetFieldWidth(0)
	form.AddFormItem(input)
	form.AddTextView("", help, 0, 2, false, false)
	submit := func() {
		if ui.hasModal("text") {
			ui.closeModal("text")
			done(strings.TrimSpace(input.GetText()))
		}
	}
	input.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEnter {
			submit()
			return nil
		}
		return ev
	})
	form.AddButton("OK", submit).AddButton("Cancel", func() { ui.closeModal("text") })
	form.SetCancelFunc(func() { ui.closeModal("text") })
	form.SetBorder(true).SetTitle(" " + title + " ")
	ui.openModal("text", form, 90, 10)
}

// sourceSettings opens the settings of the selected source.
func (ui *tui) sourceSettings() {
	row, _ := ui.srcTable.GetSelection()
	if row < 1 || row-1 >= len(ui.srcs) {
		return
	}
	name := ui.srcs[row-1].Name
	c := ui.m.Config()
	if name == "bandcamp" {
		ui.bandcampSettings(c.Bandcamp, normalizeRoute(c.source(name).Route))
		return
	}
	if name == "soulseek" { // its settings are many: they're in the Settings tab
		ui.show("settings")
		ui.settings.SetFocus(ui.slskFieldIdx + 1)
		ui.app.SetFocus(ui.settings)
		return
	}
	form := tview.NewForm()
	close := func() { ui.closeModal("srcsettings") }
	var apply []func(*Config)
	text := func(label, value string, width int, set func(*Config, string)) {
		v := value
		form.AddInputField(label, value, width, nil, func(t string) { v = t })
		apply = append(apply, func(c *Config) { set(c, strings.TrimSpace(v)) })
	}
	secret := func(label, value string, set func(*Config, string)) {
		v := value
		form.AddPasswordField(label, value, 60, '*', func(t string) { v = t })
		apply = append(apply, func(c *Config) { set(c, strings.TrimSpace(v)) })
	}
	route := c.source(name).Route
	text("Route (system, direct or proxy URL)", normalizeRoute(route), 50, func(c *Config, v string) {
		for i := range c.Sources {
			if c.Sources[i].Name == name {
				c.Sources[i].Route = v
			}
		}
	})
	height := 9
	switch name {
	case "youtube", "youtube_music", "soundcloud":
		text("Cookies file (cookies.txt)", c.Cookies, 60, func(c *Config, v string) { c.Cookies = v })
		idx := 0
		for i, b := range browsers {
			if b == c.CookiesFromBrowser {
				idx = i
			}
		}
		br := c.CookiesFromBrowser
		labels := append([]string{"(none)"}, browsers[1:]...)
		form.AddDropDown("Cookies from browser", labels, idx, func(o string, i int) { br = browsers[i] })
		apply = append(apply, func(c *Config) { c.CookiesFromBrowser = br })
		pace := fmt.Sprint(c.YouTubePace)
		form.AddInputField("Seconds between YouTube requests", pace, 6, nil, func(t string) { pace = t })
		apply = append(apply, func(c *Config) {
			if v, err := strconv.ParseFloat(strings.TrimSpace(pace), 64); err == nil && v >= 0 {
				c.YouTubePace = v
			}
		})
		text("yt-dlp program (empty: download it automatically)", c.YtDlpPath, 60, func(c *Config, v string) { c.YtDlpPath = v })
		form.AddTextView("", "Your browser's YouTube login (cookies) is the best fix for \"sign in to confirm you're not a bot\". "+
			"They're read from the browser every 10 minutes (YouTube's, Google's and SoundCloud's only) and each yt-dlp run gets a "+
			"temporary copy: your browser and your cookies file are never written to. A browser, when set, is used instead of the file.", 0, 4, false, false)
		height = 20
	case "torrent":
		text("Jackett / Prowlarr address(es), comma-separated", strings.Join(c.Torznab.Feeds, ", "), 60, func(c *Config, v string) { c.Torznab.Feeds = splitList(v) })
		secret("API key", c.Torznab.Key, func(c *Config, v string) { c.Torznab.Key = v })
		dur := func(label string, d Duration, set func(*Config, Duration)) {
			v := time.Duration(d).String()
			form.AddInputField(label, v, 10, nil, func(t string) { v = t })
			apply = append(apply, func(c *Config) {
				if x, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && x > 0 {
					set(c, Duration(x))
				}
			})
		}
		dur("Give up on an indexer search after", c.IndexerTimeout, func(c *Config, d Duration) { c.IndexerTimeout = d })
		dur("Give up on a torrent download after", c.TorrentTimeout, func(c *Config, d Duration) { c.TorrentTimeout = d })
		height = 17
	case "vk":
		secret("Token", c.VKToken, func(c *Config, v string) { c.VKToken = v })
		form.AddButton("Log in to get a token…", func() { close(); ui.vkLogin() })
		height = 11
	case "yandex":
		secret("OAuth token", c.YandexToken, func(c *Config, v string) { c.YandexToken = v })
		form.AddTextView("How to get it", "Open "+yandexTokenURL+" in your browser, log in, then copy the access_token=… value from the address bar.", 0, 4, false, false)
		height = 14
	}
	form.AddButton("Save", func() {
		if ui.update(func(c *Config) {
			for _, f := range apply {
				f(c)
			}
		}) {
			close()
		}
	})
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" " + sourceLabels[name] + " settings ")
	ui.openModal("srcsettings", form, 110, height)
}

var (
	mailboxModes  = []string{"paste", "gmail", "imap"}
	mailboxLabels = []string{"I paste them from the emails (no mailbox access)", "Gmail: sign in with Google (read only)", "IMAP mailbox (Yandex, Mail.ru, …)"}
)

// bandcampSettings edits Bandcamp's settings; d holds what's typed so far, so the dialog can be
// rebuilt with the fields of another mailbox choice.
func (ui *tui) bandcampSettings(d BandcampCfg, route string) {
	const name = "srcsettings"
	form := tview.NewForm().SetItemPadding(0)
	close := func() { ui.closeModal(name) }
	field := func(label string, p *string, width int) {
		form.AddInputField(label, *p, width, nil, func(t string) { *p = t })
	}
	secret := func(label string, p *string) {
		form.AddPasswordField(label, *p, 50, '*', func(t string) { *p = t })
	}
	intro := "[::b]Name-your-price releases that ask for an email address[::-]: Bandcamp emails you the download link.\n\n"
	field("Route (system, direct or proxy URL)", &route, 50)
	field("Your email address", &d.Email, 40)
	field("Country", &d.Country, 30)
	field("Postcode", &d.Postcode, 12)
	mode := d.mailbox()
	cur := 0
	for i, m := range mailboxModes {
		if m == mode {
			cur = i
		}
	}
	form.AddDropDown("Download links from", mailboxLabels, cur, func(_ string, i int) {
		if i != cur {
			d.Mailbox = mailboxModes[i]
			go ui.app.QueueUpdateDraw(func() { ui.bandcampSettings(d, route) }) // that choice's fields
		}
	})
	introLines := 4
	switch mode {
	case "paste":
		intro += "Copy the link from Bandcamp's email and paste it with [::b]b[::-] in the Sources tab (or Paste links below). " +
			"Tracks wait for it instead of coming from a worse source."
	case "gmail":
		field("OAuth client ID", &d.GmailClientID, 66)
		secret("OAuth client secret", &d.GmailClientSecret)
		state := "[yellow]not signed in[-]"
		if d.GmailToken != "" {
			state = "[green]signed in as " + tview.Escape(firstNonEmpty(d.GmailAccount, "?")) + "[-]"
		}
		intro += "Gmail: " + state + ". No app password needed: you sign in on Google's page in your browser and allow " +
			"read-only access. Google wants your own (free) OAuth client for that: README → \"Gmail\" shows how to make one."
		introLines = 5
		form.AddButton("Sign in with Google…", func() { ui.gmailSignIn(d, route) })
	case "imap":
		field("IMAP server (host:port)", &d.IMAPHost, 40)
		field("IMAP user", &d.IMAPUser, 40)
		secret("IMAP password", &d.IMAPPass)
		intro += "Yandex: imap.yandex.ru:993 with an app password (id.yandex.ru → Security → App passwords) and IMAP on in " +
			"Mail settings → Email clients. Mail.ru: imap.mail.ru:993 with an app password. Only read: nothing is marked, moved or deleted."
		introLines = 5
	}
	form.AddButton("Save", func() {
		d.Mailbox = mode
		if ui.update(func(c *Config) {
			c.Bandcamp = d
			c.setRoute("bandcamp", strings.TrimSpace(route))
		}) {
			close()
		}
	})
	form.AddButton("Paste links…", func() { ui.pasteBandcampLinks() })
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	help := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetText(intro)
	help.SetBorderPadding(0, 0, 1, 1)
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(help, introLines, 0, false).
		AddItem(form, 0, 1, true)
	box.SetBorder(true).SetTitle(" Bandcamp settings ")
	ui.openModal(name, box, 110, introLines+form.GetFormItemCount()+6)
}

// gmailSignIn opens Google's sign-in page in your browser and waits for it.
func (ui *tui) gmailSignIn(d BandcampCfg, route string) {
	const name = "gsignin"
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	view.SetText("Starting…")
	ctx, cancel := context.WithCancel(ui.m.ctx)
	form := tview.NewForm()
	form.AddFormItem(view)
	form.AddButton("Cancel", func() { cancel(); ui.closeModal(name) })
	form.SetCancelFunc(func() { cancel(); ui.closeModal(name) })
	form.SetBorder(true).SetTitle(" Sign in with Google ")
	ui.openModal(name, form, 110, 16)
	clientID, secret := strings.TrimSpace(d.GmailClientID), strings.TrimSpace(d.GmailClientSecret)
	go func() {
		defer cancel()
		refresh, account, err := gmailSignIn(ctx, clientID, secret, func(u string) {
			logf("Gmail sign-in page: %s", u)
			opened := "Your browser should open Google's sign-in page."
			if err := openBrowser(u); err != nil {
				opened = "Couldn't open your browser (" + err.Error() + ")."
			}
			ui.app.QueueUpdateDraw(func() {
				view.SetText(tview.Escape(opened) + " Sign in, allow read-only access, and come back here.\n\n" +
					"If it didn't open, copy this address into your browser (it's also in the Log tab; Shift+drag selects text in most terminals):\n\n[::u]" +
					tview.Escape(u) + "[::-]")
			})
		})
		ui.app.QueueUpdateDraw(func() {
			if !ui.hasModal(name) {
				return // cancelled
			}
			ui.closeModal(name)
			if err != nil {
				ui.message("Gmail sign-in didn't work", err.Error())
				return
			}
			d.GmailClientID, d.GmailClientSecret, d.GmailToken, d.GmailAccount, d.Mailbox = clientID, secret, refresh, account, "gmail"
			if d.Email == "" {
				d.Email = account
			}
			if ui.update(func(c *Config) {
				b := &c.Bandcamp
				b.GmailClientID, b.GmailClientSecret, b.GmailToken, b.GmailAccount, b.Mailbox = clientID, secret, refresh, account, "gmail"
				if b.Email == "" {
					b.Email = account
				}
			}) {
				ui.bandcampSettings(d, route)
				ui.message("Gmail", "Signed in as "+account+". Bandcamp's emails are read from this mailbox (read only).")
			}
		})
	}()
}

// pasteBandcampLinks takes download links copied from Bandcamp's emails.
func (ui *tui) pasteBandcampLinks() {
	const name = "bcpaste"
	form := tview.NewForm()
	pending, _ := ui.m.bcMail.status()
	intro := "Paste the download links from Bandcamp's emails (or the whole emails; several at once is fine), then Tab to the button."
	if pending > 0 {
		intro += fmt.Sprintf(" %d releases are waiting for theirs.", pending)
	}
	form.AddTextArea("", "", 0, 9, 0, nil)
	area := form.GetFormItem(0).(*tview.TextArea)
	status := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	status.SetBorderPadding(0, 0, 1, 1)
	busy := false
	form.AddButton("Use these links", func() {
		text := area.GetText()
		if busy || strings.TrimSpace(text) == "" {
			return
		}
		if len(findBandcampLinks(text)) == 0 {
			status.SetText("[red]No Bandcamp download links in that (they look like https://bandcamp.com/download?…).[-]")
			return
		}
		busy = true
		status.SetText("[yellow]Opening the links…[-]")
		go func() {
			found, added, errs := ui.m.AddBandcampLinks(text)
			ui.app.QueueUpdateDraw(func() {
				busy = false
				msg := fmt.Sprintf("%d links, %d new: the tracks waiting for them go on.", found, added)
				if len(errs) > 0 {
					status.SetText("[orange]" + tview.Escape(msg+" Not usable: "+strings.Join(errs, "; ")) + "[-]")
					return
				}
				ui.closeModal(name)
				ui.message("Bandcamp links", msg)
			})
		}()
	})
	form.AddButton("Cancel", func() { ui.closeModal(name) })
	form.SetCancelFunc(func() { ui.closeModal(name) })
	help := tview.NewTextView().SetWrap(true).SetText(intro)
	help.SetBorderPadding(0, 0, 1, 1)
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(help, 2, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(status, 2, 0, false)
	box.SetBorder(true).SetTitle(" Paste Bandcamp download links ")
	ui.openModal(name, box, 100, 19)
}

func (ui *tui) vkLogin() {
	form := tview.NewForm()
	var user, pass, code string
	form.AddInputField("Phone or email", "", 40, nil, func(t string) { user = t })
	form.AddPasswordField("Password (not stored)", "", 40, '*', func(t string) { pass = t })
	form.AddInputField("Confirmation code (if asked)", "", 10, nil, func(t string) { code = t })
	status := tview.NewTextView().SetDynamicColors(true)
	form.AddFormItem(status)
	close := func() { ui.closeModal("vklogin") }
	form.AddButton("Get token", func() {
		status.SetText("[yellow]Logging in…[-]")
		route := ui.m.Config().source("vk").Route
		go func() {
			ctx := ui.m.ctx
			token, needCode, err := vkLogin(ctx, strings.TrimSpace(user), pass, strings.TrimSpace(code), route)
			ui.app.QueueUpdateDraw(func() {
				switch {
				case err != nil:
					status.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
				case needCode:
					status.SetText("[yellow]VK sent a confirmation code (SMS or app): enter it and press Get token again.[-]")
				default:
					close()
					if ui.update(func(c *Config) { c.VKToken = token; c.setSource("vk", true) }) {
						ui.message("VK", "Logged in; the VK source is on.")
					}
				}
			})
		}()
	})
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" VK login (Kate Mobile token) ")
	ui.openModal("vklogin", form, 90, 13)
}

// --- settings ---------------------------------------------------------------------------------

func (ui *tui) buildSettings() tview.Primitive {
	ui.settings = tview.NewForm().SetItemPadding(0)
	ui.settings.SetBorder(true).SetTitle(" Settings ")
	ui.settings.SetCancelFunc(func() { // Esc leaves the field
		ui.settings.SetFocus(ui.settings.GetFormItemCount())
		ui.app.SetFocus(ui.settings)
	})
	ui.fillSettings()
	return ui.settings
}

func (ui *tui) fillSettings() {
	f := ui.settings
	f.Clear(true)
	c := ui.m.Config()
	var apply []func(*Config) error
	text := func(label, value string, width int, set func(*Config, string) error) *tview.InputField {
		v := value
		f.AddInputField(label, value, width, nil, func(t string) { v = t })
		apply = append(apply, func(c *Config) error { return set(c, strings.TrimSpace(v)) })
		return f.GetFormItem(f.GetFormItemCount() - 1).(*tview.InputField)
	}
	number := func(label string, value float64, width int, set func(*Config, float64)) {
		text(label, strconv.FormatFloat(value, 'f', -1, 64), width, func(c *Config, s string) error {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("%s: %q is not a number", strings.TrimSpace(label), s)
			}
			set(c, v)
			return nil
		})
	}
	out := text("Downloads folder", c.OutputDir, 70, func(c *Config, s string) error { c.OutputDir = s; return nil })
	out.SetAutocompleteFunc(func(t string) []string { return pathCompletions(t, true, nil) })
	fi := 0
	for i, x := range formats {
		if x == c.Format {
			fi = i
		}
	}
	format := c.Format
	f.AddDropDown("Format", []string{"original (no re-encoding)", "flac", "mp3 (V0)", "opus (192k)", "m4a (AAC 256k)"}, fi, func(o string, i int) { format = formats[i] })
	apply = append(apply, func(c *Config) error { c.Format = format; return nil })
	text("File layout", c.Template, 50, func(c *Config, s string) error { c.Template = s; return nil })
	ti := 0
	var tlabels []string
	for i, t := range thresholds {
		tlabels = append(tlabels, fmt.Sprintf("%s — %s", t.Name, t.Label))
		if string(t.Name) == c.Threshold {
			ti = i
		}
	}
	thr := c.Threshold
	f.AddDropDown("Good enough quality (library)", tlabels, ti, func(o string, i int) { thr = string(thresholds[i].Name) })
	apply = append(apply, func(c *Config) error { c.Threshold = thr; return nil })
	number("Bandwidth target and cap, MB/s (0: none)", c.BandwidthMBps, 8, func(c *Config, v float64) { c.BandwidthMBps = v })
	number("Most downloads at once", float64(c.MaxDownloads), 4, func(c *Config, v float64) { c.MaxDownloads = int(v) })
	number("Searches at once", float64(c.SearchWorkers), 4, func(c *Config, v float64) { c.SearchWorkers = int(v) })
	number("Results to try per source", float64(c.TriesPerSrc), 4, func(c *Config, v float64) { c.TriesPerSrc = max(1, int(v)) })
	enrich := c.Enrich
	f.AddCheckbox("Look up missing details on Deezer", c.Enrich, func(b bool) { enrich = b })
	apply = append(apply, func(c *Config) error { c.Enrich = enrich; return nil })
	iface := text("Network interface for \"direct\" (empty: detect)", c.DirectInterface, 16, func(c *Config, s string) error { c.DirectInterface = s; return nil })
	if d, err := detectPhysicalInterface(); err == nil {
		iface.SetPlaceholder(d)
	}
	ui.slskSettings(c, &apply, text, number)
	f.AddButton("Save", func() {
		trial := ui.m.Config() // check every field first, so nothing is saved half-way
		for _, a := range apply {
			if e := a(&trial); e != nil {
				ui.message("Can't save", e.Error())
				return
			}
		}
		err := ui.m.UpdateConfig(func(c *Config) {
			for _, a := range apply {
				a(c)
			}
		})
		if err != nil {
			ui.message("Can't save", err.Error())
			return
		}
		logf("Settings saved")
		ui.message("Settings", "Saved and applied.")
	})
	f.AddButton("Undo changes", func() { ui.fillSettings() })
	f.AddButton("Import Soulseek settings from Nicotine+", ui.importNicotine)
}

// slskSettings adds the Soulseek settings to the settings form.
func (ui *tui) slskSettings(c Config, apply *[]func(*Config) error, text func(string, string, int, func(*Config, string) error) *tview.InputField,
	number func(string, float64, int, func(*Config, float64))) {
	f := ui.settings
	sk := c.Soulseek
	ui.slskFieldIdx = f.GetFormItemCount()
	f.AddTextView("[::b]Soulseek[::-]", "your account, sharing and limits (turn the source on in the Sources tab)", 0, 1, true, false)
	text("Soulseek username", sk.Username, 30, func(c *Config, s string) error { c.Soulseek.Username = s; return nil })
	pass := sk.Password
	f.AddPasswordField("Soulseek password", sk.Password, 30, '*', func(t string) { pass = t })
	*apply = append(*apply, func(c *Config) error { c.Soulseek.Password = pass; return nil })
	text("Soulseek server", sk.server(), 30, func(c *Config, s string) error {
		if s == slskDefaultServer {
			s = ""
		}
		c.Soulseek.Server = s
		return nil
	})
	number("Listening port", float64(sk.port()), 6, func(c *Config, v float64) {
		c.Soulseek.Port = int(v)
		if c.Soulseek.Port == slskDefaultPort {
			c.Soulseek.Port = 0
		}
	})
	upnp := !sk.NoUPnP
	f.AddCheckbox("Ask the router to forward the port (UPnP)", upnp, func(b bool) { upnp = b })
	*apply = append(*apply, func(c *Config) error { c.Soulseek.NoUPnP = !upnp; return nil })
	number("Soulseek downloads at once (1-4)", float64(sk.downloads()), 3, func(c *Config, v float64) { c.Soulseek.Downloads = int(v) })
	number("Seconds between Soulseek searches (3 or more)", sk.searchGap().Seconds(), 5, func(c *Config, v float64) { c.Soulseek.SearchGap = v })
	text("Give up waiting in a user's queue after", fmtDurationShort(sk.queueWait()), 8, func(c *Config, s string) error {
		d, err := time.ParseDuration(s)
		if err != nil || d < time.Minute {
			return fmt.Errorf("give up waiting in a user's queue after: a duration like 5m (at least 1m)")
		}
		c.Soulseek.QueueWait = Duration(d)
		return nil
	})
	share := !sk.NoShare
	f.AddCheckbox("Share files", share, func(b bool) { share = b })
	*apply = append(*apply, func(c *Config) error { c.Soulseek.NoShare = !share; return nil })
	shares := text("Shared folders (; between them; empty: library + downloads)", strings.Join(sk.Shares, "; "), 70, func(c *Config, s string) error {
		c.Soulseek.Shares = splitBy(s, ";")
		return nil
	})
	var defaults []string
	for _, r := range c.shareRoots() {
		defaults = append(defaults, r.Path)
	}
	if len(sk.Shares) == 0 {
		shares.SetPlaceholder(strings.Join(defaults, "; "))
	}
	number("Upload slots", float64(sk.uploadSlots()), 3, func(c *Config, v float64) { c.Soulseek.UploadSlots = int(v) })
	number("Upload speed limit, KB/s (0: none)", float64(sk.UploadKBps), 7, func(c *Config, v float64) { c.Soulseek.UploadKBps = int(v) })
	number("Queue limit per user, files", float64(sk.queueFiles()), 6, func(c *Config, v float64) { c.Soulseek.QueueFiles = int(v) })
	number("Queue limit per user, MB", float64(sk.queueMB()), 7, func(c *Config, v float64) { c.Soulseek.QueueMB = int(v) })
	text("Banned users (comma-separated)", strings.Join(sk.Banned, ", "), 60, func(c *Config, s string) error {
		c.Soulseek.Banned = splitList(s)
		return nil
	})
	text("Chat rooms to join (comma-separated)", strings.Join(sk.Rooms, ", "), 60, func(c *Config, s string) error {
		c.Soulseek.Rooms = splitList(s)
		return nil
	})
	text("Description others see", sk.Description, 60, func(c *Config, s string) error { c.Soulseek.Description = s; return nil })
}

func fmtDurationShort(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "0m")
}

// importNicotine copies the account, port, shared folders and limits from Nicotine+.
func (ui *tui) importNicotine() {
	cur := ui.m.Config().Soulseek
	got, err := importNicotine(cur)
	if err != nil {
		ui.message("Nicotine+", err.Error())
		return
	}
	if !ui.update(func(c *Config) {
		c.Soulseek = got
		if got.configured() {
			c.setSource("soulseek", true)
		}
	}) {
		return
	}
	ui.fillSettings()
	shares := "your library and downloads folders"
	if len(got.Shares) > 0 {
		shares = strings.Join(got.Shares, "; ")
	}
	ui.message("Imported from Nicotine+", fmt.Sprintf("Account %s, port %d, sharing %s, %d upload slots. The Soulseek source is on.\n\n"+
		"Don't run Nicotine+ with the same account at the same time: each logs the other out.", got.Username, got.port(), shares, got.uploadSlots()))
}

// --- log ------------------------------------------------------------------------------------

func (ui *tui) buildLog() tview.Primitive {
	ui.logView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(true)
	ui.logView.SetBorder(true).SetTitle(" Log ")
	ui.logView.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyPgUp, tcell.KeyHome:
			ui.logFollow = false
		case tcell.KeyEnd:
			ui.logFollow = true
			ui.logView.ScrollToEnd()
		}
		if ev.Rune() == 'v' {
			verbose = !verbose
			logf("Detailed log %s", map[bool]string{true: "on", false: "off"}[verbose])
			return nil
		}
		return ev
	})
	return ui.logView
}

func (ui *tui) refreshLog() {
	lines, seq := appLog.snapshot()
	if seq == ui.logSeq {
		return
	}
	ui.logSeq = seq
	var b strings.Builder
	for _, l := range lines {
		color := ""
		if l.Debug {
			color = "[gray]"
		}
		fmt.Fprintf(&b, "%s[gray]%s[-] %s%s[-]\n", "", l.At.Format("15:04:05"), color, tview.Escape(l.Text))
	}
	ui.logView.SetText(b.String())
	if ui.logFollow {
		ui.logView.ScrollToEnd()
	}
}
