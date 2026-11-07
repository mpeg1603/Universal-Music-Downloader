package core

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Messages for you go to the log view (and the log file); details only to the log file,
// unless verbose is on.

var (
	DebugLog = log.New(discard{}, "", 0)
	Verbose  bool
	color    = isTerminal(os.Stdout)
)

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

type LogLine struct {
	At    time.Time
	Text  string
	Debug bool
}

type logRing struct {
	mu     sync.Mutex
	lines  []LogLine
	max    int
	seq    int64
	OnLine func(LogLine) // headless mode prints them
}

var AppLog = &logRing{max: 3000}

func (r *logRing) add(text string, debug bool) {
	l := LogLine{At: time.Now(), Text: text, Debug: debug}
	r.mu.Lock()
	r.lines = append(r.lines, l)
	if len(r.lines) > r.max {
		r.lines = append([]LogLine(nil), r.lines[len(r.lines)-r.max:]...)
	}
	r.seq++
	f := r.OnLine
	r.mu.Unlock()
	if f != nil {
		f(l)
	}
}

// snapshot returns the lines and a counter that changes whenever a line is added.
func (r *logRing) Snapshot() ([]LogLine, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]LogLine(nil), r.lines...), r.seq
}

func debugf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	DebugLog.Print(msg)
	if Verbose {
		AppLog.add(msg, true)
	}
}

// logf shows a message to you (and keeps it in the log file).
func Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	DebugLog.Print(msg)
	AppLog.add(msg, false)
}

func IsCharDevice(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func isTerminal(f *os.File) bool {
	return IsCharDevice(f) && os.Getenv("NO_COLOR") == ""
}

func Paint(code, s string) string {
	if !color {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// --- formatting ---------------------------------------------------------------------------------

func FmtBytes(n int64) string {
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

// fmtRate shows a speed in MB/s (decimal, as the bandwidth setting).
func FmtRate(bps float64) string {
	switch {
	case bps >= 1e6:
		return fmt.Sprintf("%.1f MB/s", bps/1e6)
	case bps >= 1e3:
		return fmt.Sprintf("%.0f kB/s", bps/1e3)
	}
	return fmt.Sprintf("%.0f B/s", bps)
}

// fmtBytePair shows done/total, with the unit once when they share it: "41.0/86.0 MiB".
func FmtBytePair(done, total int64) string {
	d, t := FmtBytes(done), FmtBytes(total)
	if i, j := strings.LastIndexByte(d, ' '), strings.LastIndexByte(t, ' '); d[i:] == t[j:] {
		return d[:i] + "/" + t
	}
	return d + " / " + t
}

func FmtWait(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d hour(s)", int(d.Hours()))
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return d.Round(time.Second).String()
}

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
func FitWidth(s string, width int) string {
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
