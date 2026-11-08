package tui

import (
	"fmt"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	"github.com/rivo/tview"
)

// The Dashboard tab: speeds, how far the songs have come, and what's taking up the disk.

const dashHistory = 60

func (ui *tui) buildDashboard() tview.Primitive {
	ui.dashView = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	ui.dashView.SetBorder(true).SetTitle(" Dashboard ")
	return ui.dashView
}

// dashSample keeps the speed history, whichever tab is showing.
func (ui *tui) dashSample(o core.Overview) {
	push := func(list []float64, v float64) []float64 {
		list = append(list, v)
		if len(list) > dashHistory {
			list = list[len(list)-dashHistory:]
		}
		return list
	}
	ui.dashDown, ui.dashUp = push(ui.dashDown, o.Own+o.Torrent), push(ui.dashUp, o.SlskUp)
}

func spark(vals []float64) string {
	bars := []rune("▁▂▃▄▅▆▇█")
	top := 1.0
	for _, v := range vals {
		top = max(top, v)
	}
	var b strings.Builder
	for _, v := range vals {
		b.WriteRune(bars[min(int(v/top*float64(len(bars))), len(bars)-1)])
	}
	return b.String()
}

func (ui *tui) refreshDashboard() {
	if time.Since(ui.dashRead) < time.Second {
		return
	}
	ui.dashRead = time.Now()
	v := ui.m.Dashboard()
	o := v.Overview
	p := v.Parts
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]Download speed[::-]  %-14s [aqua]%s[-]\n", core.FmtRate(o.Own+o.Torrent), spark(ui.dashDown))
	if o.SlskOn {
		fmt.Fprintf(&b, "[::b]Sharing[::-]         %-14s [aqua]%s[-]   sending %d of %d, %d files shared\n", core.FmtRate(o.SlskUp), spark(ui.dashUp), v.Slsk.UpActive, v.Slsk.UpSlots, v.Slsk.SharedFiles)
	} else {
		b.WriteString("[::b]Sharing[::-]         Soulseek isn't running\n")
	}
	if o.Free > 0 {
		fmt.Fprintf(&b, "[::b]Free space[::-]      %s where downloads go\n", core.FmtBytes(o.Free))
	}
	ready := p.InLibrary + p.Downloaded
	frac := 0.0
	if o.Total > 0 {
		frac = float64(ready) / float64(o.Total)
	}
	fmt.Fprintf(&b, "\n[::b]Your songs[::-]  %d of %d ready (%.0f%%)\n\n", ready, o.Total, frac*100)
	row := func(color, label string, n int) {
		bar := 0
		if o.Total > 0 {
			bar = n * 40 / o.Total
			if n > 0 {
				bar = max(bar, 1)
			}
		}
		fmt.Fprintf(&b, "  %-30s %7d  [%s]%s[-]\n", label, n, color, strings.Repeat("█", bar))
	}
	row("dodgerblue", "Already in my library", p.InLibrary)
	row("green", "Downloaded", p.Downloaded)
	row("yellow", "Need an upgrade", p.NeedUpgrade)
	row("aqua", "Downloading", p.Downloading)
	row("aqua", "Searching", p.Searching)
	row("orange", "Waiting", p.Waiting)
	row("gray", "Queued", p.Queued)
	row("red", "Not found", p.NotFound)
	row("gray", "Cancelled", p.Cancelled)
	b.WriteString("\n[::b]Disk use[::-]\n\n")
	d := v.Disk
	if !d.Known {
		b.WriteString("  [::d]adding it up…[::-]\n")
	} else {
		fmt.Fprintf(&b, "  My library             %12s  %d files\n", core.FmtBytes(d.LibraryBytes), d.LibraryFiles)
		fmt.Fprintf(&b, "  Downloads              %12s  %d files\n", core.FmtBytes(d.DownloadBytes), d.DownloadFiles)
		fmt.Fprintf(&b, "  App's temporary files  %12s\n\n", core.FmtBytes(d.CacheBytes))
		for _, x := range d.Disks {
			fmt.Fprintf(&b, "  %s: %s free of %s\n", tview.Escape(x.Path), core.FmtBytes(x.Free), core.FmtBytes(x.Total))
			if x.Total > 0 {
				part := func(n int64) int { return int(n * 50 / x.Total) }
				other := x.Total - x.Free - x.Library - x.Downloads
				fmt.Fprintf(&b, "  [dodgerblue]%s[green]%s[gray]%s[-]%s\n", strings.Repeat("█", part(x.Library)), strings.Repeat("█", part(x.Downloads)),
					strings.Repeat("▒", part(max(other, 0))), strings.Repeat("·", part(x.Free)))
			}
		}
		b.WriteString("  [dodgerblue]█[-] library  [green]█[-] downloads  [gray]▒[-] everything else  · free\n")
	}
	ui.dashView.SetText(b.String())
}
