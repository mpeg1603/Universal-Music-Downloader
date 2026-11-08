package tui

import (
	"fmt"
	"strings"

	"spotify-downloader/internal/core"

	"github.com/rivo/tview"
)

// The first-run setup (core.SetupSteps): one window per step, filled into a draft of the
// settings that's saved at the end.

type setupWizard struct {
	ui    *tui
	draft core.Config
	step  int
}

// runSetup opens the setup.
func (ui *tui) runSetup() {
	w := &setupWizard{ui: ui, draft: ui.m.Config()}
	w.show()
}

func (w *setupWizard) close() { w.ui.closeModal("setup") }

func (w *setupWizard) on(name string) bool { return w.draft.Source(name).Enabled }

func (w *setupWizard) show() {
	ui := w.ui
	ui.dropModal("setup")
	st := core.SetupSteps[w.step]
	form := tview.NewForm().SetItemPadding(0)
	b := &formBuilder{f: form}
	d := &w.draft
	use := func(names ...string) *bool {
		on := false
		for _, n := range names {
			on = on || w.on(n)
		}
		if !w.draft.SetupDone && !on && (names[0] == "bandcamp" || names[0] == "archive" || names[0] == "audius") {
			on = true // the free ones start on
		}
		v := on
		label := "Use " + st.Title
		form.AddCheckbox(label, on, func(x bool) { v = x })
		b.apply = append(b.apply, func(c *core.Config) error {
			for _, n := range names {
				c.SetSource(n, v)
			}
			return nil
		})
		return &v
	}
	switch st.ID {
	case "output":
		out := b.text("Downloads folder", d.OutputDir, 70, func(c *core.Config, s string) error { c.OutputDir = s; return nil })
		out.SetAutocompleteFunc(func(t string) []string { return pathCompletions(t, true, nil) })
		fi := 0
		for i, x := range core.Formats {
			if x == d.Format {
				fi = i
			}
		}
		format := d.Format
		form.AddDropDown("Format", []string{"original (no re-encoding)", "flac", "mp3 (V0)", "opus (192k)", "m4a (AAC 256k)"}, fi,
			func(_ string, i int) { format = core.Formats[i] })
		b.apply = append(b.apply, func(c *core.Config) error { c.Format = format; return nil })
	case "library":
		lib := b.text("Library folders (; between them)", strings.Join(d.LibraryDirs, "; "), 70, func(c *core.Config, s string) error {
			c.LibraryDirs = core.SplitBy(s, ";")
			return nil
		})
		lib.SetAutocompleteFunc(func(t string) []string {
			i := strings.LastIndex(t, ";")
			var out []string
			for _, p := range pathCompletions(strings.TrimSpace(t[i+1:]), true, nil) {
				out = append(out, strings.TrimSpace(t[:i+1]+" "+p))
			}
			return out
		})
		ti, thr := 0, d.Threshold
		var labels []string
		for i, t := range core.Thresholds {
			labels = append(labels, fmt.Sprintf("%s — %s", t.Name, t.Label))
			if string(t.Name) == d.Threshold {
				ti = i
			}
		}
		form.AddDropDown("Good enough quality", labels, ti, func(_ string, i int) { thr = string(core.Thresholds[i].Name) })
		b.apply = append(b.apply, func(c *core.Config) error { c.Threshold = thr; return nil })
	case "playlists":
		var have []string
		for _, in := range d.Inputs {
			have = append(have, in.Path)
		}
		b.text("Playlist files or links (; between them)", strings.Join(have, "; "), 70, func(c *core.Config, s string) error {
			var ins []core.InputCfg
			for _, p := range core.SplitBy(s, ";") {
				if !strings.Contains(p, "://") && !strings.HasPrefix(p, "spotify:") {
					p = core.CleanPath(p)
				}
				ins = append(ins, core.InputCfg{Path: p, Enabled: true})
			}
			c.Inputs = ins
			return nil
		})
	case "bandwidth":
		b.number("Bandwidth cap, MB/s (0: none)", d.BandwidthMBps, 8, func(c *core.Config, v float64) { c.BandwidthMBps = v })
		b.number("Most downloads at once", float64(d.MaxDownloads), 4, func(c *core.Config, v float64) { c.MaxDownloads = int(v) })
		b.check("Skip busy sources while bandwidth is to spare", !d.NoQueueJump, func(c *core.Config, v bool) { c.NoQueueJump = !v })
	case "bandcamp":
		use("bandcamp")
		b.text("Your email address (optional)", d.Bandcamp.Email, 40, func(c *core.Config, s string) error { c.Bandcamp.Email = s; return nil })
	case "archive", "audius", "vk", "yandex":
		use(st.ID)
		switch st.ID {
		case "vk":
			b.secret("Token (or log in later: Sources → VK → Enter)", d.VKToken, func(c *core.Config, s string) { c.VKToken = strings.TrimSpace(s) })
		case "yandex":
			b.secret("OAuth token", d.YandexToken, func(c *core.Config, s string) { c.YandexToken = strings.TrimSpace(s) })
			form.AddTextView("", "Open "+core.YandexTokenURL+" in your browser, log in, and copy the access_token=… value.", 0, 2, false, false)
		}
	case "youtube":
		for _, s := range [][2]string{{"youtube_music", "YouTube Music"}, {"youtube", "YouTube"}, {"soundcloud", "SoundCloud"}} {
			name := s[0]
			b.check("Use "+s[1], w.on(name), func(c *core.Config, v bool) { c.SetSource(name, v) })
		}
		idx := 0
		for i, x := range core.Browsers {
			if x == d.CookiesFromBrowser {
				idx = i
			}
		}
		br := d.CookiesFromBrowser
		form.AddDropDown("YouTube login from your browser", append([]string{"(none)"}, core.Browsers[1:]...), idx, func(_ string, i int) { br = core.Browsers[i] })
		b.apply = append(b.apply, func(c *core.Config) error { c.CookiesFromBrowser = br; return nil })
	case "soulseek":
		use("soulseek")
		sk := d.Soulseek
		b.text("Username", sk.Username, 30, func(c *core.Config, s string) error { c.Soulseek.Username = s; return nil })
		b.secret("Password", sk.Password, func(c *core.Config, s string) { c.Soulseek.Password = s })
		b.check("Share files (your library and downloads)", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
		b.number("Listening port", float64(sk.ListenPort()), 6, func(c *core.Config, v float64) {
			if c.Soulseek.Port = int(v); c.Soulseek.Port == core.SlskDefaultPort {
				c.Soulseek.Port = 0
			}
		})
		b.check("Ask the router to forward the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
		form.AddButton("Import from Nicotine+", func() {
			got, err := core.ImportNicotine(w.draft.Soulseek)
			if err != nil {
				ui.message("Nicotine+", err.Error())
				return
			}
			w.draft.Soulseek = got
			w.draft.SetSource("soulseek", got.Configured())
			w.show()
		})
	case "torrent":
		use("torrent")
		b.check("Built-in indexers (Jackett's public sites)", !d.Indexers.NoBuiltin, func(c *core.Config, v bool) { c.Indexers.NoBuiltin = !v })
		b.text("FlareSolverr address (optional)", d.Indexers.FlareSolverr, 40, func(c *core.Config, s string) error { c.Indexers.FlareSolverr = s; return nil })
		b.text("Jackett / Prowlarr address (optional)", strings.Join(d.Torznab.Feeds, ", "), 50, func(c *core.Config, s string) error {
			c.Torznab.Feeds = core.SplitList(s)
			return nil
		})
		b.secret("Jackett / Prowlarr API key", d.Torznab.Key, func(c *core.Config, s string) { c.Torznab.Key = s })
	case "done":
		var on []string
		for _, s := range w.draft.Sources {
			if s.Enabled {
				on = append(on, core.SourceLabels[s.Name])
			}
		}
		form.AddTextView("Sources on", strings.Join(on, ", "), 0, 2, false, false)
	}
	next := func() {
		trial := w.draft
		if err := b.applyTo(&trial); err != nil {
			ui.message("Setup", err.Error())
			return
		}
		w.draft = trial
		if w.step++; w.step >= len(core.SetupSteps) {
			w.finish()
			return
		}
		w.show()
	}
	if w.step > 0 {
		form.AddButton("Back", func() { w.step--; w.show() })
	}
	switch {
	case st.ID == "done":
		form.AddButton("Save and start", next)
	case st.ID == "welcome":
		form.AddButton("Start", next)
		form.AddButton("Skip the setup", func() {
			w.close()
			ui.m.FinishSetup(func(*core.Config) {})
		})
	default:
		form.AddButton("Next", next)
		skip := "Skip"
		if st.Source {
			skip = "Skip (leave it off)"
		}
		form.AddButton(skip, func() {
			if st.Source {
				names := []string{st.ID}
				if st.ID == "youtube" {
					names = []string{"youtube", "youtube_music", "soundcloud"}
				}
				for _, n := range names {
					w.draft.SetSource(n, false)
				}
			}
			w.step++
			w.show()
		})
	}
	form.SetCancelFunc(func() {}) // Esc doesn't lose the setup
	help := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetText(tview.Escape(st.Help))
	help.SetBorderPadding(0, 1, 1, 1)
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(help, 5, 0, false).
		AddItem(form, 0, 1, true)
	box.SetBorder(true).SetTitle(fmt.Sprintf(" Setup · %d of %d · %s ", w.step+1, len(core.SetupSteps), st.Title))
	ui.openModal("setup", box, 110, min(max(ui.height-2, 18), form.GetFormItemCount()+12))
}

func (w *setupWizard) finish() {
	draft := w.draft
	if err := w.ui.m.FinishSetup(func(c *core.Config) { *c = draft }); err != nil {
		w.ui.message("Setup", err.Error())
		w.step = len(core.SetupSteps) - 1
		w.show()
		return
	}
	w.close()
	w.ui.message("Setup", "Saved. The queue starts with your playlists; the Search tab finds more.")
}
