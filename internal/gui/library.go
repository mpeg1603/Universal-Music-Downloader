package gui

import (
	"fmt"
	"html"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The My library page: your music folders (only ever read), the scan, the quality that's good
// enough, and upgrading files below a quality.
type libraryTab struct {
	a         *app
	w         *qt.QWidget
	files     *qt.QLabel
	info      *qt.QLabel
	threshold *qt.QComboBox
	dirs      *qt.QListWidget
	remove    *qt.QPushButton
	rescan    *qt.QPushButton
	output    *qt.QLabel
}

func qualityItems() []string {
	var out []string
	for _, th := range core.Thresholds {
		out = append(out, thresholdWords[string(th.Name)])
	}
	return out
}

func newLibraryTab(a *app) *libraryTab {
	t := &libraryTab{a: a}
	p := a.newPage("My library", "The music you already have, so it doesn't get downloaded again.", "library")
	t.w = p.w

	safe, safeText := banner("banner")
	safeText.SetText("<b>Your files are safe.</b> Library folders are only ever read: the app never creates, changes, moves or deletes anything in them.")
	p.box.AddWidget(safe.QWidget)

	scroll, body, _ := scrolling()
	p.box.AddWidget2(scroll.QWidget, 1)

	// the status and the folders
	folders, fb := card()
	head := hbox()
	head.SetSpacing(10)
	nums := vbox()
	nums.SetSpacing(0)
	t.files = text("0", "stat")
	nums.AddWidget(t.files.QWidget)
	nums.AddWidget(text("songs in your library", "hint").QWidget)
	head.AddLayout(nums.QLayout)
	head.AddSpacing(16)
	t.info = richText("", "hint")
	head.AddWidget2(t.info.QWidget, 1)
	fb.AddLayout(head.QLayout)
	fb.AddWidget(rule().QWidget)
	fb.AddWidget(text("Music folders", "h2").QWidget)
	t.dirs = qt.NewQListWidget2()
	t.dirs.SetMaximumHeight(130)
	t.dirs.OnCurrentRowChanged(func(int) { t.remove.SetEnabled(t.dirs.CurrentItem() != nil) })
	fb.AddWidget(t.dirs.QWidget)
	buttons := hbox()
	buttons.SetSpacing(6)
	buttons.AddWidget(a.iconButton("plus", "Add a folder", "primary", "", func() {
		if p := qt.QFileDialog_GetExistingDirectory3(a.win.QWidget, "Add a music folder (only ever read)", ""); p != "" {
			t.try(a.m.AddLibraryDir(p))
		}
	}).QWidget)
	t.remove = a.iconButton("trash", "Remove", "danger", "Stop checking this folder. Nothing in it is touched.", func() {
		if it := t.dirs.CurrentItem(); it != nil && a.confirm("Stop checking this folder?", it.Text()+"\n\nNothing in it is touched.") {
			t.try(a.m.RemoveLibraryDir(it.Text()))
		}
	})
	buttons.AddWidget(t.remove.QWidget)
	buttons.AddStretch()
	t.rescan = a.iconButton("refresh", "Scan again", "", "Look through your folders for new or changed files", func() {
		a.m.Rescan()
		t.refresh()
	})
	buttons.AddWidget(t.rescan.QWidget)
	fb.AddLayout(buttons.QLayout)
	t.output = text("", "muted")
	fb.AddWidget(t.output.QWidget)
	body.AddWidget(folders.QWidget)

	// quality
	q, qb := card()
	qb.AddWidget(text("What counts as good enough", "h2").QWidget)
	qb.AddWidget(text("Songs you have at this quality or better are skipped. Songs you have in worse quality are downloaded only if a source has a clearly better copy; your old file stays.", "hint").QWidget)
	t.threshold = qt.NewQComboBox2()
	t.threshold.AddItems(qualityItems())
	t.threshold.OnActivated(func(i int) {
		if i >= 0 && i < len(core.Thresholds) {
			name := string(core.Thresholds[i].Name)
			if err := a.m.UpdateConfig(func(c *core.Config) { c.Threshold = name }); err != nil {
				a.message("That didn't work", err.Error())
				return
			}
			a.toast("Saved.")
		}
	})
	row := hbox()
	row.AddWidget(t.threshold.QWidget)
	row.AddStretch()
	qb.AddLayout(row.QLayout)
	body.AddWidget(q.QWidget)

	// upgrades
	up, ub := card()
	upRow := hbox()
	upRow.SetSpacing(12)
	col := vbox()
	col.SetSpacing(4)
	col.AddWidget(text("Upgrade my library", "h2").QWidget)
	col.AddWidget(text("Look for better copies of your lower-quality files. New files go to your downloads folder, and your originals stay as they are.", "hint").QWidget)
	upRow.AddLayout2(col.QLayout, 1)
	upRow.AddWidget(a.iconButton("arrow-up", "Choose what to upgrade…", "", "", t.upgrade).QWidget)
	ub.AddLayout(upRow.QLayout)
	body.AddWidget(up.QWidget)
	body.AddStretch()
	t.refresh()
	return t
}

func (t *libraryTab) try(err error) {
	if err != nil {
		t.a.message("That didn't work", err.Error())
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
	files := st.Files
	if files == 0 {
		files = st.Known
	}
	t.files.SetText(fmt.Sprint(files))
	span := func(color, s string) string {
		return "<span style='color:" + color + "'>" + html.EscapeString(s) + "</span>"
	}
	switch {
	case len(c.LibraryDirs) == 0:
		b.WriteString(span(th.Warn, "Add the folders where you keep your music, so what you have isn't downloaded twice."))
	case st.Scanning:
		b.WriteString(span(th.Accent, fmt.Sprintf("Looking through your files: %d of %d", st.Done, st.Total)))
	case !st.LastScan.IsZero():
		fmt.Fprintf(&b, "Last looked through at %s (took %s).", st.LastScan.Format("15:04"), st.Took.Round(100*time.Millisecond))
		if g := st.Files - st.ReadFromTags; g > 0 {
			fmt.Fprintf(&b, "<br>%d files have no usable tags, so they're matched by file name.", g)
		}
	default:
		b.WriteString("Known from the last scan. Scanning again soon.")
	}
	if st.Err != "" {
		b.WriteString("<br>" + span(th.Bad, st.Err))
	}
	t.info.SetText(b.String())
	t.rescan.SetEnabled(!st.Scanning)
	t.output.SetText("New downloads go to " + c.OutputDir)
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
	t.remove.SetEnabled(t.dirs.CurrentItem() != nil)
}

// upgrade picks a quality; the library files below it are queued to be downloaded again in
// better quality.
func (t *libraryTab) upgrade() {
	counts := t.a.m.UpgradeCounts()
	var labels []string
	var levels []core.Threshold
	for _, x := range core.Thresholds {
		if x.Name == "any" {
			continue
		}
		labels = append(labels, fmt.Sprintf("%d files below %s", counts[x.Name], thresholdWords[string(x.Name)]))
		levels = append(levels, x.Name)
	}
	d := qt.NewQInputDialog2()
	d.SetWindowTitle("Upgrade my library")
	d.SetLabelText("Find better copies of the files below:")
	d.SetComboBoxItems(labels)
	d.SetComboBoxEditable(false)
	d.Resize(480, 130)
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
	lvl, n := levels[i], counts[levels[i]]
	if n == 0 {
		t.a.message("Nothing to upgrade", "None of your files are below that quality.")
		return
	}
	if !t.a.confirm("Upgrade my library", fmt.Sprintf("Look for better copies of %d files?\n\nEach is downloaded only when a source has a clearly better copy. New files go to your downloads folder, and your originals stay as they are.", n)) {
		return
	}
	_, got, err := t.a.m.UpgradeLibrary(lvl)
	if err != nil {
		t.a.message("That didn't work", err.Error())
		return
	}
	t.a.toast(fmt.Sprintf("%d songs are queued for an upgrade.", got))
	t.a.queue.refresh(false)
}
