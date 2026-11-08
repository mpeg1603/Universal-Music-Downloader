package gui

import (
	qt "github.com/mappu/miqt/qt6"
)

// rowList is a list whose rows the page draws itself: fast at any length (only what's visible
// is drawn) and free to look like anything.
type rowList struct {
	view  *qt.QListView
	model *qt.QAbstractListModel
	count func() int
}

// rowCtx is a row being drawn.
type rowCtx struct {
	p          *qt.QPainter
	row        int
	x, y, w, h int
	selected   bool
	hover      bool
}

var (
	noData    *qt.QVariant
	rootIndex *qt.QModelIndex
)

func newRowList(height int, count func() int, draw func(c *rowCtx)) *rowList {
	if noData == nil {
		noData, rootIndex = qt.NewQVariant(), qt.NewQModelIndex()
	}
	l := &rowList{count: count, view: qt.NewQListView2()}
	l.model = qt.NewQAbstractListModel()
	l.model.OnRowCount(func(parent *qt.QModelIndex) int {
		if parent.IsValid() {
			return 0
		}
		return count()
	})
	l.model.OnData(func(*qt.QModelIndex, int) *qt.QVariant { return noData })
	size := qt.NewQSize2(200, height)
	d := qt.NewQStyledItemDelegate()
	d.OnSizeHint(func(_ func(*qt.QStyleOptionViewItem, *qt.QModelIndex) *qt.QSize, _ *qt.QStyleOptionViewItem, _ *qt.QModelIndex) *qt.QSize {
		return size
	})
	d.OnPaint(func(_ func(*qt.QPainter, *qt.QStyleOptionViewItem, *qt.QModelIndex), p *qt.QPainter, opt *qt.QStyleOptionViewItem, idx *qt.QModelIndex) {
		r := opt.QStyleOption.Rect()
		state := opt.QStyleOption.State()
		c := &rowCtx{p: p, row: idx.Row(), x: r.X(), y: r.Y(), w: r.Width(), h: r.Height(),
			selected: state&qt.QStyle__State_Selected != 0, hover: state&qt.QStyle__State_MouseOver != 0}
		p.Save()
		p.SetRenderHint2(qt.QPainter__Antialiasing, true)
		switch {
		case c.selected:
			fillRound(p, c.x+2, c.y+1, c.w-4, c.h-2, 8, th.AccentSoft)
		case c.hover:
			fillRound(p, c.x+2, c.y+1, c.w-4, c.h-2, 8, th.Hover)
		}
		draw(c)
		p.Restore()
	})
	l.view.SetModel(l.model.QAbstractItemModel)
	l.view.SetItemDelegate(d.QAbstractItemDelegate)
	l.view.SetUniformItemSizes(true)
	l.view.SetMouseTracking(true)
	l.view.SetVerticalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	l.view.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	l.view.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	l.view.SetContextMenuPolicy(qt.CustomContextMenu)
	l.view.VerticalScrollBar().SetSingleStep(24)
	return l
}

// current is the row selected, or -1.
func (l *rowList) current() int {
	idx := l.view.CurrentIndex()
	if !idx.IsValid() || !l.view.SelectionModel().IsSelected(idx) {
		return -1
	}
	return idx.Row()
}

func (l *rowList) selectRow(row int) {
	if row < 0 || row >= l.count() {
		return
	}
	idx := l.model.Index(row, 0, rootIndex)
	l.view.SetCurrentIndex(idx)
}

func (l *rowList) reset(change func()) {
	l.model.BeginResetModel()
	change()
	l.model.EndResetModel()
}

func (l *rowList) redraw() { l.view.Viewport().Update() }

// onSelect calls f when the row selected changes (-1 when none is). It watches the selection,
// not the current row: a list makes its first row current before anything is clicked.
func (l *rowList) onSelect(f func(row int)) {
	l.view.SelectionModel().OnSelectionChanged(func(_, _ *qt.QItemSelection) { f(l.current()) })
}

func (l *rowList) onActivate(f func()) {
	l.view.OnDoubleClicked(func(*qt.QModelIndex) { f() })
}

func (l *rowList) onMenu(f func(at *qt.QPoint)) {
	l.view.OnCustomContextMenuRequested(func(pos *qt.QPoint) { f(l.view.Viewport().MapToGlobalWithQPoint(pos)) })
}

// --- drawing helpers ----------------------------------------------------------------------------

func fillRound(p *qt.QPainter, x, y, w, h int, radius float64, color string) {
	p.SetPenWithStyle(qt.NoPen)
	p.SetBrush(brushOf(color))
	p.DrawRoundedRect2(x, y, w, h, radius, radius)
}

func drawText(p *qt.QPainter, x, y, w, h int, flags qt.AlignmentFlag, s string, font *qt.QFont, m *qt.QFontMetrics, color string) {
	p.SetFont(font)
	p.SetPen(col(color))
	p.DrawText7(x, y, w, h, int(flags), m.ElidedText(s, qt.ElideRight, w))
}

func drawBar(p *qt.QPainter, x, y, w, h int, frac float64, color string) {
	fillRound(p, x, y, w, h, float64(h)/2, th.Raised)
	if frac > 0 {
		fillRound(p, x, y, max(h, int(float64(w)*min(frac, 1))), h, float64(h)/2, color)
	}
}

func drawIcon(p *qt.QPainter, x, y int, name string, size int, color string) {
	p.DrawPixmap9(x, y, iconPixmap(name, size, color))
}

// drawBadge is an icon in a soft colored circle.
func drawBadge(p *qt.QPainter, x, y, size int, icon, color, soft string) {
	p.SetPenWithStyle(qt.NoPen)
	p.SetBrush(brushOf(soft))
	p.DrawEllipse2(x, y, size, size)
	inner := size * 5 / 9
	drawIcon(p, x+(size-inner)/2, y+(size-inner)/2, icon, inner, color)
}
