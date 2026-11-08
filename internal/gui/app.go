// Package gui is the app's window, in Qt (through the miqt bindings), over the same backend
// as the terminal app (core.Backend). It only reads views and calls methods of the backend;
// Watch says what changed, and a timer redraws what moves all the time (progress, speeds).
package gui

import (
	"fmt"
	"os"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

type app struct {
	m      core.Backend
	win    *qt.QMainWindow
	tabs   *qt.QTabWidget
	header *qt.QLabel
	counts *qt.QLabel
	pause  *qt.QPushButton

	queue    *queueTab
	search   *searchTab
	library  *libraryTab
	sources  *sourcesTab
	settings *settingsTab
	log      *logTab
}

// Run opens the window and returns when it's closed. The backend is started already.
func Run(m core.Backend) error {
	qt.NewQApplication(os.Args)
	qt.QCoreApplication_SetApplicationName("Universal Music Downloader")
	a := &app{m: m}
	a.build()
	stop := m.Watch(func(k core.EventKind) {
		mainthread.Start(func() { a.changed(k) })
	})
	defer stop()
	tick := qt.NewQTimer2(a.win.QObject)
	tick.OnTimeout(a.tick)
	tick.Start(700)
	a.tick()
	a.win.Show()
	a.queue.table.SetFocus()
	if len(a.queue.rows) > 0 {
		a.queue.table.SelectRow(0)
	}
	qt.QApplication_Exec()
	return nil
}

func (a *app) build() {
	a.win = qt.NewQMainWindow2()
	a.win.SetWindowTitle("Universal Music Downloader")
	a.win.Resize(1200, 760)

	central := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(central)
	box.SetContentsMargins(8, 6, 8, 6)

	// The header: state, speeds, downloads at once, the bandwidth, pause.
	top := qt.NewQHBoxLayout2()
	a.header = qt.NewQLabel2()
	a.header.SetTextFormat(qt.RichText)
	a.pause = qt.NewQPushButton3("Pause")
	a.pause.SetToolTip("Pause or resume everything (P)")
	a.pause.OnClicked(func() { a.m.SetPaused(!a.m.Paused()); a.tick() })
	top.AddWidget2(a.header.QWidget, 1)
	top.AddWidget(a.pause.QWidget)
	box.AddLayout(top.QLayout)
	a.counts = qt.NewQLabel2()
	a.counts.SetTextFormat(qt.RichText)
	box.AddWidget(a.counts.QWidget)

	a.tabs = qt.NewQTabWidget2()
	a.queue = newQueueTab(a)
	a.tabs.AddTab(a.queue.w, "&1 Queue")
	a.search = newSearchTab(a)
	a.tabs.AddTab(a.search.w, "&2 Search")
	a.library = newLibraryTab(a)
	a.tabs.AddTab(a.library.w, "&3 Library")
	a.sources = newSourcesTab(a)
	a.tabs.AddTab(a.sources.w, "&4 Sources")
	a.tabs.AddTab(placeholder("Soulseek"), "&5 Soulseek")
	a.settings = newSettingsTab(a)
	a.tabs.AddTab(a.settings.w, "&6 Settings")
	a.log = newLogTab(a)
	a.tabs.AddTab(a.log.w, "&7 Log")
	a.tabs.OnCurrentChanged(func(i int) {
		switch i {
		case 0:
			a.queue.refresh(false)
			a.queue.table.SetFocus()
		case 1:
			a.search.refresh()
			a.search.find.SetFocus()
		case 2:
			a.library.refresh()
		case 3:
			a.sources.refresh()
			a.sources.table.SetFocus()
		}
	})
	box.AddWidget2(a.tabs.QWidget, 1)
	a.win.SetCentralWidget(central)

	pauseKey := qt.NewQShortcut2(qt.NewQKeySequence2("Shift+P"), a.win.QObject)
	pauseKey.OnActivated(func() { a.m.SetPaused(!a.m.Paused()); a.tick() })
	quit := qt.NewQShortcut2(qt.NewQKeySequence2("Ctrl+Q"), a.win.QObject)
	quit.OnActivated(func() { a.win.Close() })
}

// placeholder stands in for a tab that comes in a later phase; the terminal app has it.
func placeholder(name string) *qt.QWidget {
	w := qt.NewQWidget2()
	l := qt.NewQVBoxLayout(w)
	label := qt.NewQLabel3(fmt.Sprintf("The %s tab comes in the next phase of the window.\n"+
		"Everything in it works in the terminal app meanwhile (universal-music-downloader).", name))
	label.SetAlignment(qt.AlignCenter)
	l.AddWidget(label.QWidget)
	return w
}

// changed redraws what an event says changed.
func (a *app) changed(k core.EventKind) {
	switch k {
	case core.EventQueue, core.EventConfig:
		a.queue.changed()
		switch a.tabs.CurrentIndex() {
		case 1:
			a.search.refresh()
		case 2:
			a.library.refresh()
		}
	case core.EventSources:
		if a.tabs.CurrentIndex() == 3 {
			a.sources.refresh()
		}
	case core.EventLog:
		a.log.refresh()
	}
}

// tick redraws what moves all the time.
func (a *app) tick() {
	o := a.m.Overview()
	state := "<span style='color:#3a3'>● running</span>"
	switch {
	case o.Paused:
		state = "<span style='color:#c80'>❚❚ paused</span>"
	case !o.Ready:
		state = "<span style='color:#c80'>… starting</span>"
	}
	if o.Paused {
		a.pause.SetText("Resume")
	} else {
		a.pause.SetText("Pause")
	}
	limit := "no limit"
	if o.Cap > 0 {
		limit = "cap " + core.FmtRate(o.Cap)
	}
	parts := []string{
		"<b>Universal Music Downloader</b>", state,
		fmt.Sprintf("⇣ %s (%s)", core.FmtRate(o.Own+o.Torrent), limit),
		fmt.Sprintf("downloads %d/%d", o.DlUsed, o.DlLimit),
		fmt.Sprintf("searches %d/%d", o.SrUsed, o.SrLimit),
	}
	if o.Torrent > 0 {
		parts = append(parts, "torrents "+core.FmtRate(o.Torrent))
	}
	if o.SlskOn {
		parts = append(parts, fmt.Sprintf("Soulseek ⇣ %s ⇡ %s", core.FmtRate(o.SlskDown), core.FmtRate(o.SlskUp)))
	}
	a.header.SetText(strings.Join(parts, " &nbsp;·&nbsp; "))
	c := o.Counts
	todo := c[core.JobQueued] + c[core.JobSearching] + c[core.JobReady] + c[core.JobDownloading] + c[core.JobWaiting]
	free := ""
	if o.Free > 0 {
		free = " · " + core.FmtBytes(o.Free) + " free"
	}
	a.counts.SetText(fmt.Sprintf("%d tracks &nbsp; <span style='color:#3a3'>✓ %d downloaded</span> &nbsp; "+
		"<span style='color:#38c'>◆ %d in library</span> &nbsp; · %d to do &nbsp; <span style='color:#c33'>✗ %d not found</span>%s",
		o.Total, c[core.JobDone], c[core.JobInLibrary], todo, c[core.JobFailed], free))
	a.queue.tick()
}

// confirm asks a yes/no question.
func (a *app) confirm(title, text string) bool {
	return qt.QMessageBox_Question(a.win.QWidget, title, text) == qt.QMessageBox__Yes
}

// ask asks for a line of text; ok is false when cancelled.
func (a *app) ask(title, label, value string) (string, bool) {
	d := qt.NewQInputDialog2()
	d.SetWindowTitle(title)
	d.SetLabelText(label)
	d.SetTextValue(value)
	d.Resize(560, 120)
	if d.Exec() != int(qt.QDialog__Accepted) {
		return "", false
	}
	return d.TextValue(), true
}

// message shows a short note in a box.
func (a *app) message(title, text string) {
	qt.QMessageBox_Information(a.win.QWidget, title, text)
}
