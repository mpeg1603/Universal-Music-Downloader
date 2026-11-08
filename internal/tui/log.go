package tui

import (
	"universal-music-downloader/internal/core"

	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (ui *tui) buildLog() tview.Primitive {
	ui.logView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true).SetWrap(true)
	ui.logView.SetBorder(true).SetTitle(" Log ")
	ui.logView.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyPgUp, tcell.KeyHome:
			ui.logFollow = false
		case tcell.KeyEnd:
			ui.logFollow = true
			ui.logView.ScrollToEnd()
		}
		if ev.Rune() == 'v' {
			core.Verbose = !core.Verbose
			core.Logf("Detailed log %s", map[bool]string{true: "on", false: "off"}[core.Verbose])
			return nil
		}
		return ev
	})
	return ui.logView
}

func (ui *tui) refreshLog() {
	lines, seq := core.AppLog.Snapshot()
	if seq == ui.logSeq {
		return
	}
	ui.logSeq = seq
	var b strings.Builder
	for _, l := range lines {
		color := ""
		if l.Debug {
			color = "[gray]"
		}
		fmt.Fprintf(&b, "%s[gray]%s[-] %s%s[-]\n", "", l.At.Format("15:04:05"), color, tview.Escape(l.Text))
	}
	ui.logView.SetText(b.String())
	if ui.logFollow {
		ui.logView.ScrollToEnd()
	}
}
