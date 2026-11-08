package tui

import (
	"spotify-downloader/internal/core"

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

var tabs = []struct{ name, title string }{
	{"queue", "Queue"}, {"search", "Search"}, {"library", "Library"},
	{"sources", "Sources"}, {"soulseek", "Soulseek"}, {"settings", "Settings"}, {"log", "Log"},
}

// tui is the terminal frontend. It sees the backend only through core.Backend.
type tui struct {
	m    core.Backend
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
	q           core.QueueQuery // what the queue shows
	selected    string

	// search
	findField                   *tview.InputField
	fArtist, fAlbum             *tview.InputField
	fTrack, fYear               *tview.InputField
	resultRows                  []int // results table row → result (-1: a heading)
	findInfo                    *tview.TextView
	resultsTable                *tview.Table
	results                     []core.MusicResult
	finding                     bool
	adding, addNote             string
	inputsTable, playlistsTable *tview.Table
	inputs                      []core.InputStatus
	playlists                   []core.PlaylistStatus

	// library
	libInfo  *tview.TextView
	libTable *tview.Table

	// sources
	srcTable  *tview.Table
	srcDetail *tview.TextView
	srcs      []core.SourceStatus

	// soulseek
	sk *slskUI

	// settings
	settings *tview.Form

	// log
	logView   *tview.TextView
	logSeq    int64
	logFollow bool
}

// Run runs the terminal app until you quit.
func Run(m core.Backend) error {
	ui := newTUI(m)
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
			core.Logf("Quitting (asked by a signal)")
			ui.app.Stop()
		}
	}()
	ui.refresh()
	return ui.app.SetRoot(ui.root, true).EnableMouse(true).EnablePaste(true).Run()
}

func newTUI(m core.Backend) *tui {
	ui := &tui{m: m, app: tview.NewApplication(), logFollow: true}
	ui.build()
	return ui
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
	ui.body.AddPage("search", ui.buildSearch(), true, false)
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
		start = "search"
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
	case "search":
		ui.app.SetFocus(ui.resultsTable)
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
	ui.update(func(c *core.Config) { c.BandwidthMBps = v })
}

func (ui *tui) update(change func(*core.Config)) bool {
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
	if n := o.Counts[core.JobSearching] + o.Counts[core.JobDownloading] + o.Counts[core.JobReady]; n > 0 {
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
	expanded := core.ExpandHome(text)
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
	speed := core.FmtRate(o.Own + o.Torrent)
	if o.Cap > 0 {
		speed += " of " + core.FmtRate(o.Cap)
	} else {
		speed += " (no limit)"
	}
	if o.Torrent > 1000 {
		speed += fmt.Sprintf(", torrents %s", core.FmtRate(o.Torrent))
	}
	if o.SlskOn && (o.SlskDown > 1000 || o.SlskUp > 1000) {
		speed += fmt.Sprintf(", soulseek ⇣%s ⇡%s", core.FmtRate(o.SlskDown), core.FmtRate(o.SlskUp))
	}
	todo := o.Counts[core.JobQueued] + o.Counts[core.JobSearching] + o.Counts[core.JobReady] + o.Counts[core.JobDownloading]
	line1 := fmt.Sprintf(" [::b]spotify-downloader[::-]  %s   ⇣ %s   downloads %d/%d", state, speed, o.DlUsed, o.DlLimit)
	if o.DlWaiting > 0 {
		line1 += fmt.Sprintf(" (+%d ready)", o.DlWaiting)
	}
	line1 += fmt.Sprintf("   searches %d/%d", o.SrUsed, o.SrLimit)
	line2 := fmt.Sprintf(" %d tracks   [green]✓ %d downloaded[-]   [blue]◆ %d in library[-]   · %d to do",
		o.Total, o.Counts[core.JobDone], o.Counts[core.JobInLibrary], todo)
	if n := o.Counts[core.JobWaiting]; n > 0 {
		line2 += fmt.Sprintf("   [yellow]⟳ %d retry later[-]", n)
	}
	if n := o.Counts[core.JobFailed]; n > 0 {
		line2 += fmt.Sprintf("   [red]✗ %d not found[-]", n)
	}
	if n := o.Counts[core.JobCancelled]; n > 0 {
		line2 += fmt.Sprintf("   ⊘ %d cancelled", n)
	}
	if o.Free >= 0 {
		disk := "disk " + core.FmtBytes(o.Free) + " free"
		if o.Free < 2<<30 {
			disk = "[red]" + disk + "[-]"
		}
		line2 += "   " + disk
	}
	ui.header.SetText(line1 + "\n" + line2)

	var tb strings.Builder
	unread := ui.m.SoulseekUnread()
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
	case "search":
		ui.refreshSearch()
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
		"queue":    " [::b]Enter[::-] details  [::b]c[::-] cancel  [::b]r[::-] restart  [::b]p[::-] do next  [::b]R[::-] retry all failed  [::b]f[::-] filter  [::b]o[::-] sort  [::b]O[::-] reverse  [::b]/[::-] find in queue  [::b]Esc[::-] show all",
		"search":   " [::b]/[::-] find music  [::b]Enter[::-] add result / show playlist  [::b]Tab[::-] next list  [::b]a[::-] add file/link  [::b]d[::-] remove  [::b]space[::-] on/off  [::b]+ -[::-] priority  [::b]R[::-] re-read",
		"library":  " [::b]a[::-] add folder  [::b]d[::-] remove  [::b]s[::-] rescan  [::b]t[::-] quality threshold  [::b]u[::-] upgrade files below a quality",
		"sources":  " [::b]space[::-] on/off  [::b]K J[::-] move up/down  [::b]Enter[::-] settings  [::b]e[::-] route  [::b]p[::-] pause/resume  [::b]b[::-] paste Bandcamp links",
		"soulseek": " [::b]/[::-] search  [::b]Enter[::-] download  [::b]d[::-] folder  [::b]Tab[::-] next pane  [::b]n[::-] message  [::b]j[::-] join room  [::b]l[::-] leave/close  [::b]m[::-] message user  [::b]x[::-] stop upload  [::b]B[::-] ban  [::b]o[::-] port  [::b]R[::-] reconnect",
		"settings": " [::b]Tab ↑ ↓[::-] move between fields  [::b]Esc[::-] leave a field  Save applies the changes right away",
		"log":      " [::b]↑ ↓ PgUp PgDn[::-] scroll  [::b]End[::-] follow  [::b]v[::-] details " + onOff(core.Verbose),
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

func headerRow(t *tview.Table, cols ...string) {
	for i, c := range cols {
		t.SetCell(0, i, tview.NewTableCell(c).SetSelectable(false).SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow))
	}
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
