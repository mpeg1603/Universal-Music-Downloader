package gui

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Sources page: the services music comes from, in the order they're tried, each with a
// switch, what it's doing, and what it takes to set up.
type sourcesTab struct {
	a     *app
	w     *qt.QWidget
	cards []*sourceCard
	box   *qt.QVBoxLayout
	order string // the names in order the cards were built for
	paste *qt.QPushButton
	rows  []core.SourceStatus
}

type sourceCard struct {
	name                  string
	w                     *qt.QFrame
	toggle                *qt.QCheckBox
	title, state, quality *qt.QLabel
	blurb, problem, stats *qt.QLabel
	detail                *qt.QLabel
	detailBtn             *qt.QPushButton
	up, down              *qt.QPushButton
	pause                 *qt.QPushButton
	open                  bool
}

func newSourcesTab(a *app) *sourcesTab {
	t := &sourcesTab{a: a}
	p := a.newPage("Sources", "Where your music comes from. For each song the app tries these from top to bottom and takes the first good copy.", "sources")
	t.w = p.w
	t.paste = a.iconButton("link", "Paste Bandcamp links", "", "Some releases are waiting for the links Bandcamp emailed you", t.pasteBandcampLinks)
	t.paste.Hide()
	p.actions.AddWidget(t.paste.QWidget)
	scroll, box, _ := scrolling()
	box.SetSpacing(8)
	t.box = box
	p.box.AddWidget2(scroll.QWidget, 1)
	box.AddStretch()
	t.refresh()
	return t
}

func (t *sourcesTab) buildCard(name string) *sourceCard {
	a := t.a
	c := &sourceCard{name: name}
	info := sourceInfos[name]
	f, box := card()
	c.w = f
	box.SetContentsMargins(14, 11, 12, 11)
	box.SetSpacing(3)

	top := hbox()
	top.SetSpacing(10)
	c.toggle = newToggle("Use this source", func(on bool) { t.try(a.m.SetSourceEnabled(name, on)) })
	top.AddWidget(c.toggle.QWidget)
	c.title = qt.NewQLabel3(core.SourceLabels[name])
	setProp(c.title.QWidget, "role", "h2")
	top.AddWidget(c.title.QWidget)
	c.state = pill("", "off")
	top.AddWidget(c.state.QWidget)
	if info.quality != "" {
		c.quality = pill(info.quality, "accent")
		top.AddWidget(c.quality.QWidget)
	}
	top.AddStretch()
	c.up = a.iconButton("arrow-up", "", "ghost", "Try this source earlier", func() { t.move(name, -1) })
	c.down = a.iconButton("arrow-down", "", "ghost", "Try this source later", func() { t.move(name, 1) })
	c.pause = a.iconButton("pause", "", "ghost", "Stop using it for now, or use it again right away after a pause", func() {
		for _, s := range t.rows {
			if s.Name == name {
				a.m.PauseSource(name, !s.Paused && s.Until.IsZero())
			}
		}
		t.refresh()
	})
	top.AddWidget(c.up.QWidget)
	top.AddWidget(c.down.QWidget)
	top.AddWidget(c.pause.QWidget)
	setup := button("Set up…", "", func() { t.settings(name) })
	top.AddWidget(setup.QWidget)
	box.AddLayout(top.QLayout)

	indent := func(l *qt.QLabel) {
		l.SetContentsMargins(48, 0, 0, 0)
		box.AddWidget(l.QWidget)
	}
	c.blurb = text(info.blurb+" "+info.needs+".", "hint")
	indent(c.blurb)
	c.problem = text("", "error")
	c.problem.SetTextFormat(qt.PlainText)
	c.problem.Hide()
	indent(c.problem)
	statRow := hbox(48, 0, 0, 0)
	statRow.SetSpacing(8)
	c.stats = oneLine("", "muted")
	statRow.AddWidget2(c.stats.QWidget, 1)
	c.detailBtn = button("More", "link", func() {
		c.open = !c.open
		t.refresh()
	})
	statRow.AddWidget(c.detailBtn.QWidget)
	box.AddLayout(statRow.QLayout)
	c.detail = richText("", "hint")
	c.detail.Hide()
	indent(c.detail)
	return c
}

func (t *sourcesTab) try(err error) {
	if err != nil {
		t.a.message("That didn't work", err.Error())
	}
	t.refresh()
}

func (t *sourcesTab) move(name string, delta int) { t.try(t.a.m.MoveSource(name, delta)) }

// sourceState is a source's state, short, and in full if there's more to say.
func sourceState(s core.SourceStatus) (state, kind, more string) {
	switch {
	case !s.Enabled:
		return "Off", "off", ""
	case s.Problem != "":
		return "Needs attention", "bad", s.Problem
	case s.Paused:
		return "Paused by you", "warn", ""
	case !s.Until.IsZero():
		return "Resting until " + s.Until.Format("15:04"), "warn", s.Reason
	case s.Note != "":
		return "Working", "ok", s.Note
	}
	return "Working", "ok", ""
}

// refresh redraws the sources; the cards are rebuilt only when the order changed.
func (t *sourcesTab) refresh() {
	t.rows = t.a.m.SourceStatus()
	pending, _ := t.a.m.BandcampMailStatus()
	t.paste.SetVisible(pending > 0)
	var names []string
	for _, s := range t.rows {
		names = append(names, s.Name)
	}
	if order := strings.Join(names, ","); order != t.order {
		t.order = order
		byName := map[string]*sourceCard{}
		for _, c := range t.cards {
			byName[c.name] = c
			t.box.RemoveWidget(c.w.QWidget)
		}
		t.cards = t.cards[:0]
		for i, n := range names {
			c := byName[n]
			if c == nil {
				c = t.buildCard(n)
			}
			t.cards = append(t.cards, c)
			t.box.InsertWidget(i, c.w.QWidget)
		}
	}
	for i, s := range t.rows {
		c := t.cards[i]
		setToggle(c.toggle, s.Enabled)
		c.toggle.Update()
		state, kind, more := sourceState(s)
		setPill(c.state, state, kind)
		c.problem.SetVisible(more != "")
		if more != "" {
			c.problem.SetText(more)
		}
		c.up.SetEnabled(i > 0)
		c.down.SetEnabled(i < len(t.rows)-1)
		c.pause.SetVisible(s.Enabled)
		if s.Paused || !s.Until.IsZero() {
			c.pause.SetIcon(iconOf("play", 16, th.Text2))
		} else {
			c.pause.SetIcon(iconOf("pause", 16, th.Text2))
		}
		bits := []string{fmt.Sprintf("Position %d", i+1), routeWords[core.ParseRoute(s.Route).Kind]}
		if s.Searches > 0 {
			bits = append(bits, fmt.Sprintf("searched %d, found %d, downloaded %d", s.Searches, s.Found, s.Downloads))
			if s.DlFails > 0 {
				bits = append(bits, fmt.Sprintf("%d failed", s.DlFails))
			}
		}
		c.stats.SetText(strings.Join(bits, "  ·  "))
		extra := t.detailHTML(s)
		c.detailBtn.SetVisible(extra != "")
		if c.open && extra != "" {
			c.detail.SetText(extra)
			c.detail.Show()
			c.detailBtn.SetText("Less")
		} else {
			c.detail.Hide()
			c.detailBtn.SetText("More")
		}
	}
}

// detailHTML is what there's to say beyond the card itself: Bandcamp's mailbox, torrent sites.
func (t *sourcesTab) detailHTML(s core.SourceStatus) string {
	e := html.EscapeString
	span := func(color, s string) string { return "<span style='color:" + color + "'>" + s + "</span>" }
	var b strings.Builder
	switch s.Name {
	case "bandcamp":
		bc := t.a.m.Config().Bandcamp
		b.WriteString("<b>Releases that ask for an email address</b><br>")
		if bc.Email == "" {
			b.WriteString("Skipped until you enter your email address in Set up.")
		} else {
			pending, problem := t.a.m.BandcampMailStatus()
			from := map[string]string{"paste": "you paste them here (Paste Bandcamp links)", "gmail": "read from Gmail (" + core.FirstNonEmpty(bc.GmailAccount, "not signed in") + ")",
				"imap": "read from " + bc.IMAPHost}[bc.MailboxMode()]
			fmt.Fprintf(&b, "Bandcamp emails the links to %s; %s.", e(bc.Email), e(from))
			if pending > 0 {
				b.WriteString("<br>" + span(th.Warn, fmt.Sprintf("%d releases are waiting for their link", pending)))
			}
			if problem != "" {
				b.WriteString("<br>" + span(th.Bad, "Mailbox: "+e(problem)))
			}
		}
	case "torrent":
		feeds := t.a.m.TorrentFeeds()
		if len(feeds) == 0 {
			return ""
		}
		b.WriteString("<b>Torrent sites</b><br>")
		for _, f := range feeds {
			state := span(th.OK, "ok")
			if !f.OK {
				state = span(th.Warn, "left out until "+f.Until.Format("15:04"))
			}
			fmt.Fprintf(&b, "%s &nbsp;%s: %d searches, %d errors", state, e(f.Name), f.Searches, f.Errors)
			if f.Avg > 0 {
				fmt.Fprintf(&b, ", answers in %.1fs", f.Avg.Seconds())
			}
			if f.LastErr != "" {
				b.WriteString("<br>&nbsp;&nbsp;&nbsp;&nbsp;" + span(th.Text3, "last error: "+e(core.FitWidth(f.LastErr, 160))))
			}
			b.WriteString("<br>")
		}
	}
	return b.String()
}

// --- setting up a source ------------------------------------------------------------------------

func (t *sourcesTab) settings(name string) {
	c := t.a.m.Config()
	label := core.SourceLabels[name]
	switch name {
	case "bandcamp":
		t.bandcampSettings()
		t.refresh()
		return
	case "soulseek":
		t.soulseekSettings(c.Soulseek)
		t.refresh()
		return
	}
	f := newForm(t.a, label, sourceInfos[name].blurb, 700)
	switch name {
	case "youtube", "youtube_music", "soundcloud":
		idx := 0
		for i, b := range core.Browsers {
			if b == c.CookiesFromBrowser {
				idx = i
			}
		}
		f.combo("Use my login from", append([]string{"Nowhere (no login)"}, browserNames()...), idx, func(c *core.Config, i int) {
			if i >= 0 && i < len(core.Browsers) {
				c.CookiesFromBrowser = core.Browsers[i]
			}
		})
		f.note("If YouTube says “sign in to confirm you're not a bot”, pick the browser you're signed in to YouTube with. " +
			"Your browser and cookies are only read, and only YouTube's, Google's and SoundCloud's.")
		f.collapsed("Advanced", c.Cookies != "" || c.YtDlpPath != "")
		f.text("Cookies file (cookies.txt)", c.Cookies, func(c *core.Config, v string) error { c.Cookies = v; return nil })
		f.number("Seconds between YouTube requests", c.YouTubePace, func(c *core.Config, v float64) { c.YouTubePace = max(0, v) })
		f.text("yt-dlp program (empty: fetch it automatically)", c.YtDlpPath, func(c *core.Config, v string) error { c.YtDlpPath = v; return nil })
		f.route(name, c.Source(name).Route)
	case "torrent":
		f.check("Search the built-in torrent sites", !c.Indexers.NoBuiltin, func(c *core.Config, v bool) { c.Indexers.NoBuiltin = !v })
		f.button("Choose torrent sites…", func() { t.a.pickIndexers() })
		f.head("Optional helper programs")
		f.note("The app searches the sites on its own. <b>FlareSolverr</b> gets past Cloudflare's “checking your browser” pages. " +
			"<b>Jackett</b> and <b>Prowlarr</b> add private trackers you have accounts on.")
		f.text("FlareSolverr address", c.Indexers.FlareSolverr, func(c *core.Config, v string) error { c.Indexers.FlareSolverr = v; return nil })
		f.text("Jackett / Prowlarr address", strings.Join(c.Torznab.Feeds, ", "), func(c *core.Config, v string) error {
			c.Torznab.Feeds = core.SplitList(v)
			return nil
		})
		f.secret("Jackett / Prowlarr API key", c.Torznab.Key, func(c *core.Config, v string) { c.Torznab.Key = v })
		f.collapsed("Advanced", false)
		dur := func(label string, d core.Duration, set func(*core.Config, core.Duration)) {
			f.text(label, time.Duration(d).String(), func(c *core.Config, v string) error {
				x, err := time.ParseDuration(v)
				if err != nil || x <= 0 {
					return fmt.Errorf("%s: a duration like 20s or 30m", label)
				}
				set(c, core.Duration(x))
				return nil
			})
		}
		dur("Give up on a site's search after", c.IndexerTimeout, func(c *core.Config, d core.Duration) { c.IndexerTimeout = d })
		dur("Give up on a torrent after", c.TorrentTimeout, func(c *core.Config, d core.Duration) { c.TorrentTimeout = d })
		f.route(name, c.Source(name).Route)
	case "vk":
		f.secret("Token", c.VKToken, func(c *core.Config, v string) { c.VKToken = v })
		f.button("Log in to get a token…", func() { f.d.Reject(); t.vkLogin() })
		f.route(name, c.Source(name).Route)
	case "yandex":
		f.secret("Token", c.YandexToken, func(c *core.Config, v string) { c.YandexToken = v })
		f.note("Open <a href='" + core.YandexTokenURL + "'>Yandex's token page</a> in your browser, log in, then copy the access_token=… part from the address bar.")
		f.route(name, c.Source(name).Route)
	default:
		f.route(name, c.Source(name).Route)
	}
	f.run()
	t.refresh()
}

// browserNames are the browsers logins can come from.
func browserNames() []string {
	var out []string
	for _, b := range core.Browsers[1:] {
		out = append(out, capitalized(b))
	}
	return out
}

var (
	mailboxModes  = []string{"paste", "gmail", "imap"}
	mailboxLabels = []string{"I'll paste them myself (no mailbox access)", "Gmail: sign in with Google (read only)", "An IMAP mailbox (Yandex, Mail.ru, …)"}
)

func (t *sourcesTab) bandcampSettings() {
	c := t.a.m.Config()
	d := c.Bandcamp
	f := newForm(t.a, "Bandcamp", "Free and name-your-price releases are downloaded by themselves. Some artists ask for an email address first: Bandcamp then emails you a download link.", 740)
	f.text("Your email address", d.Email, func(c *core.Config, v string) error { c.Bandcamp.Email = v; return nil })
	f.text("Country", d.Country, func(c *core.Config, v string) error { c.Bandcamp.Country = v; return nil })
	f.text("Postcode", d.Postcode, func(c *core.Config, v string) error { c.Bandcamp.Postcode = v; return nil })
	cur := 0
	for i, m := range mailboxModes {
		if m == d.MailboxMode() {
			cur = i
		}
	}
	mode := f.combo("Links from those emails", mailboxLabels, cur, func(c *core.Config, i int) {
		if i >= 0 && i < len(mailboxModes) {
			c.Bandcamp.Mailbox = mailboxModes[i]
		}
	})
	f.head("Gmail")
	id := f.text("OAuth client ID", d.GmailClientID, func(c *core.Config, v string) error { c.Bandcamp.GmailClientID = v; return nil })
	secret := f.secret("OAuth client secret", d.GmailClientSecret, func(c *core.Config, v string) { c.Bandcamp.GmailClientSecret = v })
	state := "Not signed in."
	if d.GmailToken != "" {
		state = "Signed in as " + core.FirstNonEmpty(d.GmailAccount, "?") + "."
	}
	f.note(state + " You sign in on Google's page in your browser and allow read-only access. Google asks for your own free OAuth client for that; the README's “Gmail” section shows how to make one.")
	f.head("IMAP mailbox")
	imapHost := f.text("Server (host:port)", d.IMAPHost, func(c *core.Config, v string) error { c.Bandcamp.IMAPHost = v; return nil })
	imapUser := f.text("User", d.IMAPUser, func(c *core.Config, v string) error { c.Bandcamp.IMAPUser = v; return nil })
	imapPass := f.secret("Password", d.IMAPPass, func(c *core.Config, v string) { c.Bandcamp.IMAPPass = v })
	f.note("Yandex: imap.yandex.ru:993 with an app password (id.yandex.ru → Security → App passwords), and IMAP on in Mail settings. " +
		"Mail.ru: imap.mail.ru:993 with an app password. The mailbox is only read: nothing is marked, moved or deleted.")
	f.route("bandcamp", c.Source("bandcamp").Route)
	enable := func(i int) {
		id.SetEnabled(i == 1)
		secret.SetEnabled(i == 1)
		for _, w := range []*qt.QLineEdit{imapHost, imapUser, imapPass} {
			w.SetEnabled(i == 2)
		}
	}
	mode.OnCurrentIndexChanged(enable)
	enable(cur)
	f.button("Sign in with Google…", func() {
		if f.save() {
			f.d.Accept()
			t.gmailSignIn()
		}
	})
	f.button("Paste links…", t.pasteBandcampLinks)
	f.run()
}

// gmailSignIn opens Google's sign-in page in the browser and waits for it.
func (t *sourcesTab) gmailSignIn() {
	bc := t.a.m.Config().Bandcamp
	id, secret := strings.TrimSpace(bc.GmailClientID), strings.TrimSpace(bc.GmailClientSecret)
	box := qt.NewQMessageBox6(qt.QMessageBox__Information, "Sign in with Google", "Starting…", qt.QMessageBox__Cancel, t.a.win.QWidget)
	ctx, cancel := context.WithCancel(t.a.m.Context())
	box.OnFinished(func(int) { cancel() })
	go func() {
		refresh, account, err := core.GmailSignIn(ctx, id, secret, func(u string) {
			core.Logf("Gmail sign-in page: %s", u)
			opened := "Your browser should open Google's sign-in page."
			if err := core.OpenBrowser(u); err != nil {
				opened = "Couldn't open your browser (" + err.Error() + ")."
			}
			mainthread.Start(func() {
				box.SetText(opened + " Sign in, allow read-only access, and come back here.\n\nIf it didn't open, copy this address into your browser:\n\n" + u)
				box.SetTextInteractionFlags(qt.TextSelectableByMouse)
			})
		})
		mainthread.Start(func() {
			if ctx.Err() != nil && err != nil {
				return // cancelled
			}
			box.Done(0)
			if err != nil {
				t.a.message("Gmail sign-in didn't work", err.Error())
				return
			}
			if err := t.a.m.UpdateConfig(func(c *core.Config) {
				b := &c.Bandcamp
				b.GmailClientID, b.GmailClientSecret, b.GmailToken, b.GmailAccount, b.Mailbox = id, secret, refresh, account, "gmail"
				if b.Email == "" {
					b.Email = account
				}
			}); err != nil {
				t.a.message("Gmail", err.Error())
				return
			}
			t.a.message("Gmail", "Signed in as "+account+". Bandcamp's emails are read from this mailbox (read only).")
			t.refresh()
		})
	}()
	box.Open()
}

// pasteBandcampLinks takes download links copied from Bandcamp's emails.
func (t *sourcesTab) pasteBandcampLinks() {
	d := qt.NewQDialog(t.a.win.QWidget)
	d.SetWindowTitle("Paste Bandcamp links")
	d.Resize(680, 380)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 16, 20, 14)
	box.SetSpacing(10)
	h := qt.NewQLabel3("Paste Bandcamp links")
	setProp(h.QWidget, "role", "h1")
	box.AddWidget(h.QWidget)
	intro := "Paste the download links from Bandcamp's emails, or the whole emails. Several at once is fine."
	if pending, _ := t.a.m.BandcampMailStatus(); pending > 0 {
		intro += fmt.Sprintf(" %d releases are waiting for theirs.", pending)
	}
	box.AddWidget(text(intro, "subtitle").QWidget)
	area := qt.NewQPlainTextEdit2()
	box.AddWidget2(area.QWidget, 1)
	status := text("", "error")
	box.AddWidget(status.QWidget)
	buttons := hbox()
	buttons.SetSpacing(8)
	buttons.AddStretch()
	buttons.AddWidget(button("Cancel", "", func() { d.Reject() }).QWidget)
	var use *qt.QPushButton
	use = button("Use these links", "primary", func() {
		pasted := area.ToPlainText()
		if strings.TrimSpace(pasted) == "" {
			return
		}
		if len(core.FindBandcampLinks(pasted)) == 0 {
			status.SetText("There are no Bandcamp download links in that. They look like https://bandcamp.com/download?…")
			return
		}
		use.SetEnabled(false)
		status.SetText("Opening the links…")
		go func() {
			found, added, errs := t.a.m.AddBandcampLinks(pasted)
			mainthread.Start(func() {
				use.SetEnabled(true)
				msg := fmt.Sprintf("%d links, %d new. The songs waiting for them carry on.", found, added)
				if len(errs) > 0 {
					status.SetText(msg + " Couldn't use: " + strings.Join(errs, "; "))
					return
				}
				d.Accept()
				t.a.toast(msg)
			})
		}()
	})
	buttons.AddWidget(use.QWidget)
	box.AddLayout(buttons.QLayout)
	d.Exec()
}

func (t *sourcesTab) vkLogin() {
	d := qt.NewQDialog(t.a.win.QWidget)
	d.SetWindowTitle("Log in to VK")
	form := qt.NewQFormLayout(d.QWidget)
	form.SetContentsMargins(20, 16, 20, 14)
	form.SetVerticalSpacing(10)
	user, pass, code := qt.NewQLineEdit2(), qt.NewQLineEdit2(), qt.NewQLineEdit2()
	pass.SetEchoMode(qt.QLineEdit__Password)
	form.AddRow3("Phone or email", user.QWidget)
	form.AddRow3("Password (not stored)", pass.QWidget)
	form.AddRow3("Confirmation code, if asked", code.QWidget)
	status := text("", "hint")
	form.AddRowWithWidget(status.QWidget)
	var get *qt.QPushButton
	get = button("Log in", "primary", func() {
		status.SetText("Logging in…")
		get.SetEnabled(false)
		route := t.a.m.Config().Source("vk").Route
		u, p, cd := strings.TrimSpace(user.Text()), pass.Text(), strings.TrimSpace(code.Text())
		go func() {
			token, needCode, err := core.VkLogin(t.a.m.Context(), u, p, cd, route)
			mainthread.Start(func() {
				get.SetEnabled(true)
				switch {
				case err != nil:
					status.SetText(err.Error())
				case needCode:
					status.SetText("VK sent a confirmation code (SMS or app). Enter it and press Log in again.")
				default:
					d.Accept()
					if err := t.a.m.UpdateConfig(func(c *core.Config) { c.VKToken = token; c.SetSource("vk", true) }); err != nil {
						t.a.message("VK", err.Error())
						return
					}
					t.a.toast("Logged in. VK is on.")
					t.refresh()
				}
			})
		}()
	})
	form.AddRowWithWidget(get.QWidget)
	d.Resize(500, 220)
	d.Exec()
}

// soulseekSettings edits the Soulseek source: account, sharing, limits and chat.
func (t *sourcesTab) soulseekSettings(sk core.SoulseekConfig) {
	c := t.a.m.Config()
	f := newForm(t.a, "Soulseek", "Pick any new name and password to create a free account, or use the one you already have.", 760)
	f.text("Username", sk.Username, func(c *core.Config, v string) error { c.Soulseek.Username = v; return nil })
	f.secret("Password", sk.Password, func(c *core.Config, v string) { c.Soulseek.Password = v })
	f.button("Import from Nicotine+", func() {
		got, err := core.ImportNicotine(sk)
		if err != nil {
			t.a.message("Nicotine+", err.Error())
			return
		}
		f.d.Reject()
		t.a.message("Imported from Nicotine+", fmt.Sprintf("Account %s, port %d, %d shared folders, %d upload slots. Check them and press Save.\n\n"+
			"Don't run Nicotine+ with the same account at the same time: each logs the other out.", got.Username, got.ListenPort(), len(got.Shares), got.Slots()))
		t.soulseekSettings(got)
	})
	f.head("Sharing")
	f.note("Sharing is what keeps the network alive, and many people won't send files to someone who shares nothing. Only what's listed here is visible to others.")
	f.check("Share files with others", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
	shares := f.text("Shared folders (separate with ;)", strings.Join(sk.Shares, "; "), func(c *core.Config, v string) error {
		c.Soulseek.Shares = core.SplitBy(v, ";")
		return nil
	})
	var defaults []string
	for _, r := range c.ShareRoots() {
		defaults = append(defaults, r.Path)
	}
	shares.SetPlaceholderText("Empty: " + strings.Join(defaults, "; "))
	f.number("People who can download at once", float64(sk.Slots()), func(c *core.Config, v float64) { c.Soulseek.UploadSlots = int(v) })
	f.number("Upload speed limit, KB/s (0: none)", float64(sk.UploadKBps), func(c *core.Config, v float64) { c.Soulseek.UploadKBps = int(v) })
	f.head("Being reachable")
	f.note("Things work better when others can reach you. The app can ask your router to allow it. With a VPN, set Connection below to Direct.")
	f.number("Port", float64(sk.ListenPort()), func(c *core.Config, v float64) {
		if c.Soulseek.Port = int(v); c.Soulseek.Port == core.SlskDefaultPort {
			c.Soulseek.Port = 0
		}
	})
	f.check("Ask my router to open the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
	f.route("soulseek", c.Source("soulseek").Route)
	f.collapsed("Advanced", false)
	f.text("Server", sk.ServerAddr(), func(c *core.Config, v string) error {
		if v == core.SlskDefaultServer {
			v = ""
		}
		c.Soulseek.Server = v
		return nil
	})
	f.number("Downloads at once (1-4)", float64(sk.MaxDownloads()), func(c *core.Config, v float64) { c.Soulseek.Downloads = int(v) })
	f.number("Seconds between searches (8 or more)", sk.SearchInterval().Seconds(), func(c *core.Config, v float64) { c.Soulseek.SearchGap = v })
	f.number("Most searches an hour (20-200)", float64(sk.SearchesPerHour()), func(c *core.Config, v float64) { c.Soulseek.PerHour = int(v) })
	f.text("Stop waiting in someone's queue after", sk.QueueTimeout().String(), func(c *core.Config, v string) error {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			return fmt.Errorf("stop waiting in someone's queue after: a duration like 5m (at least 1m)")
		}
		c.Soulseek.QueueWait = core.Duration(d)
		return nil
	})
	f.number("Queue limit per person, files", float64(sk.FilesPerUser()), func(c *core.Config, v float64) { c.Soulseek.QueueFiles = int(v) })
	f.number("Queue limit per person, MB", float64(sk.MBPerUser()), func(c *core.Config, v float64) { c.Soulseek.QueueMB = int(v) })
	f.text("Banned users (comma-separated)", strings.Join(sk.Banned, ", "), func(c *core.Config, v string) error {
		c.Soulseek.Banned = core.SplitList(v)
		return nil
	})
	f.text("Chat rooms to join (comma-separated)", strings.Join(sk.Rooms, ", "), func(c *core.Config, v string) error {
		c.Soulseek.Rooms = core.SplitList(v)
		return nil
	})
	f.text("Description others see", sk.Description, func(c *core.Config, v string) error { c.Soulseek.Description = v; return nil })
	f.run()
}
