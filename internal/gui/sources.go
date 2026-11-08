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

// The Sources tab: the sources in the order they're tried, their state and counts, and each
// one's settings window (route, account, tokens, cookies, indexers, Soulseek).
type sourcesTab struct {
	a      *app
	w      *qt.QWidget
	table  *qt.QTableWidget
	detail *qt.QLabel
	rows   []core.SourceStatus
}

func newSourcesTab(a *app) *sourcesTab {
	t := &sourcesTab{a: a, w: qt.NewQWidget2()}
	box := qt.NewQVBoxLayout(t.w)
	box.SetContentsMargins(0, 4, 0, 0)
	t.table = qt.NewQTableWidget2()
	heads := []string{"#", "On", "Source", "Route", "Status", "Searched", "Found", "Downloaded", "Failed"}
	t.table.SetColumnCount(len(heads))
	t.table.SetHorizontalHeaderLabels(heads)
	t.table.SetSelectionBehavior(qt.QAbstractItemView__SelectRows)
	t.table.SetSelectionMode(qt.QAbstractItemView__SingleSelection)
	t.table.SetEditTriggers(qt.QAbstractItemView__NoEditTriggers)
	t.table.VerticalHeader().Hide()
	h := t.table.HorizontalHeader()
	h.SetSectionResizeMode2(4, qt.QHeaderView__Stretch)
	for i, w := range []int{30, 34, 130, 140} {
		t.table.SetColumnWidth(i, w)
	}
	t.table.OnCurrentCellChanged(func(int, int, int, int) { t.showDetail() })
	t.table.OnCellDoubleClicked(func(int, int) { t.settings() })
	box.AddWidget2(t.table.QWidget, 2)

	buttons := qt.NewQHBoxLayout2()
	add := func(text, tip string, do func()) {
		b := qt.NewQPushButton3(text)
		b.SetToolTip(tip)
		b.OnClicked(do)
		buttons.AddWidget(b.QWidget)
	}
	add("Settings…", "Route, account, tokens, cookies (Enter)", t.settings)
	add("On / off", "Use this source or not (space)", func() {
		if s := t.selected(); s != nil {
			t.try(t.a.m.SetSourceEnabled(s.Name, !s.Enabled))
		}
	})
	add("Move up", "Tried earlier (K)", func() { t.move(-1) })
	add("Move down", "Tried later (J)", func() { t.move(1) })
	add("Pause / resume", "Stop using it for now, or use it again right away after a pause (P)", func() {
		if s := t.selected(); s != nil {
			t.a.m.PauseSource(s.Name, !s.Paused && s.Until.IsZero())
			t.refresh()
		}
	})
	add("Paste Bandcamp links…", "Download links from Bandcamp's emails", t.pasteBandcampLinks)
	buttons.AddStretch()
	box.AddLayout(buttons.QLayout)

	t.detail = qt.NewQLabel2()
	t.detail.SetTextFormat(qt.RichText)
	t.detail.SetWordWrap(true)
	t.detail.SetAlignment(qt.AlignTop | qt.AlignLeft)
	t.detail.SetTextInteractionFlags(qt.TextSelectableByMouse)
	scroll := qt.NewQScrollArea2()
	scroll.SetWidgetResizable(true)
	scroll.SetWidget(t.detail.QWidget)
	box.AddWidget2(scroll.QWidget, 3)

	for key, do := range map[string]func(){"Return": t.settings, "Space": func() {
		if s := t.selected(); s != nil {
			t.try(t.a.m.SetSourceEnabled(s.Name, !s.Enabled))
		}
	}, "K": func() { t.move(-1) }, "J": func() { t.move(1) }} {
		sc := qt.NewQShortcut2(qt.NewQKeySequence2(key), t.table.QObject)
		sc.SetContext(qt.WidgetShortcut)
		sc.OnActivated(do)
	}
	t.refresh()
	return t
}

func (t *sourcesTab) try(err error) {
	if err != nil {
		t.a.message("Can't do that", err.Error())
	}
	t.refresh()
}

func (t *sourcesTab) selected() *core.SourceStatus {
	if r := t.table.CurrentRow(); r >= 0 && r < len(t.rows) {
		return &t.rows[r]
	}
	return nil
}

func (t *sourcesTab) move(delta int) {
	s := t.selected()
	if s == nil {
		return
	}
	r := t.table.CurrentRow()
	t.try(t.a.m.MoveSource(s.Name, delta))
	if j := r + delta; j >= 0 && j < len(t.rows) {
		t.table.SelectRow(j)
	}
}

func sourceState(s core.SourceStatus) (string, string) {
	switch {
	case !s.Enabled:
		return "off", "#888"
	case s.Problem != "":
		return s.Problem, "#c33"
	case s.Paused:
		return "paused by you", "#b90"
	case !s.Until.IsZero():
		return "paused until " + s.Until.Format("15:04") + ": " + s.Reason, "#d70"
	case s.Note != "":
		return "ok, but " + s.Note, "#b90"
	}
	return "ok", "#3a3"
}

func (t *sourcesTab) refresh() {
	r := t.table.CurrentRow()
	t.rows = t.a.m.SourceStatus()
	t.table.SetRowCount(len(t.rows))
	for i, s := range t.rows {
		on := "✓"
		if !s.Enabled {
			on = "·"
		}
		state, color := sourceState(s)
		st := cell(state)
		st.SetForeground(qt.NewQBrush3(qt.NewQColor6(color)))
		st.SetToolTip(state)
		t.table.SetItem(i, 0, number(i+1))
		t.table.SetItem(i, 1, cell(on))
		t.table.SetItem(i, 2, cell(s.Label))
		t.table.SetItem(i, 3, cell(core.ParseRoute(s.Route).Short()))
		t.table.SetItem(i, 4, st)
		for c, n := range []int{s.Searches, s.Found, s.Downloads, s.DlFails} {
			t.table.SetItem(i, 5+c, number(n))
		}
	}
	if r < 0 && len(t.rows) > 0 {
		r = 0
	}
	if r >= 0 && r < len(t.rows) {
		t.table.SelectRow(r)
	}
	t.showDetail()
}

func (t *sourcesTab) showDetail() {
	s := t.selected()
	if s == nil {
		return
	}
	e := html.EscapeString
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b> · route: %s<br>%s", e(s.Label), e(core.DescribeRoute(s.Route)), e(core.SourceHelp[s.Name]))
	if s.Problem != "" {
		fmt.Fprintf(&b, "<p style='color:#c33'>%s</p>", e(s.Problem))
	}
	if s.Note != "" {
		fmt.Fprintf(&b, "<p style='color:#b90'>%s</p>", e(s.Note))
	}
	if s.Reason != "" {
		fmt.Fprintf(&b, "<p style='color:#d70'>Paused until %s: %s (Pause / resume uses it again now)</p>", s.Until.Format("15:04"), e(s.Reason))
	}
	switch s.Name {
	case "bandcamp":
		bc := t.a.m.Config().Bandcamp
		b.WriteString("<p><b>Releases that ask for an email address</b><br>")
		if bc.Email == "" {
			b.WriteString("skipped: enter your email address in the settings.")
		} else {
			pending, problem := t.a.m.BandcampMailStatus()
			from := map[string]string{"paste": "you paste them (Paste Bandcamp links)", "gmail": "read from Gmail (" + core.FirstNonEmpty(bc.GmailAccount, "not signed in") + ")",
				"imap": "read from " + bc.IMAPHost}[bc.MailboxMode()]
			fmt.Fprintf(&b, "Bandcamp emails the links to %s; %s.", e(bc.Email), e(from))
			if pending > 0 {
				fmt.Fprintf(&b, "<br><span style='color:#b90'>%d releases waiting for their link</span>", pending)
			}
			if problem != "" {
				fmt.Fprintf(&b, "<br><span style='color:#c33'>Mailbox: %s</span>", e(problem))
			}
		}
		b.WriteString("</p>")
	case "torrent":
		feeds := t.a.m.TorrentFeeds()
		if len(feeds) > 0 {
			b.WriteString("<p><b>Indexers</b><br>")
		}
		for _, f := range feeds {
			state := "<span style='color:#3a3'>ok</span>"
			if !f.OK {
				state = "<span style='color:#d70'>left out until " + f.Until.Format("15:04") + "</span>"
			}
			fmt.Fprintf(&b, "%s &nbsp;%s · %d searches, %d errors", state, e(f.Name), f.Searches, f.Errors)
			if f.Avg > 0 {
				fmt.Fprintf(&b, ", answers in %.1fs", f.Avg.Seconds())
			}
			if f.LastErr != "" {
				fmt.Fprintf(&b, "<br>&nbsp;&nbsp;&nbsp;&nbsp;<span style='color:#888'>last error: %s</span>", e(core.FitWidth(f.LastErr, 160)))
			}
			b.WriteString("<br>")
		}
	}
	t.detail.SetText(b.String())
}

// --- settings windows -------------------------------------------------------------------------

func (t *sourcesTab) settings() {
	s := t.selected()
	if s == nil {
		return
	}
	c := t.a.m.Config()
	switch s.Name {
	case "bandcamp":
		t.bandcampSettings()
		t.refresh()
		return
	case "soulseek":
		t.soulseekSettings(c.Soulseek)
		t.refresh()
		return
	}
	f := newForm(t.a, s.Label+" settings", 720)
	f.route(s.Name, c.Source(s.Name).Route)
	switch s.Name {
	case "youtube", "youtube_music", "soundcloud":
		f.text("Cookies file (cookies.txt)", c.Cookies, func(c *core.Config, v string) error { c.Cookies = v; return nil })
		idx := 0
		for i, b := range core.Browsers {
			if b == c.CookiesFromBrowser {
				idx = i
			}
		}
		f.combo("Cookies from browser", append([]string{"(none)"}, core.Browsers[1:]...), idx, func(c *core.Config, i int) {
			if i >= 0 && i < len(core.Browsers) {
				c.CookiesFromBrowser = core.Browsers[i]
			}
		})
		f.number("Seconds between YouTube requests", c.YouTubePace, func(c *core.Config, v float64) { c.YouTubePace = max(0, v) })
		f.text("yt-dlp program (empty: download it automatically)", c.YtDlpPath, func(c *core.Config, v string) error { c.YtDlpPath = v; return nil })
		f.note("Your browser's YouTube login (cookies) is the best fix for \"sign in to confirm you're not a bot\". They're read " +
			"from the browser every 10 minutes (YouTube's, Google's and SoundCloud's only); your browser and your cookies file are never written to.")
	case "torrent":
		f.check("Built-in indexers (Jackett's public sites, RuTracker.org with your account)", !c.Indexers.NoBuiltin, func(c *core.Config, v bool) { c.Indexers.NoBuiltin = !v })
		f.text("FlareSolverr address (for Cloudflare's checks)", c.Indexers.FlareSolverr, func(c *core.Config, v string) error { c.Indexers.FlareSolverr = v; return nil })
		f.text("Jackett / Prowlarr address(es), optional", strings.Join(c.Torznab.Feeds, ", "), func(c *core.Config, v string) error {
			c.Torznab.Feeds = core.SplitList(v)
			return nil
		})
		f.secret("Jackett / Prowlarr API key", c.Torznab.Key, func(c *core.Config, v string) { c.Torznab.Key = v })
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
		dur("Give up on an indexer search after", c.IndexerTimeout, func(c *core.Config, d core.Duration) { c.IndexerTimeout = d })
		dur("Give up on a torrent download after", c.TorrentTimeout, func(c *core.Config, d core.Duration) { c.TorrentTimeout = d })
		f.button("Choose indexers…", func() { t.a.pickIndexers() })
		f.note("Jackett, Prowlarr and FlareSolverr are optional programs: the app searches the sites itself. FlareSolverr gets " +
			"past Cloudflare's \"checking your browser\" pages; Jackett or Prowlarr add private trackers you have accounts on. TORRENTS.md explains them.")
	case "vk":
		f.secret("Token", c.VKToken, func(c *core.Config, v string) { c.VKToken = v })
		f.button("Log in to get a token…", func() { f.d.Reject(); t.vkLogin() })
	case "yandex":
		f.secret("OAuth token", c.YandexToken, func(c *core.Config, v string) { c.YandexToken = v })
		f.note("Open <a href='" + core.YandexTokenURL + "'>Yandex's token page</a> in your browser, log in, then copy the access_token=… value from the address bar.")
	}
	f.run()
	t.refresh()
}

var (
	mailboxModes  = []string{"paste", "gmail", "imap"}
	mailboxLabels = []string{"I paste them from the emails (no mailbox access)", "Gmail: sign in with Google (read only)", "IMAP mailbox (Yandex, Mail.ru, …)"}
)

func (t *sourcesTab) bandcampSettings() {
	c := t.a.m.Config()
	d := c.Bandcamp
	f := newForm(t.a, "Bandcamp settings", 760)
	f.note("<b>Name-your-price releases that ask for an email address</b>: Bandcamp emails you the download link.")
	f.route("bandcamp", c.Source("bandcamp").Route)
	f.text("Your email address", d.Email, func(c *core.Config, v string) error { c.Bandcamp.Email = v; return nil })
	f.text("Country", d.Country, func(c *core.Config, v string) error { c.Bandcamp.Country = v; return nil })
	f.text("Postcode", d.Postcode, func(c *core.Config, v string) error { c.Bandcamp.Postcode = v; return nil })
	cur := 0
	for i, m := range mailboxModes {
		if m == d.MailboxMode() {
			cur = i
		}
	}
	mode := f.combo("Download links from", mailboxLabels, cur, func(c *core.Config, i int) {
		if i >= 0 && i < len(mailboxModes) {
			c.Bandcamp.Mailbox = mailboxModes[i]
		}
	})
	f.head("Gmail")
	id := f.text("OAuth client ID", d.GmailClientID, func(c *core.Config, v string) error { c.Bandcamp.GmailClientID = v; return nil })
	secret := f.secret("OAuth client secret", d.GmailClientSecret, func(c *core.Config, v string) { c.Bandcamp.GmailClientSecret = v })
	state := "not signed in"
	if d.GmailToken != "" {
		state = "signed in as " + core.FirstNonEmpty(d.GmailAccount, "?")
	}
	f.note("Gmail: " + state + ". You sign in on Google's page in your browser and allow read-only access; Google wants your own " +
		"(free) OAuth client for that: README → \"Gmail\" shows how to make one.")
	f.head("IMAP")
	imapHost := f.text("IMAP server (host:port)", d.IMAPHost, func(c *core.Config, v string) error { c.Bandcamp.IMAPHost = v; return nil })
	imapUser := f.text("IMAP user", d.IMAPUser, func(c *core.Config, v string) error { c.Bandcamp.IMAPUser = v; return nil })
	imapPass := f.secret("IMAP password", d.IMAPPass, func(c *core.Config, v string) { c.Bandcamp.IMAPPass = v })
	f.note("Yandex: imap.yandex.ru:993 with an app password (id.yandex.ru → Security → App passwords) and IMAP on in Mail settings → " +
		"Email clients. Mail.ru: imap.mail.ru:993 with an app password. Only read: nothing is marked, moved or deleted.")
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
	d.SetWindowTitle("Paste Bandcamp download links")
	d.Resize(720, 380)
	box := qt.NewQVBoxLayout(d.QWidget)
	intro := "Paste the download links from Bandcamp's emails (or the whole emails; several at once is fine)."
	if pending, _ := t.a.m.BandcampMailStatus(); pending > 0 {
		intro += fmt.Sprintf(" %d releases are waiting for theirs.", pending)
	}
	l := qt.NewQLabel3(intro)
	l.SetWordWrap(true)
	box.AddWidget(l.QWidget)
	area := qt.NewQPlainTextEdit2()
	box.AddWidget2(area.QWidget, 1)
	status := qt.NewQLabel2()
	status.SetWordWrap(true)
	box.AddWidget(status.QWidget)
	buttons := qt.NewQHBoxLayout2()
	use := qt.NewQPushButton3("Use these links")
	cancel := qt.NewQPushButton3("Cancel")
	buttons.AddStretch()
	buttons.AddWidget(use.QWidget)
	buttons.AddWidget(cancel.QWidget)
	box.AddLayout(buttons.QLayout)
	cancel.OnClicked(func() { d.Reject() })
	use.OnClicked(func() {
		text := area.ToPlainText()
		if strings.TrimSpace(text) == "" {
			return
		}
		if len(core.FindBandcampLinks(text)) == 0 {
			status.SetText("No Bandcamp download links in that (they look like https://bandcamp.com/download?…).")
			return
		}
		use.SetEnabled(false)
		status.SetText("Opening the links…")
		go func() {
			found, added, errs := t.a.m.AddBandcampLinks(text)
			mainthread.Start(func() {
				use.SetEnabled(true)
				msg := fmt.Sprintf("%d links, %d new: the tracks waiting for them go on.", found, added)
				if len(errs) > 0 {
					status.SetText(msg + " Not usable: " + strings.Join(errs, "; "))
					return
				}
				d.Accept()
				t.a.message("Bandcamp links", msg)
			})
		}()
	})
	d.Exec()
}

func (t *sourcesTab) vkLogin() {
	d := qt.NewQDialog(t.a.win.QWidget)
	d.SetWindowTitle("VK login (Kate Mobile token)")
	form := qt.NewQFormLayout(d.QWidget)
	user, pass, code := qt.NewQLineEdit2(), qt.NewQLineEdit2(), qt.NewQLineEdit2()
	pass.SetEchoMode(qt.QLineEdit__Password)
	form.AddRow3("Phone or email", user.QWidget)
	form.AddRow3("Password (not stored)", pass.QWidget)
	form.AddRow3("Confirmation code (if asked)", code.QWidget)
	status := qt.NewQLabel2()
	status.SetWordWrap(true)
	form.AddRowWithWidget(status.QWidget)
	get := qt.NewQPushButton3("Get token")
	form.AddRowWithWidget(get.QWidget)
	get.OnClicked(func() {
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
					status.SetText("VK sent a confirmation code (SMS or app): enter it and press Get token again.")
				default:
					d.Accept()
					if err := t.a.m.UpdateConfig(func(c *core.Config) { c.VKToken = token; c.SetSource("vk", true) }); err != nil {
						t.a.message("VK", err.Error())
						return
					}
					t.a.message("VK", "Logged in; the VK source is on.")
					t.refresh()
				}
			})
		}()
	})
	d.Resize(520, 200)
	d.Exec()
}

// soulseekSettings edits the Soulseek source: route, account, limits, sharing and chat.
func (t *sourcesTab) soulseekSettings(sk core.SoulseekConfig) {
	c := t.a.m.Config()
	f := newForm(t.a, "Soulseek settings", 780)
	f.route("soulseek", c.Source("soulseek").Route)
	f.note("With a VPN, use the <b>direct</b> route so others reach your forwarded port: docs/soulseek-port-forwarding.md (MikroTik included).")
	f.head("Account")
	f.text("Username", sk.Username, func(c *core.Config, v string) error { c.Soulseek.Username = v; return nil })
	f.secret("Password", sk.Password, func(c *core.Config, v string) { c.Soulseek.Password = v })
	f.text("Server", sk.ServerAddr(), func(c *core.Config, v string) error {
		if v == core.SlskDefaultServer {
			v = ""
		}
		c.Soulseek.Server = v
		return nil
	})
	f.number("Listening port", float64(sk.ListenPort()), func(c *core.Config, v float64) {
		if c.Soulseek.Port = int(v); c.Soulseek.Port == core.SlskDefaultPort {
			c.Soulseek.Port = 0
		}
	})
	f.check("Ask the router to forward the port (UPnP)", !sk.NoUPnP, func(c *core.Config, v bool) { c.Soulseek.NoUPnP = !v })
	f.head("Downloading (the network asks for restraint)")
	f.number("Downloads at once (1-4)", float64(sk.MaxDownloads()), func(c *core.Config, v float64) { c.Soulseek.Downloads = int(v) })
	f.number("Seconds between searches (8 or more)", sk.SearchInterval().Seconds(), func(c *core.Config, v float64) { c.Soulseek.SearchGap = v })
	f.number("Most searches an hour (20 to 200; the server bans floods)", float64(sk.SearchesPerHour()), func(c *core.Config, v float64) { c.Soulseek.PerHour = int(v) })
	f.text("Give up waiting in a user's queue after", sk.QueueTimeout().String(), func(c *core.Config, v string) error {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			return fmt.Errorf("give up waiting in a user's queue after: a duration like 5m (at least 1m)")
		}
		c.Soulseek.QueueWait = core.Duration(d)
		return nil
	})
	f.head("Sharing")
	f.check("Share files", !sk.NoShare, func(c *core.Config, v bool) { c.Soulseek.NoShare = !v })
	shares := f.text("Shared folders (; between them)", strings.Join(sk.Shares, "; "), func(c *core.Config, v string) error {
		c.Soulseek.Shares = core.SplitBy(v, ";")
		return nil
	})
	var defaults []string
	for _, r := range c.ShareRoots() {
		defaults = append(defaults, r.Path)
	}
	shares.SetPlaceholderText("empty: " + strings.Join(defaults, "; "))
	f.number("Upload slots", float64(sk.Slots()), func(c *core.Config, v float64) { c.Soulseek.UploadSlots = int(v) })
	f.number("Upload speed limit, KB/s (0: none)", float64(sk.UploadKBps), func(c *core.Config, v float64) { c.Soulseek.UploadKBps = int(v) })
	f.number("Queue limit per user, files", float64(sk.FilesPerUser()), func(c *core.Config, v float64) { c.Soulseek.QueueFiles = int(v) })
	f.number("Queue limit per user, MB", float64(sk.MBPerUser()), func(c *core.Config, v float64) { c.Soulseek.QueueMB = int(v) })
	f.text("Banned users (comma-separated)", strings.Join(sk.Banned, ", "), func(c *core.Config, v string) error {
		c.Soulseek.Banned = core.SplitList(v)
		return nil
	})
	f.head("Chat")
	f.text("Rooms to join (comma-separated)", strings.Join(sk.Rooms, ", "), func(c *core.Config, v string) error {
		c.Soulseek.Rooms = core.SplitList(v)
		return nil
	})
	f.text("Description others see", sk.Description, func(c *core.Config, v string) error { c.Soulseek.Description = v; return nil })
	f.button("Import from Nicotine+", func() {
		got, err := core.ImportNicotine(sk)
		if err != nil {
			t.a.message("Nicotine+", err.Error())
			return
		}
		f.d.Reject()
		t.a.message("Imported from Nicotine+", fmt.Sprintf("Account %s, port %d, %d shared folders, %d upload slots. Check and Save.\n\n"+
			"Don't run Nicotine+ with the same account at the same time: each logs the other out.", got.Username, got.ListenPort(), len(got.Shares), got.Slots()))
		t.soulseekSettings(got)
	})
	f.run()
}
