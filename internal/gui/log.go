package gui

import (
	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
)

// The Log tab: what happened, newest at the bottom.
type logTab struct {
	a       *app
	w       *qt.QWidget
	text    *qt.QPlainTextEdit
	verbose *qt.QCheckBox
	seq     int64
}

func newLogTab(a *app) *logTab {
	t := &logTab{a: a, w: qt.NewQWidget2(), seq: -1}
	box := qt.NewQVBoxLayout(t.w)
	box.SetContentsMargins(0, 4, 0, 0)
	t.verbose = qt.NewQCheckBox3("Detailed log")
	t.verbose.OnToggled(func(on bool) { core.SetVerbose(on); t.seq = -1; t.refresh() })
	box.AddWidget(t.verbose.QWidget)
	t.text = qt.NewQPlainTextEdit2()
	t.text.SetReadOnly(true)
	t.text.SetMaximumBlockCount(5000)
	t.text.SetLineWrapMode(qt.QPlainTextEdit__NoWrap)
	box.AddWidget2(t.text.QWidget, 1)
	t.refresh()
	return t
}

// refresh adds the lines that came since the last time (the log keeps its last 3000).
func (t *logTab) refresh() {
	lines, seq := t.a.m.Log()
	if seq == t.seq {
		return
	}
	n := int(seq - t.seq)
	if t.seq < 0 || n > len(lines) {
		t.text.Clear()
		n = len(lines)
	}
	t.seq = seq
	bar := t.text.VerticalScrollBar()
	atEnd := bar.Value() >= bar.Maximum()-2
	for _, l := range lines[len(lines)-n:] {
		if l.Debug && !t.verbose.IsChecked() {
			continue
		}
		t.text.AppendPlainText(l.At.Format("15:04:05") + "  " + l.Text)
	}
	if atEnd {
		bar.SetValue(bar.Maximum())
	}
}
