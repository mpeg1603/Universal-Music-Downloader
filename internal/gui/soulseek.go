package gui

import (
	"context"
	"fmt"
	"html"
	"path"
	"sort"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// The Soulseek page: the connection and what's shared, searching the network and downloading
// by hand, transfers, browsing what people share, people, chat and interests.

type soulseekTab struct {
	a     *app
	w     *qt.QWidget
	stack *qt.QStackedWidget // not set up / the page
	off   *emptyState
	offB  *qt.QPushButton

	state, summary, share *qt.QLabel
	reconnect, away       *qt.QPushButton
	portNote              string

	segs  []*qt.QPushButton
	pages *qt.QStackedWidget
	page  int

	search    slskSearch
	transfers slskTransfers
	browse    slskBrowse
	people    slskPeople
	chat      slskChat
	interests slskInterests
}

var slskPageNames = []string{"Search", "Transfers", "Browse", "People", "Chat", "Interests"}

const (
	slskPageSearch = iota
	slskPageTransfers
	slskPageBrowse
	slskPagePeople
	slskPageChat
	slskPageInterests
)

func newSoulseekTab(a *app) *soulseekTab {
	t := &soulseekTab{a: a}
	p := a.newPage("Soulseek", "A community where people share their music with each other directly.", "soulseek")
	t.w = p.w

	t.reconnect = a.iconButton("refresh", "Reconnect", "", "Connect to the Soulseek network again", func() {
		a.m.SoulseekReconnect()
		a.toast("Reconnecting to Soulseek.")
	})
	t.away = button("Set me away", "", func() {
		a.m.SoulseekSetAway(!a.m.SoulseekStatus().Away)
		t.refresh()
	})
	t.away.SetToolTip("Tells others you're not at your computer. Downloads and sharing carry on.")
	setup := button("Set up…", "", func() {
		a.sources.soulseekSettings(a.m.Config().Soulseek)
		t.refresh()
	})
	for _, b := range []*qt.QPushButton{t.reconnect, t.away, setup} {
		p.actions.AddWidget(b.QWidget)
	}

	t.off = a.newEmpty("people", "Soulseek isn't set up yet", "It needs a free account: pick any new name and password and it's made for you. Your own music is shared in return.")
	t.offB = button("Set up Soulseek", "primary", func() {
		if a.m.SoulseekStatus().Configured {
			a.show(pageSources)
			return
		}
		a.sources.soulseekSettings(a.m.Config().Soulseek)
		t.refresh()
	})
	t.off.row.AddWidget(t.offB.QWidget)
	t.off.row.AddWidget(button("What is Soulseek?", "", func() { a.showHelp("soulseek") }).QWidget)
	t.off.done()

	body := qt.NewQWidget2()
	bb := qt.NewQVBoxLayout(body)
	bb.SetContentsMargins(0, 0, 0, 0)
	bb.SetSpacing(10)

	info, ib := card()
	ib.SetContentsMargins(14, 10, 12, 10)
	ib.SetSpacing(3)
	top := hbox()
	top.SetSpacing(10)
	t.state = pill("", "off")
	top.AddWidget(t.state.QWidget)
	t.summary = text("", "hint")
	top.AddWidget2(t.summary.QWidget, 1)
	top.AddWidget(button("Check my connection", "link", t.checkPort).QWidget)
	ib.AddLayout(top.QLayout)
	t.share = richText("", "muted")
	ib.AddWidget(t.share.QWidget)
	bb.AddWidget(info.QWidget)

	segRow := hbox()
	segs, buttons := segments(slskPageNames, t.showPage)
	t.segs = buttons
	segRow.AddWidget(segs.QWidget)
	segRow.AddStretch()
	segRow.AddWidget(a.iconButton("message", "Message someone", "", "Write to a Soulseek user", t.messageSomeone).QWidget)
	segRow.AddWidget(a.iconButton("people", "Join a room", "", "Join a chat room", t.joinRoom).QWidget)
	bb.AddLayout(segRow.QLayout)

	t.pages = qt.NewQStackedWidget2()
	for _, build := range []func() *qt.QWidget{t.buildSearch, t.buildTransfers, t.buildBrowse, t.buildPeople, t.buildChat, t.buildInterests} {
		t.pages.AddWidget(build())
	}
	bb.AddWidget2(t.pages.QWidget, 1)

	t.stack = qt.NewQStackedWidget2()
	t.stack.AddWidget(t.off.w)
	t.stack.AddWidget(body)
	p.box.AddWidget2(t.stack.QWidget, 1)
	t.showPage(0)
	t.refresh()
	return t
}

func (t *soulseekTab) showPage(i int) {
	t.page = i
	t.pages.SetCurrentIndex(i)
	t.segs[i].SetChecked(true)
	switch i {
	case slskPageInterests:
		t.fillLikes()
		if !t.interests.loaded {
			t.loadRecs("")
		}
	}
	t.refresh()
}

func (t *soulseekTab) shown() { t.refresh() }

// changed is the backend saying the connection, a chat or a transfer changed.
func (t *soulseekTab) changed() {
	if t.a.current() == pageSoulseek {
		t.refresh()
	}
}

// tick redraws what moves: transfers in progress.
func (t *soulseekTab) tick() {
	if t.a.current() == pageSoulseek && t.page == slskPageTransfers {
		t.fillTransfers()
	}
}

// refresh redraws the connection and the current sub-page.
func (t *soulseekTab) refresh() {
	v := t.a.m.SoulseekStatus()
	if !v.On {
		t.stack.SetCurrentIndex(0)
		if v.Configured {
			t.off.title.SetText("Soulseek is switched off")
			t.off.body.SetText("Turn it on in Sources to log in as " + v.Username + ".")
			t.offB.SetText("Go to Sources")
		}
		for _, b := range []*qt.QPushButton{t.reconnect, t.away} {
			b.Hide()
		}
		return
	}
	t.stack.SetCurrentIndex(1)
	t.reconnect.Show()
	t.away.Show()
	switch {
	case v.Online && v.Away:
		setPill(t.state, "Away", "warn")
	case v.Online:
		setPill(t.state, "Online", "ok")
	case v.Connecting:
		setPill(t.state, "Connecting…", "warn")
	default:
		setPill(t.state, "Offline", "bad")
	}
	if v.Away {
		t.away.SetText("I'm back")
	} else {
		t.away.SetText("Set me away")
	}
	var b strings.Builder
	switch {
	case v.Online:
		fmt.Fprintf(&b, "Signed in as %s since %s", v.User, v.Since.Format("15:04"))
		if v.Privileges > 0 {
			fmt.Fprintf(&b, ", privileged for %s", core.FmtWait(v.Privileges.Round(time.Hour)))
		}
	case v.Connecting:
		b.WriteString("Connecting to " + v.Server + "…")
	default:
		b.WriteString(core.FirstNonEmpty(v.Problem, "Not connected"))
		if v.Permanent {
			b.WriteString(" Press Reconnect to try again.")
		}
	}
	b.WriteString(fmt.Sprintf(".  Downloading %d of %d at once", v.DlUsed, v.DlLimit))
	if v.DlWaiting > 0 {
		fmt.Fprintf(&b, " (%d waiting)", v.DlWaiting)
	}
	fmt.Fprintf(&b, ", sending %d of %d", v.UpActive, v.UpSlots)
	if v.UpQueued > 0 {
		fmt.Fprintf(&b, " (%d queued)", v.UpQueued)
	}
	if v.UpSpeed > 0 {
		b.WriteString(" at " + core.FmtRate(v.UpSpeed))
	}
	b.WriteString(".")
	t.summary.SetText(b.String())

	e := html.EscapeString
	span := func(color, s string) string { return "<span style='color:" + color + "'>" + e(s) + "</span>" }
	var s []string
	switch {
	case v.NoShare:
		s = append(s, span(th.Warn, "Not sharing anything; many people won't send you files"))
	case v.ShareScanning:
		s = append(s, "Reading your shared folders…")
	case v.ShareScanned.IsZero():
		s = append(s, "Your shared folders are read after your library scan")
	default:
		x := fmt.Sprintf("Sharing %d files in %d folders", v.SharedFiles, v.SharedFolders)
		if v.ShareProblem != "" {
			x += " (" + v.ShareProblem + ")"
		}
		s = append(s, e(x))
	}
	if v.Port > 0 {
		x := fmt.Sprintf("Port %d", v.Port)
		if v.PortNote != "" {
			x += " (" + v.PortNote + ")"
		}
		s = append(s, e(x))
	} else {
		s = append(s, span(th.Warn, "Not reachable by others"))
	}
	if t.portNote != "" {
		s = append(s, t.portNote)
	}
	if v.WishTracks > 0 {
		x := fmt.Sprintf("Wishlist: %d songs no source had", v.WishTracks)
		if !v.WishNext.IsZero() && v.Online {
			x += ", next try at " + v.WishNext.Format("15:04")
		}
		s = append(s, e(x))
	}
	if v.SearchBacklog > time.Second {
		s = append(s, e("next search in "+core.FmtWait(v.SearchBacklog.Round(time.Second))))
	}
	t.share.SetText(strings.Join(s, "  ·  "))
	t.segs[slskPageChat].SetText(slskPageNames[slskPageChat] + onlyIf(v.Unread > 0, fmt.Sprintf(" (%d)", v.Unread)))

	switch t.page {
	case slskPageTransfers:
		t.fillTransfers()
	case slskPagePeople:
		t.fillPeople()
	}
	t.refreshChats()
}

func (t *soulseekTab) checkPort() {
	t.portNote = "checking…"
	t.refresh()
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 30*time.Second)
		defer cancel()
		ok, port, err := t.a.m.CheckSoulseekPort(ctx)
		mainthread.Start(func() {
			span := func(color, s string) string {
				return "<span style='color:" + color + "'>" + html.EscapeString(s) + "</span>"
			}
			switch {
			case err != nil:
				t.portNote = span(th.Warn, "couldn't check: "+err.Error())
			case ok:
				t.portNote = span(th.OK, fmt.Sprintf("port %d is open, others can reach you", port))
			default:
				t.portNote = span(th.Warn, fmt.Sprintf("port %d is closed; forward it in your router or use UPnP (Set up), and the Direct connection with a VPN", port))
			}
			t.refresh()
		})
	}()
}

func (t *soulseekTab) messageSomeone() {
	if v, ok := t.a.ask("Message someone", "Their Soulseek name:", ""); ok {
		t.openChat(v)
	}
}

func (t *soulseekTab) openChat(user string) {
	if user = strings.TrimSpace(user); user == "" {
		return
	}
	t.a.m.SoulseekOpenChat(user)
	t.chat.current, t.chat.seq = core.ChatKey(user, false), -1
	t.showPage(slskPageChat)
	t.chat.input.SetFocus()
}

func (t *soulseekTab) joinRoom() {
	rooms := t.a.m.SoulseekRooms()
	var items []string
	for i, r := range rooms {
		if i >= 300 {
			break
		}
		items = append(items, fmt.Sprintf("%s (%d)", r.Name, r.Users))
	}
	d := qt.NewQInputDialog2()
	d.SetWindowTitle("Join a room")
	d.SetLabelText("Pick a room, or type a new name to start one:")
	d.SetComboBoxItems(items)
	d.SetComboBoxEditable(true)
	d.SetTextValue("")
	d.Resize(460, 130)
	if d.Exec() != int(qt.QDialog__Accepted) {
		return
	}
	room := strings.TrimSpace(d.TextValue())
	for i, it := range items {
		if it == room {
			room = rooms[i].Name
		}
	}
	if room == "" {
		return
	}
	if err := t.a.m.SoulseekJoinRoom(room); err != nil {
		t.a.message("Couldn't join", err.Error())
		return
	}
	t.chat.current, t.chat.seq = core.ChatKey(room, true), -1
	t.showPage(slskPageChat)
}

// userMenu offers what can be done with a user.
func (t *soulseekTab) userMenu(user string, at *qt.QPoint) {
	if user == "" {
		return
	}
	a := t.a
	cfg := a.m.Config().Soulseek
	m := qt.NewQMenu2()
	m.AddActionWithText("Send a message").OnTriggered(func() { t.openChat(user) })
	m.AddActionWithText("Browse their files").OnTriggered(func() { t.browseUser(user) })
	m.AddActionWithText("About them").OnTriggered(func() { t.userInfo(user) })
	m.AddSeparator()
	toggle := func(list, on, off string, have bool) {
		label := on
		if have {
			label = off
		}
		m.AddActionWithText(label).OnTriggered(func() {
			if err := a.m.SoulseekSetUser(list, user, !have); err != nil {
				a.message("Soulseek", err.Error())
			}
			t.refresh()
		})
	}
	toggle("buddies", "Add to buddies", "Remove from buddies", core.ContainsFold(cfg.Buddies, user))
	toggle("ignored", "Ignore (hide their messages)", "Stop ignoring", core.ContainsFold(cfg.Ignored, user))
	toggle("banned", "Ban (no downloads from you, no file list)", "Unban", core.ContainsFold(cfg.Banned, user))
	m.ExecWithPos(at)
}

// tableMenu shows the user menu for a table's selected row.
func (t *soulseekTab) tableMenu(tb *qt.QTableWidget, user func() string) {
	tb.SetContextMenuPolicy(qt.CustomContextMenu)
	tb.OnCustomContextMenuRequested(func(pos *qt.QPoint) {
		t.userMenu(user(), tb.Viewport().MapToGlobalWithQPoint(pos))
	})
}

func slskBase(p string) string { return path.Base(strings.ReplaceAll(p, `\`, "/")) }

func slskQuality(r core.SlskResult) string {
	ext := strings.ToUpper(strings.TrimPrefix(path.Ext(slskBase(r.Path)), "."))
	switch {
	case r.BitDepth > 0 && r.SampleRate > 0:
		return fmt.Sprintf("%s %d/%.1f", ext, r.BitDepth, float64(r.SampleRate)/1000)
	case r.Bitrate > 0:
		return fmt.Sprintf("%s %d", ext, r.Bitrate)
	}
	return ext
}

func rightCell(s string) *qt.QTableWidgetItem {
	c := cell(s)
	c.SetTextAlignment(int(qt.AlignRight | qt.AlignVCenter))
	return c
}

func colored(s, color string) *qt.QTableWidgetItem {
	c := cell(s)
	c.SetForeground(brushOf(color))
	return c
}

func stretchColumn(tb *qt.QTableWidget, col int, widths ...int) {
	for i, w := range widths {
		tb.SetColumnWidth(i, w)
	}
	tb.HorizontalHeader().SetSectionResizeMode2(col, qt.QHeaderView__Stretch)
}

// --- search -------------------------------------------------------------------------------------

type slskSearch struct {
	find    *qt.QLineEdit
	go_     *qt.QPushButton
	note    *qt.QLabel
	table   *qt.QTableWidget
	res     []core.SlskResult
	busy    bool
	dl, dlF *qt.QPushButton
}

func (t *soulseekTab) buildSearch() *qt.QWidget {
	s := &t.search
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(8)
	row := hbox()
	row.SetSpacing(8)
	s.find = qt.NewQLineEdit2()
	s.find.SetPlaceholderText("Words in file and folder names")
	s.find.OnReturnPressed(func() { t.runSearch(s.find.Text()) })
	s.go_ = t.a.iconButton("search", "Search", "primary", "", func() { t.runSearch(s.find.Text()) })
	row.AddWidget2(s.find.QWidget, 1)
	row.AddWidget(s.go_.QWidget)
	box.AddLayout(row.QLayout)
	s.note = text("Searches are spaced out on purpose: the network bans clients that flood it.", "hint")
	box.AddWidget(s.note.QWidget)
	s.table = table("From", "File", "Size", "Quality", "Length", "Availability", "Their speed")
	stretchColumn(s.table, 1, 130, 300, 80, 100, 72, 100, 100)
	s.table.OnCellDoubleClicked(func(int, int) { t.downloadSelected() })
	s.table.OnCurrentCellChanged(func(int, int, int, int) { t.updateSearchButtons() })
	t.tableMenu(s.table, func() string {
		if r := t.selectedResult(); r != nil {
			return r.User
		}
		return ""
	})
	box.AddWidget2(s.table.QWidget, 1)
	acts := hbox()
	acts.SetSpacing(6)
	s.dl = t.a.iconButton("download", "Download file", "", "", t.downloadSelected)
	s.dlF = t.a.iconButton("folder", "Download folder", "", "Everything in this person's folder", t.downloadFolder)
	acts.AddWidget(s.dl.QWidget)
	acts.AddWidget(s.dlF.QWidget)
	acts.AddStretch()
	box.AddLayout(acts.QLayout)
	t.updateSearchButtons()
	return w
}

func (t *soulseekTab) updateSearchButtons() {
	on := t.selectedResult() != nil
	t.search.dl.SetEnabled(on)
	t.search.dlF.SetEnabled(on)
}

func (t *soulseekTab) selectedResult() *core.SlskResult {
	if r := t.search.table.CurrentRow(); r >= 0 && r < len(t.search.res) {
		return &t.search.res[r]
	}
	return nil
}

func (t *soulseekTab) runSearch(q string) {
	s := &t.search
	q = strings.TrimSpace(q)
	if q == "" || s.busy {
		return
	}
	s.busy = true
	s.go_.SetEnabled(false)
	s.note.SetText("Searching. Searches are spaced out on purpose, so this may wait its turn…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 2*time.Minute)
		defer cancel()
		res, err := t.a.m.SoulseekSearch(ctx, q)
		mainthread.Start(func() {
			s.busy = false
			s.go_.SetEnabled(true)
			if err != nil {
				s.note.SetText("The search didn't work: " + err.Error())
				return
			}
			s.res = res
			t.fillResults()
			if len(res) == 0 {
				s.note.SetText("Nobody answered with anything for “" + q + "”. Try fewer words.")
			} else {
				s.note.SetText(fmt.Sprintf("%d files for “%s”. Double-click to download; right-click for what else you can do with that person.", len(res), q))
			}
		})
	}()
}

func (t *soulseekTab) fillResults() {
	s := &t.search
	tb := s.table
	tb.SetUpdatesEnabled(false)
	defer tb.SetUpdatesEnabled(true)
	tb.ClearContents()
	tb.SetRowCount(len(s.res))
	for i, r := range s.res {
		slot, color := "Free now", th.OK
		if !r.FreeSlot {
			slot, color = fmt.Sprintf("In line (%d)", r.Queue), th.Warn
		}
		length := ""
		if r.Duration > 0 {
			length = core.FmtDuration(float64(r.Duration))
		}
		tb.SetItem(i, 0, cell(r.User))
		tb.SetItem(i, 1, cell(slskBase(r.Folder())+"/"+slskBase(r.Path)))
		tb.SetItem(i, 2, rightCell(core.FmtBytes(r.Size)))
		tb.SetItem(i, 3, cell(slskQuality(r)))
		tb.SetItem(i, 4, rightCell(length))
		tb.SetItem(i, 5, colored(slot, color))
		tb.SetItem(i, 6, rightCell(core.FmtRate(float64(r.Speed))))
	}
	if len(s.res) > 0 {
		tb.SelectRow(0)
	}
}

func (t *soulseekTab) downloadSelected() {
	r := t.selectedResult()
	if r == nil {
		return
	}
	t.a.m.SoulseekDownload(r.User, r.Path, r.Size)
	t.a.toast("Getting " + slskBase(r.Path) + " from " + r.User)
}

func (t *soulseekTab) downloadFolder() {
	r := t.selectedResult()
	if r == nil {
		return
	}
	if !t.a.confirm("Download this folder?", fmt.Sprintf("Everything in %s's folder %s.", r.User, slskBase(r.Folder()))) {
		return
	}
	if err := t.a.m.SoulseekDownloadFolder(r.User, r.Folder()); err != nil {
		t.a.message("Soulseek", err.Error())
		return
	}
	t.a.toast("Asking " + r.User + " for the folder.")
}

// --- transfers ----------------------------------------------------------------------------------

type slskTransfers struct {
	downs, ups *qt.QTableWidget
	xDown, xUp []core.SlskTransferView
	stop       *qt.QPushButton
}

func (t *soulseekTab) buildTransfers() *qt.QWidget {
	s := &t.transfers
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(8)
	mk := func(title string, rows *[]core.SlskTransferView) *qt.QTableWidget {
		box.AddWidget(text(title, "h2").QWidget)
		tb := table("From / to", "File", "State", "Progress", "Speed")
		stretchColumn(tb, 1, 150, 300, 180, 150, 90)
		t.tableMenu(tb, func() string {
			if r := tb.CurrentRow(); r >= 0 && r < len(*rows) {
				return (*rows)[r].User
			}
			return ""
		})
		box.AddWidget2(tb.QWidget, 1)
		return tb
	}
	s.downs = mk("Downloads", &s.xDown)
	s.ups = mk("What you're sending to others", &s.xUp)
	acts := hbox()
	acts.SetSpacing(6)
	s.stop = t.a.iconButton("x", "Stop sending", "danger", "Stop this upload", func() {
		if r := s.ups.CurrentRow(); r >= 0 && r < len(s.xUp) {
			t.a.m.SoulseekAbortUpload(s.xUp[r].User, s.xUp[r].File)
			t.fillTransfers()
		}
	})
	acts.AddWidget(s.stop.QWidget)
	acts.AddStretch()
	acts.AddWidget(t.a.iconButton("trash", "Clear finished", "", "Take finished transfers off the list", func() {
		t.a.m.SoulseekClearFinished()
		t.fillTransfers()
	}).QWidget)
	box.AddLayout(acts.QLayout)
	return w
}

func (t *soulseekTab) fillTransfers() {
	s := &t.transfers
	s.xDown, s.xUp = s.xDown[:0], s.xUp[:0]
	for _, x := range t.a.m.SoulseekTransfers() {
		if x.Up {
			s.xUp = append(s.xUp, x)
		} else {
			s.xDown = append(s.xDown, x)
		}
	}
	fill := func(tb *qt.QTableWidget, rows []core.SlskTransferView) {
		sel := tb.CurrentRow()
		tb.SetUpdatesEnabled(false)
		tb.SetRowCount(len(rows))
		for i, x := range rows {
			user := x.User
			if x.Privileged {
				user = "★ " + user
			}
			file := slskBase(path.Dir(strings.ReplaceAll(x.File, `\`, "/"))) + "/" + slskBase(x.File)
			prog := ""
			if x.Total > 0 && x.Done > 0 {
				prog = fmt.Sprintf("%.0f%% of %s", float64(x.Done)/float64(x.Total)*100, core.FmtBytes(x.Total))
			} else if x.Total > 0 {
				prog = core.FmtBytes(x.Total)
			}
			spd := ""
			if x.Speed > 0 && (x.State == "sending" || x.State == "downloading") {
				spd = core.FmtRate(x.Speed)
			}
			state, color := x.State, th.Text2
			switch {
			case x.State == "done", strings.HasPrefix(x.State, "done:"):
				state, color = "Finished", th.OK
			case strings.HasPrefix(x.State, "failed"):
				color = th.Bad
			case x.State == "cancelled", x.State == "stopped":
				color = th.Text3
			case x.State == "sending", x.State == "downloading":
				color = th.Accent
			}
			tb.SetItem(i, 0, cell(user))
			tb.SetItem(i, 1, cell(file))
			st := colored(capitalized(state), color)
			st.SetToolTip(x.State)
			tb.SetItem(i, 2, st)
			tb.SetItem(i, 3, cell(prog))
			tb.SetItem(i, 4, rightCell(spd))
		}
		if sel >= 0 && sel < len(rows) {
			tb.SelectRow(sel)
		}
		tb.SetUpdatesEnabled(true)
	}
	fill(s.downs, s.xDown)
	fill(s.ups, s.xUp)
}

// --- browse -------------------------------------------------------------------------------------

type slskBrowse struct {
	user       *qt.QLineEdit
	go_        *qt.QPushButton
	note       *qt.QLabel
	folders    *qt.QTableWidget
	files      *qt.QTableWidget
	browsed    []core.SlskResult
	whose      string
	folderList []string
	inFolder   []core.SlskResult
	sel        string
	busy       bool
}

func (t *soulseekTab) buildBrowse() *qt.QWidget {
	s := &t.browse
	w := qt.NewQWidget2()
	box := qt.NewQVBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(8)
	row := hbox()
	row.SetSpacing(8)
	s.user = qt.NewQLineEdit2()
	s.user.SetPlaceholderText("Someone's Soulseek name")
	s.user.OnReturnPressed(func() { t.browseUser(s.user.Text()) })
	s.go_ = t.a.iconButton("search", "See their files", "primary", "", func() { t.browseUser(s.user.Text()) })
	row.AddWidget2(s.user.QWidget, 1)
	row.AddWidget(s.go_.QWidget)
	box.AddLayout(row.QLayout)
	s.note = text("See everything someone shares, and download files or whole folders.", "hint")
	box.AddWidget(s.note.QWidget)
	split := qt.NewQSplitter3(qt.Horizontal)
	s.folders = table("Folder", "Files")
	stretchColumn(s.folders, 0, 300, 60)
	s.folders.OnCurrentCellChanged(func(r, _, _, _ int) {
		if r >= 0 && r < len(s.folderList) {
			s.sel = s.folderList[r]
			t.fillBrowseFiles()
		}
	})
	t.tableMenu(s.folders, func() string { return s.whose })
	s.files = table("File", "Size", "Quality", "Length")
	stretchColumn(s.files, 0, 300, 80, 100, 72)
	s.files.OnCellDoubleClicked(func(r, _ int) {
		if r >= 0 && r < len(s.inFolder) {
			f := s.inFolder[r]
			t.a.m.SoulseekDownload(f.User, f.Path, f.Size)
			t.a.toast("Getting " + slskBase(f.Path))
		}
	})
	split.AddWidget(s.folders.QWidget)
	split.AddWidget(s.files.QWidget)
	split.SetStretchFactor(0, 2)
	split.SetStretchFactor(1, 3)
	box.AddWidget2(split.QWidget, 1)
	acts := hbox()
	acts.AddWidget(t.a.iconButton("folder", "Download this folder", "", "Includes its subfolders", t.downloadBrowsed).QWidget)
	acts.AddStretch()
	box.AddLayout(acts.QLayout)
	return w
}

func (t *soulseekTab) browseUser(user string) {
	s := &t.browse
	user = strings.TrimSpace(user)
	if user == "" || s.busy {
		return
	}
	s.busy = true
	s.user.SetText(user)
	t.showPage(slskPageBrowse)
	s.note.SetText("Asking " + user + " for their file list…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 4*time.Minute)
		defer cancel()
		files, err := t.a.m.SoulseekBrowse(ctx, user)
		mainthread.Start(func() {
			s.busy = false
			if err != nil {
				s.note.SetText("Couldn't see " + user + "'s files: " + err.Error())
				return
			}
			s.browsed, s.whose = files, user
			byFolder := map[string]int{}
			for _, f := range files {
				byFolder[f.Folder()]++
			}
			s.folderList = s.folderList[:0]
			for f := range byFolder {
				s.folderList = append(s.folderList, f)
			}
			sort.Slice(s.folderList, func(i, j int) bool { return strings.ToLower(s.folderList[i]) < strings.ToLower(s.folderList[j]) })
			s.folders.SetRowCount(len(s.folderList))
			for i, f := range s.folderList {
				s.folders.SetItem(i, 0, cell(strings.ReplaceAll(f, `\`, "/")))
				s.folders.SetItem(i, 1, rightCell(fmt.Sprint(byFolder[f])))
			}
			s.note.SetText(fmt.Sprintf("%s shares %d files in %d folders. Double-click a file to download it.", user, len(files), len(s.folderList)))
			if len(s.folderList) > 0 {
				s.folders.SelectRow(0)
			}
		})
	}()
}

func (t *soulseekTab) fillBrowseFiles() {
	s := &t.browse
	s.inFolder = s.inFolder[:0]
	for _, f := range s.browsed {
		if f.Folder() == s.sel {
			s.inFolder = append(s.inFolder, f)
		}
	}
	s.files.SetRowCount(len(s.inFolder))
	for i, r := range s.inFolder {
		length := ""
		if r.Duration > 0 {
			length = core.FmtDuration(float64(r.Duration))
		}
		s.files.SetItem(i, 0, cell(slskBase(r.Path)))
		s.files.SetItem(i, 1, rightCell(core.FmtBytes(r.Size)))
		s.files.SetItem(i, 2, cell(slskQuality(r)))
		s.files.SetItem(i, 3, rightCell(length))
	}
}

func (t *soulseekTab) downloadBrowsed() {
	s := &t.browse
	if s.sel == "" {
		return
	}
	var list []core.SlskResult
	for _, f := range s.browsed {
		if f.Folder() == s.sel || strings.HasPrefix(f.Folder(), s.sel+`\`) {
			list = append(list, f)
		}
	}
	if !t.a.confirm("Download this folder?", fmt.Sprintf("%d files of %s's folder %s.", len(list), s.whose, slskBase(s.sel))) {
		return
	}
	for _, f := range list {
		t.a.m.SoulseekDownload(f.User, f.Path, f.Size)
	}
	t.a.toast(fmt.Sprintf("Getting %d files from %s.", len(list), s.whose))
}

// --- people -------------------------------------------------------------------------------------

type slskPerson struct {
	list string // buddies, banned, ignored ("" a heading)
	user core.SlskUser
}

type slskPeople struct {
	table *qt.QTableWidget
	rows  []slskPerson
	info  *qt.QLabel
	shown string
}

func (t *soulseekTab) buildPeople() *qt.QWidget {
	s := &t.people
	w := qt.NewQWidget2()
	box := qt.NewQHBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(12)
	left := vbox()
	left.SetSpacing(6)
	s.table = table("Person", "Status", "Shares")
	stretchColumn(s.table, 0, 200, 90, 160)
	s.table.OnCurrentCellChanged(func(r, _, _, _ int) {
		if r >= 0 && r < len(s.rows) && s.rows[r].list != "" {
			t.userInfo(s.rows[r].user.Name)
		}
	})
	t.tableMenu(s.table, func() string {
		if r := s.table.CurrentRow(); r >= 0 && r < len(s.rows) {
			return s.rows[r].user.Name
		}
		return ""
	})
	left.AddWidget2(s.table.QWidget, 1)
	acts := hbox()
	acts.SetSpacing(6)
	acts.AddWidget(t.a.iconButton("plus", "Add a buddy", "", "See when someone's online", func() {
		if v, ok := t.a.ask("Add a buddy", "Their Soulseek name:", ""); ok {
			if err := t.a.m.SoulseekSetUser("buddies", v, true); err != nil {
				t.a.message("Soulseek", err.Error())
			}
			t.fillPeople()
		}
	}).QWidget)
	acts.AddWidget(t.a.iconButton("x", "Take off the list", "", "", func() {
		if r := s.table.CurrentRow(); r >= 0 && r < len(s.rows) && s.rows[r].list != "" {
			t.a.m.SoulseekSetUser(s.rows[r].list, s.rows[r].user.Name, false)
			t.fillPeople()
		}
	}).QWidget)
	acts.AddStretch()
	left.AddLayout(acts.QLayout)
	box.AddLayout2(left.QLayout, 3)

	f, fb := card()
	s.info = richText("Pick someone on the left, or find people in Search and Interests.", "")
	s.info.SetAlignment(qt.AlignTop | qt.AlignLeft)
	scroll, inner, _ := scrolling()
	inner.AddWidget(s.info.QWidget)
	inner.AddStretch()
	fb.AddWidget2(scroll.QWidget, 1)
	row := hbox()
	row.SetSpacing(6)
	row.AddWidget(t.a.iconButton("message", "Message", "", "", func() { t.openChat(s.shown) }).QWidget)
	row.AddWidget(t.a.iconButton("folder", "Browse", "", "", func() { t.browseUser(s.shown) }).QWidget)
	row.AddStretch()
	fb.AddLayout(row.QLayout)
	box.AddWidget2(f.QWidget, 2)
	return w
}

func (t *soulseekTab) fillPeople() {
	s := &t.people
	cfg := t.a.m.Config().Soulseek
	sel := s.table.CurrentRow()
	s.rows = s.rows[:0]
	tb := s.table
	tb.SetUpdatesEnabled(false)
	defer tb.SetUpdatesEnabled(true)
	tb.ClearContents()
	var head []int
	section := func(title, list string, users []core.SlskUser) {
		head = append(head, len(s.rows))
		s.rows = append(s.rows, slskPerson{user: core.SlskUser{Name: title}})
		for _, u := range users {
			s.rows = append(s.rows, slskPerson{list: list, user: u})
		}
	}
	plain := func(names []string) []core.SlskUser {
		var out []core.SlskUser
		for _, n := range names {
			out = append(out, core.SlskUser{Name: n, Status: -1})
		}
		return out
	}
	section("Buddies", "buddies", t.a.m.SoulseekBuddies())
	section("Banned: can't download from you or see your files", "banned", plain(cfg.Banned))
	section("Ignored: their messages are hidden", "ignored", plain(cfg.Ignored))
	tb.SetRowCount(len(s.rows))
	for i, p := range s.rows {
		if p.list == "" {
			it := colored(p.user.Name, th.Text3)
			it.SetFlags(qt.NoItemFlags)
			tb.SetItem(i, 0, it)
			tb.SetSpan(i, 0, 1, 3)
			continue
		}
		tb.SetSpan(i, 0, 1, 1)
		color := th.Text3
		switch p.user.Status {
		case 2:
			color = th.OK
		case 1:
			color = th.Warn
		}
		extra := ""
		if p.user.Files > 0 {
			extra = fmt.Sprintf("%d files, %s", p.user.Files, core.FmtRate(float64(p.user.Speed)))
		}
		tb.SetItem(i, 0, cell("   "+p.user.Name))
		tb.SetItem(i, 1, colored(capitalized(p.user.StatusText()), color))
		tb.SetItem(i, 2, cell(extra))
	}
	if sel > 0 && sel < len(s.rows) && s.rows[sel].list != "" {
		tb.SelectRow(sel)
	}
}

// userInfo shows what's known about someone on the People page.
func (t *soulseekTab) userInfo(user string) {
	s := &t.people
	if t.page != slskPagePeople {
		t.showPage(slskPagePeople)
	}
	s.shown = user
	e := html.EscapeString
	s.info.SetText("<b>" + e(user) + "</b><br>Asking…")
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 70*time.Second)
		defer cancel()
		u, err := t.a.m.SoulseekUserInfo(ctx, user)
		likes, hates, _ := t.a.m.SoulseekUserInterests(ctx, user)
		mainthread.Start(func() {
			if s.shown != user {
				return
			}
			var b strings.Builder
			fmt.Fprintf(&b, "<b style='font-size:large'>%s</b><br>%s", e(u.Name), e(capitalized(u.StatusText())))
			if u.Privileged {
				b.WriteString(" · privileged")
			}
			if u.Country != "" {
				b.WriteString(" · " + e(u.Country))
			}
			fmt.Fprintf(&b, "<p>Shares %d files in %d folders, sends at about %s.</p>", u.Files, u.Folders, core.FmtRate(float64(u.Speed)))
			if err == nil {
				free := "No free upload slot right now"
				if u.FreeSlot {
					free = "A free upload slot right now"
				}
				fmt.Fprintf(&b, "<p>%d upload slots, %d people in line. %s.</p>", u.Slots, u.Queue, free)
				if u.Description != "" {
					fmt.Fprintf(&b, "<p>%s</p>", strings.ReplaceAll(e(u.Description), "\n", "<br>"))
				}
			} else {
				fmt.Fprintf(&b, "<p style='color:%s'>%s</p>", th.Warn, e(err.Error()))
			}
			if len(likes) > 0 {
				fmt.Fprintf(&b, "<p><b>Likes:</b> %s</p>", e(strings.Join(likes, ", ")))
			}
			if len(hates) > 0 {
				fmt.Fprintf(&b, "<p><b>Dislikes:</b> %s</p>", e(strings.Join(hates, ", ")))
			}
			s.info.SetText(b.String())
		})
	}()
}

// --- chat ---------------------------------------------------------------------------------------

type slskChat struct {
	convs     *qt.QListWidget
	keys      []string
	view      *qt.QTextEdit
	input     *qt.QLineEdit
	title     *qt.QLabel
	current   string
	seq       int64
	filling   bool
	placement *qt.QStackedWidget
}

func (t *soulseekTab) buildChat() *qt.QWidget {
	s := &t.chat
	s.seq = -1
	w := qt.NewQWidget2()
	box := qt.NewQHBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(12)
	left := vbox()
	left.SetSpacing(6)
	s.convs = qt.NewQListWidget2()
	s.convs.SetFixedWidth(220)
	s.convs.OnCurrentRowChanged(func(r int) {
		if !s.filling && r >= 0 && r < len(s.keys) {
			s.current = s.keys[r]
			s.seq = -1
			t.refreshChats()
		}
	})
	left.AddWidget2(s.convs.QWidget, 1)
	left.AddWidget(t.a.iconButton("x", "Close this chat", "", "Leaves a room, so it isn't joined next time", func() {
		if s.current != "" {
			t.a.m.SoulseekCloseChat(s.current)
			s.current, s.seq = "", -1
			t.refreshChats()
		}
	}).QWidget)
	box.AddLayout(left.QLayout)
	right := vbox()
	right.SetSpacing(6)
	s.title = text("Pick a chat, message someone, or join a room", "h2")
	right.AddWidget(s.title.QWidget)
	s.view = qt.NewQTextEdit2()
	s.view.SetReadOnly(true)
	right.AddWidget2(s.view.QWidget, 1)
	row := hbox()
	row.SetSpacing(6)
	s.input = qt.NewQLineEdit2()
	s.input.SetPlaceholderText("Write a message")
	s.input.OnReturnPressed(t.send)
	row.AddWidget2(s.input.QWidget, 1)
	row.AddWidget(t.a.iconButton("message", "Send", "primary", "", t.send).QWidget)
	right.AddLayout(row.QLayout)
	box.AddLayout2(right.QLayout, 1)
	return w
}

func (t *soulseekTab) refreshChats() {
	s := &t.chat
	convs, seq := t.a.m.SoulseekChats()
	if seq == s.seq {
		if s.current != "" && t.a.current() == pageSoulseek && t.page == slskPageChat {
			t.a.m.SoulseekMarkRead(s.current)
		}
		return
	}
	s.seq = seq
	s.filling = true
	s.keys = s.keys[:0]
	s.convs.Clear()
	sel := -1
	for i, cv := range convs {
		k := cv.Key()
		s.keys = append(s.keys, k)
		label := cv.Name
		if cv.Room {
			label = "# " + cv.Name
			if !cv.Joined {
				label += " (left)"
			}
		}
		if cv.Unread > 0 && !cv.Room {
			label += fmt.Sprintf("  (%d)", cv.Unread)
		}
		s.convs.AddItem(label)
		if k == s.current {
			sel = i
		}
	}
	if sel < 0 && len(convs) > 0 {
		sel = len(convs) - 1
		s.current = s.keys[sel]
	}
	if len(convs) == 0 {
		s.current = ""
	}
	if sel >= 0 {
		s.convs.SetCurrentRow(sel)
	}
	s.filling = false
	t.fillChat(convs)
}

// fillChat shows the selected conversation.
func (t *soulseekTab) fillChat(convs []core.SlskConversation) {
	s := &t.chat
	if s.current == "" {
		s.view.SetHtml("")
		s.title.SetText("Pick a chat, message someone, or join a room")
		s.input.SetEnabled(false)
		return
	}
	s.input.SetEnabled(true)
	e := html.EscapeString
	for _, cv := range convs {
		if cv.Key() != s.current {
			continue
		}
		var b strings.Builder
		for _, l := range cv.Lines {
			fmt.Fprintf(&b, "<span style='color:%s'>%s</span>&nbsp; ", th.Text3, l.At.Format("15:04"))
			switch {
			case l.From == "" && l.Own && cv.Room:
				fmt.Fprintf(&b, "<span style='color:%s'>%s</span><br>", th.Text3, e(l.Text))
				continue
			case l.Own:
				fmt.Fprintf(&b, "<b style='color:%s'>you</b>: ", th.Accent)
			default:
				fmt.Fprintf(&b, "<b>%s</b>: ", e(l.From))
			}
			b.WriteString(e(l.Text) + "<br>")
		}
		if cv.Room {
			s.title.SetText(fmt.Sprintf("# %s  ·  %d people", cv.Name, len(cv.Users)))
			s.input.SetPlaceholderText("Say something to the room")
		} else {
			s.title.SetText(cv.Name)
			s.input.SetPlaceholderText("Write to " + cv.Name)
		}
		s.view.SetHtml(b.String())
		s.view.MoveCursor(qt.QTextCursor__End)
		if t.a.current() == pageSoulseek && t.page == slskPageChat {
			t.a.m.SoulseekMarkRead(s.current)
		}
		return
	}
}

func (t *soulseekTab) send() {
	s := &t.chat
	msg := strings.TrimSpace(s.input.Text())
	if msg == "" || s.current == "" {
		return
	}
	if err := t.a.m.SoulseekSay(s.current, msg); err != nil {
		t.a.message("Not sent", err.Error())
		return
	}
	s.input.SetText("")
	s.seq = -1
	t.refreshChats()
}

// --- interests ----------------------------------------------------------------------------------

type slskLike struct {
	item string
	hate bool
}

type slskInterests struct {
	likes    *qt.QListWidget
	likeRows []slskLike
	recs     *qt.QTableWidget
	recRows  []core.SlskRecommendation
	similar  *qt.QTableWidget
	simRows  []core.SlskUserScore
	recsFor  string
	global   bool
	loaded   bool
	title    *qt.QLabel
}

func (t *soulseekTab) buildInterests() *qt.QWidget {
	s := &t.interests
	w := qt.NewQWidget2()
	box := qt.NewQHBoxLayout(w)
	box.SetContentsMargins(0, 0, 0, 0)
	box.SetSpacing(12)

	left := vbox()
	left.SetSpacing(6)
	left.AddWidget(text("What you like", "h2").QWidget)
	left.AddWidget(text("Artists, genres, anything. People who like the same find you, and you them.", "hint").QWidget)
	s.likes = qt.NewQListWidget2()
	s.likes.SetMinimumWidth(190)
	s.likes.OnItemDoubleClicked(func(*qt.QListWidgetItem) {
		if r := s.likes.CurrentRow(); r >= 0 && r < len(s.likeRows) {
			t.loadRecs(s.likeRows[r].item)
		}
	})
	left.AddWidget2(s.likes.QWidget, 1)
	acts := hbox()
	acts.SetSpacing(6)
	addLike := func(hate bool, title, help string) func() {
		return func() {
			if v, ok := t.a.ask(title, help, ""); ok && v != "" {
				if err := t.a.m.SoulseekSetInterest(v, hate, false); err != nil {
					t.a.message("Soulseek", err.Error())
				}
				t.fillLikes()
			}
		}
	}
	acts.AddWidget(t.a.iconButton("heart", "Like", "", "", addLike(false, "Something you like", "An artist, a genre, anything:")).QWidget)
	acts.AddWidget(button("Dislike", "", addLike(true, "Something you dislike", "Recommendations steer away from it:")).QWidget)
	acts.AddWidget(button("Remove", "", func() {
		if r := s.likes.CurrentRow(); r >= 0 && r < len(s.likeRows) {
			t.a.m.SoulseekSetInterest(s.likeRows[r].item, s.likeRows[r].hate, true)
			t.fillLikes()
		}
	}).QWidget)
	left.AddLayout(acts.QLayout)
	box.AddLayout2(left.QLayout, 1)

	mid := vbox()
	mid.SetSpacing(6)
	s.title = text("Recommended for you", "h2")
	mid.AddWidget(s.title.QWidget)
	s.recs = table("Interest", "People")
	stretchColumn(s.recs, 0, 200, 70)
	s.recs.OnCellDoubleClicked(func(r, _ int) {
		if r >= 0 && r < len(s.recRows) {
			t.loadRecs(s.recRows[r].Item)
		}
	})
	mid.AddWidget2(s.recs.QWidget, 1)
	recActs := hbox()
	recActs.SetSpacing(6)
	sel := func() *core.SlskRecommendation {
		if r := s.recs.CurrentRow(); r >= 0 && r < len(s.recRows) {
			return &s.recRows[r]
		}
		return nil
	}
	recActs.AddWidget(button("Mine", "", func() { s.global = false; t.loadRecs("") }).QWidget)
	recActs.AddWidget(button("Everyone's", "", func() { s.global = true; t.loadRecs("") }).QWidget)
	recActs.AddStretch()
	recActs.AddWidget(t.a.iconButton("heart", "Like", "", "Add to what you like", func() {
		if r := sel(); r != nil {
			t.a.m.SoulseekSetInterest(r.Item, false, false)
			t.fillLikes()
		}
	}).QWidget)
	recActs.AddWidget(t.a.iconButton("search", "Search", "", "Look for this on Soulseek", func() {
		if r := sel(); r != nil {
			t.showPage(slskPageSearch)
			t.search.find.SetText(r.Item)
			t.runSearch(r.Item)
		}
	}).QWidget)
	mid.AddLayout(recActs.QLayout)
	box.AddLayout2(mid.QLayout, 2)

	right := vbox()
	right.SetSpacing(6)
	right.AddWidget(text("People like you", "h2").QWidget)
	s.similar = table("Person", "Match")
	stretchColumn(s.similar, 0, 160, 60)
	t.tableMenu(s.similar, func() string {
		if r := s.similar.CurrentRow(); r >= 0 && r < len(s.simRows) {
			return s.simRows[r].Name
		}
		return ""
	})
	s.similar.OnCellDoubleClicked(func(r, _ int) {
		if r >= 0 && r < len(s.simRows) {
			t.userInfo(s.simRows[r].Name)
		}
	})
	right.AddWidget2(s.similar.QWidget, 1)
	box.AddLayout2(right.QLayout, 1)
	return w
}

func (t *soulseekTab) fillLikes() {
	s := &t.interests
	cfg := t.a.m.Config().Soulseek
	s.likes.Clear()
	s.likeRows = s.likeRows[:0]
	for _, x := range cfg.Likes {
		s.likes.AddItem("+  " + x)
		s.likeRows = append(s.likeRows, slskLike{item: x})
	}
	for _, x := range cfg.Hates {
		s.likes.AddItem("−  " + x)
		s.likeRows = append(s.likeRows, slskLike{item: x, hate: true})
	}
}

// loadRecs loads recommendations and similar people: yours ("") or for an item.
func (t *soulseekTab) loadRecs(item string) {
	s := &t.interests
	s.loaded = true
	s.recsFor = item
	switch {
	case item != "":
		s.title.SetText("People who like " + item + " also like")
	case s.global:
		s.title.SetText("What everyone likes")
	default:
		s.title.SetText("Recommended for you")
	}
	global := s.global
	go func() {
		ctx, cancel := context.WithTimeout(t.a.m.Context(), 30*time.Second)
		defer cancel()
		var recs []core.SlskRecommendation
		var users []core.SlskUserScore
		var err error
		if item != "" {
			recs, err = t.a.m.SoulseekItemRecommendations(ctx, item)
			users, _ = t.a.m.SoulseekSimilarUsers(ctx, item)
		} else {
			recs, err = t.a.m.SoulseekRecommendations(ctx, global)
			users, _ = t.a.m.SoulseekSimilarUsers(ctx, "")
		}
		mainthread.Start(func() {
			if s.recsFor != item {
				return
			}
			if err != nil {
				s.title.SetText("Recommendations aren't available: " + err.Error())
			}
			if err == nil && len(recs) == 0 && item == "" && !global && len(t.a.m.Config().Soulseek.Likes) == 0 {
				s.title.SetText("Add what you like to get recommendations")
			}
			s.recRows, s.simRows = recs, users
			s.recs.SetRowCount(len(recs))
			for i, r := range recs {
				color := th.Text
				if r.Score < 0 {
					color = th.Bad
				}
				s.recs.SetItem(i, 0, colored(r.Item, color))
				s.recs.SetItem(i, 1, rightCell(fmt.Sprint(r.Score)))
			}
			s.similar.SetRowCount(len(users))
			for i, u := range users {
				s.similar.SetItem(i, 0, cell(u.Name))
				score := ""
				if u.Score > 0 {
					score = fmt.Sprint(u.Score)
				}
				s.similar.SetItem(i, 1, rightCell(score))
			}
		})
	}()
}
