package tui

import (
	"spotify-downloader/internal/core"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func screenText(s tcell.SimulationScreen) string {
	cells, w, h := s.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) > 0 {
				b.WriteRune(c.Runes[0])
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// The app starts, shows every tab, and keys reach the right places.
func TestTUIPages(t *testing.T) {
	tools := core.NeedFFmpeg(t)
	a := core.GetLucky()
	a.Key, a.DurationMs = "a", 4000
	b := core.MakeTrack("Nonexistent [Demo]", []string{"Nobody"}, "", 4)
	b.Key = "b"
	src := &core.FakeSource{SrcName: "youtube", T: t, Results: map[string][]core.Candidate{"a": core.Found("youtube", "Get Lucky", "Daft Punk", 4)}}
	m := core.TestManager(t, tools, []core.Source{src}, core.TestLibrary(a, b), func(c *core.Config) {
		c.Inputs = []core.InputCfg{{Path: "/tmp/playlists.csv", Enabled: true}}
	})
	core.RunManager(t, m)

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	ui := newTUI(m)
	ui.app.SetScreen(screen)
	done := make(chan error)
	go func() { done <- ui.app.SetRoot(ui.root, true).Run() }()
	time.Sleep(100 * time.Millisecond)
	// A small terminal first: the details window must fit.
	screen.SetSize(80, 24)
	screenText(screen)
	show := func() string {
		ui.app.QueueUpdateDraw(ui.refresh)
		time.Sleep(150 * time.Millisecond)
		var s string
		ui.app.QueueUpdate(func() { s = screenText(screen) }) // not while it draws
		return s
	}
	key := func(r rune) {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
		time.Sleep(80 * time.Millisecond)
	}
	text := show()
	for _, want := range []string{"Daft Punk - Get Lucky", "Nobody - Nonexistent [Demo]", "1 downloaded", "Queue", "Settings"} {
		if !strings.Contains(text, want) {
			t.Errorf("queue page lacks %q:\n%s", want, text)
		}
	}
	key('f') // the filter list
	if text = show(); !strings.Contains(text, "downloaded in lossless quality") {
		t.Errorf("filter list:\n%s", text)
	}
	for i := 0; i < 6; i++ { // down to "downloaded"
		screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	}
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	if text = show(); !strings.Contains(text, "downloaded (f)") || strings.Contains(text, "Nonexistent") {
		t.Errorf("filter:\n%s", text)
	}
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	if text = show(); !strings.Contains(text, "║File: ") || !strings.Contains(text, "Get Lucky") {
		t.Errorf("details:\n%s", text)
	}
	screen.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	key('o') // sort list
	screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	key('O')
	if text = show(); !strings.Contains(text, "sorted by artist,") {
		t.Errorf("sort:\n%s", text)
	}
	screen.SetSize(150, 40)
	pages := map[rune][]string{
		'2': {"Find:", "Playlist files", "/tmp/playlists.csv", "Liked Songs"},
		'3': {"Good enough quality", "No library folders yet"},
		'4': {"Sources, tried top to bottom", "Bandcamp", "Yandex Music", "direct"},
		'5': {"Soulseek is off", "Sources tab → Soulseek", "Search Soulseek", "Transfers", "Chats"},
		'6': {"Downloads folder", "Bandwidth target", "Save"},
		'7': {"Log"},
	}
	for _, k := range []rune{'2', '3', '4', '5', '6', '7'} {
		key(k)
		text = show()
		for _, want := range pages[k] {
			if !strings.Contains(text, want) {
				t.Errorf("page %c lacks %q:\n%s", k, want, text)
			}
		}
	}
	// Bandwidth keys work anywhere outside text fields.
	key('7')
	key(']')
	key(']')
	time.Sleep(100 * time.Millisecond)
	if got := m.Config().BandwidthMBps; got != 1 {
		t.Errorf("bandwidth after ] ]: %v\n%s", got, show())
	}
	key('q')
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(3 * time.Second):
		ui.app.Stop()
		t.Error("q + Enter didn't quit")
	}
}

// Adding a library folder: suggestions show as you type, Tab and Enter pick them without
// freezing the app, Esc closes the list and then the window, clicks beside it go nowhere.
func TestTUIAddLibraryFolder(t *testing.T) {
	tools := core.NeedFFmpeg(t)
	a := core.GetLucky()
	a.Key = "a"
	src := &core.FakeSource{SrcName: "youtube", T: t, Results: map[string][]core.Candidate{}}
	m := core.TestManager(t, tools, []core.Source{src}, core.TestLibrary(a), nil)
	core.RunManager(t, m)
	music := t.TempDir()
	for _, d := range []string{"Music Alpha", "Music Beta", "Other"} {
		if err := os.Mkdir(filepath.Join(music, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	d := startUI(t, m)
	ui, screen, settle, typeText, press := d.ui, d.screen, d.settle, d.typeText, d.press
	inputText := d.focusedText

	typeText("3a")
	if !ui.hasModal("path") {
		t.Fatal("a didn't open the folder window")
	}
	// Esc closes the suggestions first, then the window.
	typeText(music + "/Mu")
	press(tcell.KeyEscape, "Esc on the suggestions")
	if !ui.hasModal("path") {
		t.Fatal("the first Esc should only close the suggestions")
	}
	press(tcell.KeyEscape, "Esc")
	if ui.hasModal("path") {
		t.Fatal("the second Esc didn't close the window")
	}

	// Tab takes the highlighted suggestion and goes into it; Enter accepts.
	typeText("a")
	typeText(music + "/Mu")
	press(tcell.KeyDown, "↓")
	press(tcell.KeyTab, "Tab on a suggestion") // froze the app before
	if got, want := inputText(), music+"/Music Beta/"; got != want {
		t.Fatalf("after Tab: %q, want %q", got, want)
	}
	// A click beside the window must not reach the tabs under it.
	screen.InjectMouse(3, 2, tcell.Button1, tcell.ModNone)
	screen.InjectMouse(3, 2, tcell.ButtonNone, tcell.ModNone)
	settle("a click")
	if ui.current != "library" || !ui.hasModal("path") {
		t.Fatalf("a click beside the window switched to %s (window open: %v)", ui.current, ui.hasModal("path"))
	}
	press(tcell.KeyEnter, "Enter")
	if ui.hasModal("path") {
		t.Fatalf("Enter didn't accept %q:\n%s", inputText(), d.text())
	}
	want := filepath.Join(music, "Music Beta")
	deadline := time.Now().Add(2 * time.Second)
	for !core.ContainsString(m.Config().LibraryDirs, want) {
		if time.Now().After(deadline) {
			t.Fatalf("library folders: %v, want %s", m.Config().LibraryDirs, want)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// With the suggestions closed, Tab completes like a shell.
	typeText("a")
	typeText(music + "/Ot")
	press(tcell.KeyEscape, "Esc on the suggestions")
	press(tcell.KeyTab, "Tab")
	if got := inputText(); got != music+"/Other/" {
		t.Fatalf("Tab completed to %q", got)
	}
	press(tcell.KeyEscape, "Esc")
	// Enter on a suggestion that's already typed out accepts it.
	typeText("a")
	typeText(music + "/Other")
	press(tcell.KeyEnter, "Enter on the suggestion")
	settle("accepting")
	deadline = time.Now().Add(2 * time.Second)
	for !core.ContainsString(m.Config().LibraryDirs, filepath.Join(music, "Other")) {
		if time.Now().After(deadline) {
			t.Fatalf("library folders: %v", m.Config().LibraryDirs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The keys reach the library page again.
	typeText("t")
	if !ui.hasModal("threshold") {
		t.Errorf("t after adding a folder didn't reach the library page (focus on %T)", ui.app.GetFocus())
	}
	press(tcell.KeyEscape, "Esc")
	if len(ui.modals) != 0 {
		t.Errorf("windows left open: %v", ui.modals)
	}
}

// uiDriver runs the app on a simulated screen; a frozen event loop fails the test instead of
// hanging it.
type uiDriver struct {
	t      *testing.T
	ui     *tui
	screen tcell.SimulationScreen
	synced chan chan struct{}
}

func startUI(t *testing.T, m *core.Manager) *uiDriver {
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	ui := newTUI(m)
	ui.app.SetScreen(screen)
	// Keys are handled in order: a marker key after them tells when they all have been.
	d := &uiDriver{t: t, ui: ui, screen: screen, synced: make(chan chan struct{}, 1)}
	keys := ui.app.GetInputCapture()
	ui.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyF64 {
			close(<-d.synced)
			return nil
		}
		return keys(ev)
	})
	done := make(chan error, 1)
	go func() { done <- ui.app.SetRoot(ui.root, true).Run() }()
	time.Sleep(50 * time.Millisecond)
	screen.SetSize(120, 35)
	t.Cleanup(func() {
		go ui.app.Stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second): // frozen: leave it
		}
	})
	return d
}

// keys sends keys to the app and waits until it has handled them all.
func (d *uiDriver) keys(what string, evs ...*tcell.EventKey) {
	d.t.Helper()
	ok := make(chan struct{})
	d.synced <- ok
	sent := make(chan struct{})
	go func() {
		for _, ev := range evs {
			d.ui.app.QueueEvent(ev)
		}
		d.ui.app.QueueEvent(tcell.NewEventKey(tcell.KeyF64, 0, tcell.ModNone))
		close(sent)
	}()
	select {
	case <-ok:
	case <-time.After(5 * time.Second):
		d.t.Fatalf("the app froze after %s", what)
	}
	<-sent
	d.onUI(what, func() {}) // and drawn
}

// onUI runs f on the app's event loop.
func (d *uiDriver) onUI(what string, f func()) {
	d.t.Helper()
	ok := make(chan struct{})
	go d.ui.app.QueueUpdateDraw(func() { f(); close(ok) })
	select {
	case <-ok:
	case <-time.After(3 * time.Second):
		d.t.Fatalf("the app froze after %s", what)
	}
}

func (d *uiDriver) settle(what string) {
	d.t.Helper()
	time.Sleep(40 * time.Millisecond) // the screen hands keys over on its own goroutine
	d.onUI(what, func() {})
	d.onUI(what, func() {})
}

func (d *uiDriver) typeText(s string) {
	d.t.Helper()
	var evs []*tcell.EventKey
	for _, r := range s {
		evs = append(evs, tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	d.keys("typing "+s, evs...)
}

func (d *uiDriver) press(k tcell.Key, what string) {
	d.t.Helper()
	d.keys(what, tcell.NewEventKey(k, 0, tcell.ModNone))
}

func (d *uiDriver) focusedText() string {
	var s string
	d.onUI("reading the focused field", func() {
		if in, ok := d.ui.app.GetFocus().(*tview.InputField); ok {
			s = in.GetText()
		} else {
			s = fmt.Sprintf("(focus on %T)", d.ui.app.GetFocus())
		}
	})
	return s
}

func (d *uiDriver) text() string {
	d.settle("drawing")
	var s string
	d.onUI("reading the screen", func() { s = screenText(d.screen) }) // not while it draws
	return s
}

// Bandcamp's settings: the mailbox choice shows its own fields; Save keeps them; b opens the
// window for pasting links.
func TestTUIBandcampSettings(t *testing.T) {
	tools := core.NeedFFmpeg(t)
	a := core.GetLucky()
	a.Key = "a"
	bc := &core.FakeSource{SrcName: "bandcamp", T: t, Results: map[string][]core.Candidate{}}
	m := core.TestManager(t, tools, []core.Source{bc}, core.TestLibrary(a), func(c *core.Config) {
		c.Bandcamp.Email = "me@example.com"
	})
	core.RunManager(t, m)
	d := startUI(t, m)
	d.typeText("4")
	d.onUI("selecting Bandcamp", func() {
		for i, s := range d.ui.srcs {
			if s.Name == "bandcamp" {
				d.ui.srcTable.Select(i+1, 0)
			}
		}
	})
	text := d.text()
	if !strings.Contains(text, "Bandcamp emails the links to me@example.com; you paste them") {
		t.Errorf("Bandcamp's details should say where the links come from:\n%s", text)
	}
	d.press(tcell.KeyEnter, "Enter on Bandcamp")
	if text = d.text(); !d.ui.hasModal("srcsettings") || !strings.Contains(text, "I paste them from the emails") {
		t.Fatalf("Bandcamp settings:\n%s", text)
	}
	// Choose Gmail: its fields appear.
	d.onUI("choosing Gmail", func() {
		form := d.ui.root.GetPage("srcsettings").(modalLayer).Primitive
		var dd *tview.DropDown
		walkForm(form, func(p tview.Primitive) {
			if x, ok := p.(*tview.DropDown); ok {
				dd = x
			}
		})
		dd.SetCurrentOption(1)
	})
	if text = d.text(); !strings.Contains(text, "OAuth client ID") || !strings.Contains(text, "not signed in") ||
		!strings.Contains(text, "Sign in with Google") || len(d.ui.modals) != 1 {
		t.Fatalf("Gmail fields (windows %v):\n%s", d.ui.modals, text)
	}
	if got := d.focusedText(); strings.HasPrefix(got, "(focus on") && !strings.Contains(got, "DropDown") && !strings.Contains(got, "Form") {
		t.Errorf("focus after rebuilding the window: %s", got)
	}
	d.typeText("") // nothing
	d.onUI("saving", func() {
		form := d.ui.root.GetPage("srcsettings").(modalLayer).Primitive
		walkForm(form, func(p tview.Primitive) {
			if f, ok := p.(*tview.Form); ok {
				if i := f.GetButtonIndex("Save"); i >= 0 {
					f.SetFocus(f.GetFormItemCount() + i)
					d.ui.app.SetFocus(f)
				}
			}
		})
	})
	d.press(tcell.KeyEnter, "Save")
	if d.ui.hasModal("srcsettings") {
		t.Fatalf("Save didn't close the window:\n%s", d.text())
	}
	core.WaitFor(t, func() bool { return m.Config().Bandcamp.Mailbox == "gmail" })
	if c := m.Config().Bandcamp; c.Email != "me@example.com" {
		t.Errorf("email lost: %+v", c)
	}
	// b: the paste window; text without links is refused.
	d.typeText("b")
	if !d.ui.hasModal("bcpaste") {
		t.Fatal("b didn't open the paste window")
	}
	d.typeText("no links here")
	d.press(tcell.KeyTab, "Tab to the button")
	d.press(tcell.KeyEnter, "Use these links")
	if text = d.text(); !strings.Contains(text, "No Bandcamp download links") {
		t.Errorf("paste window:\n%s", text)
	}
	d.press(tcell.KeyEscape, "Esc")
	if len(d.ui.modals) != 0 {
		t.Errorf("windows left open: %v", d.ui.modals)
	}
}

// walkForm calls f for p and everything inside it (flexes and forms).
func walkForm(p tview.Primitive, f func(tview.Primitive)) {
	f(p)
	switch x := p.(type) {
	case *tview.Flex:
		for i := 0; i < x.GetItemCount(); i++ {
			if it := x.GetItem(i); it != nil {
				walkForm(it, f)
			}
		}
	case *tview.Form:
		for i := 0; i < x.GetFormItemCount(); i++ {
			walkForm(x.GetFormItem(i), f)
		}
	}
}

// Nothing is drawn on black, so a translucent terminal shows through; selected rows keep
// their dark text on a light bar.
func TestTUITransparentBackground(t *testing.T) {
	tools := core.NeedFFmpeg(t)
	a := core.GetLucky()
	a.Key = "a"
	src := &core.FakeSource{SrcName: "youtube", T: t, Results: map[string][]core.Candidate{"a": core.Found("youtube", "Get Lucky", "Daft Punk", 4)}}
	m := core.TestManager(t, tools, []core.Source{src}, core.TestLibrary(a), func(c *core.Config) {
		c.Inputs = []core.InputCfg{{Path: "/tmp/playlists.csv", Enabled: true}}
	})
	core.RunManager(t, m)
	d := startUI(t, m)
	if text := d.text(); !strings.Contains(text, "Daft Punk - Get Lucky") {
		t.Fatalf("queue not shown:\n%s", text)
	}
	var black, selected int
	d.onUI("reading the screen", func() {
		cells, _, _ := d.screen.GetContents()
		for _, c := range cells {
			fg, bg, _ := c.Style.Decompose()
			if bg == tcell.ColorBlack {
				black++
			}
			if bg != tcell.ColorDefault && fg == tcell.ColorBlack {
				selected++
			}
		}
	})
	if black > 0 {
		t.Errorf("%d cells drawn on black", black)
	}
	if selected == 0 {
		t.Error("no selected row drawn with dark text")
	}
}

// Clicks outside the queue's cells (below the last row, right of the last column) make tview
// ask the table content for row or column -1; that used to crash the app.
func TestQueueClickOutsideCells(t *testing.T) {
	content := &queueContent{rows: []core.JobView{{Key: "a", Track: core.MakeTrack("Get Lucky", []string{"Daft Punk"}, "RAM", 4)}}}
	table := tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).SetContent(content)
	screen := tcell.NewSimulationScreen("")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(200, 10)
	table.SetRect(0, 0, 200, 10)
	table.Draw(screen)

	click := table.MouseHandler()
	for _, at := range [][2]int{{5, 8}, {199, 0}, {199, 1}, {199, 8}} {
		ev := tcell.NewEventMouse(at[0], at[1], tcell.Button1, 0)
		click(tview.MouseLeftClick, ev, func(tview.Primitive) {})
		table.Draw(screen)
	}
	for _, rc := range [][2]int{{-1, 0}, {0, -1}, {1, -1}, {0, len(queueHeaders)}, {2, 0}} {
		if c := content.GetCell(rc[0], rc[1]); c != nil {
			t.Errorf("GetCell(%d, %d) = %q, want nil", rc[0], rc[1], c.Text)
		}
	}
}
