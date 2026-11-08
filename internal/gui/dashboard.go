package gui

import (
	"fmt"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Dashboard: speeds, how far the songs have come, and what's taking up the disk. Speeds are
// tiles with a short history; the songs and the disks are one thin segmented bar each, above a
// list that says every part's name and number (colors alone never carry anything here).

const speedHistory = 90 // about a minute of ticks

// chartColors are the parts of a bar: validated for colorblind safety on each theme's surface.
func chartColors() (library, done, active, upgrade, missing string) {
	if th.Dark {
		return "#3987e5", "#199e70", "#7a5fe0", "#c98500", "#e34948"
	}
	return "#2a78d6", "#1baf7a", "#6250d6", "#eda100", "#e34948"
}

type dashTile struct {
	value, sub *qt.QLabel
	spark      *sparkline
}

type dashRow struct {
	count  *qt.QLabel
	swatch *qt.QLabel
}

type dashboardTab struct {
	a    *app
	w    *qt.QWidget
	down *dashTile
	up   *dashTile
	done *dashTile
	free *dashTile

	bar  *segmentBar
	rows map[string]*dashRow

	disks    *qt.QVBoxLayout
	diskBars []*diskRow
	libSize  *qt.QLabel
	dlSize   *qt.QLabel
	cache    *qt.QLabel
	legend   *qt.QLabel

	busy  bool
	ticks int
}

type diskRow struct {
	w     *qt.QWidget
	title *qt.QLabel
	bar   *segmentBar
}

func newDashboardTab(a *app) *dashboardTab {
	t := &dashboardTab{a: a, rows: map[string]*dashRow{}}
	p := a.newPage("Dashboard", "Everything at a glance.", "")
	t.w = p.w
	scroll, body, _ := scrolling()
	p.box.AddWidget2(scroll.QWidget, 1)

	tiles := qt.NewQGridLayout2()
	tiles.SetSpacing(12)
	t.down = t.tile(tiles, 0, "Download speed", true)
	t.up = t.tile(tiles, 1, "Sharing on Soulseek", true)
	t.done = t.tile(tiles, 2, "Songs ready", false)
	t.free = t.tile(tiles, 3, "Free space", false)
	body.AddLayout(tiles.QLayout)

	// the songs
	songs, sb := card()
	sb.SetSpacing(10)
	sb.AddWidget(text("Your songs", "h2").QWidget)
	t.bar = newSegmentBar()
	sb.AddWidget(t.bar.w)
	grid := qt.NewQGridLayout2()
	grid.SetHorizontalSpacing(18)
	grid.SetVerticalSpacing(2)
	entries := []struct{ key, icon, label, tip, filter string }{
		{"library", "library", "Already in my library", "You have these at a quality you're happy with", "library"},
		{"done", "check", "Downloaded", "Saved to your downloads folder", "done"},
		{"upgrade", "arrow-up", "Need an upgrade", "You have a copy, but below the quality you chose", "below"},
		{"downloading", "download", "Downloading", "On their way now", "downloading"},
		{"searching", "search", "Searching", "Being looked for right now", "searching"},
		{"waiting", "clock", "Waiting", "Their turn hasn't come yet", "queued"},
		{"retry", "refresh", "Will retry", "A source was busy; the app tries again by itself", "waiting"},
		{"missing", "x", "Not found", "No source had them", "failed"},
		{"cancelled", "ban", "Cancelled", "You stopped these", "cancelled"},
	}
	for i, e := range entries {
		grid.AddWidget2(t.stateRow(e.key, e.icon, e.label, e.tip, e.filter), i/3, i%3)
	}
	sb.AddLayout(grid.QLayout)
	body.AddWidget(songs.QWidget)

	// the disks
	disk, db := card()
	db.SetSpacing(10)
	db.AddWidget(text("Disk use", "h2").QWidget)
	nums := hbox()
	nums.SetSpacing(32)
	number := func(label string) *qt.QLabel {
		col := vbox()
		col.SetSpacing(0)
		v := text("…", "h1")
		col.AddWidget(v.QWidget)
		col.AddWidget(text(label, "hint").QWidget)
		nums.AddLayout(col.QLayout)
		return v
	}
	t.libSize = number("My library")
	t.dlSize = number("Downloads")
	t.cache = number("App's own temporary files")
	nums.AddStretch()
	db.AddLayout(nums.QLayout)
	t.disks = vbox()
	t.disks.SetSpacing(10)
	db.AddLayout(t.disks.QLayout)
	t.legend = richText("", "hint")
	db.AddWidget(t.legend.QWidget)
	body.AddWidget(disk.QWidget)
	body.AddStretch()
	return t
}

func (t *dashboardTab) tile(grid *qt.QGridLayout, col int, label string, spark bool) *dashTile {
	f, box := card()
	box.SetSpacing(2)
	box.AddWidget(text(label, "hint").QWidget)
	tile := &dashTile{value: text("…", "stat"), sub: text("", "muted")}
	tile.value.SetWordWrap(false)
	box.AddWidget(tile.value.QWidget)
	box.AddWidget(tile.sub.QWidget)
	if spark {
		tile.spark = newSparkline()
		box.AddWidget(tile.spark.w)
	}
	box.AddStretch()
	grid.AddWidget2(f.QWidget, 0, col)
	grid.SetColumnStretch(col, 1)
	return tile
}

func (t *dashboardTab) stateRow(key, icon, label, tip, filter string) *qt.QWidget {
	a := t.a
	b := button("", "ghost", func() {
		a.queue.setFilterFromOutside(filter)
		a.show(pageDownloads)
	})
	b.SetToolTip(tip + ". Click to see them.")
	b.SetMinimumHeight(34)
	row := qt.NewQHBoxLayout(b.QWidget)
	row.SetContentsMargins(4, 0, 6, 0)
	row.SetSpacing(8)
	pic := qt.NewQLabel2()
	a.onTheme(func() { pic.SetPixmap(iconPixmap(icon, 16, th.Text2)) })
	swatch := qt.NewQLabel2()
	swatch.SetFixedSize2(8, 8)
	name := qt.NewQLabel3(label)
	count := qt.NewQLabel3("0")
	count.SetStyleSheet("font-weight: 600;")
	for _, l := range []*qt.QLabel{pic, swatch, name, count} {
		l.SetAttribute(qt.WA_TransparentForMouseEvents)
	}
	row.AddWidget(pic.QWidget)
	row.AddWidget(swatch.QWidget)
	row.AddWidget2(name.QWidget, 1)
	row.AddWidget(count.QWidget)
	t.rows[key] = &dashRow{count, swatch}
	return b.QWidget
}

// sample keeps the speed history going, whichever page is showing.
func (t *dashboardTab) sample(o core.Overview) {
	t.down.spark.push(o.Own + o.Torrent)
	t.up.spark.push(o.SlskUp)
}

// tick keeps the speeds moving; the rest is read every couple of seconds.
func (t *dashboardTab) tick(o core.Overview) {
	t.down.value.SetText(core.FmtRate(o.Own + o.Torrent))
	t.down.spark.redraw()
	if o.SlskOn {
		t.up.value.SetText(core.FmtRate(o.SlskUp))
	}
	t.up.spark.redraw()
	if t.ticks++; t.ticks%3 == 0 {
		t.refresh()
	}
}

// refresh reads the numbers in the background and shows them.
func (t *dashboardTab) refresh() {
	if t.busy {
		return
	}
	t.busy = true
	go func() {
		v := t.a.m.Dashboard()
		mainDo(func() {
			t.busy = false
			t.show(v)
		})
	}()
}

// compactCount keeps a big number short: 51k, 1.2M.
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}

func fmtCount(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (t *dashboardTab) show(v core.DashboardView) {
	c := v.Overview
	t.down.value.SetText(core.FmtRate(c.Own + c.Torrent))
	t.down.sub.SetText(fmt.Sprintf("%s downloading, %s searching", fmtCount(v.Parts.Downloading), fmtCount(v.Parts.Searching)))
	t.down.spark.redraw()
	if c.SlskOn {
		t.up.value.SetText(core.FmtRate(c.SlskUp))
		t.up.sub.SetText(fmt.Sprintf("Sending %d of %d, sharing %s files", v.Slsk.UpActive, v.Slsk.UpSlots, fmtCount(v.Slsk.SharedFiles)))
	} else {
		t.up.value.SetText("Off")
		t.up.sub.SetText("Soulseek isn't running")
	}
	t.up.spark.redraw()
	p := v.Parts
	ready := p.InLibrary + p.Downloaded
	if c.Total > 0 {
		t.done.value.SetText(fmt.Sprintf("%.0f%%", 100*float64(ready)/float64(c.Total)))
	} else {
		t.done.value.SetText("–")
	}
	t.done.sub.SetText(fmt.Sprintf("%s of %s", fmtCount(ready), fmtCount(c.Total)))
	if c.Free > 0 {
		t.free.value.SetText(core.FmtBytes(c.Free))
		t.free.sub.SetText("Where downloads go")
	}

	lib, done, active, upgrade, missing := chartColors()
	set := func(key string, n int, color string) {
		r := t.rows[key]
		r.count.SetText(fmtCount(n))
		if color == "" {
			color = th.Border
		}
		r.swatch.SetStyleSheet("background: " + color + "; border-radius: 4px;")
	}
	set("library", p.InLibrary, lib)
	set("done", p.Downloaded, done)
	set("upgrade", p.NeedUpgrade, upgrade)
	set("downloading", p.Downloading, active)
	set("searching", p.Searching, active)
	set("waiting", p.Waiting, "")
	set("retry", p.Retrying, "")
	set("missing", p.NotFound, missing)
	set("cancelled", p.Cancelled, missing)
	t.bar.set([]barPart{
		{"Already in my library", p.InLibrary, lib},
		{"Downloaded", p.Downloaded, done},
		{"In progress", p.Downloading + p.Searching, active},
		{"Need an upgrade", p.NeedUpgrade, upgrade},
		{"Not found or cancelled", p.NotFound + p.Cancelled, missing},
		{"Waiting", p.Waiting + p.Retrying, ""},
	}, func(n float64) string { return fmtCount(int(n)) + " songs" })

	d := v.Disk
	if !d.Known {
		return
	}
	t.libSize.SetText(core.FmtBytes(d.LibraryBytes))
	t.libSize.SetToolTip(fmtCount(d.LibraryFiles) + " files")
	t.dlSize.SetText(core.FmtBytes(d.DownloadBytes))
	t.dlSize.SetToolTip(fmtCount(d.DownloadFiles) + " files")
	t.cache.SetText(core.FmtBytes(d.CacheBytes))
	mark := func(color, name string) string {
		return "<span style='color:" + color + "'>●</span> " + name + " &nbsp;&nbsp;"
	}
	t.legend.SetText(mark(lib, "My library") + mark(done, "Downloads") + mark(th.Text3, "Everything else") + mark(th.Raised, "Free"))
	for len(t.diskBars) < len(d.Disks) {
		w := qt.NewQWidget2()
		col := qt.NewQVBoxLayout(w)
		col.SetContentsMargins(0, 0, 0, 0)
		col.SetSpacing(4)
		r := &diskRow{w: w, title: text("", "hint"), bar: newSegmentBar()}
		col.AddWidget(r.title.QWidget)
		col.AddWidget(r.bar.w)
		t.disks.AddWidget(w)
		t.diskBars = append(t.diskBars, r)
	}
	for i, r := range t.diskBars {
		r.w.SetVisible(i < len(d.Disks))
		if i >= len(d.Disks) {
			continue
		}
		x := d.Disks[i]
		r.title.SetText(fmt.Sprintf("%s: %s free of %s", x.Path, core.FmtBytes(x.Free), core.FmtBytes(x.Total)))
		other := x.Total - x.Free - x.Library - x.Downloads
		r.bar.setBytes([]barPart{
			{"My library", int(x.Library >> 20), lib},
			{"Downloads", int(x.Downloads >> 20), done},
			{"Everything else", int(max(other, 0) >> 20), th.Text3},
			{"Free", int(x.Free >> 20), ""},
		})
	}
}

// --- the two drawn parts ------------------------------------------------------------------------

type barPart struct {
	name  string
	value int
	color string // "" is the bar's empty track
}

// segmentBar is one whole split into parts: thin, rounded, with hover saying what each part is.
type segmentBar struct {
	w      *qt.QWidget
	parts  []barPart
	format func(float64) string
}

func newSegmentBar() *segmentBar {
	s := &segmentBar{w: qt.NewQWidget2()}
	s.w.SetFixedHeight(10)
	s.w.SetMouseTracking(true)
	s.w.OnPaintEvent(func(_ func(*qt.QPaintEvent), _ *qt.QPaintEvent) { s.paint() })
	s.w.OnMouseMoveEvent(func(_ func(*qt.QMouseEvent), e *qt.QMouseEvent) {
		if part, ok := s.at(e.Position().X()); ok {
			total := 0
			for _, p := range s.parts {
				total += p.value
			}
			tip := fmt.Sprintf("%s: %s (%.0f%%)", part.name, s.format(float64(part.value)), 100*float64(part.value)/float64(max(total, 1)))
			qt.QToolTip_ShowText(e.GlobalPosition().ToPoint(), tip)
		}
	})
	return s
}

func (s *segmentBar) set(parts []barPart, format func(float64) string) {
	s.parts, s.format = parts, format
	s.w.Update()
}

func (s *segmentBar) setBytes(parts []barPart) {
	s.set(parts, func(mb float64) string { return core.FmtBytes(int64(mb) << 20) })
}

// widths are the parts' widths in pixels, leaving a 2px gap between them.
func (s *segmentBar) widths(total float64, full float64) []float64 {
	out := make([]float64, len(s.parts))
	count := 0
	for _, p := range s.parts {
		if p.value > 0 {
			count++
		}
	}
	room := full - float64(max(count-1, 0))*2
	for i, p := range s.parts {
		if p.value > 0 {
			out[i] = max(room*float64(p.value)/total, 3)
		}
	}
	return out
}

func (s *segmentBar) paint() {
	p := qt.NewQPainter2(s.w.QPaintDevice)
	defer func() {
		p.End()
		p.Delete()
	}()
	p.SetRenderHint2(qt.QPainter__Antialiasing, true)
	width, height := float64(s.w.Width()), 8.0
	y := (s.w.Height() - 8) / 2
	total := 0
	for _, part := range s.parts {
		total += part.value
	}
	if total == 0 {
		fillRound(p, 0, y, int(width), int(height), 4, th.Raised)
		return
	}
	x := 0.0
	for i, w := range s.widths(float64(total), width) {
		if w == 0 {
			continue
		}
		color := s.parts[i].color
		if color == "" {
			color = th.Raised
		}
		fillRound(p, int(x), y, int(w+0.5), int(height), 4, color)
		x += w + 2
	}
}

func (s *segmentBar) at(px float64) (barPart, bool) {
	total := 0
	for _, part := range s.parts {
		total += part.value
	}
	if total == 0 {
		return barPart{}, false
	}
	x := 0.0
	for i, w := range s.widths(float64(total), float64(s.w.Width())) {
		if w > 0 && px >= x && px <= x+w+2 {
			return s.parts[i], true
		}
		if w > 0 {
			x += w + 2
		}
	}
	return barPart{}, false
}

// sparkline is one value's last minute: a line, with the latest value marked and the value
// under the mouse told.
type sparkline struct {
	w     *qt.QWidget
	vals  []float64
	hover int
}

func newSparkline() *sparkline {
	s := &sparkline{w: qt.NewQWidget2(), hover: -1}
	s.w.SetFixedHeight(36)
	s.w.SetMouseTracking(true)
	s.w.OnPaintEvent(func(_ func(*qt.QPaintEvent), _ *qt.QPaintEvent) { s.paint() })
	s.w.OnMouseMoveEvent(func(_ func(*qt.QMouseEvent), e *qt.QMouseEvent) {
		if len(s.vals) < 2 {
			return
		}
		i := int(e.Position().X() / float64(s.w.Width()) * float64(speedHistory))
		i -= speedHistory - len(s.vals)
		s.hover = -1
		if i >= 0 && i < len(s.vals) {
			s.hover = i
			ago := float64(len(s.vals)-1-i) * 0.7
			qt.QToolTip_ShowText(e.GlobalPosition().ToPoint(), fmt.Sprintf("%s, %.0f seconds ago", core.FmtRate(s.vals[i]), ago))
		}
		s.w.Update()
	})
	s.w.OnLeaveEvent(func(_ func(*qt.QEvent), _ *qt.QEvent) {
		s.hover = -1
		s.w.Update()
	})
	return s
}

func (s *sparkline) push(v float64) {
	s.vals = append(s.vals, v)
	if len(s.vals) > speedHistory {
		s.vals = append(s.vals[:0], s.vals[len(s.vals)-speedHistory:]...)
	}
}

func (s *sparkline) redraw() { s.w.Update() }

func (s *sparkline) paint() {
	if len(s.vals) < 2 {
		return
	}
	p := qt.NewQPainter2(s.w.QPaintDevice)
	defer func() {
		p.End()
		p.Delete()
	}()
	p.SetRenderHint2(qt.QPainter__Antialiasing, true)
	width, height := float64(s.w.Width()-6), float64(s.w.Height()-6)
	top := 1.0
	for _, v := range s.vals {
		top = max(top, v)
	}
	pos := func(i int) (float64, float64) {
		x := float64(i+speedHistory-len(s.vals)) / float64(speedHistory-1) * width
		return x, 4 + height - s.vals[i]/top*height
	}
	line, area := qt.NewQPainterPath(), qt.NewQPainterPath()
	defer line.Delete()
	defer area.Delete()
	x0, _ := pos(0)
	area.MoveTo2(x0, 4+height)
	for i := range s.vals {
		x, y := pos(i)
		if i == 0 {
			line.MoveTo2(x, y)
		} else {
			line.LineTo2(x, y)
		}
		area.LineTo2(x, y)
	}
	xN, _ := pos(len(s.vals) - 1)
	area.LineTo2(xN, 4+height)
	area.CloseSubpath()
	wash := qt.NewQColor6(th.Accent)
	wash.SetAlphaF(0.12)
	washBrush := qt.NewQBrush3(wash)
	p.FillPath(area, washBrush)
	washBrush.Delete()
	wash.Delete()
	pen := qt.NewQPen8(brushOf(th.Accent), 2, qt.SolidLine, qt.RoundCap, qt.RoundJoin)
	p.SetPenWithPen(pen)
	p.SetBrushWithStyle(qt.NoBrush)
	p.DrawPath(line)
	pen.Delete()
	dot := func(i int) {
		x, y := pos(i)
		ring := qt.NewQPen4(brushOf(th.Surface), 2)
		p.SetPenWithPen(ring)
		p.SetBrush(brushOf(th.Accent))
		p.DrawEllipse2(int(x-4), int(y-4), 8, 8)
		ring.Delete()
	}
	if s.hover >= 0 && s.hover < len(s.vals) {
		x, _ := pos(s.hover)
		p.SetPen(col(th.Border))
		p.DrawLine2(int(x), 2, int(x), s.w.Height()-2)
		dot(s.hover)
	} else {
		dot(len(s.vals) - 1)
	}
}
