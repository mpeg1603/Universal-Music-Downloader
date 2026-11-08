package gui

import (
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Settings page: changes are saved as they're made (checked first; what can't be used is
// put back and said why). Each source's own settings are in Sources.
type settingsTab struct {
	a         *app
	w         *qt.QWidget
	loading   bool
	status    *qt.QLabel
	themeBtns []*qt.QPushButton
	out       *qt.QLineEdit
	format    *qt.QComboBox
	layout    *qt.QComboBox
	custom    *qt.QLineEdit
	thr       *qt.QComboBox
	bw        *qt.QDoubleSpinBox
	maxDl     *qt.QSpinBox
	searches  *qt.QSpinBox
	tries     *qt.QSpinBox
	jump      *qt.QCheckBox
	enrich    *qt.QCheckBox
	iface     *qt.QLineEdit
	tray      *qt.QCheckBox
	notify    *qt.QCheckBox
	advanced  *qt.QWidget
	advancedB *qt.QPushButton
}

// layouts are the file layouts offered by name.
var layouts = []struct{ label, template string }{
	{"Artist / Album / 01 - Song (recommended)", "{album_artist}/{album}/{tracknum} - {title}"},
	{"Artist / Album (Year) / 01 - Song", "{album_artist}/{album} ({year})/{tracknum} - {title}"},
	{"Artist / Song", "{artist}/{title}"},
	{"Artist - Song, all in one folder", "{artists} - {title}"},
	{"Custom…", ""},
}

// row adds a labelled setting with an explanation under it.
func settingRow(box *qt.QVBoxLayout, label, hint string, field *qt.QWidget) {
	r := hbox()
	r.SetSpacing(16)
	col := vbox()
	col.SetSpacing(1)
	col.AddWidget(text(label, "").QWidget)
	if hint != "" {
		col.AddWidget(text(hint, "hint").QWidget)
	}
	r.AddLayout2(col.QLayout, 3)
	if field != nil {
		r.AddWidget2(field, 2)
	}
	box.AddLayout(r.QLayout)
}

func newSettingsTab(a *app) *settingsTab {
	t := &settingsTab{a: a}
	p := a.newPage("Settings", "Changes are saved as you make them.", "")
	t.w = p.w
	t.status = text("", "error")
	t.status.Hide()
	p.box.AddWidget(t.status.QWidget)
	scroll, body, _ := scrolling()
	p.box.AddWidget2(scroll.QWidget, 1)

	// the look
	look, lb := card()
	lb.AddWidget(text("Appearance", "h2").QWidget)
	seg, btns := segments([]string{"Match my system", "Light", "Dark"}, func(i int) {
		t.save(func(c *core.Config) { c.GUI.Theme = core.Themes[i] })
	})
	t.themeBtns = btns
	icons := []string{"monitor", "sun", "moon"}
	a.onTheme(func() {
		for i, b := range btns {
			b.SetIcon(iconOf(icons[i], 16, th.Text2))
		}
	})
	settingRow(lb, "Theme", "“Match my system” follows your desktop's light or dark mode, and changes when it does.", seg.QWidget)
	body.AddWidget(look.QWidget)

	// files
	files, fb := card()
	fb.AddWidget(text("Files", "h2").QWidget)
	t.out = qt.NewQLineEdit2()
	t.out.OnEditingFinished(t.saveAll)
	browse := button("Browse…", "", func() {
		if d := qt.QFileDialog_GetExistingDirectory3(a.win.QWidget, "Downloads folder", t.out.Text()); d != "" {
			t.out.SetText(d)
			t.saveAll()
		}
	})
	field := qt.NewQWidget2()
	fr := qt.NewQHBoxLayout(field)
	fr.SetContentsMargins(0, 0, 0, 0)
	fr.SetSpacing(6)
	fr.AddWidget2(t.out.QWidget, 1)
	fr.AddWidget(browse.QWidget)
	settingRow(fb, "Downloads folder", "New files go here, never into your library.", field)
	t.format = qt.NewQComboBox2()
	t.format.AddItems(formatWords)
	t.format.OnActivated(func(int) { t.saveAll() })
	settingRow(fb, "Format", "“Keep original” saves files exactly as they came. Converting only makes sense if your player can't handle the original format, and it can't improve quality.", t.format.QWidget)
	t.layout = qt.NewQComboBox2()
	for _, l := range layouts {
		t.layout.AddItem(l.label)
	}
	t.layout.OnActivated(func(int) {
		t.custom.SetVisible(t.layout.CurrentIndex() == len(layouts)-1)
		t.saveAll()
	})
	settingRow(fb, "Folders and names", "How files are arranged in your downloads folder.", t.layout.QWidget)
	t.custom = qt.NewQLineEdit2()
	t.custom.SetToolTip("You can use {artist} {artists} {album_artist} {album} {title} {tracknum} {year}")
	t.custom.OnEditingFinished(t.saveAll)
	t.custom.Hide()
	fb.AddWidget(t.custom.QWidget)
	t.thr = qt.NewQComboBox2()
	t.thr.AddItems(qualityItems())
	t.thr.OnActivated(func(int) { t.saveAll() })
	settingRow(fb, "Good enough quality", "Songs in your library at this quality or better aren't downloaded again.", t.thr.QWidget)
	body.AddWidget(files.QWidget)

	// speed
	speed, sb := card()
	sb.AddWidget(text("Speed", "h2").QWidget)
	t.bw = qt.NewQDoubleSpinBox2()
	t.bw.SetRange(0, 1000)
	t.bw.SetDecimals(1)
	t.bw.SetSuffix(" MB/s")
	t.bw.SetSpecialValueText("No limit")
	t.bw.SetKeyboardTracking(false)
	t.bw.OnValueChanged(func(float64) { t.saveAll() })
	settingRow(sb, "Speed limit", "All downloads together. Useful if downloads get in the way of everything else.", t.bw.QWidget)
	t.maxDl = t.spin(1, 64)
	settingRow(sb, "Downloads at once", "More is faster if your connection can take it.", t.maxDl.QWidget)
	body.AddWidget(speed.QWidget)

	// the app
	appCard, ab := card()
	ab.AddWidget(text("The app", "h2").QWidget)
	t.tray = qt.NewQCheckBox3("Keep running in the system tray when the window is closed")
	t.tray.SetToolTip("Downloads carry on. Quit from the tray icon or with Ctrl+Q.")
	t.tray.OnClicked(t.saveAll)
	if a.tray == nil {
		t.tray.SetEnabled(false)
		t.tray.SetToolTip("Your desktop has no system tray.")
	}
	ab.AddWidget(t.tray.QWidget)
	t.notify = qt.NewQCheckBox3("Tell me when downloads finish, or someone messages me on Soulseek")
	t.notify.OnClicked(t.saveAll)
	ab.AddWidget(t.notify.QWidget)
	again := hbox()
	again.AddWidget(button("Run the first-time setup again", "", func() { a.runSetup(); t.load() }).QWidget)
	again.AddStretch()
	ab.AddLayout(again.QLayout)
	body.AddWidget(appCard.QWidget)

	// advanced
	t.advancedB = button("▸  Advanced", "link", func() {
		on := !t.advanced.IsVisible()
		t.advanced.SetVisible(on)
		if on {
			t.advancedB.SetText("▾  Advanced")
		} else {
			t.advancedB.SetText("▸  Advanced")
		}
	})
	body.AddWidget(t.advancedB.QWidget)
	adv, advb := card()
	t.advanced = adv.QWidget
	t.searches = t.spin(1, 32)
	settingRow(advb, "Searches at once", "How many songs are looked for at the same time.", t.searches.QWidget)
	t.tries = t.spin(1, 50)
	settingRow(advb, "Copies to try per source", "How many results of a source are tried before moving on to the next source.", t.tries.QWidget)
	t.jump = qt.NewQCheckBox3("Skip busy sources while there's bandwidth to spare")
	t.jump.SetToolTip("Songs move on to the next source instead of waiting for a busy one, like Soulseek's few download slots")
	t.jump.OnClicked(t.saveAll)
	advb.AddWidget(t.jump.QWidget)
	t.enrich = qt.NewQCheckBox3("Look up missing album details on Deezer")
	t.enrich.OnClicked(t.saveAll)
	advb.AddWidget(t.enrich.QWidget)
	t.iface = qt.NewQLineEdit2()
	if d, err := core.DetectPhysicalInterface(); err == nil {
		t.iface.SetPlaceholderText("Detected: " + d)
	}
	t.iface.OnEditingFinished(t.saveAll)
	settingRow(advb, "Network interface for Direct connections", "The app finds this on its own; set it only if that goes wrong.", t.iface.QWidget)
	t.advanced.Hide()
	body.AddWidget(t.advanced)
	verRow := text(appName+", version "+Version+". Free software under the GNU GPL 3.0.", "muted")
	body.AddWidget(verRow.QWidget)
	body.AddStretch()
	t.load()
	return t
}

// spin is a number field saved when it changes (typed numbers when you're done typing).
func (t *settingsTab) spin(lo, hi int) *qt.QSpinBox {
	s := qt.NewQSpinBox2()
	s.SetRange(lo, hi)
	s.SetKeyboardTracking(false)
	s.OnValueChanged(func(int) { t.saveAll() })
	return s
}

// load fills the page from the saved settings, leaving alone what's being edited.
func (t *settingsTab) load() {
	t.loading = true
	defer func() { t.loading = false }()
	c := t.a.m.Config()
	for i, m := range core.Themes {
		if m == t.a.themeMode() {
			t.themeBtns[i].SetChecked(true)
		}
	}
	if !t.out.HasFocus() {
		t.out.SetText(c.OutputDir)
		t.out.SetCursorPosition(0)
	}
	for i, f := range core.Formats {
		if f == c.Format {
			t.format.SetCurrentIndex(i)
		}
	}
	idx := len(layouts) - 1
	for i, l := range layouts[:len(layouts)-1] {
		if l.template == c.Template {
			idx = i
		}
	}
	t.layout.SetCurrentIndex(idx)
	if !t.custom.HasFocus() {
		t.custom.SetText(c.Template)
	}
	t.custom.SetVisible(idx == len(layouts)-1)
	for i, th := range core.Thresholds {
		if string(th.Name) == c.Threshold {
			t.thr.SetCurrentIndex(i)
		}
	}
	if !t.bw.HasFocus() {
		t.bw.SetValue(c.BandwidthMBps)
	}
	if !t.maxDl.HasFocus() {
		t.maxDl.SetValue(c.MaxDownloads)
	}
	if !t.searches.HasFocus() {
		t.searches.SetValue(c.SearchWorkers)
	}
	if !t.tries.HasFocus() {
		t.tries.SetValue(c.TriesPerSrc)
	}
	t.jump.SetChecked(!c.NoQueueJump)
	t.enrich.SetChecked(c.Enrich)
	if !t.iface.HasFocus() {
		t.iface.SetText(c.DirectInterface)
	}
	t.tray.SetChecked(c.GUI.CloseToTray)
	t.notify.SetChecked(!c.GUI.NoNotify)
}

// save applies one change; what can't be saved is put back and explained.
func (t *settingsTab) save(change func(*core.Config)) {
	if t.loading {
		return
	}
	if err := t.a.m.UpdateConfig(change); err != nil {
		t.status.SetText("That can't be saved: " + err.Error())
		t.status.Show()
		t.load()
		return
	}
	t.status.Hide()
	t.a.applyTheme()
}

// saveAll saves the page if anything on it changed.
func (t *settingsTab) saveAll() {
	if t.loading {
		return
	}
	apply := func(c *core.Config) {
		c.OutputDir = strings.TrimSpace(t.out.Text())
		if i := t.format.CurrentIndex(); i >= 0 && i < len(core.Formats) {
			c.Format = core.Formats[i]
		}
		if i := t.layout.CurrentIndex(); i >= 0 && i < len(layouts)-1 {
			c.Template = layouts[i].template
		} else if s := strings.TrimSpace(t.custom.Text()); s != "" {
			c.Template = s
		}
		if i := t.thr.CurrentIndex(); i >= 0 && i < len(core.Thresholds) {
			c.Threshold = string(core.Thresholds[i].Name)
		}
		c.BandwidthMBps = t.bw.Value()
		c.MaxDownloads, c.SearchWorkers, c.TriesPerSrc = t.maxDl.Value(), t.searches.Value(), t.tries.Value()
		c.NoQueueJump, c.Enrich = !t.jump.IsChecked(), t.enrich.IsChecked()
		c.DirectInterface = strings.TrimSpace(t.iface.Text())
		c.GUI.CloseToTray, c.GUI.NoNotify = t.tray.IsChecked(), !t.notify.IsChecked()
	}
	before := t.a.m.Config()
	after := t.a.m.Config()
	apply(&after)
	if sameSettings(before, after) {
		return
	}
	t.save(apply)
}

// sameSettings compares what the page can change.
func sameSettings(a, b core.Config) bool {
	return a.OutputDir == b.OutputDir && a.Format == b.Format && a.Template == b.Template && a.Threshold == b.Threshold &&
		a.BandwidthMBps == b.BandwidthMBps && a.MaxDownloads == b.MaxDownloads && a.SearchWorkers == b.SearchWorkers &&
		a.TriesPerSrc == b.TriesPerSrc && a.NoQueueJump == b.NoQueueJump && a.Enrich == b.Enrich &&
		a.DirectInterface == b.DirectInterface && a.GUI == b.GUI
}
