package gui

import (
	"cmp"
	"slices"
	"strings"

	qt "github.com/mappu/miqt/qt6"
)

// dataTable is every table in the app: it reads its cells from the page's own data (so a
// hundred thousand rows cost nothing), columns are dragged wider or narrower one at a time,
// clicking a header sorts, and the widths are remembered.
//
// One column, "flex", takes what's left of the width while you haven't sized any column
// yourself; after that every column keeps the width you gave it and the last one fills the rest.

type column struct {
	title string
	width int  // starting width
	right bool // numbers line up on the right
	flex  bool
}

type dataTable struct {
	a     *app
	name  string
	view  *qt.QTableView
	model *qt.QAbstractTableModel
	cols  []column

	count func() int
	text  func(row, col int) string
	color func(row, col int) string                      // "" is the normal color
	num   func(row, col int) (value float64, isNum bool) // how a column sorts, if not as text
	key   func(row int) string                           // what identifies a row, to keep it selected
	plain func(row int) bool                             // a heading: can't be selected or sorted away

	order     []int32 // shown position → row; nil when not sorted
	rowsShown int
	sortCol   int
	sortDesc  bool
	spans     []int
	adjusting bool
	userSized bool
	selKey    string // the selected row's key, taken while its rows were the current ones

	last, lastHead *qt.QVariant // what was just handed to Qt, which copies it
}

func (a *app) newTable(name string, cols ...column) *dataTable {
	t := &dataTable{a: a, name: name, cols: cols, sortCol: -1}
	t.view = qt.NewQTableView2()
	m := qt.NewQAbstractTableModel()
	t.model = m
	m.OnRowCount(func(parent *qt.QModelIndex) int {
		if parent.IsValid() || t.count == nil {
			return 0
		}
		return t.count()
	})
	m.OnColumnCount(func(parent *qt.QModelIndex) int {
		if parent.IsValid() {
			return 0
		}
		return len(cols)
	})
	m.OnHeaderData(func(super func(int, qt.Orientation, int) *qt.QVariant, section int, o qt.Orientation, role int) *qt.QVariant {
		if o == qt.Horizontal && section >= 0 && section < len(cols) {
			switch qt.ItemDataRole(role) {
			case qt.DisplayRole:
				return t.handHead(cols[section].title)
			case qt.TextAlignmentRole:
				return alignment(cols[section].right)
			}
		}
		return super(section, o, role)
	})
	m.OnData(t.data)
	m.OnFlags(func(super func(*qt.QModelIndex) qt.ItemFlag, idx *qt.QModelIndex) qt.ItemFlag {
		if t.plain != nil && idx.IsValid() && t.plain(t.rowAt(idx.Row())) {
			return qt.ItemIsEnabled
		}
		return super(idx)
	})
	v := t.view
	v.SetModel(m.QAbstractItemModel)
	v.SelectionModel().OnSelectionChanged(func(_, _ *qt.QItemSelection) { t.rememberSelection() })
	v.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	v.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	v.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	v.SetShowGrid(false)
	v.SetWordWrap(false)
	v.SetVerticalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	v.SetHorizontalScrollMode(qt.QAbstractItemView__ScrollPerPixel)
	v.SetContextMenuPolicy(qt.CustomContextMenu)
	v.SetMouseTracking(false)
	vh := v.VerticalHeader()
	vh.Hide()
	vh.SetSectionResizeMode(qt.QHeaderView__Fixed) // no measuring of rows
	vh.SetDefaultSectionSize(30)
	h := v.HorizontalHeader()
	h.SetDefaultAlignment(qt.AlignLeft | qt.AlignVCenter)
	h.SetHighlightSections(false)
	h.SetSectionsMovable(false)
	h.SetMinimumSectionSize(44)
	h.SetSectionResizeMode(qt.QHeaderView__Interactive)
	h.SetStretchLastSection(true)
	h.SetSectionsClickable(true)
	h.SetSortIndicatorShown(false)
	h.OnSectionClicked(t.toggleSort)
	h.OnSectionResized(func(section, _, _ int) {
		if !t.adjusting && section != len(cols)-1 {
			t.userSized = true
			a.ui.columnsChanged(a, t)
		}
	})
	v.OnResizeEvent(func(super func(*qt.QResizeEvent), e *qt.QResizeEvent) {
		super(e)
		t.fit()
	})
	t.adjusting = true
	for i, c := range cols {
		v.SetColumnWidth(i, c.width)
	}
	if saved := a.ui.Columns[name]; len(saved) == len(cols) {
		for i, w := range saved {
			v.SetColumnWidth(i, w)
		}
		t.userSized = true
	}
	t.adjusting = false
	return t
}

func alignment(right bool) *qt.QVariant {
	if alignLeft == nil {
		alignLeft = qt.NewQVariant4(int(qt.AlignLeft | qt.AlignVCenter))
		alignRight = qt.NewQVariant4(int(qt.AlignRight | qt.AlignVCenter))
	}
	if right {
		return alignRight
	}
	return alignLeft
}

var alignLeft, alignRight *qt.QVariant

var colorVariants = map[string]*qt.QVariant{}

func colorVariant(hex string) *qt.QVariant {
	v := colorVariants[hex]
	if v == nil {
		v = col(hex).ToQVariant()
		colorVariants[hex] = v
	}
	return v
}

func (t *dataTable) hand(s string) *qt.QVariant {
	if t.last != nil {
		t.last.Delete()
	}
	t.last = qt.NewQVariant14(s)
	return t.last
}

func (t *dataTable) handHead(s string) *qt.QVariant {
	if t.lastHead != nil {
		t.lastHead.Delete()
	}
	t.lastHead = qt.NewQVariant14(s)
	return t.lastHead
}

func (t *dataTable) data(idx *qt.QModelIndex, role int) *qt.QVariant {
	r, c := idx.Row(), idx.Column()
	if t.count == nil || r < 0 || r >= t.count() || c < 0 || c >= len(t.cols) {
		return noData
	}
	row := t.rowAt(r)
	switch qt.ItemDataRole(role) {
	case qt.DisplayRole:
		return t.hand(t.text(row, c))
	case qt.ForegroundRole:
		if t.color != nil {
			if hex := t.color(row, c); hex != "" {
				return colorVariant(hex)
			}
		}
	case qt.TextAlignmentRole:
		return alignment(t.cols[c].right)
	case qt.ToolTipRole:
		if s := t.text(row, c); len(s) > 24 {
			return t.hand(s)
		}
	}
	return noData
}

func (t *dataTable) rowAt(shown int) int {
	if t.order != nil && shown >= 0 && shown < len(t.order) {
		return int(t.order[shown])
	}
	return shown
}

// fit gives the flex column what's left, until you've sized a column yourself.
func (t *dataTable) fit() {
	if t.userSized {
		return
	}
	flex, others := -1, 0
	for i, c := range t.cols {
		switch {
		case c.flex:
			flex = i
		case i == len(t.cols)-1:
			others += c.width // the last one stretches, so its real width says nothing
		default:
			others += t.view.ColumnWidth(i)
		}
	}
	if flex < 0 || flex == len(t.cols)-1 {
		return
	}
	t.adjusting = true
	t.view.SetColumnWidth(flex, max(120, t.view.Viewport().Width()-others))
	t.adjusting = false
}

func (t *dataTable) toggleSort(col int) {
	switch {
	case t.sortCol != col:
		t.sortCol, t.sortDesc = col, false
	case !t.sortDesc:
		t.sortDesc = true
	default:
		t.sortCol = -1
	}
	h := t.view.HorizontalHeader()
	h.SetSortIndicatorShown(t.sortCol >= 0)
	if t.sortCol >= 0 {
		o := qt.AscendingOrder
		if t.sortDesc {
			o = qt.DescendingOrder
		}
		h.SetSortIndicator(t.sortCol, o)
	}
	t.reload()
}

func (t *dataTable) sortRows() {
	if t.sortCol < 0 || t.count == nil || t.plain != nil {
		t.order = nil
		return
	}
	n := t.count()
	order := make([]int32, n)
	keys := make([]string, n)
	nums := make([]float64, n)
	isNum := false
	for i := range n {
		order[i] = int32(i)
		if t.num != nil {
			if v, ok := t.num(i, t.sortCol); ok {
				nums[i], isNum = v, true
				continue
			}
		}
		keys[i] = strings.ToLower(t.text(i, t.sortCol))
	}
	dir := 1
	if t.sortDesc {
		dir = -1
	}
	slices.SortStableFunc(order, func(a, b int32) int {
		if isNum {
			return dir * cmp.Compare(nums[a], nums[b])
		}
		return dir * cmp.Compare(keys[a], keys[b])
	})
	t.order = order
}

// changed is to be called when the rows changed: they're read again, and the selected row
// stays selected. Cheap when the number of rows is the same.
func (t *dataTable) changed() {
	if t.sortCol >= 0 || t.count() != t.rowsShown {
		t.reload()
		return
	}
	t.view.Viewport().Update()
}

func (t *dataTable) rememberSelection() {
	t.selKey = ""
	if row := t.current(); row >= 0 && t.key != nil && t.count != nil && row < t.count() {
		t.selKey = t.key(row)
	}
}

// reload redoes the table: for a new set of rows or a new order. By now the page's data may
// already be the new rows, so the selected row's key can't be read here: it could be gone.
func (t *dataTable) reload() {
	sel := t.selKey
	bar := t.view.VerticalScrollBar().Value()
	t.model.BeginResetModel()
	t.sortRows()
	t.model.EndResetModel()
	if t.count != nil {
		t.rowsShown = t.count()
	}
	t.view.ClearSpans()
	for _, r := range t.spans {
		t.view.SetSpan(r, 0, 1, len(t.cols))
	}
	t.selKey = ""
	if sel != "" {
		for i := range t.rowsShown {
			if t.key(t.rowAt(i)) == sel {
				t.view.SelectRow(i)
				break
			}
		}
	}
	t.view.VerticalScrollBar().SetValue(bar)
}

// current is the selected row of the page's data, or -1.
func (t *dataTable) current() int {
	idx := t.view.CurrentIndex()
	if !idx.IsValid() || !t.view.SelectionModel().IsSelected(idx) {
		return -1
	}
	return t.rowAt(idx.Row())
}

// selectRow selects a row of the page's data.
func (t *dataTable) selectRow(row int) {
	if t.order == nil {
		t.view.SelectRow(row)
		return
	}
	for i, r := range t.order {
		if int(r) == row {
			t.view.SelectRow(i)
			return
		}
	}
}

func (t *dataTable) onActivate(f func(row int)) {
	t.view.OnDoubleClicked(func(idx *qt.QModelIndex) { f(t.rowAt(idx.Row())) })
}

func (t *dataTable) onSelect(f func()) {
	t.view.SelectionModel().OnSelectionChanged(func(_, _ *qt.QItemSelection) { f() })
}

func (t *dataTable) onMenu(f func(row int, at *qt.QPoint)) {
	t.view.OnCustomContextMenuRequested(func(pos *qt.QPoint) {
		idx := t.view.IndexAt(pos)
		if idx.IsValid() {
			t.view.SelectRow(idx.Row())
			f(t.rowAt(idx.Row()), t.view.Viewport().MapToGlobalWithQPoint(pos))
		}
	})
}
