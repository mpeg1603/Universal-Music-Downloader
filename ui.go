package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The terminal display while downloading: finished tracks scroll by as lines, and below
// them a panel, redrawn a few times a second, shows every track in progress (with a progress
// bar while it downloads), the overall progress and the free disk space. When the output
// isn't a terminal, only the lines are printed.

type UI struct {
	out *os.File

	mu       sync.Mutex
	live     bool
	drawn    int // panel lines currently on screen
	board    *Board
	diskPath string
	total    int
	finished int
	counts   map[string]int
	started  time.Time
	stopping bool
	frame    int
	free     int64
	freeAt   time.Time
	quit     chan struct{}
	exited   chan struct{}
}

var screen = &UI{out: os.Stdout}

func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// println prints a line above the panel (or just prints it when there's no panel).
func (u *UI) println(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.live {
		fmt.Fprintln(u.out, s)
		return
	}
	u.redrawLocked(s)
}

// printDebug shows a -v line: above the panel while it's up, otherwise on stderr.
func (u *UI) printDebug(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.live {
		u.redrawLocked(paint("2", s))
	} else {
		fmt.Fprintln(os.Stderr, s)
	}
}

// start shows the panel for a run of total tracks; diskPath is where the music goes.
func (u *UI) start(board *Board, total int, diskPath string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.board, u.total, u.diskPath, u.started, u.counts = board, total, diskPath, time.Now(), map[string]int{}
	if !liveDisplaySupported || !isCharDevice(u.out) || os.Getenv("TERM") == "dumb" {
		return
	}
	if w, h := termSize(u.out); w < 40 || h < 5 {
		return
	}
	u.live = true
	u.quit, u.exited = make(chan struct{}), make(chan struct{})
	go u.loop()
}

func (u *UI) loop() {
	defer close(u.exited)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-u.quit:
			return
		case <-tick.C:
			u.mu.Lock()
			u.frame++
			u.redrawLocked()
			u.mu.Unlock()
		}
	}
}

// record counts a finished track (status as in Outcome).
func (u *UI) record(status string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.counts == nil {
		u.counts = map[string]int{}
	}
	u.counts[status]++
	u.finished++
}

func (u *UI) setStopping() {
	u.mu.Lock()
	u.stopping = true
	u.mu.Unlock()
}

// stop removes the panel; output is plain lines again.
func (u *UI) stop() {
	u.mu.Lock()
	if !u.live {
		u.mu.Unlock()
		return
	}
	close(u.quit)
	u.mu.Unlock()
	<-u.exited
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.drawn > 0 {
		fmt.Fprintf(u.out, "\r\x1b[%dA\x1b[J", u.drawn)
	}
	u.drawn, u.live = 0, false
}

func (u *UI) redrawLocked(lines ...string) {
	width, height := termSize(u.out)
	if width <= 0 {
		width, height = 80, 24
	}
	var b strings.Builder
	if u.drawn > 0 {
		fmt.Fprintf(&b, "\r\x1b[%dA\x1b[J", u.drawn) // back to the panel's first line, erase it
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	panel := u.panel(width, height)
	for _, l := range panel {
		b.WriteString(l + "\n")
	}
	u.drawn = len(panel)
	u.out.WriteString(b.String())
}

// --- the panel -------------------------------------------------------------------------------

func (u *UI) panel(width, height int) []string {
	tasks := u.board.snapshot()
	// Busiest first: converting, then downloading, then searching, then waiting on others.
	sort.SliceStable(tasks, func(i, j int) bool { return stageRank(tasks[i]) < stageRank(tasks[j]) })
	rows := max(1, min(height-4, 14))
	lines := []string{paint("2", strings.Repeat("─", width-1))}
	shown := tasks
	if len(tasks) > rows {
		shown = tasks[:rows-1]
	}
	for _, t := range shown {
		lines = append(lines, fitWidth(u.taskLine(t, width), width-1))
	}
	if hidden := tasks[len(shown):]; len(hidden) > 0 {
		lines = append(lines, paint("2", fitWidth(fmt.Sprintf("  … and %d more (%s)", len(hidden), summarize(hidden)), width-1)))
	}
	lines = append(lines, fitWidth(u.overallLine(tasks, width), width-1))
	return lines
}

func stageRank(t taskView) int {
	switch {
	case t.Stage == "converting":
		return 0
	case t.Total > 0:
		return 1
	case strings.HasPrefix(t.Stage, "searching"), strings.HasPrefix(t.Stage, "downloading"):
		return 2
	}
	return 3
}

func summarize(tasks []taskView) string {
	n := map[string]int{}
	var order []string
	for _, t := range tasks {
		s := t.Stage
		if t.Total > 0 {
			s = "downloading"
		}
		if n[s] == 0 {
			order = append(order, s)
		}
		n[s]++
	}
	var parts []string
	for _, s := range order {
		parts = append(parts, fmt.Sprintf("%d %s", n[s], s))
	}
	return strings.Join(parts, ", ")
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func (u *UI) taskLine(t taskView, width int) string {
	nameW := max(18, min(48, width/3))
	label := t.Track.String()
	if a := t.Track.Album; a != "" && !strings.EqualFold(a, t.Track.Title) {
		label += paint("2", " · "+a)
	}
	name := padWidth(label, nameW)
	if t.Total > 0 {
		frac := float64(t.Done) / float64(t.Total)
		parts := []string{
			paint("36", "↓"), name, bar(frac, max(10, min(30, width/6)), "32"),
			fmt.Sprintf("%3.0f%%", 100*frac),
			fmtBytePair(t.Done, t.Total),
		}
		if t.Speed > 0 && t.Group == "" {
			parts = append(parts, fmtBytes(int64(t.Speed))+"/s")
		}
		if t.Note != "" {
			parts = append(parts, paint("2", t.Note))
		}
		return " " + strings.Join(parts, "  ")
	}
	icon := string(spinner[(u.frame+int(t.Started.UnixMilli()/97))%len(spinner)])
	if stageRank(t) == 3 {
		icon = "·"
	}
	stage := firstNonEmpty(t.Stage, "starting")
	if t.Note != "" {
		stage += " · " + t.Note
	}
	return " " + paint("33", icon) + "  " + name + "  " + paint("2", stage)
}

func (u *UI) overallLine(tasks []taskView, width int) string {
	frac := 0.0
	if u.total > 0 {
		frac = float64(u.finished) / float64(u.total)
	}
	parts := []string{
		paint("1", "Overall"),
		bar(frac, max(10, min(40, width/4)), "36"),
		fmt.Sprintf("%d/%d", u.finished, u.total),
		paint("32", fmt.Sprintf("✓ %d", u.counts["downloaded"])),
		paint("31", fmt.Sprintf("✗ %d", u.counts["failed"]+u.counts["error"])),
	}
	// Tracks sharing a torrent count once: its speed once, each of its files once.
	speed, remaining := 0.0, int64(0)
	groups, files := map[string]bool{}, map[string]bool{}
	for _, t := range tasks {
		if t.Group == "" || !groups[t.Group] {
			speed += t.Speed
			groups[t.Group] = t.Group != ""
		}
		if key := t.Group + "#" + t.File; t.Total > 0 && (t.Group == "" || !files[key]) {
			remaining += t.Total - t.Done
			files[key] = true
		}
	}
	if speed > 0 {
		dl := "⇣ " + fmtBytes(int64(speed)) + "/s"
		if remaining > 0 {
			dl += fmt.Sprintf(" · %s to go (~%s)", fmtBytes(remaining), fmtETA(time.Duration(float64(remaining)/speed*float64(time.Second))))
		}
		parts = append(parts, dl)
	}
	if time.Since(u.freeAt) > 2*time.Second {
		u.free, u.freeAt = diskFree(u.diskPath), time.Now()
	}
	if u.free >= 0 {
		disk := "disk: " + fmtBytes(u.free) + " free"
		if u.free < 2<<30 {
			disk = paint("31", disk)
		}
		parts = append(parts, disk)
	}
	if u.stopping {
		parts = append(parts, paint("33", "stopping after the tracks in progress (Ctrl-C again to quit now)"))
	}
	return " " + strings.Join(parts, "  ")
}

// bar draws a progress bar of width cells: heavy line for done, light line for the rest.
func bar(frac float64, width int, color string) string {
	frac = max(0, min(1, frac))
	cells := frac * float64(width)
	full := int(cells)
	done := strings.Repeat("━", full)
	rest := width - full
	if rest > 0 && cells-float64(full) >= 0.5 {
		done += "╸"
		rest--
	}
	return paint(color, done) + paint("2", strings.Repeat("─", rest))
}

func fmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, 0
	for v >= 1024 && unit < 5 {
		v /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %ciB", v, "KMGTPE"[unit])
}

// fmtBytePair shows done/total, with the unit once when they share it: "41.0/86.0 MiB".
func fmtBytePair(done, total int64) string {
	d, t := fmtBytes(done), fmtBytes(total)
	if i, j := strings.LastIndexByte(d, ' '), strings.LastIndexByte(t, ' '); d[i:] == t[j:] {
		return d[:i] + "/" + t
	}
	return d + " / " + t
}

func fmtETA(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// --- display width ----------------------------------------------------------------------------

func runeWidth(r rune) int {
	switch {
	case r == 0 || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
		return 0
	case r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe6f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || (r >= 0x1f300 && r <= 0x1f64f) || (r >= 0x1f900 && r <= 0x1f9ff) ||
		(r >= 0x20000 && r <= 0x3fffd)):
		return 2
	}
	return 1
}

// displayWidth is how many terminal columns s takes; color codes take none.
func displayWidth(s string) int {
	w, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			esc = !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
		default:
			w += runeWidth(r)
		}
	}
	return w
}

// fitWidth cuts s (which may contain color codes) to at most width columns, ending it with
// an ellipsis when something was cut.
func fitWidth(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w, esc := 0, false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc:
			esc = !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
		default:
			if w+runeWidth(r) > width-1 {
				b.WriteString("…")
				if strings.Contains(s, "\x1b[") {
					b.WriteString("\x1b[0m")
				}
				return b.String()
			}
			w += runeWidth(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// padWidth cuts or pads plain text to exactly width columns.
func padWidth(s string, width int) string {
	s = fitWidth(s, width)
	return s + strings.Repeat(" ", max(0, width-displayWidth(s)))
}
