package gui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// form builds a settings window: each field knows how to put its value into the settings, and
// Save checks them all before anything is saved.
type form struct {
	a     *app
	d     *qt.QDialog
	f     *qt.QFormLayout
	col   *qt.QVBoxLayout // the column the sections stack in
	apply []func(*core.Config) error
	row   *qt.QHBoxLayout // the buttons
	inner *qt.QWidget
}

func newForm(a *app, title, intro string, width int) *form {
	f := &form{a: a, d: qt.NewQDialog(a.win.QWidget)}
	f.d.SetWindowTitle(title)
	f.d.Resize(width, 300)
	outer := qt.NewQVBoxLayout(f.d.QWidget)
	outer.SetContentsMargins(20, 16, 12, 14)
	outer.SetSpacing(10)
	h := qt.NewQLabel3(title)
	setProp(h.QWidget, "role", "h1")
	outer.AddWidget(h.QWidget)
	if intro != "" {
		outer.AddWidget(richText(intro, "subtitle").QWidget)
	}
	scroll, col, inner := scrolling()
	f.col, f.inner = col, inner
	f.newSection()
	outer.AddWidget2(scroll.QWidget, 1)
	f.row = hbox()
	f.row.SetContentsMargins(0, 0, 8, 0)
	f.row.SetSpacing(8)
	outer.AddLayout(f.row.QLayout)
	return f
}

// newSection starts a new block of fields.
func (f *form) newSection() {
	fl := qt.NewQFormLayout2()
	fl.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)
	fl.SetHorizontalSpacing(14)
	fl.SetVerticalSpacing(9)
	fl.SetContentsMargins(0, 0, 0, 0)
	f.f = fl
	f.col.AddLayout(fl.QLayout)
}

// head starts a titled part of the form.
func (f *form) head(title string) {
	h := qt.NewQLabel3(title)
	setProp(h.QWidget, "role", "h2")
	h.SetContentsMargins(0, 8, 0, 0)
	f.col.AddWidget(h.QWidget)
	f.newSection()
}

func (f *form) note(s string) {
	l := richText(s, "hint")
	f.f.AddRowWithWidget(l.QWidget)
}

// collapsed starts a part that's hidden until asked for ("Connection", say).
func (f *form) collapsed(title string, open bool) {
	body := qt.NewQWidget2()
	fl := qt.NewQFormLayout(body)
	fl.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)
	fl.SetHorizontalSpacing(14)
	fl.SetVerticalSpacing(9)
	fl.SetContentsMargins(0, 2, 0, 0)
	var head *qt.QPushButton
	label := func(on bool) string {
		if on {
			return "▾  " + title
		}
		return "▸  " + title
	}
	head = button(label(open), "link", func() {
		on := !body.IsVisible()
		body.SetVisible(on)
		head.SetText(label(on))
	})
	f.col.AddWidget(head.QWidget)
	f.col.AddWidget(body)
	body.SetVisible(open)
	f.f = fl
}

func (f *form) text(label, value string, set func(*core.Config, string) error) *qt.QLineEdit {
	e := qt.NewQLineEdit3(value)
	f.f.AddRow3(label, e.QWidget)
	f.apply = append(f.apply, func(c *core.Config) error { return set(c, strings.TrimSpace(e.Text())) })
	return e
}

func (f *form) secret(label, value string, set func(*core.Config, string)) *qt.QLineEdit {
	e := qt.NewQLineEdit3(value)
	e.SetEchoMode(qt.QLineEdit__Password)
	f.f.AddRow3(label, e.QWidget)
	f.apply = append(f.apply, func(c *core.Config) error { set(c, strings.TrimSpace(e.Text())); return nil })
	return e
}

func (f *form) number(label string, value float64, set func(*core.Config, float64)) {
	e := qt.NewQLineEdit3(strconv.FormatFloat(value, 'f', -1, 64))
	e.SetMaximumWidth(120)
	f.f.AddRow3(label, e.QWidget)
	f.apply = append(f.apply, func(c *core.Config) error {
		v, err := strconv.ParseFloat(strings.TrimSpace(e.Text()), 64)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", label, e.Text())
		}
		set(c, v)
		return nil
	})
}

func (f *form) check(label string, value bool, set func(*core.Config, bool)) *qt.QCheckBox {
	b := qt.NewQCheckBox3(label)
	b.SetChecked(value)
	f.f.AddRowWithWidget(b.QWidget)
	f.apply = append(f.apply, func(c *core.Config) error { set(c, b.IsChecked()); return nil })
	return b
}

func (f *form) combo(label string, items []string, cur int, set func(*core.Config, int)) *qt.QComboBox {
	cb := qt.NewQComboBox2()
	cb.AddItems(items)
	cb.SetCurrentIndex(cur)
	f.f.AddRow3(label, cb.QWidget)
	f.apply = append(f.apply, func(c *core.Config) error { set(c, cb.CurrentIndex()); return nil })
	return cb
}

// route adds how a source connects: the same as everything else, direct, or through a proxy.
func (f *form) route(source, route string) {
	spec := core.ParseRoute(route)
	f.collapsed("Connection (most people can leave this alone)", spec.Kind != core.RouteSystem)
	kind := qt.NewQComboBox2()
	cur := 0
	for i, k := range core.RouteKinds {
		kind.AddItem(core.FirstNonEmpty(routeWords[k], core.RouteKindLabels[k]))
		if k == spec.Kind {
			cur = i
		}
	}
	kind.SetCurrentIndex(cur)
	host := qt.NewQLineEdit3(spec.Host)
	port := qt.NewQLineEdit2()
	if spec.Port > 0 {
		port.SetText(strconv.Itoa(spec.Port))
	}
	port.SetMaximumWidth(90)
	user := qt.NewQLineEdit3(spec.User)
	pass := qt.NewQLineEdit3(spec.Pass)
	pass.SetEchoMode(qt.QLineEdit__Password)
	f.f.AddRow3("Connect", kind.QWidget)
	f.f.AddRow3("Proxy address", host.QWidget)
	f.f.AddRow3("Proxy port", port.QWidget)
	f.f.AddRow3("Proxy user", user.QWidget)
	f.f.AddRow3("Proxy password", pass.QWidget)
	f.note("\"Direct\" goes around a VPN, for this source only. Leave the proxy fields empty unless you have a proxy.")
	enable := func(i int) {
		on := i >= 0 && i < len(core.RouteKinds) && (core.RouteKinds[i] == "socks5" || core.RouteKinds[i] == "http")
		for _, w := range []*qt.QLineEdit{host, port, user, pass} {
			w.SetEnabled(on)
		}
	}
	kind.OnCurrentIndexChanged(enable)
	enable(cur)
	f.apply = append(f.apply, func(c *core.Config) error {
		i := kind.CurrentIndex()
		if i < 0 || i >= len(core.RouteKinds) {
			return nil
		}
		k := core.RouteKinds[i]
		r := core.RouteSpec{Kind: k}
		if k == "socks5" || k == "http" {
			p, _ := strconv.Atoi(strings.TrimSpace(port.Text()))
			r = core.RouteSpec{Kind: k, Host: strings.TrimSpace(host.Text()), Port: p, User: strings.TrimSpace(user.Text()), Pass: pass.Text()}
		}
		if err := r.Validate(); err != nil {
			return fmt.Errorf("connection: %w", err)
		}
		c.SetRoute(source, r.String())
		return nil
	})
	f.newSection()
}

func (f *form) button(label string, do func()) *qt.QPushButton {
	b := button(label, "", do)
	f.row.AddWidget(b.QWidget)
	return b
}

// save checks every field on a copy of the settings, then saves them all.
func (f *form) save() bool {
	trial := f.a.m.Config()
	for _, a := range f.apply {
		if err := a(&trial); err != nil {
			f.a.message("That can't be saved", err.Error())
			return false
		}
	}
	if err := f.a.m.UpdateConfig(func(c *core.Config) {
		for _, a := range f.apply {
			a(c)
		}
	}); err != nil {
		f.a.message("That can't be saved", err.Error())
		return false
	}
	return true
}

// run shows the window with Save and Cancel (after the buttons added before) until closed.
func (f *form) run() {
	f.row.AddStretch()
	f.button("Cancel", func() { f.d.Reject() })
	save := button("Save", "primary", func() {
		if f.save() {
			f.d.Accept()
			f.a.toast("Saved.")
		}
	})
	save.SetDefault(true)
	f.row.AddWidget(save.QWidget)
	f.col.AddStretch()
	f.d.Resize(f.d.Width(), min(f.inner.SizeHint().Height()+140, 760))
	f.d.Exec()
}
