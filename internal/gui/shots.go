//go:build guishots

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The screenshot tool: walks through the real window off-screen, saves a picture of every page
// and dialog in both themes, and exercises the controls. Built with -tags guishots, run with
// QT_QPA_PLATFORM=offscreen UMD_SHOTS=<folder>; UMD_SHOTS_ONLY limits it to names containing
// the given words (comma-separated).

func init() {
	runtime.LockOSThread()
	shotsHook = runShots
}

type shotter struct {
	a    *app
	dir  string
	only []string
	prob int
}

func pump(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		qt.QCoreApplication_ProcessEvents()
		time.Sleep(4 * time.Millisecond)
	}
}

func (s *shotter) wanted(name string) bool {
	if len(s.only) == 0 {
		return true
	}
	for _, w := range s.only {
		if strings.Contains(name, w) {
			return true
		}
	}
	return false
}

func (s *shotter) grab(name string, w *qt.QWidget) {
	if !s.wanted(name) {
		return
	}
	pm := w.Grab()
	p := filepath.Join(s.dir, name+".png")
	if !pm.Save(p) {
		s.fail("couldn't save %s", p)
		return
	}
	fmt.Fprintln(os.Stderr, "shot", name, pm.Width(), pm.Height())
}

func (s *shotter) fail(f string, args ...any) {
	s.prob++
	fmt.Fprintf(os.Stderr, "PROBLEM: "+f+"\n", args...)
}

// dialog grabs and closes the modal window that open opens.
func (s *shotter) dialog(name string, open func()) {
	if !s.wanted(name) {
		return
	}
	t := qt.NewQTimer2(s.a.win.QObject)
	t.SetSingleShot(true)
	seen := false
	t.OnTimeout(func() {
		w := qt.QApplication_ActiveModalWidget()
		if w == nil {
			return
		}
		seen = true
		s.grab(name, w)
		w.Close()
	})
	t.Start(450)
	open()
	if !seen {
		s.fail("dialog %s didn't open", name)
	}
	pump(60 * time.Millisecond)
}

var pageNames = []string{"downloads", "find", "playlists", "library", "sources", "soulseek", "settings", "activity", "help"}

func runShots(a *app) {
	if p := os.Getenv("UMD_ICON"); p != "" {
		th = lightTheme
		logoPixmap(256).Save(p)
		return
	}
	dir := os.Getenv("UMD_SHOTS")
	os.MkdirAll(dir, 0o755)
	s := &shotter{a: a, dir: dir}
	if o := os.Getenv("UMD_SHOTS_ONLY"); o != "" {
		s.only = strings.Split(o, ",")
	}
	a.win.Resize(1240, 800)
	a.win.Show()
	pump(1500 * time.Millisecond)

	for _, mode := range []string{"dark", "light"} {
		s.setTheme(mode)
		for i := pageDownloads; i < pageCount; i++ {
			a.show(i)
			pump(350 * time.Millisecond)
			s.grab(mode+"-"+pageNames[i], a.win.QWidget)
		}
	}
	if os.Getenv("UMD_SHOTS_TIMING") != "" {
		s.timing()
		return
	}
	s.setTheme("dark")
	if os.Getenv("UMD_SHOTS_LIVE") != "" {
		s.live()
		return
	}
	s.queue()
	s.search()
	s.playlists()
	s.sources()
	s.soulseek()
	s.settings()
	s.setup()
	s.narrow()
	if s.prob > 0 {
		fmt.Fprintf(os.Stderr, "%d problems\n", s.prob)
	} else {
		fmt.Fprintln(os.Stderr, "no problems")
	}
}

func (s *shotter) setTheme(mode string) {
	if err := s.a.m.UpdateConfig(func(c *core.Config) { c.GUI.Theme = mode }); err != nil {
		s.fail("theme %s: %v", mode, err)
	}
	s.a.applyTheme()
	pump(150 * time.Millisecond)
	if (mode == "dark") != th.Dark {
		s.fail("theme %s wasn't applied", mode)
	}
}

func (s *shotter) queue() {
	a, t := s.a, s.a.queue
	a.show(pageDownloads)
	pump(200 * time.Millisecond)
	if len(t.rows) == 0 {
		s.fail("the queue is empty")
		return
	}
	for name, b := range t.chips {
		b.Click()
		pump(80 * time.Millisecond)
		want := name
		if want == "all" {
			want = ""
		}
		if t.q.Filter != want {
			s.fail("chip %s set the filter %q", name, t.q.Filter)
		}
	}
	t.chips["all"].Click()
	for i := range core.QueueSorts {
		t.sort.SetCurrentIndex(i)
		pump(40 * time.Millisecond)
	}
	t.sort.SetCurrentIndex(0)
	t.reverse.Click()
	if !t.q.Reverse {
		s.fail("reverse didn't reverse")
	}
	t.reverse.Click()
	t.find.SetText("a")
	pump(80 * time.Millisecond)
	t.find.SetText("")
	pump(80 * time.Millisecond)
	t.focus() // focusing a list makes its first row current, without selecting it
	pump(60 * time.Millisecond)
	t.list.selectRow(0)
	pump(150 * time.Millisecond)
	if !t.pane.IsVisible() {
		s.fail("selecting a track didn't open its details")
	}
	s.grab("flow-queue-details", a.win.QWidget)
	t.hidePane()
	pump(80 * time.Millisecond)
	if t.pane.IsVisible() {
		s.fail("the details didn't close")
	}
	s.dialog("dialog-help-downloads", func() { a.showHelp("downloads") })
}

func (s *shotter) search() {
	a, t := s.a, s.a.search
	a.show(pageFind)
	t.find.SetText("Boards of Canada")
	t.search()
	for i := 0; i < 150 && t.busy; i++ {
		pump(100 * time.Millisecond)
	}
	pump(200 * time.Millisecond)
	if len(t.found) == 0 {
		s.fail("the catalog search found nothing (%s)", t.note.Text())
	}
	s.grab("flow-find-results", a.win.QWidget)
}

func (s *shotter) playlists() {
	a := s.a
	a.show(pagePlaylists)
	pump(150 * time.Millisecond)
	if len(a.lists.rows) > 0 {
		a.lists.list.selectRow(0)
		pump(60 * time.Millisecond)
		a.lists.show.Click()
		pump(120 * time.Millisecond)
		if a.current() != pageDownloads || a.queue.q.Playlist == "" {
			s.fail("Show songs didn't show the playlist")
		}
		s.grab("flow-playlist-songs", a.win.QWidget)
		a.queue.clearFilters()
		a.queue.clearFilters()
		if a.queue.q.Playlist != "" {
			s.fail("the playlist filter didn't clear")
		}
	}
}

func (s *shotter) sources() {
	a, t := s.a, s.a.sources
	a.show(pageSources)
	pump(100 * time.Millisecond)
	first := t.rows[0].Name
	t.move(first, 1)
	pump(100 * time.Millisecond)
	if t.rows[1].Name != first {
		s.fail("moving a source didn't move it")
	}
	t.move(first, -1)
	pump(100 * time.Millisecond)
	c := t.cards[0]
	was := c.toggle.IsChecked()
	c.toggle.Click()
	pump(100 * time.Millisecond)
	if t.rows[0].Enabled == was {
		s.fail("the switch didn't change the source")
	}
	c.toggle.Click()
	pump(60 * time.Millisecond)
	for _, name := range []string{"bandcamp", "archive", "audius", "youtube", "soundcloud", "torrent", "soulseek", "vk", "yandex"} {
		name := name
		s.dialog("dialog-source-"+name, func() { t.settings(name) })
	}
	s.dialog("dialog-indexers", func() { a.pickIndexers() })
	s.dialog("dialog-paste-links", t.pasteBandcampLinks)
}

func (s *shotter) soulseek() {
	a, t := s.a, s.a.soulseek
	a.show(pageSoulseek)
	pump(150 * time.Millisecond)
	for i := range slskPageNames {
		t.showPage(i)
		pump(250 * time.Millisecond)
		s.grab("flow-soulseek-"+strings.ToLower(slskPageNames[i]), a.win.QWidget)
	}
	t.showPage(0)
}

func (s *shotter) settings() {
	a, t := s.a, s.a.settings
	a.show(pageSettings)
	pump(100 * time.Millisecond)
	t.advancedB.Click()
	pump(120 * time.Millisecond)
	s.grab("flow-settings-advanced", a.win.QWidget)
	t.advancedB.Click()
	before := a.m.Config().MaxDownloads
	t.maxDl.SetValue(before%64 + 1)
	t.saveAll()
	pump(60 * time.Millisecond)
	if a.m.Config().MaxDownloads == before {
		s.fail("the setting wasn't saved")
	}
	t.maxDl.SetValue(before)
	t.saveAll()
	// something invalid is refused and put back
	prev := a.m.Config().OutputDir
	lib := a.m.Config().LibraryDirs
	if len(lib) > 0 {
		t.out.SetText(filepath.Join(lib[0], "x"))
		t.saveAll()
		pump(60 * time.Millisecond)
		if a.m.Config().OutputDir != prev || !t.status.IsVisible() {
			s.fail("an invalid setting wasn't refused")
		}
		s.grab("flow-settings-error", a.win.QWidget)
		t.out.SetText(prev)
		t.saveAll()
		pump(60 * time.Millisecond)
		if t.status.IsVisible() {
			s.fail("the error stayed after a good value")
		}
	}
}

func (s *shotter) setup() {
	a := s.a
	w := a.newSetup()
	w.d.Show()
	for i := range setupSteps {
		w.step = i
		w.show()
		pump(200 * time.Millisecond)
		s.grab(fmt.Sprintf("setup-%02d-%s", i, setupSteps[i].id), w.d.QWidget)
	}
	w.d.Close()
	pump(100 * time.Millisecond)
}

func (s *shotter) narrow() {
	a := s.a
	a.win.Resize(1000, 640)
	pump(200 * time.Millisecond)
	for i := pageDownloads; i < pageCount; i++ {
		a.show(i)
		pump(150 * time.Millisecond)
		s.grab("narrow-"+pageNames[i], a.win.QWidget)
	}
}

// timing measures what a big queue costs (UMD_SHOTS_TIMING).
func (s *shotter) timing() {
	a, t := s.a, s.a.queue
	a.show(pageDownloads)
	report := func(what string, f func()) {
		start := time.Now()
		f()
		pump(0)
		fmt.Fprintf(os.Stderr, "timing %-32s %v\n", what, time.Since(start).Round(time.Millisecond))
	}
	fmt.Fprintf(os.Stderr, "tracks: %d, playlists: %d\n", len(t.rows), len(a.m.Playlists()))
	report("refresh, reset", func() { t.refresh(true) })
	report("refresh, same rows", func() { t.refresh(false) })
	report("tick", a.tick)
	report("filter: done", func() { t.chips["done"].Click() })
	report("filter: all", func() { t.chips["all"].Click() })
	report("find text", func() { t.find.SetText("the") })
	report("find cleared", func() { t.find.SetText("") })
	for i := range core.QueueSorts {
		report("sort: "+core.QueueSorts[i].Name, func() { t.sort.SetCurrentIndex(i) })
	}
	t.sort.SetCurrentIndex(0)
	report("select, details", func() { t.list.selectRow(1000) })
	report("scroll to the end", func() { t.list.view.ScrollToBottom(); pump(50 * time.Millisecond) })
	report("playlists page", func() { a.show(pagePlaylists) })
	report("sources page", func() { a.show(pageSources) })
	report("theme switch", func() { s.setTheme("light"); s.setTheme("dark") })
	a.show(pageDownloads)
	pump(300 * time.Millisecond)
	s.grab("scale-downloads", a.win.QWidget)
	a.show(pagePlaylists)
	pump(300 * time.Millisecond)
	s.grab("scale-playlists", a.win.QWidget)
}

// live goes through the real thing, gently: the queue working, then one Soulseek search and
// one download by hand (UMD_SHOTS_LIVE; the config has the sources on).
func (s *shotter) live() {
	a, t := s.a, s.a.soulseek
	a.show(pageDownloads)
	fmt.Fprintln(os.Stderr, "live: waiting for the queue")
	shots := 0
	worked := false
	for i := 0; i < 240; i++ {
		pump(time.Second)
		o := a.m.Overview()
		active := o.Counts[core.JobSearching] + o.Counts[core.JobReady] + o.Counts[core.JobDownloading]
		todo := active + o.Counts[core.JobQueued] + o.Counts[core.JobWaiting]
		if active > 0 {
			worked = true
		}
		if active > 0 && i%3 == 0 && shots < 6 {
			s.grab(fmt.Sprintf("live-queue-%d", shots), a.win.QWidget)
			shots++
		}
		if worked && todo == 0 {
			break
		}
	}
	o := a.m.Overview()
	fmt.Fprintf(os.Stderr, "live: queue: %d downloaded, %d not found, %d failed states\n", o.Counts[core.JobDone], o.Counts[core.JobFailed], o.Counts[core.JobWaiting])
	s.grab("live-queue-done", a.win.QWidget)
	if len(a.queue.rows) > 0 {
		a.queue.list.selectRow(0)
		pump(200 * time.Millisecond)
		s.grab("live-queue-details", a.win.QWidget)
		a.queue.hidePane()
	}

	a.show(pageSoulseek)
	for i := 0; i < 60 && !a.m.SoulseekStatus().Online; i++ {
		pump(time.Second)
	}
	if !a.m.SoulseekStatus().Online {
		s.fail("Soulseek didn't log in: %s", a.m.SoulseekStatus().Problem)
		return
	}
	s.grab("live-soulseek", a.win.QWidget)
	t.search.find.SetText("aphex twin xtal")
	t.runSearch(t.search.find.Text())
	for i := 0; i < 130 && t.search.busy; i++ {
		pump(time.Second)
	}
	pump(300 * time.Millisecond)
	s.grab("live-soulseek-search", a.win.QWidget)
	if len(t.search.res) == 0 {
		s.fail("the Soulseek search found nothing: %s", t.search.note.Text())
		return
	}
	fmt.Fprintf(os.Stderr, "live: %d Soulseek results\n", len(t.search.res))
	pick := -1
	for i, r := range t.search.res {
		if r.FreeSlot && r.Size < 60_000_000 && r.Size > 1_000_000 {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = 0
	}
	r := t.search.res[pick]
	t.search.table.SelectRow(pick)
	t.downloadSelected()
	t.showPage(slskPageTransfers)
	finished := ""
	for i := 0; i < 300; i++ {
		pump(time.Second)
		for _, x := range a.m.SoulseekTransfers() {
			if !x.Up && x.File == r.Path && (strings.HasPrefix(x.State, "done") || strings.HasPrefix(x.State, "failed")) {
				finished = x.State
			}
		}
		if i == 5 {
			s.grab("live-soulseek-transfers", a.win.QWidget)
		}
		if finished != "" {
			break
		}
	}
	fmt.Fprintf(os.Stderr, "live: hand download: %q\n", finished)
	if !strings.HasPrefix(finished, "done") {
		s.fail("the hand download didn't finish: %q", finished)
	}
	s.grab("live-soulseek-transfers-done", a.win.QWidget)

	t.browseUser(r.User)
	for i := 0; i < 90 && t.browse.busy; i++ {
		pump(time.Second)
	}
	pump(200 * time.Millisecond)
	fmt.Fprintf(os.Stderr, "live: browse %s: %d files; %s\n", r.User, len(t.browse.browsed), t.browse.note.Text())
	s.grab("live-soulseek-browse", a.win.QWidget)

	t.userInfo(r.User)
	pump(8 * time.Second)
	s.grab("live-soulseek-people", a.win.QWidget)

	t.showPage(slskPageInterests)
	pump(6 * time.Second)
	fmt.Fprintf(os.Stderr, "live: %d recommendations, %d similar\n", len(t.interests.recRows), len(t.interests.simRows))
	s.grab("live-soulseek-interests", a.win.QWidget)

	fmt.Fprintf(os.Stderr, "live: %d rooms\n", len(a.m.SoulseekRooms()))
	me := a.m.SoulseekStatus().User
	t.openChat(me)
	t.chat.input.SetText("window chat check")
	t.send()
	pump(3 * time.Second)
	t.chat.seq = -1
	t.refreshChats()
	s.grab("live-soulseek-chat", a.win.QWidget)

	t.checkPort()
	for i := 0; i < 35 && strings.Contains(t.portNote, "checking"); i++ {
		pump(time.Second)
	}
	fmt.Fprintf(os.Stderr, "live: port check: %s\n", t.portNote)
	s.grab("live-soulseek-port", a.win.QWidget)
	if s.prob > 0 {
		fmt.Fprintf(os.Stderr, "%d problems\n", s.prob)
	} else {
		fmt.Fprintln(os.Stderr, "no problems")
	}
}
