package tui

import (
	"universal-music-downloader/internal/core"

	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (ui *tui) buildSources() tview.Primitive {
	ui.srcTable = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	ui.srcTable.SetBorder(true).SetTitle(" Sources, tried top to bottom ")
	ui.srcDetail = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	ui.srcDetail.SetBorder(true)
	ui.srcTable.SetSelectionChangedFunc(func(row, col int) { ui.refreshSourceDetail() })
	ui.srcTable.SetSelectedFunc(func(row, col int) { ui.sourceSettings() })
	ui.srcTable.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := ui.srcTable.GetSelection()
		if row < 1 || row-1 >= len(ui.srcs) {
			return ev
		}
		s := ui.srcs[row-1]
		move := 0
		switch {
		case ev.Rune() == ' ':
			ui.update(func(c *core.Config) { c.SetSource(s.Name, !c.Source(s.Name).Enabled) })
		case ev.Rune() == 't':
			ui.testSource(s.Name)
		case ev.Rune() == 'K' || (ev.Key() == tcell.KeyUp && ev.Modifiers()&(tcell.ModShift|tcell.ModAlt) != 0):
			move = -1
		case ev.Rune() == 'J' || (ev.Key() == tcell.KeyDown && ev.Modifiers()&(tcell.ModShift|tcell.ModAlt) != 0):
			move = 1
		case ev.Rune() == 'e':
			ui.pickRoute(s.Name)
		case ev.Rune() == 'p':
			ui.m.PauseSource(s.Name, !s.Paused && s.Until.IsZero())
		case ev.Rune() == 'b':
			ui.pasteBandcampLinks()
		default:
			return ev
		}
		if move != 0 {
			i := row - 1
			j := i + move
			if j >= 0 && j < len(ui.srcs) {
				ui.update(func(c *core.Config) { c.Sources[i], c.Sources[j] = c.Sources[j], c.Sources[i] })
				ui.srcTable.Select(j+1, 0)
			}
		}
		ui.refresh()
		return nil
	})
	return tview.NewFlex().SetDirection(tview.FlexRow).AddItem(ui.srcTable, 12, 0, true).AddItem(ui.srcDetail, 0, 1, false)
}

func (ui *tui) refreshSources() {
	ui.srcs = ui.m.SourceStatus()
	t := ui.srcTable
	t.Clear()
	headerRow(t, "#", "On", "Source", "Route", "Status", "Searched", "Found", "Downloaded", "Failed")
	for i, s := range ui.srcs {
		on, color := "✓", tcell.ColorGreen
		if !s.Enabled {
			on, color = "·", tcell.ColorGray
		}
		status, scolor := "ok", tcell.ColorGreen
		switch {
		case !s.Enabled:
			status, scolor = "off", tcell.ColorGray
		case s.Problem != "":
			status, scolor = s.Problem, tcell.ColorRed
		case s.Paused:
			status, scolor = "paused by you (p)", tcell.ColorYellow
		case !s.Until.IsZero():
			status, scolor = "paused until "+s.Until.Format("15:04")+": "+s.Reason, tcell.ColorOrange
		case s.Note != "":
			status, scolor = "ok, but "+s.Note, tcell.ColorYellow
		}
		route := s.Route
		cells := []*tview.TableCell{
			tview.NewTableCell(fmt.Sprint(i + 1)).SetAlign(tview.AlignRight),
			tview.NewTableCell(on).SetTextColor(color),
			tview.NewTableCell(s.Label),
			tview.NewTableCell(tview.Escape(core.ParseRoute(route).Short())).SetMaxWidth(26),
			tview.NewTableCell(tview.Escape(status)).SetTextColor(scolor).SetExpansion(1).SetMaxWidth(70),
			tview.NewTableCell(fmt.Sprint(s.Searches)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.Found)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.Downloads)).SetAlign(tview.AlignRight),
			tview.NewTableCell(fmt.Sprint(s.DlFails)).SetAlign(tview.AlignRight),
		}
		for c, cell := range cells {
			t.SetCell(i+1, c, cell)
		}
	}
	if r, _ := t.GetSelection(); r < 1 && len(ui.srcs) > 0 {
		t.Select(1, 0)
	}
	ui.refreshSourceDetail()
}

func (ui *tui) refreshSourceDetail() {
	row, _ := ui.srcTable.GetSelection()
	if row < 1 || row-1 >= len(ui.srcs) {
		return
	}
	s := ui.srcs[row-1]
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]%s[::-] · route: %s\n%s\n", s.Label, tview.Escape(core.DescribeRoute(s.Route)), tview.Escape(core.SourceHelp[s.Name]))
	if s.Problem != "" {
		fmt.Fprintf(&b, "\n[red]%s[-]\n", tview.Escape(s.Problem))
	}
	if s.Note != "" {
		fmt.Fprintf(&b, "\n[yellow]%s[-]\n", tview.Escape(s.Note))
	}
	if s.Reason != "" {
		fmt.Fprintf(&b, "\n[orange]Paused until %s: %s[-] (p resumes it now)\n", s.Until.Format("15:04"), tview.Escape(s.Reason))
	}
	if s.Name == "bandcamp" {
		bc := ui.m.Config().Bandcamp
		b.WriteString("\n[::b]Releases that ask for an email address[::-]\n")
		if bc.Email == "" {
			b.WriteString("skipped: enter your email address in the settings (Enter).\n")
		} else {
			pending, problem := ui.m.BandcampMailStatus()
			from := map[string]string{"paste": "you paste them (b)", "gmail": "read from Gmail (" + core.FirstNonEmpty(bc.GmailAccount, "not signed in") + ")",
				"imap": "read from " + bc.IMAPHost}[bc.MailboxMode()]
			fmt.Fprintf(&b, "Bandcamp emails the links to %s; %s.\n", tview.Escape(bc.Email), tview.Escape(from))
			if pending > 0 {
				fmt.Fprintf(&b, "[yellow]%d releases waiting for their link[-] (b pastes links from the emails)\n", pending)
			}
			if problem != "" {
				fmt.Fprintf(&b, "[red]Mailbox: %s[-]\n", tview.Escape(problem))
			}
		}
	}
	if s.Name == "torrent" {
		feeds := ui.m.TorrentFeeds()
		if len(feeds) > 0 {
			b.WriteString("\n[::b]Indexers[::-]\n")
		}
		for _, f := range feeds {
			state := "[green]ok[-]"
			if !f.OK {
				state = "[orange]left out until " + f.Until.Format("15:04") + "[-]"
			}
			fmt.Fprintf(&b, " %s  %s · %d searches, %d errors", state, tview.Escape(f.Name), f.Searches, f.Errors)
			if f.Avg > 0 {
				fmt.Fprintf(&b, ", answers in %.1fs", f.Avg.Seconds())
			}
			if f.LastErr != "" {
				fmt.Fprintf(&b, "\n      [gray]last error: %s[-]", tview.Escape(core.FitWidth(f.LastErr, 140)))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n[gray]Enter: settings · e: route · space: on/off · K/J: move · p: pause/resume · b: paste Bandcamp links[-]")
	ui.srcDetail.SetText(b.String())
	ui.srcDetail.SetTitle(" " + s.Label + " ")
}

// pickRoute edits a source's route: system, direct, or a SOCKS5 or HTTP proxy.
func (ui *tui) pickRoute(name string) {
	form := tview.NewForm()
	b := &formBuilder{f: form}
	b.route(name, ui.m.Config().Source(name).Route)
	close := func() { ui.closeModal("route") }
	form.AddButton("Save", func() { ui.saveForm(b, close) })
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" Route for " + core.SourceLabels[name] + " ")
	ui.openModal("route", form, 96, 15)
}

func (ui *tui) sourceSettings() {
	row, _ := ui.srcTable.GetSelection()
	if row < 1 || row-1 >= len(ui.srcs) {
		return
	}
	name := ui.srcs[row-1].Name
	c := ui.m.Config()
	if name == "bandcamp" {
		ui.bandcampSettings(c.Bandcamp, core.NormalizeRoute(c.Source(name).Route))
		return
	}
	if name == "soulseek" {
		ui.soulseekSettings(c.Soulseek, c.Source(name).Route)
		return
	}
	form := tview.NewForm()
	close := func() { ui.closeModal("srcsettings") }
	var apply []func(*core.Config)
	text := func(label, value string, width int, set func(*core.Config, string)) {
		v := value
		form.AddInputField(label, value, width, nil, func(t string) { v = t })
		apply = append(apply, func(c *core.Config) { set(c, strings.TrimSpace(v)) })
	}
	secret := func(label, value string, set func(*core.Config, string)) {
		v := value
		form.AddPasswordField(label, value, 60, '*', func(t string) { v = t })
		apply = append(apply, func(c *core.Config) { set(c, strings.TrimSpace(v)) })
	}
	getRoute := routeFields(form, c.Source(name).Route)
	apply = append(apply, func(c *core.Config) {
		if r, err := getRoute(); err == nil {
			c.SetRoute(name, r)
		}
	})
	height := 13
	switch name {
	case "youtube", "youtube_music", "soundcloud":
		text("Cookies file (cookies.txt)", c.Cookies, 60, func(c *core.Config, v string) { c.Cookies = v })
		idx := 0
		for i, b := range core.Browsers {
			if b == c.CookiesFromBrowser {
				idx = i
			}
		}
		br := c.CookiesFromBrowser
		labels := append([]string{"(none)"}, core.Browsers[1:]...)
		form.AddDropDown("Cookies from browser", labels, idx, func(o string, i int) { br = core.Browsers[i] })
		apply = append(apply, func(c *core.Config) { c.CookiesFromBrowser = br })
		pace := fmt.Sprint(c.YouTubePace)
		form.AddInputField("Seconds between YouTube requests", pace, 6, nil, func(t string) { pace = t })
		apply = append(apply, func(c *core.Config) {
			if v, err := strconv.ParseFloat(strings.TrimSpace(pace), 64); err == nil && v >= 0 {
				c.YouTubePace = v
			}
		})
		text("yt-dlp program (empty: download it automatically)", c.YtDlpPath, 60, func(c *core.Config, v string) { c.YtDlpPath = v })
		form.AddTextView("", "Your browser's YouTube login (cookies) is the best fix for \"sign in to confirm you're not a bot\". "+
			"They're read from the browser every 10 minutes (YouTube's, Google's and SoundCloud's only) and each yt-dlp run gets a "+
			"temporary copy: your browser and your cookies file are never written to. A browser, when set, is used instead of the file.", 0, 4, false, false)
		height = 24
	case "torrent":
		builtin := !c.Indexers.NoBuiltin
		form.AddCheckbox("Built-in indexers", builtin, func(b bool) { builtin = b })
		apply = append(apply, func(c *core.Config) { c.Indexers.NoBuiltin = !builtin })
		text("FlareSolverr address (for Cloudflare's checks)", c.Indexers.FlareSolverr, 40, func(c *core.Config, v string) { c.Indexers.FlareSolverr = v })
		text("Jackett / Prowlarr address(es), optional", strings.Join(c.Torznab.Feeds, ", "), 60, func(c *core.Config, v string) { c.Torznab.Feeds = core.SplitList(v) })
		secret("Jackett / Prowlarr API key", c.Torznab.Key, func(c *core.Config, v string) { c.Torznab.Key = v })
		form.AddButton("Choose indexers…", func() { ui.pickIndexers() })
		dur := func(label string, d core.Duration, set func(*core.Config, core.Duration)) {
			v := time.Duration(d).String()
			form.AddInputField(label, v, 10, nil, func(t string) { v = t })
			apply = append(apply, func(c *core.Config) {
				if x, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && x > 0 {
					set(c, core.Duration(x))
				}
			})
		}
		dur("Give up on an indexer search after", c.IndexerTimeout, func(c *core.Config, d core.Duration) { c.IndexerTimeout = d })
		dur("Give up on a torrent download after", c.TorrentTimeout, func(c *core.Config, d core.Duration) { c.TorrentTimeout = d })
		height = 25
	case "vk":
		secret("Token", c.VKToken, func(c *core.Config, v string) { c.VKToken = v })
		form.AddButton("Log in to get a token…", func() { close(); ui.vkLogin() })
		height = 15
	case "yandex":
		secret("OAuth token", c.YandexToken, func(c *core.Config, v string) { c.YandexToken = v })
		form.AddTextView("How to get it", "Open "+core.YandexTokenURL+" in your browser, log in, then copy the access_token=… value from the address bar.", 0, 4, false, false)
		height = 18
	}
	form.AddButton("Save", func() {
		if _, err := getRoute(); err != nil {
			ui.message("Can't use that route", err.Error())
			return
		}
		if ui.update(func(c *core.Config) {
			for _, f := range apply {
				f(c)
			}
		}) {
			close()
		}
	})
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" " + core.SourceLabels[name] + " settings ")
	ui.openModal("srcsettings", form, 110, height)
}

var (
	mailboxModes  = []string{"paste", "gmail", "imap"}
	mailboxLabels = []string{"I paste them from the emails (no mailbox access)", "Gmail: sign in with Google (read only)", "IMAP mailbox (Yandex, Mail.ru, …)"}
)

// bandcampSettings edits Bandcamp's settings; d holds what's typed so far, so the dialog can be
// rebuilt with the fields of another mailbox choice.
func (ui *tui) bandcampSettings(d core.BandcampCfg, route string) {
	const name = "srcsettings"
	form := tview.NewForm().SetItemPadding(0)
	close := func() { ui.closeModal(name) }
	field := func(label string, p *string, width int) {
		form.AddInputField(label, *p, width, nil, func(t string) { *p = t })
	}
	secret := func(label string, p *string) {
		form.AddPasswordField(label, *p, 50, '*', func(t string) { *p = t })
	}
	intro := "[::b]Name-your-price releases that ask for an email address[::-]: Bandcamp emails you the download link.\n\n"
	getRoute := routeFields(form, route)
	curRoute := func() string { r, _ := getRoute(); return r }
	field("Your email address", &d.Email, 40)
	field("Country", &d.Country, 30)
	field("Postcode", &d.Postcode, 12)
	mode := d.MailboxMode()
	cur := 0
	for i, m := range mailboxModes {
		if m == mode {
			cur = i
		}
	}
	form.AddDropDown("Download links from", mailboxLabels, cur, func(_ string, i int) {
		if i != cur {
			d.Mailbox = mailboxModes[i]
			r := curRoute()
			go ui.app.QueueUpdateDraw(func() { ui.bandcampSettings(d, r) }) // that choice's fields
		}
	})
	introLines := 4
	switch mode {
	case "paste":
		intro += "Copy the link from Bandcamp's email and paste it with [::b]b[::-] in the Sources tab (or Paste links below). " +
			"Tracks wait for it instead of coming from a worse source."
	case "gmail":
		field("OAuth client ID", &d.GmailClientID, 66)
		secret("OAuth client secret", &d.GmailClientSecret)
		state := "[yellow]not signed in[-]"
		if d.GmailToken != "" {
			state = "[green]signed in as " + tview.Escape(core.FirstNonEmpty(d.GmailAccount, "?")) + "[-]"
		}
		intro += "Gmail: " + state + ". No app password needed: you sign in on Google's page in your browser and allow " +
			"read-only access. Google wants your own (free) OAuth client for that: README → \"Gmail\" shows how to make one."
		introLines = 5
		form.AddButton("Sign in with Google…", func() { ui.gmailSignIn(d, curRoute()) })
	case "imap":
		field("IMAP server (host:port)", &d.IMAPHost, 40)
		field("IMAP user", &d.IMAPUser, 40)
		secret("IMAP password", &d.IMAPPass)
		intro += "Yandex: imap.yandex.ru:993 with an app password (id.yandex.ru → Security → App passwords) and IMAP on in " +
			"Mail settings → Email clients. Mail.ru: imap.mail.ru:993 with an app password. Only read: nothing is marked, moved or deleted."
		introLines = 5
	}
	form.AddButton("Save", func() {
		d.Mailbox = mode
		r, err := getRoute()
		if err != nil {
			ui.message("Can't use that route", err.Error())
			return
		}
		if ui.update(func(c *core.Config) {
			c.Bandcamp = d
			c.SetRoute("bandcamp", r)
		}) {
			close()
		}
	})
	form.AddButton("Paste links…", func() { ui.pasteBandcampLinks() })
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	help := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetText(intro)
	help.SetBorderPadding(0, 0, 1, 1)
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(help, introLines, 0, false).
		AddItem(form, 0, 1, true)
	box.SetBorder(true).SetTitle(" Bandcamp settings ")
	ui.openModal(name, box, 110, introLines+form.GetFormItemCount()+6)
}

// gmailSignIn opens Google's sign-in page in your browser and waits for it.
func (ui *tui) gmailSignIn(d core.BandcampCfg, route string) {
	const name = "gsignin"
	view := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	view.SetText("Starting…")
	ctx, cancel := context.WithCancel(ui.m.Context())
	form := tview.NewForm()
	form.AddFormItem(view)
	form.AddButton("Cancel", func() { cancel(); ui.closeModal(name) })
	form.SetCancelFunc(func() { cancel(); ui.closeModal(name) })
	form.SetBorder(true).SetTitle(" Sign in with Google ")
	ui.openModal(name, form, 110, 16)
	clientID, secret := strings.TrimSpace(d.GmailClientID), strings.TrimSpace(d.GmailClientSecret)
	go func() {
		defer cancel()
		refresh, account, err := core.GmailSignIn(ctx, clientID, secret, func(u string) {
			core.Logf("Gmail sign-in page: %s", u)
			opened := "Your browser should open Google's sign-in page."
			if err := core.OpenBrowser(u); err != nil {
				opened = "Couldn't open your browser (" + err.Error() + ")."
			}
			ui.app.QueueUpdateDraw(func() {
				view.SetText(tview.Escape(opened) + " Sign in, allow read-only access, and come back here.\n\n" +
					"If it didn't open, copy this address into your browser (it's also in the Log tab; Shift+drag selects text in most terminals):\n\n[::u]" +
					tview.Escape(u) + "[::-]")
			})
		})
		ui.app.QueueUpdateDraw(func() {
			if !ui.hasModal(name) {
				return // cancelled
			}
			ui.closeModal(name)
			if err != nil {
				ui.message("Gmail sign-in didn't work", err.Error())
				return
			}
			d.GmailClientID, d.GmailClientSecret, d.GmailToken, d.GmailAccount, d.Mailbox = clientID, secret, refresh, account, "gmail"
			if d.Email == "" {
				d.Email = account
			}
			if ui.update(func(c *core.Config) {
				b := &c.Bandcamp
				b.GmailClientID, b.GmailClientSecret, b.GmailToken, b.GmailAccount, b.Mailbox = clientID, secret, refresh, account, "gmail"
				if b.Email == "" {
					b.Email = account
				}
			}) {
				ui.bandcampSettings(d, route)
				ui.message("Gmail", "Signed in as "+account+". Bandcamp's emails are read from this mailbox (read only).")
			}
		})
	}()
}

// pasteBandcampLinks takes download links copied from Bandcamp's emails.
func (ui *tui) pasteBandcampLinks() {
	const name = "bcpaste"
	form := tview.NewForm()
	pending, _ := ui.m.BandcampMailStatus()
	intro := "Paste the download links from Bandcamp's emails (or the whole emails; several at once is fine), then Tab to the button."
	if pending > 0 {
		intro += fmt.Sprintf(" %d releases are waiting for theirs.", pending)
	}
	form.AddTextArea("", "", 0, 9, 0, nil)
	area := form.GetFormItem(0).(*tview.TextArea)
	status := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	status.SetBorderPadding(0, 0, 1, 1)
	busy := false
	form.AddButton("Use these links", func() {
		text := area.GetText()
		if busy || strings.TrimSpace(text) == "" {
			return
		}
		if len(core.FindBandcampLinks(text)) == 0 {
			status.SetText("[red]No Bandcamp download links in that (they look like https://bandcamp.com/download?…).[-]")
			return
		}
		busy = true
		status.SetText("[yellow]Opening the links…[-]")
		go func() {
			found, added, errs := ui.m.AddBandcampLinks(text)
			ui.app.QueueUpdateDraw(func() {
				busy = false
				msg := fmt.Sprintf("%d links, %d new: the tracks waiting for them go on.", found, added)
				if len(errs) > 0 {
					status.SetText("[orange]" + tview.Escape(msg+" Not usable: "+strings.Join(errs, "; ")) + "[-]")
					return
				}
				ui.closeModal(name)
				ui.message("Bandcamp links", msg)
			})
		}()
	})
	form.AddButton("Cancel", func() { ui.closeModal(name) })
	form.SetCancelFunc(func() { ui.closeModal(name) })
	help := tview.NewTextView().SetWrap(true).SetText(intro)
	help.SetBorderPadding(0, 0, 1, 1)
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(help, 2, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(status, 2, 0, false)
	box.SetBorder(true).SetTitle(" Paste Bandcamp download links ")
	ui.openModal(name, box, 100, 19)
}

func (ui *tui) vkLogin() {
	form := tview.NewForm()
	var user, pass, code string
	form.AddInputField("Phone or email", "", 40, nil, func(t string) { user = t })
	form.AddPasswordField("Password (not stored)", "", 40, '*', func(t string) { pass = t })
	form.AddInputField("Confirmation code (if asked)", "", 10, nil, func(t string) { code = t })
	status := tview.NewTextView().SetDynamicColors(true)
	form.AddFormItem(status)
	close := func() { ui.closeModal("vklogin") }
	form.AddButton("Get token", func() {
		status.SetText("[yellow]Logging in…[-]")
		route := ui.m.Config().Source("vk").Route
		go func() {
			ctx := ui.m.Context()
			token, needCode, err := core.VkLogin(ctx, strings.TrimSpace(user), pass, strings.TrimSpace(code), route)
			ui.app.QueueUpdateDraw(func() {
				switch {
				case err != nil:
					status.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
				case needCode:
					status.SetText("[yellow]VK sent a confirmation code (SMS or app): enter it and press Get token again.[-]")
				default:
					close()
					if ui.update(func(c *core.Config) { c.VKToken = token; c.SetSource("vk", true) }) {
						ui.message("VK", "Logged in; the VK source is on.")
					}
				}
			})
		}()
	})
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" VK login (Kate Mobile token) ")
	ui.openModal("vklogin", form, 90, 13)
}

// --- settings ---------------------------------------------------------------------------------

// soulseekSettings edits the Soulseek source: route, account, limits, sharing and chat. sk
// and route are what the window starts with (the Nicotine+ import fills it in again).
func (ui *tui) soulseekSettings(sk core.SoulseekConfig, route string) {
	const name = "srcsettings"
	form := tview.NewForm().SetItemPadding(0)
	b := &formBuilder{f: form}
	close := func() { ui.closeModal(name) }
	b.route("soulseek", route)
	head := func(text string) { form.AddTextView("", "[::b]"+text+"[::-]", 0, 1, true, false) }
	head("Account")
	b.text("Username", sk.Username, 30, func(c *core.Config, s string) error { c.Soulseek.Username = s; return nil })
	b.secret("Password", sk.Password, func(c *core.Config, s string) { c.Soulseek.Password = s })
	b.text("Server", sk.ServerAddr(), 30, func(c *core.Config, s string) error {
		if s == core.SlskDefaultServer {
			s = ""
		}
		c.Soulseek.Server = s
		return nil
	})
	b.number("Listening port", float64(sk.ListenPort()), 6, func(c *core.Config, v float64) {
		c.Soulseek.Port = int(v)
		if c.Soulseek.Port == core.SlskDefaultPort {
			c.Soulseek.Port = 0
		}
	})
	b.check("Ask the router to forward the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
	head("Downloading (the network asks for restraint)")
	b.number("Downloads at once (1-4)", float64(sk.MaxDownloads()), 3, func(c *core.Config, v float64) { c.Soulseek.Downloads = int(v) })
	b.number("Seconds between searches (8 or more)", sk.SearchInterval().Seconds(), 5, func(c *core.Config, v float64) { c.Soulseek.SearchGap = v })
	b.number("Most searches an hour (20 to 200; the server bans floods)", float64(sk.SearchesPerHour()), 5, func(c *core.Config, v float64) { c.Soulseek.PerHour = int(v) })
	b.text("Give up waiting in a user's queue after", fmtDurationShort(sk.QueueTimeout()), 8, func(c *core.Config, s string) error {
		d, err := time.ParseDuration(s)
		if err != nil || d < time.Minute {
			return fmt.Errorf("give up waiting in a user's queue after: a duration like 5m (at least 1m)")
		}
		c.Soulseek.QueueWait = core.Duration(d)
		return nil
	})
	head("Sharing")
	b.check("Share files", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
	shares := b.text("Shared folders (; between them)", strings.Join(sk.Shares, "; "), 70, func(c *core.Config, s string) error {
		c.Soulseek.Shares = core.SplitBy(s, ";")
		return nil
	})
	var defaults []string
	for _, r := range ui.m.Config().ShareRoots() {
		defaults = append(defaults, r.Path)
	}
	shares.SetPlaceholder("empty: " + strings.Join(defaults, "; "))
	b.number("Upload slots", float64(sk.Slots()), 3, func(c *core.Config, v float64) { c.Soulseek.UploadSlots = int(v) })
	b.number("Upload speed limit, KB/s (0: none)", float64(sk.UploadKBps), 7, func(c *core.Config, v float64) { c.Soulseek.UploadKBps = int(v) })
	b.number("Queue limit per user, files", float64(sk.FilesPerUser()), 6, func(c *core.Config, v float64) { c.Soulseek.QueueFiles = int(v) })
	b.number("Queue limit per user, MB", float64(sk.MBPerUser()), 7, func(c *core.Config, v float64) { c.Soulseek.QueueMB = int(v) })
	b.text("Banned users (comma-separated)", strings.Join(sk.Banned, ", "), 60, func(c *core.Config, s string) error {
		c.Soulseek.Banned = core.SplitList(s)
		return nil
	})
	head("Chat")
	b.text("Rooms to join (comma-separated)", strings.Join(sk.Rooms, ", "), 60, func(c *core.Config, s string) error {
		c.Soulseek.Rooms = core.SplitList(s)
		return nil
	})
	b.text("Description others see", sk.Description, 60, func(c *core.Config, s string) error { c.Soulseek.Description = s; return nil })
	form.AddButton("Save", func() { ui.saveForm(b, close) })
	form.AddButton("Import from Nicotine+", func() {
		got, err := core.ImportNicotine(sk)
		if err != nil {
			ui.message("Nicotine+", err.Error())
			return
		}
		close()
		ui.soulseekSettings(got, route)
		ui.message("Imported from Nicotine+", fmt.Sprintf("Account %s, port %d, %d shared folders, %d upload slots. Check and Save.\n\n"+
			"Don't run Nicotine+ with the same account at the same time: each logs the other out.", got.Username, got.ListenPort(), len(got.Shares), got.Slots()))
	})
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" Soulseek settings (Tab moves · Esc closes) ")
	ui.openModal(name, form, 110, min(max(ui.height-4, 20), form.GetFormItemCount()+5))
}

// testSource checks that a source answers and says how it went.
func (ui *tui) testSource(name string) {
	ui.srcTable.SetTitle(" Testing… ")
	go func() {
		r := ui.m.CheckSource(ui.m.Context(), name)
		ui.app.QueueUpdateDraw(func() {
			ui.srcTable.SetTitle(" Sources, tried top to bottom ")
			word := "Not working"
			if r.OK {
				word = "OK"
			}
			ui.message("Test: "+r.Name, word+": "+r.Detail)
		})
	}()
}

// pickIndexers lists the built-in indexers: Enter or space turns one on or off.
func (ui *tui) pickIndexers() {
	const name = "indexers"
	t := tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	info := tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	musicOnly := true
	find := ""
	var rows []core.IndexerStatus
	tested := map[string]core.ConnectionCheck{}
	fill := func() {
		row, _ := t.GetSelection()
		t.Clear()
		headerRow(t, "On", "Indexer", "Language", "Music", "Searches", "Test, or last problem")
		rows = rows[:0]
		for _, x := range ui.m.Indexers() {
			if musicOnly && !x.Music && !x.On {
				continue
			}
			if find != "" && !strings.Contains(strings.ToLower(x.Name+" "+x.ID+" "+x.Site), strings.ToLower(find)) {
				continue
			}
			rows = append(rows, x)
		}
		for i, x := range rows {
			on, color := "·", tcell.ColorGray
			if x.On {
				on, color = "✓", tcell.ColorGreen
			}
			music := ""
			if x.Music {
				music = "♪"
			}
			searches, problem := "", ""
			if x.Health != nil {
				searches = fmt.Sprintf("%d (%d errors)", x.Health.Searches, x.Health.Errors)
				problem = x.Health.LastErr
			}
			problemColor := tcell.ColorOrange
			if r, ok := tested[x.ID]; ok {
				problem, problemColor = r.Detail, tcell.ColorRed
				if r.OK {
					problemColor = tcell.ColorGreen
				}
			}
			name := x.Name
			if x.Default {
				name += " ★"
			}
			if x.Login {
				if len(ui.m.Config().Indexers.Settings[x.ID]) > 0 {
					name += " 🔑"
				} else {
					name += " (account: Enter)"
				}
			}
			cells := []*tview.TableCell{
				tview.NewTableCell(on).SetTextColor(color),
				tview.NewTableCell(tview.Escape(name)).SetExpansion(1).SetMaxWidth(40),
				tview.NewTableCell(x.Language),
				tview.NewTableCell(music).SetTextColor(tcell.ColorAqua),
				tview.NewTableCell(searches).SetAlign(tview.AlignRight),
				tview.NewTableCell(tview.Escape(core.FitWidth(problem, 50))).SetTextColor(problemColor),
			}
			for c, cell := range cells {
				t.SetCell(i+1, c, cell)
			}
		}
		if len(rows) == 0 && find != "" {
			t.SetCell(1, 1, tview.NewTableCell("[yellow]None match \""+tview.Escape(find)+"\" (/ to change, a to show all, not just music)[-]").SetSelectable(false))
		} else if len(rows) == 0 {
			t.SetCell(1, 1, tview.NewTableCell("[yellow]No definitions yet: u fetches them from GitHub (Jackett's)[-]").SetSelectable(false))
		}
		t.Select(min(max(row, 1), max(1, len(rows))), 0)
	}
	show := func() {
		row, _ := t.GetSelection()
		if row >= 1 && row-1 < len(rows) {
			x := rows[row-1]
			text := fmt.Sprintf("[::b]%s[::-] · %s\n%s", tview.Escape(x.Name), tview.Escape(x.Site), tview.Escape(x.Description))
			if x.Login {
				text += "\n[yellow]Needs your account on the site: Enter sets it.[-]"
			}
			info.SetText(text)
		}
	}
	t.SetSelectionChangedFunc(func(int, int) { show() })
	account := func(x core.IndexerStatus) {
		ui.indexerAccount(x, func() { fill() })
	}
	toggle := func(enter bool) {
		row, _ := t.GetSelection()
		if row >= 1 && row-1 < len(rows) {
			x := rows[row-1]
			if x.Login && (enter || !x.On && len(ui.m.Config().Indexers.Settings[x.ID]) == 0) {
				account(x)
				return
			}
			if err := ui.m.SetIndexer(x.ID, !x.On); err != nil {
				ui.message("Indexers", err.Error())
			}
			fill()
		}
	}
	t.SetSelectedFunc(func(int, int) { toggle(true) })
	test := func(sites []core.IndexerStatus) {
		sem := make(chan struct{}, 6)
		for _, x := range sites {
			tested[x.ID] = core.ConnectionCheck{OK: true, Detail: "testing…"}
			go func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				r := ui.m.CheckIndexer(ui.m.Context(), x.ID)
				ui.app.QueueUpdateDraw(func() {
					tested[x.ID] = r
					fill()
				})
			}()
		}
		fill()
	}
	t.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
			ui.closeModal(name)
		case ev.Rune() == 't':
			if row, _ := t.GetSelection(); row >= 1 && row-1 < len(rows) {
				test([]core.IndexerStatus{rows[row-1]})
			}
		case ev.Rune() == 'T':
			var on []core.IndexerStatus
			for _, x := range ui.m.Indexers() {
				if x.On {
					on = append(on, x)
				}
			}
			test(on)
		case ev.Rune() == ' ':
			toggle(false)
		case ev.Rune() == 'a':
			musicOnly = !musicOnly
			fill()
		case ev.Rune() == '/':
			ui.askText("Find an indexer", "Name or address (empty: all)", find, func(v string) {
				find = strings.TrimSpace(v)
				fill()
			})
		case ev.Rune() == 'u':
			info.SetText("[yellow]Fetching Jackett's definitions from GitHub…[-]")
			go func() {
				ctx, cancel := context.WithTimeout(ui.m.Context(), 3*time.Minute)
				defer cancel()
				n, err := ui.m.RefreshIndexers(ctx)
				ui.app.QueueUpdateDraw(func() {
					if err != nil {
						info.SetText("[red]" + tview.Escape(err.Error()) + "[-]")
					} else {
						info.SetText(fmt.Sprintf("[green]%d definitions new or changed.[-]", n))
					}
					fill()
				})
			}()
		default:
			return ev
		}
		return nil
	})
	fill()
	show()
	box := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(t, 0, 1, true).AddItem(info, 3, 0, false)
	box.SetBorder(true).SetTitle(" Built-in indexers (space: on/off · Enter: on/off, or the account · / find · ★ default · ♪ music · a: all/music · t: test · T: test all on · u: update · Esc) ")
	ui.openModal(name, box, 120, min(max(ui.height-4, 16), 40))
}

// indexerAccount asks for an indexer's settings (a login), saves them and turns it on.
func (ui *tui) indexerAccount(x core.IndexerStatus, done func()) {
	const name = "indexer-account"
	have := ui.m.Config().Indexers.Settings[x.ID]
	form := tview.NewForm().SetItemPadding(0)
	values := map[string]string{}
	for _, s := range x.Settings {
		key := s.Name
		values[key] = have[key]
		if s.Secret {
			form.AddPasswordField(s.Label, have[key], 30, '*', func(v string) { values[key] = v })
		} else {
			form.AddInputField(s.Label, have[key], 30, nil, func(v string) { values[key] = v })
		}
	}
	form.AddTextView("", "Kept in the settings file, readable only by you. The app logs in once and keeps the session; "+
		"a failed login isn't tried again for 30 minutes (sites lock accounts or ask for captchas after many).", 0, 3, false, false)
	close := func() { ui.closeModal(name); done() }
	form.AddButton("Save and turn on", func() {
		if err := ui.m.SetIndexerSettings(x.ID, values); err != nil {
			ui.message(x.Name, err.Error())
			return
		}
		close()
	})
	if len(have) > 0 {
		form.AddButton("Forget the account", func() {
			if err := ui.m.SetIndexerSettings(x.ID, nil); err != nil {
				ui.message(x.Name, err.Error())
				return
			}
			close()
		})
	}
	form.AddButton("Cancel", close)
	form.SetCancelFunc(close)
	form.SetBorder(true).SetTitle(" " + x.Name + ": your account (" + x.Site + ") ")
	ui.openModal(name, form, 90, form.GetFormItemCount()+8)
}
