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
	box   *qt.QVBoxLayout
	apply []func(*core.Config) error
	row   *qt.QHBoxLayout // the buttons
	inner *qt.QWidget
}

func newForm(a *app, title string, width int) *form {
	f := &form{a: a, d: qt.NewQDialog(a.win.QWidget)}
	f.d.SetWindowTitle(title)
	f.d.Resize(width, 200)
	f.box = qt.NewQVBoxLayout(f.d.QWidget)
	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	inner := qt.NewQWidget2()
	f.f = qt.NewQFormLayout(inner)
	f.f.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)
	scroll.SetWidget(inner)
	scroll.SetFrameShape(qt.QFrame__NoFrame)
	scroll.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	f.inner = inner
	f.box.AddWidget2(scroll.QWidget, 1)
	f.row = qt.NewQHBoxLayout2()
	f.box.AddLayout(f.row.QLayout)
	return f
}

func (f *form) note(text string) {
	l := qt.NewQLabel3(text)
	l.SetWordWrap(true)
	l.SetTextFormat(qt.RichText)
	l.SetOpenExternalLinks(true)
	f.f.AddRowWithWidget(l.QWidget)
}

func (f *form) head(text string) {
	l := qt.NewQLabel3("<b>" + text + "</b>")
	f.f.AddRowWithWidget(l.QWidget)
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

// route adds the route editor: its type, and for proxies the address, port and login.
func (f *form) route(source, route string) {
	spec := core.ParseRoute(route)
	kind := qt.NewQComboBox2()
	cur := 0
	for i, k := range core.RouteKinds {
		kind.AddItem(core.RouteKindLabels[k])
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
	f.f.AddRow3("Route", kind.QWidget)
	f.f.AddRow3("   Proxy address (host or IP)", host.QWidget)
	f.f.AddRow3("   Proxy port", port.QWidget)
	f.f.AddRow3("   Proxy user (if it needs a login)", user.QWidget)
	f.f.AddRow3("   Proxy password", pass.QWidget)
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
			return fmt.Errorf("route: %w", err)
		}
		c.SetRoute(source, r.String())
		return nil
	})
}

func (f *form) button(text string, do func()) *qt.QPushButton {
	b := qt.NewQPushButton3(text)
	b.OnClicked(do)
	f.row.AddWidget(b.QWidget)
	return b
}

// save checks every field on a copy of the settings, then saves them all.
func (f *form) save() bool {
	trial := f.a.m.Config()
	for _, a := range f.apply {
		if err := a(&trial); err != nil {
			f.a.message("Can't save", err.Error())
			return false
		}
	}
	if err := f.a.m.UpdateConfig(func(c *core.Config) {
		for _, a := range f.apply {
			a(c)
		}
	}); err != nil {
		f.a.message("Can't save", err.Error())
		return false
	}
	return true
}

// run shows the window with Save and Cancel (after the buttons added before) until closed.
func (f *form) run() {
	f.row.AddStretch()
	save := f.button("Save", func() {
		if f.save() {
			f.d.Accept()
		}
	})
	save.SetDefault(true)
	f.button("Cancel", func() { f.d.Reject() })
	f.d.Resize(f.d.Width(), min(f.inner.SizeHint().Height()+90, 860))
	f.d.Exec()
}
