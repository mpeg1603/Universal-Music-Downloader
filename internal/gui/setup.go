package gui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The first-run setup (core.SetupSteps): one page per step, filled into a draft of the
// settings that's saved at the end. Every step can be skipped.
type setupWizard struct {
	a     *app
	d     *qt.QDialog
	draft core.Config
	step  int
	body  *qt.QWidget
	apply []func(*core.Config) error
}

func (a *app) runSetup() {
	w := &setupWizard{a: a, draft: a.m.Config(), d: qt.NewQDialog(a.win.QWidget)}
	w.d.SetWindowTitle("Universal Music Downloader: setup")
	w.d.Resize(760, 560)
	qt.NewQVBoxLayout(w.d.QWidget)
	w.show()
	w.d.Exec()
}

func (w *setupWizard) on(names ...string) bool {
	for _, n := range names {
		if w.draft.Source(n).Enabled {
			return true
		}
	}
	return false
}

func (w *setupWizard) show() {
	if w.body != nil {
		w.body.Hide()
		w.body.DeleteLater()
	}
	w.apply = nil
	st := core.SetupSteps[w.step]
	w.body = qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w.body)
	title := qt.NewQLabel3(fmt.Sprintf("<h3>%s</h3><span style='color:#888'>Step %d of %d</span>", st.Title, w.step+1, len(core.SetupSteps)))
	box.AddWidget(title.QWidget)
	help := qt.NewQLabel3(st.Help)
	help.SetWordWrap(true)
	box.AddWidget(help.QWidget)
	form := qt.NewQFormLayout2()
	form.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)
	box.AddLayout(form.QLayout)
	d := &w.draft
	text := func(label, value string, set func(*core.Config, string)) *qt.QLineEdit {
		e := qt.NewQLineEdit3(value)
		form.AddRow3(label, e.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { set(c, strings.TrimSpace(e.Text())); return nil })
		return e
	}
	secret := func(label, value string, set func(*core.Config, string)) {
		e := text(label, value, set)
		e.SetEchoMode(qt.QLineEdit__Password)
	}
	check := func(label string, value bool, set func(*core.Config, bool)) {
		b := qt.NewQCheckBox3(label)
		b.SetChecked(value)
		form.AddRowWithWidget(b.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { set(c, b.IsChecked()); return nil })
	}
	number := func(label string, value float64, set func(*core.Config, float64)) {
		e := text(label, strconv.FormatFloat(value, 'f', -1, 64), func(*core.Config, string) {})
		e.SetMaximumWidth(120)
		w.apply = append(w.apply, func(c *core.Config) error {
			v, err := strconv.ParseFloat(strings.TrimSpace(e.Text()), 64)
			if err != nil {
				return fmt.Errorf("%s: %q is not a number", label, e.Text())
			}
			set(c, v)
			return nil
		})
	}
	use := func(names ...string) {
		on := w.on(names...)
		if !w.draft.SetupDone && !on && (names[0] == "bandcamp" || names[0] == "archive" || names[0] == "audius") {
			on = true // the free ones start on
		}
		check("Use "+st.Title, on, func(c *core.Config, v bool) {
			for _, n := range names {
				c.SetSource(n, v)
			}
		})
	}
	switch st.ID {
	case "output":
		text("Downloads folder", d.OutputDir, func(c *core.Config, s string) { c.OutputDir = s })
		cb := qt.NewQComboBox2()
		cb.AddItems(formatLabels)
		for i, f := range core.Formats {
			if f == d.Format {
				cb.SetCurrentIndex(i)
			}
		}
		form.AddRow3("Format", cb.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { c.Format = core.Formats[max(0, cb.CurrentIndex())]; return nil })
	case "library":
		lib := text("Library folders (; between them)", strings.Join(d.LibraryDirs, "; "), func(c *core.Config, s string) { c.LibraryDirs = core.SplitBy(s, ";") })
		browse := qt.NewQPushButton3("Add a folder…")
		browse.OnClicked(func() {
			if p := qt.QFileDialog_GetExistingDirectory3(w.d.QWidget, "A library folder (only ever read)", ""); p != "" {
				lib.SetText(strings.Trim(strings.TrimSpace(lib.Text())+"; "+p, "; "))
			}
		})
		form.AddRowWithWidget(browse.QWidget)
		cb := qt.NewQComboBox2()
		for i, t := range core.Thresholds {
			cb.AddItem(fmt.Sprintf("%s — %s", t.Name, t.Label))
			if string(t.Name) == d.Threshold {
				cb.SetCurrentIndex(i)
			}
		}
		form.AddRow3("Good enough quality", cb.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error {
			c.Threshold = string(core.Thresholds[max(0, cb.CurrentIndex())].Name)
			return nil
		})
	case "playlists":
		var have []string
		for _, in := range d.Inputs {
			have = append(have, in.Path)
		}
		files := text("Playlist files or links (; between them)", strings.Join(have, "; "), func(c *core.Config, s string) {
			var ins []core.InputCfg
			for _, p := range core.SplitBy(s, ";") {
				if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
					p = core.CleanPath(p)
				}
				ins = append(ins, core.InputCfg{Path: p, Enabled: true})
			}
			c.Inputs = ins
		})
		browse := qt.NewQPushButton3("Add a file…")
		browse.OnClicked(func() {
			if p := qt.QFileDialog_GetOpenFileName4(w.d.QWidget, "A playlist file", "", "Playlists (*.csv *.zip *.txt *.json);;All files (*)"); p != "" {
				files.SetText(strings.Trim(strings.TrimSpace(files.Text())+"; "+p, "; "))
			}
		})
		form.AddRowWithWidget(browse.QWidget)
	case "bandwidth":
		number("Bandwidth cap, MB/s (0: none)", d.BandwidthMBps, func(c *core.Config, v float64) { c.BandwidthMBps = v })
		number("Most downloads at once", float64(d.MaxDownloads), func(c *core.Config, v float64) { c.MaxDownloads = int(v) })
		check("Skip busy sources while bandwidth is to spare", !d.NoQueueJump, func(c *core.Config, v bool) { c.NoQueueJump = !v })
	case "bandcamp":
		use("bandcamp")
		text("Your email address (optional)", d.Bandcamp.Email, func(c *core.Config, s string) { c.Bandcamp.Email = s })
	case "archive", "audius":
		use(st.ID)
	case "vk":
		use("vk")
		secret("Token (or log in later: Sources → VK → Settings)", d.VKToken, func(c *core.Config, s string) { c.VKToken = s })
	case "yandex":
		use("yandex")
		secret("OAuth token", d.YandexToken, func(c *core.Config, s string) { c.YandexToken = s })
		note := qt.NewQLabel3("Open " + core.YandexTokenURL + " in your browser, log in, and copy the access_token=… value.")
		note.SetWordWrap(true)
		note.SetTextInteractionFlags(qt.TextSelectableByMouse)
		form.AddRowWithWidget(note.QWidget)
	case "youtube":
		for _, s := range [][2]string{{"youtube_music", "YouTube Music"}, {"youtube", "YouTube"}, {"soundcloud", "SoundCloud"}} {
			name := s[0]
			check("Use "+s[1], w.on(name), func(c *core.Config, v bool) { c.SetSource(name, v) })
		}
		cb := qt.NewQComboBox2()
		cb.AddItems(append([]string{"(none)"}, core.Browsers[1:]...))
		for i, b := range core.Browsers {
			if b == d.CookiesFromBrowser {
				cb.SetCurrentIndex(i)
			}
		}
		form.AddRow3("YouTube login from your browser", cb.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error {
			c.CookiesFromBrowser = core.Browsers[max(0, cb.CurrentIndex())]
			return nil
		})
	case "soulseek":
		use("soulseek")
		sk := d.Soulseek
		text("Username", sk.Username, func(c *core.Config, s string) { c.Soulseek.Username = s })
		secret("Password", sk.Password, func(c *core.Config, s string) { c.Soulseek.Password = s })
		check("Share files (your library and downloads)", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
		number("Listening port", float64(sk.ListenPort()), func(c *core.Config, v float64) {
			if c.Soulseek.Port = int(v); c.Soulseek.Port == core.SlskDefaultPort {
				c.Soulseek.Port = 0
			}
		})
		check("Ask the router to forward the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
		imp := qt.NewQPushButton3("Import from Nicotine+")
		imp.OnClicked(func() {
			got, err := core.ImportNicotine(w.draft.Soulseek)
			if err != nil {
				w.a.message("Nicotine+", err.Error())
				return
			}
			w.draft.Soulseek = got
			w.draft.SetSource("soulseek", got.Configured())
			w.show()
		})
		form.AddRowWithWidget(imp.QWidget)
	case "torrent":
		use("torrent")
		check("Built-in indexers (Jackett's public sites)", !d.Indexers.NoBuiltin, func(c *core.Config, v bool) { c.Indexers.NoBuiltin = !v })
		text("FlareSolverr address (optional)", d.Indexers.FlareSolverr, func(c *core.Config, s string) { c.Indexers.FlareSolverr = s })
		text("Jackett / Prowlarr address (optional)", strings.Join(d.Torznab.Feeds, ", "), func(c *core.Config, s string) { c.Torznab.Feeds = core.SplitList(s) })
		secret("Jackett / Prowlarr API key", d.Torznab.Key, func(c *core.Config, s string) { c.Torznab.Key = s })
	case "done":
		var on []string
		for _, s := range w.draft.Sources {
			if s.Enabled {
				on = append(on, core.SourceLabels[s.Name])
			}
		}
		l := qt.NewQLabel3("Sources on: " + strings.Join(on, ", "))
		l.SetWordWrap(true)
		form.AddRowWithWidget(l.QWidget)
	}
	box.AddStretch()

	buttons := qt.NewQHBoxLayout2()
	button := func(text string, do func()) *qt.QPushButton {
		b := qt.NewQPushButton3(text)
		b.OnClicked(do)
		buttons.AddWidget(b.QWidget)
		return b
	}
	if st.More != "" {
		button("What's this?", func() { w.a.message(st.Title, st.More) })
	}
	buttons.AddStretch()
	if w.step > 0 {
		button("Back", func() { w.step--; w.show() })
	}
	next := func() {
		trial := w.draft
		for _, a := range w.apply {
			if err := a(&trial); err != nil {
				w.a.message("Setup", err.Error())
				return
			}
		}
		w.draft = trial
		if w.step++; w.step >= len(core.SetupSteps) {
			w.finish()
			return
		}
		w.show()
	}
	var forward *qt.QPushButton
	switch st.ID {
	case "welcome":
		button("Skip the setup", func() {
			w.a.m.FinishSetup(func(*core.Config) {})
			w.d.Accept()
		})
		forward = button("Start", next)
	case "done":
		forward = button("Save and start", next)
	default:
		skip := "Skip"
		if st.Source {
			skip = "Skip (leave it off)"
		}
		button(skip, func() {
			if st.Source {
				names := []string{st.ID}
				if st.ID == "youtube" {
					names = []string{"youtube", "youtube_music", "soundcloud"}
				}
				for _, n := range names {
					w.draft.SetSource(n, false)
				}
			}
			w.step++
			w.show()
		})
		forward = button("Next", next)
	}
	forward.SetDefault(true)
	box.AddLayout(buttons.QLayout)
	w.d.Layout().AddWidget(w.body)
	// Enter goes forward: the first field to fill in has the focus, or else the forward button.
	if form.RowCount() > 0 {
		w.body.FocusNextChild()
	} else {
		forward.SetFocus()
	}
}

func (w *setupWizard) finish() {
	draft := w.draft
	if err := w.a.m.FinishSetup(func(c *core.Config) { *c = draft }); err != nil {
		w.a.message("Setup", err.Error())
		w.step = len(core.SetupSteps) - 1
		w.show()
		return
	}
	w.d.Accept()
	w.a.message("Setup", "Saved. The queue starts with your playlists; the Search tab finds more.")
}
