package gui

import (
	"fmt"
	"html"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Library tab: your music folders (only ever read), the scan, the quality that's good
// enough, and upgrading files below a quality.
type libraryTab struct {
	a         *app
	w         *qt.QWidget
	info      *qt.QLabel
	threshold *qt.QComboBox
	dirs      *qt.QListWidget
}

func newLibraryTab(a *app) *libraryTab {
	t := &libraryTab{a: a, w: qt.NewQWidget2()}
	box := qt.NewQVBoxLayout(t.w)
	box.SetContentsMargins(0, 6, 0, 0)

	row := qt.NewQHBoxLayout2()
	row.AddWidget(qt.NewQLabel3("Good enough quality:").QWidget)
	t.threshold = qt.NewQComboBox2()
	for _, th := range core.Thresholds {
		t.threshold.AddItem(fmt.Sprintf("%s — %s", th.Name, th.Label))
	}
	t.threshold.OnActivated(func(i int) {
		if i >= 0 && i < len(core.Thresholds) {
			name := string(core.Thresholds[i].Name)
			if err := t.a.m.UpdateConfig(func(c *core.Config) { c.Threshold = name }); err != nil {
				t.a.message("Can't save", err.Error())
			}
		}
	})
	row.AddWidget2(t.threshold.QWidget, 1)
	upgrade := qt.NewQPushButton3("Upgrade the library…")
	upgrade.SetToolTip("Download better copies of your library files below a quality (never converted)")
	upgrade.OnClicked(t.upgrade)
	row.AddWidget(upgrade.QWidget)
	box.AddLayout(row.QLayout)

	explain := qt.NewQLabel3("Tracks you have at this quality or better are skipped. Ones you have in lower quality are downloaded " +
		"only if a source has them in clearly better quality; the new file goes to the downloads folder and your copy stays. " +
		"Your library folders are only read: nothing in them is ever created, changed, moved or deleted.")
	explain.SetWordWrap(true)
	box.AddWidget(explain.QWidget)
	t.info = qt.NewQLabel2()
	t.info.SetTextFormat(qt.RichText)
	t.info.SetWordWrap(true)
	box.AddWidget(t.info.QWidget)

	group := qt.NewQGroupBox3("Library folders")
	gb := qt.NewQVBoxLayout(group.QWidget)
	t.dirs = qt.NewQListWidget2()
	gb.AddWidget2(t.dirs.QWidget, 1)
	buttons := qt.NewQHBoxLayout2()
	add := qt.NewQPushButton3("Add folder…")
	add.OnClicked(func() {
		if p := qt.QFileDialog_GetExistingDirectory3(t.a.win.QWidget, "Add a library folder (only ever read)", ""); p != "" {
			t.try(t.a.m.AddLibraryDir(p))
		}
	})
	remove := qt.NewQPushButton3("Remove")
	remove.OnClicked(func() {
		if it := t.dirs.CurrentItem(); it != nil {
			d := it.Text()
			if t.a.confirm("Remove", "Stop checking "+d+"? (Nothing in it is touched.)") {
				t.try(t.a.m.RemoveLibraryDir(d))
			}
		}
	})
	rescan := qt.NewQPushButton3("Scan again")
	rescan.OnClicked(func() { t.a.m.Rescan(); t.refresh() })
	for _, b := range []*qt.QPushButton{add, remove, rescan} {
		buttons.AddWidget(b.QWidget)
	}
	buttons.AddStretch()
	gb.AddLayout(buttons.QLayout)
	box.AddWidget2(group.QWidget, 1)
	t.refresh()
	return t
}

func (t *libraryTab) try(err error) {
	if err != nil {
		t.a.message("Can't do that", err.Error())
	}
	t.refresh()
}

func (t *libraryTab) refresh() {
	c := t.a.m.Config()
	for i, th := range core.Thresholds {
		if string(th.Name) == c.Threshold {
			t.threshold.SetCurrentIndex(i)
		}
	}
	st := t.a.m.LibraryStatus()
	var b strings.Builder
	switch {
	case len(c.LibraryDirs) == 0:
		b.WriteString("<span style='color:#c80'>No library folders yet: add one below.</span>")
	case st.Scanning:
		fmt.Fprintf(&b, "<span style='color:#c80'>Scanning: %d of %d files</span>", st.Done, st.Total)
	case !st.LastScan.IsZero():
		fmt.Fprintf(&b, "%d audio files, scanned %s (took %s)", st.Files, st.LastScan.Format("15:04"), st.Took.Round(100*time.Millisecond))
		if g := st.Files - st.ReadFromTags; g > 0 {
			fmt.Fprintf(&b, "; %d without usable tags are matched by file name", g)
		}
	default:
		fmt.Fprintf(&b, "%d files known from the last scan; scanning again soon", st.Known)
	}
	if st.Err != "" {
		fmt.Fprintf(&b, "<br><span style='color:#c33'>%s</span>", html.EscapeString(st.Err))
	}
	fmt.Fprintf(&b, "<br>Downloads go to: %s", html.EscapeString(c.OutputDir))
	t.info.SetText(b.String())
	cur := ""
	if it := t.dirs.CurrentItem(); it != nil {
		cur = it.Text()
	}
	t.dirs.Clear()
	for i, d := range c.LibraryDirs {
		t.dirs.AddItem(d)
		if d == cur {
			t.dirs.SetCurrentRow(i)
		}
	}
}

// upgrade picks a quality; the library files below it are queued to be downloaded again in
// better quality.
func (t *libraryTab) upgrade() {
	counts := t.a.m.UpgradeCounts()
	var labels []string
	var levels []core.Threshold
	for _, th := range core.Thresholds {
		if th.Name == "any" {
			continue
		}
		labels = append(labels, fmt.Sprintf("below %s: %d files", strings.SplitN(th.Label, " (", 2)[0], counts[th.Name]))
		levels = append(levels, th.Name)
	}
	d := qt.NewQInputDialog2()
	d.SetWindowTitle("Upgrade your library")
	d.SetLabelText("Download better copies of your library files below:")
	d.SetComboBoxItems(labels)
	d.SetComboBoxEditable(false)
	if d.Exec() != int(qt.QDialog__Accepted) {
		return
	}
	i := -1
	for j, l := range labels {
		if l == d.TextValue() {
			i = j
		}
	}
	if i < 0 {
		return
	}
	th, n := levels[i], counts[levels[i]]
	if n == 0 {
		t.a.message("Upgrade", "No library files below that.")
		return
	}
	if !t.a.confirm("Upgrade", fmt.Sprintf("Look for better copies of %d library files below %s? Each is downloaded only when a source has it in "+
		"clearly better quality (never converted); the new file goes to the downloads folder and your library copy stays as it is.", n, th)) {
		return
	}
	name, got, err := t.a.m.UpgradeLibrary(th)
	if err != nil {
		t.a.message("Upgrade", err.Error())
		return
	}
	t.a.message("Upgrade", fmt.Sprintf("%d tracks queued as the playlist %s (the Queue's filter \"library upgrades\" shows them; the Search tab can remove it).", got, name))
}
