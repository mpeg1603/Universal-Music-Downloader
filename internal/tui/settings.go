package tui

import (
	"spotify-downloader/internal/core"

	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rivo/tview"
)

func (ui *tui) buildSettings() tview.Primitive {
	ui.settings = tview.NewForm().SetItemPadding(0)
	ui.settings.SetBorder(true).SetTitle(" Settings ")
	ui.settings.SetCancelFunc(func() { // Esc leaves the field
		ui.settings.SetFocus(ui.settings.GetFormItemCount())
		ui.app.SetFocus(ui.settings)
	})
	ui.fillSettings()
	return ui.settings
}

func (ui *tui) fillSettings() {
	f := ui.settings
	f.Clear(true)
	c := ui.m.Config()
	var apply []func(*core.Config) error
	text := func(label, value string, width int, set func(*core.Config, string) error) *tview.InputField {
		v := value
		f.AddInputField(label, value, width, nil, func(t string) { v = t })
		apply = append(apply, func(c *core.Config) error { return set(c, strings.TrimSpace(v)) })
		return f.GetFormItem(f.GetFormItemCount() - 1).(*tview.InputField)
	}
	number := func(label string, value float64, width int, set func(*core.Config, float64)) {
		text(label, strconv.FormatFloat(value, 'f', -1, 64), width, func(c *core.Config, s string) error {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("%s: %q is not a number", strings.TrimSpace(label), s)
			}
			set(c, v)
			return nil
		})
	}
	out := text("Downloads folder", c.OutputDir, 70, func(c *core.Config, s string) error { c.OutputDir = s; return nil })
	out.SetAutocompleteFunc(func(t string) []string { return pathCompletions(t, true, nil) })
	fi := 0
	for i, x := range core.Formats {
		if x == c.Format {
			fi = i
		}
	}
	format := c.Format
	f.AddDropDown("Format", []string{"original (no re-encoding)", "flac", "mp3 (V0)", "opus (192k)", "m4a (AAC 256k)"}, fi, func(o string, i int) { format = core.Formats[i] })
	apply = append(apply, func(c *core.Config) error { c.Format = format; return nil })
	text("File layout", c.Template, 50, func(c *core.Config, s string) error { c.Template = s; return nil })
	ti := 0
	var tlabels []string
	for i, t := range core.Thresholds {
		tlabels = append(tlabels, fmt.Sprintf("%s — %s", t.Name, t.Label))
		if string(t.Name) == c.Threshold {
			ti = i
		}
	}
	thr := c.Threshold
	f.AddDropDown("Good enough quality (library)", tlabels, ti, func(o string, i int) { thr = string(core.Thresholds[i].Name) })
	apply = append(apply, func(c *core.Config) error { c.Threshold = thr; return nil })
	number("Bandwidth target and cap, MB/s (0: none)", c.BandwidthMBps, 8, func(c *core.Config, v float64) { c.BandwidthMBps = v })
	number("Most downloads at once", float64(c.MaxDownloads), 4, func(c *core.Config, v float64) { c.MaxDownloads = int(v) })
	number("Searches at once", float64(c.SearchWorkers), 4, func(c *core.Config, v float64) { c.SearchWorkers = int(v) })
	number("Results to try per source", float64(c.TriesPerSrc), 4, func(c *core.Config, v float64) { c.TriesPerSrc = max(1, int(v)) })
	jump := !c.NoQueueJump
	f.AddCheckbox("Skip busy sources while bandwidth is to spare", jump, func(b bool) { jump = b })
	apply = append(apply, func(c *core.Config) error { c.NoQueueJump = !jump; return nil })
	enrich := c.Enrich
	f.AddCheckbox("Look up missing details on Deezer", c.Enrich, func(b bool) { enrich = b })
	apply = append(apply, func(c *core.Config) error { c.Enrich = enrich; return nil })
	iface := text("Network interface for \"direct\" (empty: detect)", c.DirectInterface, 16, func(c *core.Config, s string) error { c.DirectInterface = s; return nil })
	if d, err := core.DetectPhysicalInterface(); err == nil {
		iface.SetPlaceholder(d)
	}
	f.AddButton("Save", func() {
		trial := ui.m.Config() // check every field first, so nothing is saved half-way
		for _, a := range apply {
			if e := a(&trial); e != nil {
				ui.message("Can't save", e.Error())
				return
			}
		}
		err := ui.m.UpdateConfig(func(c *core.Config) {
			for _, a := range apply {
				a(c)
			}
		})
		if err != nil {
			ui.message("Can't save", err.Error())
			return
		}
		core.Logf("Settings saved")
		ui.message("Settings", "Saved and applied.")
	})
	f.AddButton("Undo changes", func() { ui.fillSettings() })
	f.AddButton("Run the setup again", ui.runSetup)
}

func fmtDurationShort(d time.Duration) string {
	s := d.String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "0m")
}
