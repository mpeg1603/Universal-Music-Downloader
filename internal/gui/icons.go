package gui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	qt "github.com/mappu/miqt/qt6"
)

// The app's icons: line drawings on a 24×24 grid, drawn by the app so they're sharp at any
// size and take any color. Paths use SVG's commands M L H V C Q Z (and lowercase, relative);
// "O x y r" is a circle.
var iconPaths = map[string]string{
	"download":      "M12 3v12 M7 10l5 5 5-5 M4 20h16",
	"search":        "O 11 11 7 M20 20L16.2 16.2",
	"playlist":      "M4 6h13 M4 11h13 M4 16h7 M20 8v9 O 17.5 17.5 2.5 M20 8l-3-1",
	"library":       "O 12 12 9 O 12 12 2.6 M12 3v3 M21 12h-3",
	"sources":       "M9 3v5 M15 3v5 M6 8h12v3c0 3.6-2.7 6-6 6s-6-2.4-6-6z M12 17v4",
	"people":        "O 9 8 3.5 M3 20c0-3.5 2.7-6 6-6s6 2.5 6 6 M16 4.7c1.5.5 2.5 1.8 2.5 3.3s-1 2.8-2.5 3.3 M18 14.5c1.8.8 3 2.8 3 5.5",
	"settings":      "M4 7h9 M17 7h3 M4 17h3 M11 17h9 O 15 7 2 O 9 17 2",
	"activity":      "M3 12h4l3 7 4-14 3 7h4",
	"merge":         "M6 3v5c0 3.5 6 4 6 8v5 M18 3v5c0 3.5-6 4-6 8",
	"log":           "M5 6h14 M5 12h14 M5 18h9",
	"gauge":         "M3.5 18c-1-1.6-1.5-3.3-1.5-5 0-5.5 4.5-10 10-10s10 4.5 10 10c0 1.7-.5 3.4-1.5 5 M12 14l4-5",
	"help":          "O 12 12 9 M9.5 9.6c0-1.4 1.1-2.6 2.5-2.6s2.5 1.1 2.5 2.5c0 2-2.5 2.1-2.5 4.1 M12 17.2v.1",
	"pause":         "M8 5v14 M16 5v14",
	"play":          "M7 4.5l12.5 7.5-12.5 7.5z",
	"plus":          "M12 5v14 M5 12h14",
	"check":         "M5 12.5l4.5 4.5L19 7",
	"x":             "M6 6l12 12 M18 6L6 18",
	"chevron-down":  "M6 9l6 6 6-6",
	"chevron-up":    "M6 15l6-6 6 6",
	"chevron-right": "M9 6l6 6-6 6",
	"folder":        "M3 7c0-1.1.9-2 2-2h4l2 2.5h8c1.1 0 2 .9 2 2V17c0 1.1-.9 2-2 2H5c-1.1 0-2-.9-2-2z",
	"file":          "M6 3h8l4 4v14H6z M14 3v4h4",
	"link":          "M10 14c1.4 1.5 3.6 1.5 5 0l3.5-3.5c1.4-1.4 1.4-3.6 0-5s-3.6-1.4-5 0L12 7 M14 10c-1.4-1.5-3.6-1.5-5 0l-3.5 3.5c-1.4 1.4-1.4 3.6 0 5s3.6 1.4 5 0L12 17",
	"refresh":       "M20 11c-.6-4-4-7-8-7-3 0-5.6 1.6-7 4 M4 4v4.5h4.5 M4 13c.6 4 4 7 8 7 3 0 5.6-1.6 7-4 M20 20v-4.5h-4.5",
	"trash":         "M4 7h16 M9 7V4h6v3 M6 7l1 13h10l1-13 M10 11v5 M14 11v5",
	"star":          "M12 3.5l2.7 5.5 6 .9-4.4 4.2 1 6-5.3-2.8-5.3 2.8 1-6-4.4-4.2 6-.9z",
	"moon":          "M20 14.5c-1 .3-2 .5-3 .5-4.7 0-8.5-3.8-8.5-8.5 0-1.2.2-2.3.7-3.4C5.7 4.5 3.5 7.9 3.5 11.5c0 5 4 9 9 9 3.4 0 6.3-1.9 7.5-6z",
	"sun":           "O 12 12 4 M12 3v2 M12 19v2 M3 12h2 M19 12h2 M5.6 5.6L7 7 M17 17l1.4 1.4 M5.6 18.4L7 17 M17 7l1.4-1.4",
	"monitor":       "M3 5h18v12H3z M9 21h6 M12 17v4",
	"music":         "M9 18V6l11-2v12 O 6.5 18 2.5 O 17.5 16 2.5",
	"album":         "M3 3h18v18H3z O 12 12 5 O 12 12 1",
	"artist":        "O 12 8 4 M4.5 21c0-4 3.4-7 7.5-7s7.5 3 7.5 7",
	"arrow-up":      "M12 19V5 M6 11l6-6 6 6",
	"arrow-down":    "M12 5v14 M6 13l6 6 6-6",
	"sort":          "M8 4v16 M4 8l4-4 4 4 M16 20V4 M12 16l4 4 4-4",
	"external":      "M14 4h6v6 M20 4l-9 9 M18 14v6H4V6h6",
	"message":       "M4 5h16v12H9l-5 4z",
	"clock":         "O 12 12 9 M12 7v5l3.5 2",
	"alert":         "M12 4l9.5 16.5h-19z M12 10v5 M12 18v.1",
	"ban":           "O 12 12 9 M5.7 5.7l12.6 12.6",
	"dot":           "O 12 12 2",
	"signal":        "M4 20v-4 M10 20V11 M16 20V7 M22 20V3",
	"heart":         "M12 20s-8-5-8-11c0-2.8 2.2-4.6 4.5-4.6 1.5 0 2.8.7 3.5 2 .7-1.3 2-2 3.5-2 2.3 0 4.5 1.8 4.5 4.6 0 6-8 11-8 11z",
	"key":           "O 8 15 4.5 M11.2 11.8L20 3 M16.5 6.5l3 3",
	"logo":          "M9 18V6l11-2v12 O 6.5 18 2.5 O 17.5 16 2.5",
}

var (
	pixmapCache = map[string]*qt.QPixmap{}
	iconCache   = map[string]*qt.QIcon{}
)

// iconScale is how much finer than its size an icon is drawn, for high-density screens.
const iconScale = 2

// iconPixmap is an icon as a picture; the result is shared, don't change it.
func iconPixmap(name string, size int, color string) *qt.QPixmap {
	key := fmt.Sprint(name, size, color)
	if p := pixmapCache[key]; p != nil {
		return p
	}
	pm := qt.NewQPixmap2(size*iconScale, size*iconScale)
	pm.FillWithFillColor(qt.NewQColor2(qt.Transparent))
	paint := qt.NewQPainter2(pm.QPaintDevice)
	paint.SetRenderHint2(qt.QPainter__Antialiasing, true)
	s := float64(size*iconScale) / 24
	paint.Scale(s, s)
	pen := qt.NewQPen8(brushOf(color), 1.8, qt.SolidLine, qt.RoundCap, qt.RoundJoin)
	paint.SetPenWithPen(pen)
	paint.SetBrushWithStyle(qt.NoBrush)
	for _, sub := range iconShapes(iconPaths[name]) {
		paint.DrawPath(sub)
		sub.Delete()
	}
	paint.End()
	paint.Delete()
	pen.Delete()
	pm.SetDevicePixelRatio(iconScale)
	pixmapCache[key] = pm
	return pm
}

// iconOf is an icon for a button; the result is shared, and looks faded when disabled.
func iconOf(name string, size int, color string) *qt.QIcon {
	key := fmt.Sprint(name, size, color)
	ic := iconCache[key]
	if ic == nil {
		ic = qt.NewQIcon2(iconPixmap(name, size, color))
		ic.AddPixmap3(iconPixmap(name, size, th.Text3), qt.QIcon__Disabled, qt.QIcon__Off)
		iconCache[key] = ic
	}
	return ic
}

// iconTwo is an icon in one color normally and another when the button is checked.
func iconTwo(name string, size int, off, on string) *qt.QIcon {
	ic := qt.NewQIcon()
	ic.AddPixmap3(iconPixmap(name, size, off), qt.QIcon__Normal, qt.QIcon__Off)
	ic.AddPixmap3(iconPixmap(name, size, on), qt.QIcon__Normal, qt.QIcon__On)
	return ic
}

// iconShapes reads an icon's description.
func iconShapes(d string) []*qt.QPainterPath {
	r := iconReader{s: d}
	path := qt.NewQPainterPath()
	var x, y, startX, startY float64
	var cmd byte
	for {
		r.skip()
		if r.done() {
			break
		}
		if c := r.s[r.i]; unicode.IsLetter(rune(c)) {
			cmd = c
			r.i++
		}
		rel := cmd >= 'a' && cmd <= 'z'
		base := func() (float64, float64) {
			if rel {
				return x, y
			}
			return 0, 0
		}
		switch cmd {
		case 'O':
			cx, cy, rad := r.num(), r.num(), r.num()
			path.AddEllipse2(cx-rad, cy-rad, 2*rad, 2*rad)
		case 'M', 'm':
			bx, by := base()
			x, y = bx+r.num(), by+r.num()
			startX, startY = x, y
			path.MoveTo2(x, y)
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L', 'l':
			bx, by := base()
			x, y = bx+r.num(), by+r.num()
			path.LineTo2(x, y)
		case 'H', 'h':
			bx, _ := base()
			x = bx + r.num()
			path.LineTo2(x, y)
		case 'V', 'v':
			_, by := base()
			y = by + r.num()
			path.LineTo2(x, y)
		case 'C', 'c':
			bx, by := base()
			x1, y1, x2, y2, ex, ey := bx+r.num(), by+r.num(), bx+r.num(), by+r.num(), bx+r.num(), by+r.num()
			path.CubicTo2(x1, y1, x2, y2, ex, ey)
			x, y = ex, ey
		case 'S', 's':
			// the first control point repeats the end point: enough for these drawings
			bx, by := base()
			x2, y2, ex, ey := bx+r.num(), by+r.num(), bx+r.num(), by+r.num()
			path.CubicTo2(x, y, x2, y2, ex, ey)
			x, y = ex, ey
		case 'Q', 'q':
			bx, by := base()
			x1, y1, ex, ey := bx+r.num(), by+r.num(), bx+r.num(), by+r.num()
			path.QuadTo2(x1, y1, ex, ey)
			x, y = ex, ey
		case 'Z', 'z':
			path.CloseSubpath()
			x, y = startX, startY
		default:
			return []*qt.QPainterPath{path}
		}
	}
	return []*qt.QPainterPath{path}
}

type iconReader struct {
	s string
	i int
}

func (r *iconReader) done() bool { return r.i >= len(r.s) }

func (r *iconReader) skip() {
	for r.i < len(r.s) && (r.s[r.i] == ' ' || r.s[r.i] == ',') {
		r.i++
	}
}

// num reads a number; SVG lets them run together ("1.5-2.5", ".5.6").
func (r *iconReader) num() float64 {
	r.skip()
	start, dot := r.i, false
	if r.i < len(r.s) && (r.s[r.i] == '-' || r.s[r.i] == '+') {
		r.i++
	}
	for r.i < len(r.s) {
		c := r.s[r.i]
		if c == '.' {
			if dot {
				break
			}
			dot = true
		} else if c < '0' || c > '9' {
			break
		}
		r.i++
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(r.s[start:r.i]), 64)
	return v
}
