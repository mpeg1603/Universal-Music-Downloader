package tui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	"github.com/rivo/tview"
)

// formBuilder adds settings fields to a form; apply puts what was entered into a Config, or
// says what's wrong with it.
type formBuilder struct {
	f     *tview.Form
	apply []func(*core.Config) error
}

func (b *formBuilder) text(label, value string, width int, set func(*core.Config, string) error) *tview.InputField {
	v := value
	b.f.AddInputField(label, value, width, nil, func(t string) { v = t })
	b.apply = append(b.apply, func(c *core.Config) error { return set(c, strings.TrimSpace(v)) })
	return b.f.GetFormItem(b.f.GetFormItemCount() - 1).(*tview.InputField)
}

func (b *formBuilder) secret(label, value string, set func(*core.Config, string)) {
	v := value
	b.f.AddPasswordField(label, value, 40, '*', func(t string) { v = t })
	b.apply = append(b.apply, func(c *core.Config) error { set(c, v); return nil })
}

func (b *formBuilder) number(label string, value float64, width int, set func(*core.Config, float64)) {
	b.text(label, strconv.FormatFloat(value, 'f', -1, 64), width, func(c *core.Config, s string) error {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", strings.TrimSpace(label), s)
		}
		set(c, v)
		return nil
	})
}

func (b *formBuilder) check(label string, value bool, set func(*core.Config, bool)) {
	v := value
	b.f.AddCheckbox(label, value, func(x bool) { v = x })
	b.apply = append(b.apply, func(c *core.Config) error { set(c, v); return nil })
}

// route adds a route editor for a source: its type, and for proxies the address and login.
func (b *formBuilder) route(source, route string) {
	get := routeFields(b.f, route)
	b.apply = append(b.apply, func(c *core.Config) error {
		r, err := get()
		if err != nil {
			return fmt.Errorf("route: %w", err)
		}
		c.SetRoute(source, r)
		return nil
	})
}

// saveForm applies the form's fields to the settings, or says what's wrong; done runs when saved.
func (ui *tui) saveForm(b *formBuilder, done func()) {
	trial := ui.m.Config()
	if err := b.applyTo(&trial); err != nil {
		ui.message("Can't save", err.Error())
		return
	}
	if ui.update(func(c *core.Config) { b.applyTo(c) }) {
		done()
	}
}

// applyTo checks every field into c; nothing is changed when one is wrong.
func (b *formBuilder) applyTo(c *core.Config) error {
	trial := *c
	trial = cloneConfig(trial)
	for _, a := range b.apply {
		if err := a(&trial); err != nil {
			return err
		}
	}
	for _, a := range b.apply {
		a(c)
	}
	return nil
}

func cloneConfig(c core.Config) core.Config {
	out := c
	out.Sources = append([]core.SourceCfg(nil), c.Sources...)
	out.Soulseek.Shares = append([]string(nil), c.Soulseek.Shares...)
	out.Soulseek.Banned = append([]string(nil), c.Soulseek.Banned...)
	out.Soulseek.Rooms = append([]string(nil), c.Soulseek.Rooms...)
	out.LibraryDirs = append([]string(nil), c.LibraryDirs...)
	return out
}

// routeFields adds the route editor to form. The getter returns the route as settings keep
// it, with an error when the proxy fields don't make sense (the text is still the best guess).
func routeFields(form *tview.Form, route string) func() (string, error) {
	spec := core.ParseRoute(route)
	port := ""
	if spec.Port > 0 {
		port = strconv.Itoa(spec.Port)
	}
	host := tview.NewInputField().SetLabel("  Proxy address (host or IP)").SetText(spec.Host).SetFieldWidth(36)
	portF := tview.NewInputField().SetLabel("  Proxy port").SetText(port).SetFieldWidth(7).SetAcceptanceFunc(tview.InputFieldInteger)
	user := tview.NewInputField().SetLabel("  Proxy user (if it needs a login)").SetText(spec.User).SetFieldWidth(30)
	pass := tview.NewInputField().SetLabel("  Proxy password").SetText(spec.Pass).SetFieldWidth(30).SetMaskCharacter('*')
	kind := spec.Kind
	enable := func() {
		off := kind != "socks5" && kind != "http"
		for _, f := range []*tview.InputField{host, portF, user, pass} {
			f.SetDisabled(off)
		}
	}
	var labels []string
	cur := 0
	for i, k := range core.RouteKinds {
		labels = append(labels, core.RouteKindLabels[k])
		if k == spec.Kind {
			cur = i
		}
	}
	form.AddDropDown("Route", labels, cur, func(_ string, i int) {
		if i >= 0 {
			kind = core.RouteKinds[i]
			enable()
		}
	})
	form.AddFormItem(host).AddFormItem(portF).AddFormItem(user).AddFormItem(pass)
	enable()
	return func() (string, error) {
		p, _ := strconv.Atoi(strings.TrimSpace(portF.GetText()))
		r := core.RouteSpec{Kind: kind, Host: strings.TrimSpace(host.GetText()), Port: p, User: strings.TrimSpace(user.GetText()), Pass: pass.GetText()}
		if kind != "socks5" && kind != "http" {
			r = core.RouteSpec{Kind: kind}
		}
		return r.String(), r.Validate()
	}
}
