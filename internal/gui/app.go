// Package gui is the app's window, in Qt (through the miqt bindings), over the same backend
// as the terminal app (core.Backend). It only reads views and calls methods of the backend;
// Watch says what changed, and a timer redraws what moves all the time (progress, speeds).
//
// The terminal app is for people who like keys and columns; the window is for everyone else.
// It does everything the terminal app does, but says it in plain words, explains itself, and
// leads with what most people want: find music, watch it arrive.
package gui

import (
	"context"
	"fmt"
	"html"
	"os"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// Version is set by the program that opens the window.
var Version = "dev"

// shotsHook takes over after the window is built, in the screenshot tool (build tag guishots).
var shotsHook func(*app)

const appName = "Universal Music Downloader"

type pageID int

const (
	pageDashboard pageID = iota
	pageDownloads
	pageFind
	pagePlaylists
	pageLibrary
	pageSort
	pageSources
	pageSoulseek
	pageSettings
	pageActivity
	pageHelp
	pageCount
)

type navItem struct {
	b     *qt.QPushButton
	badge *qt.QLabel
}

type app struct {
	m      core.Backend
	qapp   *qt.QApplication
	win    *qt.QMainWindow
	stack  *qt.QStackedWidget
	nav    [pageCount]navItem
	assets string // pictures the style sheet refers to
	ui     *uiState

	themed   []func()
	themeSet bool
	sysDark  bool

	status, speed, free *qt.QLabel
	pause               *qt.QPushButton
	toastLabel          *qt.QLabel
	toastTimer          *qt.QTimer

	tray      *qt.QSystemTrayIcon
	trayCheck int
	trayShow  *qt.QAction
	trayStop  *qt.QAction
	hadWork   bool
	unread    int
	quitting  bool

	dash     *dashboardTab
	queue    *queueTab
	search   *searchTab
	lists    *playlistsTab
	library  *libraryTab
	sorter   *sortTab
	sources  *sourcesTab
	soulseek *soulseekTab
	settings *settingsTab
	log      *logTab
}

// Run opens the window and returns when it's closed. The backend is started already.
func Run(m core.Backend) error {
	a := newApp(m)
	defer a.close()
	stop := a.begin()
	defer stop()
	if shotsHook != nil {
		shotsHook(a)
		return nil
	}
	a.win.Show()
	if m.NeedsSetup() {
		a.runSetup()
	}
	a.queue.focus()
	qt.QApplication_Exec()
	return nil
}

// newApp builds the window, hidden.
func newApp(m core.Backend) *app {
	a := &app{m: m}
	a.qapp = qt.NewQApplication(os.Args)
	qt.QCoreApplication_SetApplicationName(appName)
	qt.QGuiApplication_SetApplicationDisplayName(appName)
	qt.QGuiApplication_SetDesktopFileName("universal-music-downloader")
	qt.QGuiApplication_SetQuitOnLastWindowClosed(false) // the tray may keep the app running
	qt.QApplication_SetStyleWithStyle("Fusion")
	initFonts()
	a.assets, _ = os.MkdirTemp("", "umd-theme-*")
	sys := qt.QGuiApplication_Palette().ColorWithCr(qt.QPalette__Window)
	a.sysDark = sys.Lightness() < 128 // what Qt thinks until the desktop answers
	ctx, cancel := context.WithTimeout(m.Context(), 1500*time.Millisecond)
	if dark, known := systemPrefersDark(ctx); known {
		a.sysDark = dark
	}
	cancel()
	a.ui = loadUIState()
	a.applyTheme()
	a.build()
	a.setupTray() // settings ask whether there's a tray when they're shown
	return a
}

// close cleans up after the window.
func (a *app) close() {
	if a.tray != nil {
		a.tray.Hide()
	}
	if a.assets != "" {
		os.RemoveAll(a.assets)
	}
}

// begin starts following the backend's changes; the result stops that.
func (a *app) begin() func() {
	stop := a.m.Watch(func(k core.EventKind) {
		mainthread.Start(func() { a.changed(k) })
	})
	tick := qt.NewQTimer2(a.win.QObject)
	tick.OnTimeout(a.tick)
	tick.Start(700)
	a.watchSystemTheme()
	a.tick()
	return stop
}

func (a *app) build() {
	a.win = qt.NewQMainWindow2()
	a.win.SetWindowTitle(appName)
	a.win.Resize(max(a.ui.Width, 1240), max(a.ui.Height, 800))
	if a.ui.Width > 0 {
		a.win.Resize(a.ui.Width, a.ui.Height)
	}
	a.win.SetMinimumSize2(1040, 720)
	a.onTheme(func() { a.win.SetWindowIcon(appIcon()) })

	central := qt.NewQWidget2()
	root := qt.NewQHBoxLayout(central)
	root.SetContentsMargins(0, 0, 0, 0)
	root.SetSpacing(0)

	a.stack = qt.NewQStackedWidget2()
	root.AddWidget(a.buildSidebar())
	root.AddWidget2(a.stack.QWidget, 1)
	a.win.SetCentralWidget(central)

	a.dash = newDashboardTab(a)
	a.queue = newQueueTab(a)
	a.search = newSearchTab(a)
	a.lists = newPlaylistsTab(a)
	a.library = newLibraryTab(a)
	a.sorter = newSortTab(a)
	a.sources = newSourcesTab(a)
	a.soulseek = newSoulseekTab(a)
	a.settings = newSettingsTab(a)
	a.log = newLogTab(a)
	for _, w := range []*qt.QWidget{a.dash.w, a.queue.w, a.search.w, a.lists.w, a.library.w, a.sorter.w, a.sources.w, a.soulseek.w, a.settings.w, a.log.w, a.buildHelpPage()} {
		a.stack.AddWidget(w)
	}

	a.toastLabel = qt.NewQLabel3("")
	a.toastLabel.SetParent(central)
	setProp(a.toastLabel.QWidget, "role", "toast")
	a.toastLabel.Hide()
	a.toastTimer = qt.NewQTimer2(a.win.QObject)
	a.toastTimer.SetSingleShot(true)
	a.toastTimer.OnTimeout(func() { a.toastLabel.Hide() })

	a.show(pageDashboard)
	a.shortcuts()
	a.win.OnCloseEvent(func(super func(*qt.QCloseEvent), e *qt.QCloseEvent) {
		if !a.quitting && a.m.Config().GUI.CloseToTray && a.tray != nil && a.tray.IsVisible() {
			a.win.Hide()
			e.Ignore()
			return
		}
		super(e)
		a.quitting = true
		a.rememberWindow()
		qt.QCoreApplication_Quit()
	})
}

func (a *app) buildSidebar() *qt.QWidget {
	bar := qt.NewQFrame2()
	bar.SetObjectName(*qt.NewQAnyStringView3("sidebar"))
	bar.SetFixedWidth(224)
	box := qt.NewQVBoxLayout(bar.QWidget)
	box.SetContentsMargins(12, 16, 12, 14)
	box.SetSpacing(2)

	brand := hbox(6, 0, 0, 8)
	brand.SetSpacing(10)
	logo := qt.NewQLabel2()
	a.onTheme(func() { logo.SetPixmap(logoPixmap(30)) })
	brand.AddWidget(logo.QWidget)
	name := text("Universal Music\nDownloader", "")
	name.SetWordWrap(false)
	name.SetStyleSheet("font-weight: 700;")
	brand.AddWidget2(name.QWidget, 1)
	box.AddLayout(brand.QLayout)

	group := qt.NewQButtonGroup2(bar.QObject)
	group.SetExclusive(true)
	item := func(id pageID, icon, label, tip string) {
		b := button("  "+label, "", func() { a.show(id) })
		setProp(b.QWidget, "nav", "true")
		b.SetCheckable(true)
		b.SetToolTip(tip)
		group.AddButton(b.QAbstractButton)
		a.onTheme(func() { b.SetIcon(iconTwo(icon, 18, th.Text2, th.Accent)) })
		b.SetIconSize(qt.NewQSize2(18, 18))
		inner := qt.NewQHBoxLayout(b.QWidget)
		inner.SetContentsMargins(0, 0, 8, 0)
		inner.AddStretch()
		badge := qt.NewQLabel3("")
		setProp(badge.QWidget, "role", "badge")
		badge.SetAttribute(qt.WA_TransparentForMouseEvents)
		badge.SetFixedHeight(18)
		badge.Hide()
		inner.AddWidget3(badge.QWidget, 0, qt.AlignVCenter)
		a.nav[id] = navItem{b, badge}
		box.AddWidget(b.QWidget)
	}
	head := func(s string) {
		l := qt.NewQLabel3(s)
		setProp(l.QWidget, "role", "navhead")
		box.AddWidget(l.QWidget)
	}
	head("MUSIC")
	item(pageDashboard, "gauge", "Dashboard", "Speeds, progress and disk use at a glance (Ctrl+1)")
	item(pageDownloads, "download", "Downloads", "Everything being downloaded, waiting or finished (Ctrl+2)")
	item(pageFind, "search", "Find music", "Search for songs, albums and artists (Ctrl+3)")
	item(pagePlaylists, "playlist", "Playlists", "Bring in your playlists from Spotify and other places (Ctrl+4)")
	item(pageLibrary, "library", "My library", "The music you already have, so it isn't downloaded twice (Ctrl+5)")
	item(pageSort, "merge", "Sort && merge", "Tidy your library and downloads, and move duplicates aside (Ctrl+6)")
	head("CONNECTIONS")
	item(pageSources, "sources", "Sources", "The services the app downloads from, and their order (Ctrl+7)")
	item(pageSoulseek, "people", "Soulseek", "The Soulseek file-sharing network: search, chat, transfers (Ctrl+8)")
	head("APP")
	item(pageSettings, "settings", "Settings", "Where files go, quality, speed and the app's look (Ctrl+9)")
	item(pageActivity, "log", "Activity", "A running record of what the app is doing (Ctrl+0)")
	item(pageHelp, "help", "Help", "How the app works, in plain words (F1)")

	box.AddStretch()
	box.AddWidget(rule().QWidget)
	box.AddSpacing(8)
	a.status = richText("", "")
	box.AddWidget(a.status.QWidget)
	a.speed = text("", "hint")
	box.AddWidget(a.speed.QWidget)
	a.free = text("", "muted")
	box.AddWidget(a.free.QWidget)
	box.AddSpacing(8)
	a.pause = a.iconButton("pause", "Pause everything", "", "Stop all downloads for now (Shift+P). Nothing is lost.", a.togglePause)
	box.AddWidget(a.pause.QWidget)
	return bar.QWidget
}

func (a *app) shortcuts() {
	add := func(key string, do func()) {
		sc := qt.NewQShortcut2(qt.NewQKeySequence2(key), a.win.QObject)
		sc.OnActivated(do)
	}
	for i := pageDashboard; i < pageActivity; i++ {
		i := i
		add(fmt.Sprintf("Ctrl+%d", int(i)+1), func() { a.show(i) })
	}
	add("Ctrl+0", func() { a.show(pageActivity) })
	add("F1", func() { a.show(pageHelp) })
	add("Shift+P", a.togglePause)
	add("Ctrl+Q", a.quit)
	add("Ctrl+F", func() {
		a.show(pageFind)
		a.search.find.SetFocus()
		a.search.find.SelectAll()
	})
}

// quit leaves the app even when closing keeps it in the tray.
func (a *app) quit() {
	a.quitting = true
	a.rememberWindow()
	a.win.Close()
	qt.QCoreApplication_Quit()
}

// rememberWindow keeps the window's size for next time.
func (a *app) rememberWindow() {
	a.ui.Maximized = a.win.IsMaximized()
	if !a.ui.Maximized {
		a.ui.Width, a.ui.Height = a.win.Width(), a.win.Height()
	}
	a.ui.save()
}

func (a *app) togglePause() {
	a.m.SetPaused(!a.m.Paused())
	a.tick()
}

// show switches to a page.
func (a *app) show(id pageID) {
	a.stack.SetCurrentIndex(int(id))
	a.stack.CurrentWidget().SetFocus() // not to the first button on the page
	a.nav[id].b.SetChecked(true)
	switch id {
	case pageDashboard:
		a.dash.refresh()
	case pageSort:
		a.sorter.refresh()
	case pageDownloads:
		a.queue.refresh(false)
		a.queue.focus()
	case pageFind:
		a.search.find.SetFocus()
	case pagePlaylists:
		a.lists.refresh()
	case pageLibrary:
		a.library.refresh()
	case pageSources:
		a.sources.refresh()
	case pageSoulseek:
		a.soulseek.shown()
	case pageSettings:
		a.settings.load()
		a.settings.readCaches()
	case pageActivity:
		a.log.refresh()
	}
}

func (a *app) current() pageID { return pageID(a.stack.CurrentIndex()) }

// changed redraws what an event says changed.
func (a *app) changed(k core.EventKind) {
	switch k {
	case core.EventQueue, core.EventConfig:
		a.queue.changed()
		switch a.current() {
		case pagePlaylists:
			a.lists.reread.poke()
		case pageLibrary:
			a.library.refresh()
		case pageSources:
			if k == core.EventConfig {
				a.sources.refresh()
			}
		case pageSettings:
			if k == core.EventConfig {
				a.settings.load()
			}
		}
		if k == core.EventConfig {
			a.applyTheme()
		}
	case core.EventSources:
		if a.current() == pageSources {
			a.sources.refresh()
		}
	case core.EventSoulseek:
		a.soulseek.changed()
	case core.EventLog:
		if a.current() == pageActivity {
			a.log.refresh()
		}
	case core.EventSort:
		if a.current() == pageSort {
			a.sorter.refresh()
		}
	}
}

// tick redraws what moves all the time.
func (a *app) tick() {
	if !a.win.IsVisible() && !a.quitting { // in the tray: nothing to draw, only to tell
		o := a.m.Overview()
		c := o.Counts
		a.notify(o, c[core.JobSearching]+c[core.JobReady]+c[core.JobDownloading]+c[core.JobQueued]+c[core.JobWaiting])
		a.checkTray()
		return
	}
	o := a.m.Overview()
	dot := func(color, label string) string {
		return fmt.Sprintf("<span style='color:%s'>●</span>&nbsp; <b>%s</b>", color, label)
	}
	c := o.Counts
	active := c[core.JobSearching] + c[core.JobReady] + c[core.JobDownloading]
	todo := active + c[core.JobQueued] + c[core.JobWaiting]
	state := dot(th.OK, "Ready")
	switch {
	case o.Paused:
		state = dot(th.Warn, "Paused")
	case !o.Ready:
		state = dot(th.Warn, "Starting…")
	case o.Importing:
		state = dot(th.Warn, "Reading playlists…")
	case active > 0:
		state = dot(th.Accent, "Working")
	case todo > 0:
		state = dot(th.Info, "Waiting")
	}
	a.status.SetText(state)
	if o.Paused {
		a.pause.SetText(" Resume")
		a.pause.SetIcon(iconOf("play", 16, th.Text))
	} else {
		a.pause.SetText(" Pause everything")
		a.pause.SetIcon(iconOf("pause", 16, th.Text))
	}
	if a.tray != nil {
		if o.Paused {
			a.trayStop.SetText("Resume")
		} else {
			a.trayStop.SetText("Pause everything")
		}
		a.tray.SetToolTip(fmt.Sprintf("%s: %d of %d tracks done", appName, c[core.JobDone]+c[core.JobInLibrary], o.Total))
	}
	speed := fmt.Sprintf("↓ %s", core.FmtRate(o.Own+o.Torrent))
	if o.Cap > 0 {
		speed += " · limit " + core.FmtRate(o.Cap)
	}
	if o.SlskOn && o.SlskUp > 0 {
		speed += " · ↑ " + core.FmtRate(o.SlskUp)
	}
	a.speed.SetText(speed)
	a.free.SetVisible(o.Free > 0)
	if o.Free > 0 {
		a.free.SetText(core.FmtBytes(o.Free) + " free on disk")
	}
	a.setBadge(pageDownloads, active)
	a.dash.sample(o)
	if a.current() == pageDashboard {
		a.dash.tick(o)
	}
	a.queue.tick(o)
	a.soulseek.tick()
	a.notify(o, todo)
	a.checkTray()
}

func (a *app) setBadge(id pageID, n int) {
	b := a.nav[id].badge
	b.SetVisible(n > 0)
	if n > 0 {
		b.SetText(fmt.Sprint(n))
	}
}

// --- tray and notifications ---------------------------------------------------------------------

// checkTray puts the icon up if a tray has appeared since (panels often start after apps do).
func (a *app) checkTray() {
	a.trayCheck++
	if a.tray == nil && a.trayCheck%8 == 0 {
		a.setupTray()
	}
}

func (a *app) setupTray() {
	if a.tray != nil || !qt.QSystemTrayIcon_IsSystemTrayAvailable() {
		return
	}
	a.tray = qt.NewQSystemTrayIcon4(appIcon(), a.win.QObject)
	menu := qt.NewQMenu2()
	a.trayShow = menu.AddActionWithText("Open " + appName)
	a.trayShow.OnTriggered(a.raise)
	a.trayStop = menu.AddActionWithText("Pause everything")
	a.trayStop.OnTriggered(a.togglePause)
	menu.AddSeparator()
	menu.AddActionWithText("Quit").OnTriggered(a.quit)
	a.tray.SetContextMenu(menu)
	a.tray.OnActivated(func(r qt.QSystemTrayIcon__ActivationReason) {
		if r == qt.QSystemTrayIcon__Trigger || r == qt.QSystemTrayIcon__DoubleClick {
			if a.win.IsVisible() && a.win.IsActiveWindow() {
				a.win.Hide()
			} else {
				a.raise()
			}
		}
	})
	a.tray.Show()
	if a.settings != nil {
		a.settings.load()
	}
}

func (a *app) raise() {
	a.win.Show()
	a.win.ShowNormal()
	a.win.Raise()
	a.win.ActivateWindow()
}

// notify tells the desktop when the queue has finished and when a private message comes.
func (a *app) notify(o core.Overview, todo int) {
	if todo > 0 {
		a.hadWork = true
	}
	unread := a.m.SoulseekUnread()
	if a.tray != nil && !a.m.Config().GUI.NoNotify {
		if a.hadWork && todo == 0 && o.Ready && !o.Importing {
			c := o.Counts
			a.tray.ShowMessage2("Downloads finished", fmt.Sprintf("%d downloaded, %d already in your library, %d not found.", c[core.JobDone], c[core.JobInLibrary], c[core.JobFailed]))
		}
		if unread > a.unread && !a.win.IsActiveWindow() {
			a.tray.ShowMessage2("Soulseek", "You have a new private message.")
		}
	}
	if a.hadWork && todo == 0 && o.Ready && !o.Importing {
		a.hadWork = false
	}
	a.unread = unread
	a.setBadge(pageSoulseek, unread)
}

// --- talking to you -----------------------------------------------------------------------------

// confirm asks a yes/no question.
func (a *app) confirm(title, body string) bool {
	return qt.QMessageBox_Question(a.win.QWidget, title, body) == qt.QMessageBox__Yes
}

// ask asks for a line of text; ok is false when cancelled.
func (a *app) ask(title, label, value string) (string, bool) {
	d := qt.NewQInputDialog2()
	d.SetWindowTitle(title)
	d.SetLabelText(label)
	d.SetTextValue(value)
	d.Resize(520, 130)
	if d.Exec() != int(qt.QDialog__Accepted) {
		return "", false
	}
	return strings.TrimSpace(d.TextValue()), true
}

// message shows a short note in a box; for things you have to read.
func (a *app) message(title, body string) {
	qt.QMessageBox_Information(a.win.QWidget, title, body)
}

// toast says something in passing, at the bottom of the window.
func (a *app) toast(s string) {
	l := a.toastLabel
	l.SetText(s)
	l.SetMinimumWidth(0)
	l.SetMaximumWidth(16777215)
	l.AdjustSize()
	parent := a.win.CentralWidget()
	l.SetFixedWidth(max(min(l.Width(), parent.Width()-260), 120))
	l.Move(224+(parent.Width()-224-l.Width())/2, parent.Height()-l.Height()-22)
	l.Raise()
	l.Show()
	a.toastTimer.Start(3200)
}

// logoPixmap is the app's mark: a note on the accent color.
func logoPixmap(size int) *qt.QPixmap {
	const scale = 2
	pm := qt.NewQPixmap2(size*scale, size*scale)
	pm.FillWithFillColor(qt.NewQColor2(qt.Transparent))
	p := qt.NewQPainter2(pm.QPaintDevice)
	p.SetRenderHint2(qt.QPainter__Antialiasing, true)
	p.SetRenderHint2(qt.QPainter__SmoothPixmapTransform, true)
	fillRound(p, 0, 0, size*scale, size*scale, float64(size*scale)/4.4, th.Accent)
	inner := size * 62 / 100
	note := iconPixmap("logo", inner, th.OnAccent)
	off := (size - inner) * scale / 2
	p.Scale(scale, scale)
	p.DrawPixmap9(off/scale, off/scale, note)
	p.End()
	p.Delete()
	pm.SetDevicePixelRatio(scale)
	return pm
}

func appIcon() *qt.QIcon { return qt.NewQIcon2(logoPixmap(64)) }

// mainDo runs f on the window's thread.
func mainDo(f func()) { mainthread.Start(f) }

func htmlEscape(s string) string { return html.EscapeString(s) }
