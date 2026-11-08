package gui

import (
	"path/filepath"

	qt "github.com/mappu/miqt/qt6"
)

// The building blocks every page is made of.

func setProp(w *qt.QWidget, name, value string) {
	v := qt.NewQVariant14(value)
	w.SetProperty(name, v)
	v.Delete()
}

// restyle redraws a widget after one of its style properties changed.
func restyle(w *qt.QWidget) {
	w.Style().Unpolish(w)
	w.Style().Polish(w)
}

// text makes a label with a role of the style sheet ("title", "hint", ...). Text wraps.
func text(s, role string) *qt.QLabel {
	l := qt.NewQLabel3(s)
	if role != "" {
		setProp(l.QWidget, "role", role)
	}
	l.SetWordWrap(true)
	return l
}

// richText is a label that shows simple HTML; links open in the browser.
func richText(s, role string) *qt.QLabel {
	l := text(s, role)
	l.SetTextFormat(qt.RichText)
	l.SetOpenExternalLinks(true)
	return l
}

// line is a single label that doesn't wrap and doesn't ask for more width than it gets.
func oneLine(s, role string) *qt.QLabel {
	l := text(s, role)
	l.SetWordWrap(false)
	l.SetSizePolicy2(qt.QSizePolicy__Ignored, qt.QSizePolicy__Preferred)
	return l
}

// pill is a small colored label: "ok", "warn", "bad", "info", "accent" or "off".
func pill(s, kind string) *qt.QLabel {
	l := qt.NewQLabel3(s)
	setProp(l.QWidget, "role", "pill")
	setProp(l.QWidget, "pill", kind)
	l.SetSizePolicy2(qt.QSizePolicy__Maximum, qt.QSizePolicy__Fixed)
	return l
}

func setPill(l *qt.QLabel, s, kind string) {
	l.SetText(s)
	if l.Property("pill").ToString() != kind {
		setProp(l.QWidget, "pill", kind)
		restyle(l.QWidget)
	}
}

// button makes a push button: kind "", "primary", "ghost", "danger", "chip", "segment" or "link".
func button(label, kind string, do func()) *qt.QPushButton {
	b := qt.NewQPushButton3(label)
	if kind != "" {
		setProp(b.QWidget, "kind", kind)
	}
	if do != nil {
		b.OnClicked(do)
	}
	b.SetCursor(qt.NewQCursor2(qt.PointingHandCursor))
	return b
}

// iconButton is a button with an icon (and a label if given); the icon follows the theme.
func (a *app) iconButton(icon, label, kind, tip string, do func()) *qt.QPushButton {
	if label != "" {
		label = " " + label
	}
	b := button(label, kind, do)
	if tip != "" {
		b.SetToolTip(tip)
	}
	a.onTheme(func() {
		color := th.Text2
		switch kind {
		case "primary":
			color = th.OnAccent
		case "danger":
			color = th.Bad
		case "":
			color = th.Text
		}
		b.SetIcon(iconOf(icon, 16, color))
	})
	if label == "" {
		b.SetFixedSize2(32, 32)
		setProp(b.QWidget, "kind", "ghost")
		b.SetStyleSheet("padding: 0;")
	}
	return b
}

func hbox(margin ...int) *qt.QHBoxLayout {
	l := qt.NewQHBoxLayout2()
	setMargins(l.QLayout, margin)
	return l
}

func vbox(margin ...int) *qt.QVBoxLayout {
	l := qt.NewQVBoxLayout2()
	setMargins(l.QLayout, margin)
	return l
}

func setMargins(l *qt.QLayout, m []int) {
	switch len(m) {
	case 0:
		l.SetContentsMargins(0, 0, 0, 0)
	case 1:
		l.SetContentsMargins(m[0], m[0], m[0], m[0])
	case 4:
		l.SetContentsMargins(m[0], m[1], m[2], m[3])
	}
}

// card is a raised surface with a column inside.
func card() (*qt.QFrame, *qt.QVBoxLayout) {
	f := qt.NewQFrame2()
	setProp(f.QWidget, "role", "card")
	box := qt.NewQVBoxLayout(f.QWidget)
	box.SetContentsMargins(16, 14, 16, 14)
	box.SetSpacing(8)
	return f, box
}

// banner is a tinted strip for a note: role "banner" or "warnbanner".
func banner(role string) (*qt.QFrame, *qt.QLabel) {
	f := qt.NewQFrame2()
	setProp(f.QWidget, "role", role)
	box := qt.NewQHBoxLayout(f.QWidget)
	box.SetContentsMargins(14, 10, 14, 10)
	l := richText("", "")
	box.AddWidget2(l.QWidget, 1)
	return f, l
}

func rule() *qt.QFrame {
	f := qt.NewQFrame2()
	setProp(f.QWidget, "role", "line")
	return f
}

// page is the frame of a page: a title with what it's for, and a place for its buttons.
type page struct {
	w       *qt.QWidget
	box     *qt.QVBoxLayout
	actions *qt.QHBoxLayout
	sub     *qt.QLabel
}

func (a *app) newPage(title, subtitle, helpTopic string) *page {
	p := &page{w: qt.NewQWidget2()}
	p.box = qt.NewQVBoxLayout(p.w)
	p.box.SetContentsMargins(26, 20, 26, 16)
	p.box.SetSpacing(12)

	head := hbox()
	head.SetSpacing(10)
	titles := vbox()
	titles.SetSpacing(2)
	titleRow := hbox()
	titleRow.SetSpacing(4)
	t := qt.NewQLabel3(title)
	setProp(t.QWidget, "role", "title")
	titleRow.AddWidget(t.QWidget)
	if helpTopic != "" {
		titleRow.AddWidget(a.helpButton(helpTopic).QWidget)
	}
	titleRow.AddStretch()
	titles.AddLayout(titleRow.QLayout)
	p.sub = text(subtitle, "subtitle")
	titles.AddWidget(p.sub.QWidget)
	head.AddLayout2(titles.QLayout, 1)
	p.actions = hbox()
	p.actions.SetSpacing(8)
	head.AddLayout(p.actions.QLayout)
	p.box.AddLayout(head.QLayout)
	return p
}

// helpButton is the small "?" that explains something in plain words.
func (a *app) helpButton(topic string) *qt.QPushButton {
	b := a.iconButton("help", "", "ghost", "What's this?", func() { a.showHelp(topic) })
	b.SetFixedSize2(26, 26)
	return b
}

// scrolling puts a column in a scroll area; the column's widgets keep their width.
func scrolling() (*qt.QScrollArea, *qt.QVBoxLayout, *qt.QWidget) {
	inner := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(inner)
	box.SetContentsMargins(0, 0, 8, 0)
	box.SetSpacing(12)
	s := qt.NewQScrollArea2()
	s.SetWidgetResizable(true)
	s.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	s.SetFrameShape(qt.QFrame__NoFrame)
	s.SetWidget(inner)
	return s, box, inner
}

// emptyState is what a page shows when it has nothing yet.
type emptyState struct {
	w     *qt.QWidget
	title *qt.QLabel
	body  *qt.QLabel
	row   *qt.QHBoxLayout
}

func (a *app) newEmpty(icon, title, body string) *emptyState {
	e := &emptyState{w: qt.NewQWidget2()}
	box := qt.NewQVBoxLayout(e.w)
	box.AddStretch()
	pic := qt.NewQLabel2()
	pic.SetAlignment(qt.AlignCenter)
	a.onTheme(func() { pic.SetPixmap(iconPixmap(icon, 44, th.Text3)) })
	box.AddWidget(pic.QWidget)
	box.AddSpacing(6)
	e.title = qt.NewQLabel3(title)
	setProp(e.title.QWidget, "role", "h1")
	e.title.SetAlignment(qt.AlignCenter)
	box.AddWidget(e.title.QWidget)
	e.body = text(body, "subtitle")
	e.body.SetAlignment(qt.AlignCenter)
	e.body.SetMaximumWidth(520)
	centre := hbox()
	centre.AddStretch()
	centre.AddWidget(e.body.QWidget)
	centre.AddStretch()
	box.AddLayout(centre.QLayout)
	box.AddSpacing(8)
	e.row = hbox()
	e.row.SetSpacing(8)
	e.row.AddStretch()
	box.AddLayout(e.row.QLayout)
	box.AddStretch()
	return e
}

// done finishes the empty state's row of buttons (after they're added).
func (e *emptyState) done() { e.row.AddStretch() }

// newToggle is a sliding on/off switch; it's a checkbox underneath, so keys work.
func newToggle(tip string, changed func(on bool)) *qt.QCheckBox {
	b := qt.NewQCheckBox2()
	b.SetFixedSize2(38, 22)
	b.SetToolTip(tip)
	b.SetCursor(qt.NewQCursor2(qt.PointingHandCursor))
	b.OnPaintEvent(func(_ func(*qt.QPaintEvent), _ *qt.QPaintEvent) {
		p := qt.NewQPainter2(b.QWidget.QPaintDevice)
		p.SetRenderHint2(qt.QPainter__Antialiasing, true)
		p.SetPenWithStyle(qt.NoPen)
		track, knob, x := th.Border, "#ffffff", 3
		if b.IsChecked() {
			track, knob, x = th.Accent, th.OnAccent, 19
		}
		if !b.IsEnabled() {
			track = th.Raised
		}
		p.SetBrush(brushOf(track))
		p.DrawRoundedRect2(0, 0, 38, 22, 11, 11)
		p.SetBrush(brushOf(knob))
		p.DrawEllipse2(x, 3, 16, 16)
		if b.HasFocus() {
			pen := qt.NewQPen4(brushOf(th.Text2), 1)
			p.SetPenWithPen(pen)
			p.SetBrushWithStyle(qt.NoBrush)
			p.DrawRoundedRect2(0, 0, 37, 21, 11, 11)
			pen.Delete()
		}
		p.End()
		p.Delete()
	})
	// A check box only takes clicks on its little box and its label: this one is all switch.
	b.OnHitButton(func(_ func(*qt.QPoint) bool, _ *qt.QPoint) bool { return true })
	if changed != nil {
		b.OnClicked(func() { changed(b.IsChecked()) })
	}
	return b
}

// setToggle sets a switch without calling its changed.
func setToggle(b *qt.QCheckBox, on bool) {
	if b.IsChecked() != on {
		b.SetChecked(on)
	}
}

// segments are buttons of which one is picked, side by side.
func segments(labels []string, picked func(i int)) (*qt.QFrame, []*qt.QPushButton) {
	f := qt.NewQFrame2()
	setProp(f.QWidget, "role", "segments")
	box := qt.NewQHBoxLayout(f.QWidget)
	box.SetContentsMargins(3, 3, 3, 3)
	box.SetSpacing(2)
	group := qt.NewQButtonGroup2(f.QObject)
	group.SetExclusive(true)
	var buttons []*qt.QPushButton
	for i, l := range labels {
		i := i
		b := button(l, "segment", func() { picked(i) })
		b.SetCheckable(true)
		group.AddButton(b.QAbstractButton)
		box.AddWidget(b.QWidget)
		buttons = append(buttons, b)
	}
	f.SetSizePolicy2(qt.QSizePolicy__Maximum, qt.QSizePolicy__Fixed)
	return f, buttons
}

// compactCombo keeps a combo box as wide as chars characters, however long its items are.
func compactCombo(c *qt.QComboBox, chars int) {
	c.SetSizeAdjustPolicy(qt.QComboBox__AdjustToMinimumContentsLengthWithIcon)
	c.SetMinimumContentsLength(chars)
	c.View().SetMinimumWidth(240)
}

// fileFolder opens the folder a file is in.
func openFolder(path string) {
	openPath(filepath.Dir(path))
}

// openPath opens a file or folder in the desktop's app for it.
func openPath(path string) {
	u := qt.QUrl_FromLocalFile(path)
	qt.QDesktopServices_OpenUrl(u)
}

// escapeHTML is for text that goes into a rich-text label.
func onlyIf(ok bool, s string) string {
	if ok {
		return s
	}
	return ""
}
