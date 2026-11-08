//go:build guishots

package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
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

var pageNames = []string{"dashboard", "downloads", "find", "playlists", "library", "sort", "sources", "soulseek", "settings", "activity", "help"}

func runShots(a *app) {
	if os.Getenv("UMD_SHOTS_REAL") != "" {
		a.win.Show()
		pump(3 * time.Second)
		for i := pageDashboard; i < pageCount; i++ {
			a.show(i)
			pump(200 * time.Millisecond)
		}
		fmt.Fprintf(os.Stderr, "platform %s; tray available %v, tray made %v, tray visible %v\n", qt.QGuiApplication_PlatformName(), qt.QSystemTrayIcon_IsSystemTrayAvailable(), a.tray != nil, a.tray != nil && a.tray.IsVisible())
		memLine("real desktop")
		smapsSummary()
		// Close to tray, for real.
		a.m.UpdateConfig(func(c *core.Config) { c.GUI.CloseToTray = true })
		pump(300 * time.Millisecond)
		a.show(pageSettings)
		pump(200 * time.Millisecond)
		fmt.Fprintf(os.Stderr, "tray setting enabled %v, checked %v\n", a.settings.tray.IsEnabled(), a.settings.tray.IsChecked())
		a.win.Close()
		pump(500 * time.Millisecond)
		fmt.Fprintf(os.Stderr, "after closing: window visible %v, app quitting %v\n", a.win.IsVisible(), a.quitting)
		a.raise()
		pump(500 * time.Millisecond)
		fmt.Fprintf(os.Stderr, "after reopening from the tray: window visible %v\n", a.win.IsVisible())
		a.m.UpdateConfig(func(c *core.Config) { c.GUI.CloseToTray = false })
		pump(200 * time.Millisecond)
		return
	}
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
		for i := pageDashboard; i < pageCount; i++ {
			a.show(i)
			pump(350 * time.Millisecond)
			s.grab(mode+"-"+pageNames[i], a.win.QWidget)
		}
	}
	if p := os.Getenv("UMD_SHOTS_SORTPLAN"); p != "" {
		s.sortPlanOnly(p)
		return
	}
	if p := os.Getenv("UMD_SHOTS_PROFILE"); p != "" {
		s.profile(p)
		return
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
	s.dashboard()
	s.tables()
	s.tray()
	s.queue()
	s.search()
	s.playlists()
	s.sortFlow()
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
	pump(300 * time.Millisecond)
	if len(t.keys) == 0 {
		s.fail("the queue is empty")
		return
	}
	for name, b := range t.chips {
		b.Click()
		pump(120 * time.Millisecond)
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
	a.win.Resize(1040, 720)
	pump(200 * time.Millisecond)
	for i := pageDashboard; i < pageCount; i++ {
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
	settle := func() {
		for i := 0; i < 2000 && (t.loading || t.again); i++ {
			pump(time.Millisecond)
		}
	}
	fmt.Fprintf(os.Stderr, "tracks: %d, playlists: %d\n", len(t.keys), len(a.m.Playlists()))
	report("refresh, reset", func() { t.refresh(true); settle() })
	report("refresh, same rows", func() { t.refresh(false); settle() })
	report("tick", a.tick)
	report("filter: done", func() { t.chips["done"].Click(); settle() })
	report("filter: all", func() { t.chips["all"].Click(); settle() })
	report("find text", func() { t.find.SetText("the"); settle() })
	report("find cleared", func() { t.find.SetText(""); settle() })
	for i := range core.QueueSorts {
		report("sort: "+core.QueueSorts[i].Name, func() { t.sort.SetCurrentIndex(i); settle() })
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
	if len(a.queue.keys) > 0 {
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
	t.search.table.view.SelectRow(pick)
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

func vmField(name string) string {
	data, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, name+":") {
			return strings.TrimSpace(strings.TrimPrefix(l, name+":"))
		}
	}
	return "?"
}

func memLine(what string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(os.Stderr, "mem %-22s rss %s, peak %s; go heap in use %d MB, held %d MB, released %d MB, GCs %d\n",
		what, vmField("VmRSS"), vmField("VmHWM"), m.HeapInuse>>20, m.HeapSys>>20, m.HeapReleased>>20, m.NumGC)
}

// profile measures the window under a stream of changes, as with downloads running: CPU and
// heap profiles to <prefix>.cpu and <prefix>.heap (UMD_SHOTS_PROFILE).
func (s *shotter) profile(prefix string) {
	a := s.a
	seconds := 30
	if n, err := strconv.Atoi(os.Getenv("UMD_SHOTS_SECONDS")); err == nil && n > 0 {
		seconds = n
	}
	memLine("started")
	a.show(pageDownloads)
	pump(2 * time.Second)
	memLine("after 2s")
	f, _ := os.Create(prefix + ".cpu")
	pprof.StartCPUProfile(f)
	start := time.Now()
	busy := time.Duration(0)
	worst := time.Duration(0)
	n := 0
	var last time.Time
	for time.Since(start) < time.Duration(seconds)*time.Second {
		if time.Since(last) > 250*time.Millisecond {
			last = time.Now()
			a.changed(core.EventQueue) // what the backend sends while tracks change state
		}
		t0 := time.Now()
		qt.QCoreApplication_ProcessEvents()
		d := time.Since(t0)
		busy += d
		worst = max(worst, d)
		n++
		time.Sleep(4 * time.Millisecond)
	}
	pprof.StopCPUProfile()
	f.Close()
	fmt.Fprintf(os.Stderr, "window thread busy %.1f%%, longest stall %v\n", 100*busy.Seconds()/float64(seconds), worst.Round(time.Millisecond))
	memLine("after the changes")
	for i := pageDashboard; i < pageCount; i++ {
		a.show(i)
		pump(250 * time.Millisecond)
	}
	memLine("after every page")
	smapsSummary()
	h, _ := os.Create(prefix + ".heap")
	runtime.GC()
	pprof.WriteHeapProfile(h)
	h.Close()
	memLine("after a GC")
}

// smapsSummary lists where the process's memory is, biggest first.
func smapsSummary() {
	data, _ := os.ReadFile("/proc/self/smaps")
	byName := map[string]int{}
	name := ""
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) >= 5 && strings.Contains(f[0], "-") && len(f[1]) == 4 {
			name = "[anon]"
			if len(f) >= 6 {
				name = filepath.Base(f[5])
			}
		}
		if len(f) >= 2 && f[0] == "Rss:" {
			n, _ := strconv.Atoi(f[1])
			byName[name] += n
		}
	}
	var names []string
	for n, kb := range byName {
		if kb > 3000 {
			names = append(names, n)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return byName[b] - byName[a] })
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %7d kB  %s\n", byName[n], n)
	}
}

func (s *shotter) dashboard() {
	a := s.a
	a.show(pageDashboard)
	for i := 0; i < 40; i++ { // a little speed history, and the disk numbers
		a.dash.down.spark.push(float64(i%13) * 90000)
		a.dash.up.spark.push(float64(i%7) * 20000)
		pump(50 * time.Millisecond)
	}
	s.grab("flow-dashboard", a.win.QWidget)
	if a.dash.rows["library"] == nil {
		s.fail("the dashboard has no rows")
	}
}

// tables checks the tables: columns move one at a time, sorting sorts, widths are remembered.
func (s *shotter) tables() {
	a := s.a
	rows := [][]string{{"zebra", "10"}, {"apple", "2"}, {"mango", "33"}}
	tb := a.newTable("test-table", column{title: "Name", width: 200, flex: true}, column{title: "N", width: 80, right: true}, column{title: "Note", width: 120})
	tb.count = func() int { return len(rows) }
	tb.key = func(r int) string { return rows[r][0] }
	tb.text = func(r, c int) string {
		if c == 2 {
			return ""
		}
		return rows[r][c]
	}
	tb.num = func(r, c int) (float64, bool) {
		if c == 1 {
			v, _ := strconv.ParseFloat(rows[r][1], 64)
			return v, true
		}
		return 0, false
	}
	win := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(win)
	box.AddWidget(tb.view.QWidget)
	win.Resize(700, 300)
	win.Show()
	tb.reload()
	pump(150 * time.Millisecond)
	before := tb.view.ColumnWidth(1)
	tb.view.HorizontalHeader().ResizeSection(0, 300)
	pump(50 * time.Millisecond)
	if tb.view.ColumnWidth(1) != before {
		s.fail("resizing one column changed another: %d to %d", before, tb.view.ColumnWidth(1))
	}
	if !tb.userSized || len(a.ui.Columns["test-table"]) != 3 {
		s.fail("the widths weren't noted")
	}
	tb.view.SelectRow(0) // zebra
	tb.toggleSort(0)
	if got := tb.text(tb.rowAt(0), 0); got != "apple" {
		s.fail("sorting by name: first is %s", got)
	}
	if r := tb.current(); r < 0 || rows[r][0] != "zebra" {
		s.fail("the selection didn't follow the sort")
	}
	tb.toggleSort(1)
	if got := tb.text(tb.rowAt(2), 0); got != "mango" {
		s.fail("sorting by number: last is %s", got)
	}
	tb.toggleSort(1)
	if got := tb.text(tb.rowAt(0), 0); got != "mango" {
		s.fail("reversed: first is %s", got)
	}
	s.grab("flow-table", win)
	win.Close()
	delete(a.ui.Columns, "test-table")
}

func (s *shotter) tray() {
	a := s.a
	a.show(pageSettings)
	pump(100 * time.Millisecond)
	if a.tray != nil && !a.settings.tray.IsEnabled() {
		s.fail("there's a tray, but its setting is greyed out")
	}
	if a.tray == nil && a.settings.tray.IsEnabled() {
		s.fail("no tray, but its setting can be turned on")
	}
}

func (s *shotter) waitSort(maxSeconds int) core.SortView {
	for i := 0; i < maxSeconds*20; i++ {
		pump(50 * time.Millisecond)
		if v := s.a.m.SortStatus(); !v.Busy {
			return v
		}
	}
	s.fail("Sort & merge is still busy")
	return s.a.m.SortStatus()
}

// sortPlanOnly plans a real folder, only reading it, and reports what the plan wants.
func (s *shotter) sortPlanOnly(root string) {
	a := s.a
	start := time.Now()
	if err := a.m.SortPlan(core.SortOptions{Sources: []string{root}, Dest: root, DuplicatesDir: filepath.Join(filepath.Dir(root), "Duplicates"), Offline: true, Tolerance: 3,
		ArtistComps: os.Getenv("UMD_SHOTS_ARTISTCOMPS") != ""}); err != nil {
		s.fail("%v", err)
		return
	}
	last := time.Now()
	for a.m.SortStatus().Busy {
		pump(50 * time.Millisecond)
		if time.Since(last) > 10*time.Second {
			last = time.Now()
			v := a.m.SortStatus()
			fmt.Fprintf(os.Stderr, "  %s %d/%d (%v, rss %s)\n", v.Stage, v.Done, v.Total, time.Since(start).Round(time.Second), vmField("VmRSS"))
		}
	}
	v := a.m.SortStatus()
	fmt.Fprintf(os.Stderr, "plan took %v; error %q; peak %s\n", time.Since(start).Round(time.Second), v.Err, vmField("VmHWM"))
	if v.Plan == nil {
		return
	}
	fmt.Fprintln(os.Stderr, v.Plan.Text)
	f, _ := os.Create(filepath.Join(s.dir, "moves.txt"))
	for _, m := range a.m.SortMoves("", "") {
		fmt.Fprintf(f, "%s: %s\n   -> %s\n", m.Kind, m.Src, m.Dst)
	}
	f.Close()
	a.show(pageSort)
	pump(500 * time.Millisecond)
	s.grab("scale-sort-plan", a.win.QWidget)
}

// sortFlow goes through plan, apply and undo on a throwaway folder, through the page.
func (s *shotter) sortFlow() {
	a, t := s.a, s.a.sorter
	root, err := os.MkdirTemp(s.dir, "sorttest-")
	if err != nil {
		s.fail("%v", err)
		return
	}
	defer os.RemoveAll(root)
	tone := func(rel, artist, title, album, track, bitrate string, seconds string) {
		p := filepath.Join(root, "in", rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		args := []string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=" + seconds}
		if bitrate != "" {
			args = append(args, "-b:a", bitrate)
		}
		for k, v := range map[string]string{"artist": artist, "title": title, "album": album, "track": track} {
			if v != "" {
				args = append(args, "-metadata", k+"="+v)
			}
		}
		if out, err := exec.Command("ffmpeg", append(args, p)...).CombinedOutput(); err != nil {
			s.fail("ffmpeg: %s", out)
		}
	}
	tone("mess/01 one.flac", "Band", "One", "First Record", "1", "", "3")
	tone("mess/one copy.mp3", "Band", "One", "", "", "128k", "3")
	tone("other/two.mp3", "Band", "Two", "First Record", "2", "192k", "4")
	in, dest, dup := filepath.Join(root, "in"), filepath.Join(root, "Sorted"), filepath.Join(root, "Dup")
	a.show(pageSort)
	pump(100 * time.Millisecond)
	t.sources.Clear()
	t.sources.AddItem(in)
	t.dest.SetText(dest)
	t.dups.SetText(dup)
	t.online.SetChecked(false)
	t.makePlan()
	v := s.waitSort(60)
	pump(150 * time.Millisecond)
	a.sorter.refresh()
	pump(150 * time.Millisecond)
	if v.Plan == nil {
		s.fail("no plan: %s", v.Err)
		return
	}
	if len(t.moveRows) != 3 || v.Plan.Summary.DupFiles != 1 {
		s.fail("the plan: %d moves, %d duplicates", len(t.moveRows), v.Plan.Summary.DupFiles)
	}
	if t.stack.CurrentIndex() != sortPagePlan {
		s.fail("the plan isn't shown")
	}
	s.grab("flow-sort-plan", a.win.QWidget)
	t.segs[1].Click()
	pump(100 * time.Millisecond)
	s.grab("flow-sort-duplicates", a.win.QWidget)
	if err := a.m.SortApply(""); err != nil {
		s.fail("%v", err)
	}
	v = s.waitSort(60)
	pump(100 * time.Millisecond)
	a.sorter.refresh()
	pump(100 * time.Millisecond)
	if v.Result == nil || v.Result.Done != 3 || v.Result.ProblemCount != 0 {
		s.fail("applying: %+v (%s)", v.Result, v.Err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Band", "First Record", "01 - One.flac")); err != nil {
		s.fail("not sorted: %v", err)
	}
	s.grab("flow-sort-done", a.win.QWidget)
	if err := a.m.SortUndo(t.lastPlan); err != nil {
		s.fail("undo: %v", err)
	}
	v = s.waitSort(60)
	if _, err := os.Stat(filepath.Join(in, "mess", "01 one.flac")); err != nil || v.Result == nil || v.ResultWas != "undo" {
		s.fail("undo didn't put it back: %v %+v", err, v.Result)
	}
	if _, err := os.Stat(dest); err == nil {
		s.fail("the empty sorted folder stayed")
	}
	a.m.SortDiscard()
	pump(100 * time.Millisecond)
	a.sorter.refresh()
	pump(100 * time.Millisecond)
	s.grab("flow-sort-history", a.win.QWidget)
	if t.stack.CurrentIndex() != sortPageChoose || len(t.histRows) == 0 {
		s.fail("the history: page %d, %d plans", t.stack.CurrentIndex(), len(t.histRows))
	}
}
