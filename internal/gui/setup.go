package gui

import (
	"fmt"
	"strconv"
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The first-time setup: a page per step, filled into a draft of the settings that's saved at
// the end. Everything can be skipped and changed later. The sources that need nothing share a
// page, so it's short for most people; every download method gets set up or left off.
type setupStep struct {
	id, title, help, more string
}

var setupSteps = []setupStep{
	{"welcome", "Welcome", "This app downloads the songs of your playlists, and any music you search for, from many places, and skips what you already have. A few questions and you're ready. Skip anything you like; everything can be changed later.", ""},
	{"output", "Where should downloads go?", "New files go to this folder, never into your existing music.", ""},
	{"library", "Do you already have music?", "Add the folders where you keep it, and the app won't download what you already have. The app only reads them, unless you use Sort & merge yourself.", ""},
	{"playlists", "Got any playlists?", "Add playlist files now, or skip this and search for music by name instead.", "playlists"},
	{"free", "Sources that need no account", "These work right away. Bandcamp and the Internet Archive offer free, often lossless music; YouTube and SoundCloud have almost everything, in lower quality.", "sources"},
	{"soulseek", "Soulseek", "A community sharing their collections directly with each other, often in lossless. It needs a free account, and you share your music in return.", "soulseek"},
	{"torrent", "Torrents", "Albums and discographies from torrent sites. The app searches the sites itself and fetches only the files it needs.", ""},
	{"vk", "VK", "The music library of the VK social network, with your account's token (MP3 up to 320).", ""},
	{"yandex", "Yandex Music", "Yandex's streaming service, with your account's token (FLAC with Plus).", ""},
	{"done", "You're all set", "You can turn sources on or off, and change their order, on the Sources page. This setup can be run again from Settings.", ""},
}

type setupWizard struct {
	a      *app
	d      *qt.QDialog
	draft  core.Config
	step   int
	body   *qt.QWidget
	apply  []func(*core.Config) error
	bar    *qt.QProgressBar
	stepOf *qt.QLabel
}

func (a *app) runSetup() {
	a.newSetup().d.Exec()
}

func (a *app) newSetup() *setupWizard {
	w := &setupWizard{a: a, draft: a.m.Config(), d: qt.NewQDialog(a.win.QWidget)}
	w.d.SetWindowTitle("Set up " + appName)
	w.d.Resize(760, 600)
	outer := qt.NewQVBoxLayout(w.d.QWidget)
	outer.SetContentsMargins(26, 20, 26, 18)
	outer.SetSpacing(14)
	w.stepOf = text("", "muted")
	outer.AddWidget(w.stepOf.QWidget)
	w.bar = qt.NewQProgressBar2()
	w.bar.SetTextVisible(false)
	w.bar.SetRange(0, len(setupSteps)-1)
	outer.AddWidget(w.bar.QWidget)
	w.show()
	return w
}

func (w *setupWizard) on(names ...string) bool {
	for _, n := range names {
		if w.draft.Source(n).Enabled {
			return true
		}
	}
	return false
}

// setupFree says which sources start on, the first time through.
func (w *setupWizard) startOn(name string) bool {
	if w.draft.SetupDone {
		return w.on(name)
	}
	return name == "bandcamp" || name == "archive" || name == "audius" || w.on(name)
}

func (w *setupWizard) show() {
	if w.body != nil {
		w.body.Hide()
		w.body.DeleteLater()
	}
	w.apply = nil
	st := setupSteps[w.step]
	w.stepOf.SetText(fmt.Sprintf("Step %d of %d", w.step+1, len(setupSteps)))
	w.bar.SetValue(w.step)
	w.body = qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w.body)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(10)
	box.AddWidget(text(st.title, "title").QWidget)
	box.AddWidget(text(st.help, "subtitle").QWidget)
	box.AddSpacing(8)
	form := qt.NewQFormLayout2()
	form.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)
	form.SetVerticalSpacing(10)
	form.SetHorizontalSpacing(14)
	box.AddLayout(form.QLayout)
	d := &w.draft
	field := func(label, value string, set func(*core.Config, string)) *qt.QLineEdit {
		e := qt.NewQLineEdit3(value)
		form.AddRow3(label, e.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { set(c, strings.TrimSpace(e.Text())); return nil })
		return e
	}
	secret := func(label, value string, set func(*core.Config, string)) {
		e := field(label, value, set)
		e.SetEchoMode(qt.QLineEdit__Password)
	}
	check := func(label string, value bool, set func(*core.Config, bool)) *qt.QCheckBox {
		b := qt.NewQCheckBox3(label)
		b.SetChecked(value)
		form.AddRowWithWidget(b.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { set(c, b.IsChecked()); return nil })
		return b
	}
	number := func(label string, value float64, set func(*core.Config, float64)) {
		e := field(label, strconv.FormatFloat(value, 'f', -1, 64), func(*core.Config, string) {})
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
	use := func(name, label string) {
		check(label, w.startOn(name), func(c *core.Config, v bool) { c.SetSource(name, v) })
	}
	note := func(s string) {
		l := richText(s, "hint")
		form.AddRowWithWidget(l.QWidget)
	}
	switch st.id {
	case "welcome":
		seg, btns := segments([]string{"Match my system", "Light", "Dark"}, func(i int) {
			w.a.m.UpdateConfig(func(c *core.Config) { c.GUI.Theme = core.Themes[i] })
			w.draft.GUI.Theme = core.Themes[i]
			w.a.applyTheme()
		})
		for i, m := range core.Themes {
			if m == w.a.themeMode() {
				btns[i].SetChecked(true)
			}
		}
		form.AddRow3("Look", seg.QWidget)
	case "output":
		e := field("Downloads folder", d.OutputDir, func(c *core.Config, s string) { c.OutputDir = s })
		browse := button("Browse…", "", func() {
			if p := qt.QFileDialog_GetExistingDirectory3(w.d.QWidget, "Downloads folder", e.Text()); p != "" {
				e.SetText(p)
			}
		})
		form.AddRowWithWidget(browse.QWidget)
		cb := qt.NewQComboBox2()
		cb.AddItems(formatWords)
		for i, f := range core.Formats {
			if f == d.Format {
				cb.SetCurrentIndex(i)
			}
		}
		form.AddRow3("Format", cb.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error { c.Format = core.Formats[max(0, cb.CurrentIndex())]; return nil })
		note("“Keep original” saves files exactly as they came, the best choice for nearly everyone.")
		number("Speed limit, MB/s (0: none)", d.BandwidthMBps, func(c *core.Config, v float64) { c.BandwidthMBps = v })
		number("Downloads at once", float64(d.MaxDownloads), func(c *core.Config, v float64) { c.MaxDownloads = int(v) })
	case "library":
		lib := field("Music folders (separate with ;)", strings.Join(d.LibraryDirs, "; "), func(c *core.Config, s string) { c.LibraryDirs = core.SplitBy(s, ";") })
		browse := button("Add a folder…", "", func() {
			if p := qt.QFileDialog_GetExistingDirectory3(w.d.QWidget, "A music folder (only ever read)", ""); p != "" {
				lib.SetText(strings.Trim(strings.TrimSpace(lib.Text())+"; "+p, "; "))
			}
		})
		form.AddRowWithWidget(browse.QWidget)
		cb := qt.NewQComboBox2()
		cb.AddItems(qualityItems())
		for i, t := range core.Thresholds {
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
		files := field("Files or links (separate with ;)", strings.Join(have, "; "), func(c *core.Config, s string) {
			var ins []core.InputCfg
			for _, p := range core.SplitBy(s, ";") {
				if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
					p = core.CleanPath(p)
				}
				ins = append(ins, core.InputCfg{Path: p, Enabled: true})
			}
			c.Inputs = ins
		})
		browse := button("Add a file…", "", func() {
			if p := qt.QFileDialog_GetOpenFileName4(w.d.QWidget, "A playlist file", "", "Playlists (*.csv *.zip *.txt *.json);;All files (*)"); p != "" {
				files.SetText(strings.Trim(strings.TrimSpace(files.Text())+"; "+p, "; "))
			}
		})
		form.AddRowWithWidget(browse.QWidget)
		note("From Spotify: export your playlists at <a style='color:" + th.Accent + "' href='https://exportify.net'>exportify.net</a> and add the file.")
	case "free":
		use("bandcamp", "Bandcamp: free and name-your-price music, often lossless")
		field("Email for Bandcamp (optional)", d.Bandcamp.Email, func(c *core.Config, s string) { c.Bandcamp.Email = s })
		note("Some artists ask for an email address before giving a free download.")
		use("archive", "Internet Archive: freely licensed recordings")
		use("audius", "Audius: independent artists' own uploads, MP3 320")
		use("youtube_music", "YouTube Music: nearly everything, in lower quality")
		use("soundcloud", "SoundCloud")
		use("youtube", "YouTube")
		cb := qt.NewQComboBox2()
		cb.AddItems(append([]string{"Nowhere"}, browserNames()...))
		for i, b := range core.Browsers {
			if b == d.CookiesFromBrowser {
				cb.SetCurrentIndex(i)
			}
		}
		form.AddRow3("Use my YouTube login from", cb.QWidget)
		w.apply = append(w.apply, func(c *core.Config) error {
			c.CookiesFromBrowser = core.Browsers[max(0, cb.CurrentIndex())]
			return nil
		})
		note("Only needed if YouTube asks you to sign in. Your browser is only read.")
	case "soulseek":
		use("soulseek", "Use Soulseek")
		sk := d.Soulseek
		field("Username", sk.Username, func(c *core.Config, s string) { c.Soulseek.Username = s })
		secret("Password", sk.Password, func(c *core.Config, s string) { c.Soulseek.Password = s })
		note("A name nobody uses yet makes a new account.")
		check("Share my music with others", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
		number("Port", float64(sk.ListenPort()), func(c *core.Config, v float64) {
			if c.Soulseek.Port = int(v); c.Soulseek.Port == core.SlskDefaultPort {
				c.Soulseek.Port = 0
			}
		})
		check("Ask my router to open the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
		imp := button("Import from Nicotine+", "", func() {
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
		use("torrent", "Use torrent sites")
		check("Search the built-in sites", !d.Indexers.NoBuiltin, func(c *core.Config, v bool) { c.Indexers.NoBuiltin = !v })
		field("FlareSolverr address (optional)", d.Indexers.FlareSolverr, func(c *core.Config, s string) { c.Indexers.FlareSolverr = s })
		field("Jackett / Prowlarr address (optional)", strings.Join(d.Torznab.Feeds, ", "), func(c *core.Config, s string) { c.Torznab.Feeds = core.SplitList(s) })
		secret("Jackett / Prowlarr API key", d.Torznab.Key, func(c *core.Config, s string) { c.Torznab.Key = s })
		note("These helper programs are optional. FlareSolverr gets past some sites' browser checks; Jackett and Prowlarr add private trackers you have accounts on.")
	case "vk":
		use("vk", "Use VK")
		secret("Token", d.VKToken, func(c *core.Config, s string) { c.VKToken = s })
		note("Or log in later: Sources → VK → Set up.")
	case "yandex":
		use("yandex", "Use Yandex Music")
		secret("Token", d.YandexToken, func(c *core.Config, s string) { c.YandexToken = s })
		note("Open <a style='color:" + th.Accent + "' href='" + core.YandexTokenURL + "'>Yandex's token page</a>, log in, and copy the access_token=… value.")
	case "done":
		var on []string
		for _, s := range w.draft.Sources {
			if s.Enabled {
				on = append(on, core.SourceLabels[s.Name])
			}
		}
		form.AddRowWithWidget(text("Sources you're using: "+core.FirstNonEmpty(strings.Join(on, ", "), "none yet"), "").QWidget)
	}
	box.AddStretch()

	buttons := hbox()
	buttons.SetSpacing(8)
	if st.more != "" {
		buttons.AddWidget(button("What's this?", "ghost", func() { w.a.showHelp(st.more) }).QWidget)
	} else if st.id == "torrent" {
		buttons.AddWidget(button("What's this?", "ghost", func() {
			for _, x := range core.SetupSteps {
				if x.ID == "torrent" {
					w.a.message("Torrents", x.More)
				}
			}
		}).QWidget)
	}
	buttons.AddStretch()
	add := func(label, kind string, do func()) *qt.QPushButton {
		b := button(label, kind, do)
		buttons.AddWidget(b.QWidget)
		return b
	}
	if w.step > 0 && st.id != "done" {
		add("Back", "ghost", func() { w.step--; w.show() })
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
		if w.step++; w.step >= len(setupSteps) {
			w.finish()
			return
		}
		w.show()
	}
	var forward *qt.QPushButton
	switch st.id {
	case "welcome":
		add("Skip the setup", "ghost", func() {
			w.a.m.FinishSetup(func(*core.Config) {})
			w.d.Accept()
		})
		forward = add("Get started", "primary", next)
	case "done":
		add("Back", "ghost", func() { w.step--; w.show() })
		forward = add("Start using the app", "primary", next)
	default:
		isSource := st.id == "soulseek" || st.id == "torrent" || st.id == "vk" || st.id == "yandex"
		label := "Skip"
		if isSource {
			label = "Not for me"
		}
		add(label, "ghost", func() {
			if isSource {
				w.draft.SetSource(st.id, false)
			}
			w.step++
			w.show()
		})
		forward = add("Next", "primary", next)
	}
	forward.SetDefault(true)
	box.AddLayout(buttons.QLayout)
	w.d.Layout().AddWidget(w.body)
	if form.RowCount() > 0 && st.id != "welcome" {
		w.body.FocusNextChild()
	} else {
		forward.SetFocus()
	}
}

func (w *setupWizard) finish() {
	draft := w.draft
	if err := w.a.m.FinishSetup(func(c *core.Config) { *c = draft }); err != nil {
		w.a.message("Setup", err.Error())
		w.step = len(setupSteps) - 1
		w.show()
		return
	}
	w.d.Accept()
	w.a.toast("You're set. Find some music, or bring in your playlists.")
	w.a.show(pageFind)
}
