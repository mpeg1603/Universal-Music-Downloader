package tui

import (
	"universal-music-downloader/internal/core"

	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (ui *tui) buildLibrary() tview.Primitive {
	ui.libInfo = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	ui.libInfo.SetBorder(true).SetTitle(" Your music library (read only: nothing in it is ever changed) ")
	ui.libTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.libTable.SetBorder(true).SetTitle(" Library folders (a add · d remove · s rescan · t threshold) ")
	ui.libTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		dirs := ui.m.Config().LibraryDirs
		row, _ := ui.libTable.GetSelection()
		switch ev.Rune() {
		case 'a':
			ui.askPath("Add a library folder", "A folder with your music. The app only reads it (unless you use the Sort tab yourself); downloads go to the downloads folder (Settings).",
				true, nil, func(p string) {
					p = core.CleanPath(p)
					if st, err := os.Stat(p); err != nil || !st.IsDir() {
						ui.message("Can't add that", p+" is not a folder")
						return
					}
					ui.update(func(c *core.Config) {
						if !core.ContainsString(c.LibraryDirs, p) {
							c.LibraryDirs = append(c.LibraryDirs, p)
						}
					})
				})
		case 'd':
			if row >= 1 && row-1 < len(dirs) {
				d := dirs[row-1]
				ui.confirm("Stop checking "+d+"? (Nothing in it is touched.)", "Remove", func() {
					ui.update(func(c *core.Config) {
						var keep []string
						for _, x := range c.LibraryDirs {
							if x != d {
								keep = append(keep, x)
							}
						}
						c.LibraryDirs = keep
					})
				})
			}
		case 's':
			ui.m.Rescan()
		case 't':
			ui.pickThreshold()
		case 'u':
			ui.pickUpgrade()
		default:
			if ev.Key() == tcell.KeyDelete && row >= 1 {
				return tcell.NewEventKey(tcell.KeyRune, 'd', 0)
			}
			return ev
		}
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ui.libInfo, 11, 0, false).AddItem(ui.libTable, 0, 1, true)
}

// pickUpgrade lists how many library files are below each quality level; picking one queues
// them to be downloaded again in better quality.
func (ui *tui) pickUpgrade() {
	counts := ui.m.UpgradeCounts()
	list := tview.NewList().ShowSecondaryText(true)
	for _, t := range core.Thresholds {
		if t.Name == "any" {
			continue
		}
		th, n := t.Name, counts[t.Name]
		list.AddItem(fmt.Sprintf("below %s: %d files", strings.SplitN(t.Label, " (", 2)[0], n), "   "+t.Label, 0, func() {
			ui.closeModal("upgrade")
			if n == 0 {
				ui.message("Upgrade", "No library files below that.")
				return
			}
			ui.confirm(fmt.Sprintf("Look for better copies of %d library files below %s? Each is downloaded only when a source has it in clearly better quality "+
				"(never converted); the new file goes to the downloads folder and your library copy stays as it is.", n, th), "Upgrade", func() {
				name, got, err := ui.m.UpgradeLibrary(th)
				if err != nil {
					ui.message("Upgrade", err.Error())
					return
				}
				ui.message("Upgrade", fmt.Sprintf("%d tracks queued as the playlist %s (f in the Queue: \"library upgrades\"; remove it in the Search tab with d).", got, name))
			})
		})
	}
	list.SetDoneFunc(func() { ui.closeModal("upgrade") })
	list.SetBorder(true).SetTitle(" Upgrade your library: download better copies of files below… ")
	ui.openModal("upgrade", list, 90, list.GetItemCount()*2+2)
}

func (ui *tui) pickThreshold() {
	list := tview.NewList().ShowSecondaryText(false)
	cur := ui.m.Config().Threshold
	for i, t := range core.Thresholds {
		name := string(t.Name)
		label := fmt.Sprintf("%-9s %s", name, t.Label)
		if name == cur {
			label += "  ✓"
		}
		list.AddItem(label, "", 0, func() {
			ui.closeModal("threshold")
			ui.update(func(c *core.Config) { c.Threshold = name })
		})
		if name == cur {
			list.SetCurrentItem(i)
		}
	}
	list.SetDoneFunc(func() { ui.closeModal("threshold") })
	list.SetBorder(true).SetTitle(" Good enough quality: tracks you have at this quality or better are skipped ")
	ui.openModal("threshold", list, 84, len(core.Thresholds)+2)
}

func (ui *tui) refreshLibrary() {
	c := ui.m.Config()
	st := ui.m.LibraryStatus()
	var b strings.Builder
	th := core.Threshold(c.Threshold)
	label := ""
	for _, t := range core.Thresholds {
		if t.Name == th {
			label = t.Label
		}
	}
	fmt.Fprintf(&b, "Good enough quality (t): [::b]%s[::-] — %s\n", c.Threshold, label)
	b.WriteString("Tracks you have at this quality or better are skipped. Ones you have in lower quality are downloaded only\n" +
		"if a source has them in clearly better quality; the new file goes to the downloads folder and your copy stays.\n" +
		"[::b]u[::-] upgrades your library's files below a quality: better copies are downloaded (never converted).\n\n")
	switch {
	case len(c.LibraryDirs) == 0:
		b.WriteString("[yellow]No library folders yet: press a to add one.[-]\n")
	case st.Scanning:
		fmt.Fprintf(&b, "[yellow]Scanning: %d of %d files[-]\n", st.Done, st.Total)
	case !st.LastScan.IsZero():
		fmt.Fprintf(&b, "%d audio files, scanned %s (took %s)", st.Files, st.LastScan.Format("15:04"), st.Took.Round(100*time.Millisecond))
		if g := st.Files - st.ReadFromTags; g > 0 {
			fmt.Fprintf(&b, "; %d without usable tags are matched by file name", g)
		}
		b.WriteString("\n")
	default:
		fmt.Fprintf(&b, "%d files known from the last scan; rescanning soon\n", st.Known)
	}
	if st.Err != "" {
		fmt.Fprintf(&b, "[red]%s[-]\n", tview.Escape(st.Err))
	}
	fmt.Fprintf(&b, "Downloads go to: %s", tview.Escape(c.OutputDir))
	ui.libInfo.SetText(b.String())
	t := ui.libTable
	t.Clear()
	headerRow(t, "Folder")
	for i, d := range c.LibraryDirs {
		t.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(d)).SetExpansion(1))
	}
	if r, _ := t.GetSelection(); r < 1 && len(c.LibraryDirs) > 0 {
		t.Select(1, 0)
	}
}

// --- sources -----------------------------------------------------------------------------------
