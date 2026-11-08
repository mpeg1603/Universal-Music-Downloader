package gui

import (
	"strings"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Settings tab: the app's main settings, saved together with Save (checked first, so
// nothing is saved half-way). Each source's own settings are in the Sources tab.
type settingsTab struct {
	a        *app
	w        *qt.QWidget
	out      *qt.QLineEdit
	format   *qt.QComboBox
	layout   *qt.QLineEdit
	thr      *qt.QComboBox
	bw       *qt.QDoubleSpinBox
	maxDl    *qt.QSpinBox
	searches *qt.QSpinBox
	tries    *qt.QSpinBox
	jump     *qt.QCheckBox
	enrich   *qt.QCheckBox
	iface    *qt.QLineEdit
}

var formatLabels = []string{"original (no re-encoding)", "flac", "mp3 (V0)", "opus (192k)", "m4a (AAC 256k)"}

func newSettingsTab(a *app) *settingsTab {
	t := &settingsTab{a: a, w: qt.NewQWidget2()}
	outer := qt.NewQVBoxLayout(t.w)
	form := qt.NewQFormLayout2()
	form.SetFieldGrowthPolicy(qt.QFormLayout__ExpandingFieldsGrow)

	t.out = qt.NewQLineEdit2()
	browse := qt.NewQPushButton3("Browse…")
	browse.OnClicked(func() {
		if p := qt.QFileDialog_GetExistingDirectory3(t.a.win.QWidget, "Downloads folder", t.out.Text()); p != "" {
			t.out.SetText(p)
		}
	})
	outRow := qt.NewQHBoxLayout2()
	outRow.AddWidget2(t.out.QWidget, 1)
	outRow.AddWidget(browse.QWidget)
	form.AddRow4("Downloads folder", outRow.QLayout)

	t.format = qt.NewQComboBox2()
	t.format.AddItems(formatLabels)
	form.AddRow3("Format", t.format.QWidget)
	t.layout = qt.NewQLineEdit2()
	t.layout.SetToolTip("Where files go in the downloads folder, e.g. {album_artist}/{album}/{track:02} - {title}")
	form.AddRow3("File layout", t.layout.QWidget)
	t.thr = qt.NewQComboBox2()
	for _, th := range core.Thresholds {
		t.thr.AddItem(string(th.Name) + " — " + th.Label)
	}
	form.AddRow3("Good enough quality (library)", t.thr.QWidget)
	t.bw = qt.NewQDoubleSpinBox2()
	t.bw.SetRange(0, 1000)
	t.bw.SetDecimals(1)
	t.bw.SetSuffix(" MB/s")
	t.bw.SetSpecialValueText("no limit")
	form.AddRow3("Bandwidth target and cap", t.bw.QWidget)
	spin := func(lo, hi int) *qt.QSpinBox {
		s := qt.NewQSpinBox2()
		s.SetRange(lo, hi)
		return s
	}
	t.maxDl, t.searches, t.tries = spin(1, 64), spin(1, 64), spin(1, 50)
	form.AddRow3("Most downloads at once", t.maxDl.QWidget)
	form.AddRow3("Searches at once", t.searches.QWidget)
	form.AddRow3("Results to try per source", t.tries.QWidget)
	t.jump = qt.NewQCheckBox3("Skip busy sources while bandwidth is to spare (queue jumping)")
	form.AddRowWithWidget(t.jump.QWidget)
	t.enrich = qt.NewQCheckBox3("Look up missing details on Deezer")
	form.AddRowWithWidget(t.enrich.QWidget)
	t.iface = qt.NewQLineEdit2()
	if d, err := core.DetectPhysicalInterface(); err == nil {
		t.iface.SetPlaceholderText("detected: " + d)
	}
	form.AddRow3("Network interface for \"direct\"", t.iface.QWidget)
	outer.AddLayout(form.QLayout)

	buttons := qt.NewQHBoxLayout2()
	save := qt.NewQPushButton3("Save")
	save.SetDefault(true)
	save.OnClicked(t.save)
	undo := qt.NewQPushButton3("Undo changes")
	undo.OnClicked(t.load)
	buttons.AddWidget(save.QWidget)
	buttons.AddWidget(undo.QWidget)
	buttons.AddStretch()
	outer.AddLayout(buttons.QLayout)
	outer.AddStretch()
	t.load()
	return t
}

// load fills the form from the saved settings.
func (t *settingsTab) load() {
	c := t.a.m.Config()
	t.out.SetText(c.OutputDir)
	for i, f := range core.Formats {
		if f == c.Format {
			t.format.SetCurrentIndex(i)
		}
	}
	t.layout.SetText(c.Template)
	for i, th := range core.Thresholds {
		if string(th.Name) == c.Threshold {
			t.thr.SetCurrentIndex(i)
		}
	}
	t.bw.SetValue(c.BandwidthMBps)
	t.maxDl.SetValue(c.MaxDownloads)
	t.searches.SetValue(c.SearchWorkers)
	t.tries.SetValue(c.TriesPerSrc)
	t.jump.SetChecked(!c.NoQueueJump)
	t.enrich.SetChecked(c.Enrich)
	t.iface.SetText(c.DirectInterface)
}

func (t *settingsTab) save() {
	err := t.a.m.UpdateConfig(func(c *core.Config) {
		c.OutputDir = strings.TrimSpace(t.out.Text())
		if i := t.format.CurrentIndex(); i >= 0 && i < len(core.Formats) {
			c.Format = core.Formats[i]
		}
		c.Template = strings.TrimSpace(t.layout.Text())
		if i := t.thr.CurrentIndex(); i >= 0 && i < len(core.Thresholds) {
			c.Threshold = string(core.Thresholds[i].Name)
		}
		c.BandwidthMBps = t.bw.Value()
		c.MaxDownloads, c.SearchWorkers, c.TriesPerSrc = t.maxDl.Value(), t.searches.Value(), t.tries.Value()
		c.NoQueueJump, c.Enrich = !t.jump.IsChecked(), t.enrich.IsChecked()
		c.DirectInterface = strings.TrimSpace(t.iface.Text())
	})
	if err != nil {
		t.a.message("Can't save", err.Error())
		return
	}
	core.Logf("Settings saved")
	t.a.message("Settings", "Saved and applied.")
}
