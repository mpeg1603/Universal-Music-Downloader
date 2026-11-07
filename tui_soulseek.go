package main

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The Soulseek tab: the connection, what's shared, transfers both ways, and chat.

type slskUI struct {
	status    *tview.TextView
	transfers *tview.Table
	convs     *tview.Table
	chatView  *tview.TextView
	input     *tview.InputField
	xfers     []slskTransferView
	convKeys  []string
	current   string // the conversation shown
	chatSeq   int64
	shown     string // the conversation whose lines are in chatView
	portNote  string
}

func (ui *tui) buildSoulseek() tview.Primitive {
	s := &slskUI{chatSeq: -1}
	ui.sk = s
	s.status = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	s.status.SetBorder(true).SetTitle(" Soulseek ")
	s.transfers = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.transfers.SetBorder(true).SetTitle(" Transfers ")
	s.convs = tview.NewTable().SetSelectable(true, false)
	s.convs.SetBorder(true).SetTitle(" Chats ")
	s.chatView = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	s.chatView.SetBorder(true)
	s.input = tview.NewInputField().SetLabel(" Say: ").SetFieldWidth(0)
	s.input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			ui.slskSend()
		case tcell.KeyEscape:
			ui.app.SetFocus(s.convs)
		case tcell.KeyTab:
			ui.app.SetFocus(s.transfers)
		}
	})
	s.convs.SetSelectionChangedFunc(func(row, col int) {
		if row >= 0 && row < len(s.convKeys) {
			s.current = s.convKeys[row]
			ui.refreshChat(true)
		}
	})
	s.convs.SetSelectedFunc(func(row, col int) { ui.app.SetFocus(s.input) })
	keys := func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyTab {
			switch ui.app.GetFocus() {
			case s.transfers:
				ui.app.SetFocus(s.convs)
			default:
				ui.app.SetFocus(s.input)
			}
			return nil
		}
		if ev.Key() != tcell.KeyRune {
			return ev
		}
		c := ui.m.Soulseek()
		switch ev.Rune() {
		case 'R':
			if c != nil {
				c.Reconnect()
			}
		case 'o':
			ui.slskPortCheck()
		case 'n':
			ui.askText("Message a Soulseek user", "Their username", "", func(v string) {
				if c := ui.m.Soulseek(); c != nil && v != "" {
					c.OpenPrivate(v)
					s.current = chatKey(v, false)
					s.chatSeq = -1
					ui.refreshSoulseek()
					ui.app.SetFocus(s.input)
				}
			})
		case 'j':
			ui.slskJoinRoom()
		default:
			return ev
		}
		return nil
	}
	s.transfers.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.transfers.GetSelection()
		var x *slskTransferView
		if row >= 1 && row-1 < len(s.xfers) {
			x = &s.xfers[row-1]
		}
		c := ui.m.Soulseek()
		switch {
		case ev.Rune() == 'x' && x != nil && x.Up && c != nil:
			c.ups.Abort(x.User, x.File)
			return nil
		case ev.Rune() == 'x' && x != nil && !x.Up:
			ui.message("Soulseek", "Downloads belong to tracks: cancel the track in the Queue tab (c).")
			return nil
		case ev.Rune() == 'm' && x != nil && c != nil:
			c.OpenPrivate(x.User)
			s.current, s.chatSeq = chatKey(x.User, false), -1
			ui.refreshSoulseek()
			ui.app.SetFocus(s.input)
			return nil
		case ev.Rune() == 'B' && x != nil:
			user := x.User
			ui.confirm("Ban "+user+"? They can't download from you or see your files any more.", "Ban", func() {
				if ui.update(func(c *Config) {
					if !containsFold(c.Soulseek.Banned, user) {
						c.Soulseek.Banned = append(c.Soulseek.Banned, user)
					}
				}) {
					if c := ui.m.Soulseek(); c != nil {
						c.ups.dropUser(user)
					}
				}
			})
			return nil
		}
		return keys(ev)
	})
	s.convs.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		c := ui.m.Soulseek()
		if ev.Rune() == 'l' && c != nil && s.current != "" {
			name, room := strings.TrimLeft(s.current, "#@"), strings.HasPrefix(s.current, "#")
			if room {
				c.LeaveRoom(name)
				ui.update(func(cfg *Config) {
					var keep []string
					for _, r := range cfg.Soulseek.Rooms {
						if r != name {
							keep = append(keep, r)
						}
					}
					cfg.Soulseek.Rooms = keep
				})
			}
			c.chat.close(s.current)
			s.current, s.chatSeq = "", -1
			ui.refreshSoulseek()
			return nil
		}
		return keys(ev)
	})
	chat := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.chatView, 0, 1, false).
		AddItem(s.input, 1, 0, false)
	bottom := tview.NewFlex().
		AddItem(s.convs, 26, 0, false).
		AddItem(chat, 0, 1, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.status, 7, 0, false).
		AddItem(s.transfers, 0, 1, true).
		AddItem(bottom, 0, 1, false)
}

func (ui *tui) refreshSoulseek() {
	s := ui.sk
	c := ui.m.Soulseek()
	cfg := ui.m.Config()
	var b strings.Builder
	if c == nil {
		switch {
		case !cfg.Soulseek.configured():
			b.WriteString("[yellow]Soulseek is off.[-] Put your Soulseek username and password in Settings → Soulseek (a name nobody " +
				"uses yet makes a new account), or import them from Nicotine+ there; then turn the source on in the Sources tab (space).")
		default:
			b.WriteString("[yellow]Soulseek is off.[-] Turn the Soulseek source on in the Sources tab (space) to log in as " + tview.Escape(cfg.Soulseek.Username) + ".")
		}
		s.status.SetText(b.String())
		s.transfers.Clear()
		s.xfers = nil
		ui.refreshChats(nil)
		return
	}
	st := c.State()
	switch {
	case st.Online:
		fmt.Fprintf(&b, "[green]● online[-] as [::b]%s[::-] on %s since %s · your address %s", tview.Escape(st.User), tview.Escape(st.Server),
			st.Since.Format("15:04"), st.PublicIP)
		if st.Privileges > 0 {
			fmt.Fprintf(&b, " · [yellow]privileged[-] for %s", fmtWait(st.Privileges.Round(time.Hour)))
		}
	case st.Connecting:
		b.WriteString("[yellow]◌ connecting to " + tview.Escape(st.Server) + "…[-]")
	default:
		b.WriteString("[red]● offline: " + tview.Escape(firstNonEmpty(st.Problem, "not connected")) + "[-]")
		if st.Permanent {
			b.WriteString(" (R reconnects)")
		}
	}
	b.WriteString("\n")
	if st.Port > 0 {
		fmt.Fprintf(&b, "Listening on port %d", st.Port)
		if st.PortNote != "" {
			b.WriteString(" · " + tview.Escape(st.PortNote))
		}
		if s.portNote != "" {
			b.WriteString(" · " + s.portNote)
		} else {
			b.WriteString(" · [::d]o: check whether others can reach it[::-]")
		}
	} else {
		b.WriteString("[orange]Not listening for peers[-]: only users with open ports can be reached")
	}
	b.WriteString("\n")
	sh := ui.m.shares.Status()
	switch {
	case cfg.Soulseek.NoShare:
		b.WriteString("[orange]Not sharing[-] (many users don't send files to people who share nothing)")
	case sh.Scanning:
		b.WriteString("[yellow]Reading the shared folders…[-]")
	case sh.Scanned.IsZero():
		b.WriteString("[yellow]The shared folders are read once your library scan is done[-]")
	default:
		var names []string
		for _, r := range sh.Roots {
			names = append(names, r.Name)
		}
		fmt.Fprintf(&b, "Sharing %d files in %d folders (%s)", sh.Files, sh.Folders, tview.Escape(strings.Join(names, ", ")))
		if sh.Problem != "" {
			b.WriteString(" · [orange]" + tview.Escape(sh.Problem) + "[-]")
		}
	}
	fmt.Fprintf(&b, " · answered %d of %d searches", c.searchHits.Load(), c.searchReqs.Load())
	if parent, level := c.Parent(); parent != "" {
		fmt.Fprintf(&b, " · search network via %s (level %d)", tview.Escape(parent), level)
	}
	b.WriteString("\n")
	used, limit, waiting := c.dlGate.stats()
	fmt.Fprintf(&b, "Downloads: %d of %d at once", used, limit)
	if waiting > 0 {
		fmt.Fprintf(&b, " (+%d waiting)", waiting)
	}
	fmt.Fprintf(&b, " · a search every %s", fmtWait(cfg.Soulseek.searchGap()))
	if bl := c.searchBacklog(); bl > time.Second {
		fmt.Fprintf(&b, " (next free in %s)", fmtWait(bl.Round(time.Second)))
	}
	act, queued, speed := c.UploadCounts()
	fmt.Fprintf(&b, " · Uploads: %d of %d slots", act, cfg.Soulseek.uploadSlots())
	if speed > 0 {
		b.WriteString(", " + fmtRate(speed))
	}
	if queued > 0 {
		fmt.Fprintf(&b, ", %d queued", queued)
	}
	if cfg.Soulseek.UploadKBps > 0 {
		fmt.Fprintf(&b, " (limit %d KB/s)", cfg.Soulseek.UploadKBps)
	}
	b.WriteString("\n")
	every, next := c.WishlistStatus()
	fmt.Fprintf(&b, "Wishlist: %d tracks no source had, one searched every %s", ui.m.WishlistSize(), fmtWait(every))
	if !next.IsZero() && st.Online {
		fmt.Fprintf(&b, " (next %s)", next.Format("15:04"))
	}
	if st.MOTD != "" {
		b.WriteString("\n[::d]" + tview.Escape(fitWidth(strings.ReplaceAll(st.MOTD, "\n", " "), 300)) + "[::-]")
	}
	s.status.SetText(b.String())

	// Transfers.
	s.xfers = c.Transfers()
	t := s.transfers
	t.Clear()
	headerRow(t, "", "User", "File", "State", "Progress", "Speed")
	for i, x := range s.xfers {
		dir, color := "↓", tcell.ColorAqua
		if x.Up {
			dir, color = "↑", tcell.ColorGreen
		}
		user := x.User
		if x.Privileged {
			user = "★ " + user
		}
		file := strings.ReplaceAll(x.File, `\`, "/")
		file = path.Base(path.Dir(file)) + "/" + path.Base(file)
		prog := ""
		if x.Total > 0 && x.Done > 0 {
			frac := float64(x.Done) / float64(x.Total)
			prog = fmt.Sprintf("%s %3.0f%%", tviewBar(frac, 10), frac*100)
		} else if x.Total > 0 {
			prog = fmtBytes(x.Total)
		}
		spd := ""
		if x.Speed > 0 && (x.State == "sending" || x.State == "downloading") {
			spd = fmtRate(x.Speed)
		}
		scolor := tcell.ColorDefault
		switch {
		case x.State == "done":
			scolor = tcell.ColorGreen
		case strings.HasPrefix(x.State, "failed"), x.State == "cancelled":
			scolor = tcell.ColorGray
		case x.State == "sending", x.State == "downloading":
			scolor = tcell.ColorYellow
		}
		cells := []*tview.TableCell{
			tview.NewTableCell(dir).SetTextColor(color),
			tview.NewTableCell(tview.Escape(user)).SetMaxWidth(24),
			tview.NewTableCell(tview.Escape(file)).SetExpansion(1).SetMaxWidth(80),
			tview.NewTableCell(tview.Escape(x.State)).SetTextColor(scolor).SetMaxWidth(40),
			tview.NewTableCell(prog),
			tview.NewTableCell(spd).SetAlign(tview.AlignRight),
		}
		for col, cell := range cells {
			t.SetCell(i+1, col, cell)
		}
	}
	if len(s.xfers) == 0 {
		t.SetCell(1, 2, tview.NewTableCell("[::d]nothing yet: downloads appear while tracks come from Soulseek, uploads when people download from you[::-]").SetSelectable(false))
	}
	ui.refreshChats(c)
}

func (ui *tui) refreshChats(c *slskClient) {
	s := ui.sk
	var convs []slskConversation
	var seq int64
	if c != nil {
		convs, seq = c.chat.Conversations()
	}
	if seq == s.chatSeq && c != nil {
		if s.current != "" && ui.current == "soulseek" {
			c.chat.markRead(s.current) // read while shown
		}
		return
	}
	s.chatSeq = seq
	s.convKeys = s.convKeys[:0]
	t := s.convs
	t.Clear()
	sel := -1
	for i, cv := range convs {
		k := cv.key()
		s.convKeys = append(s.convKeys, k)
		label := "@ " + cv.Name
		if cv.Room {
			label = "# " + cv.Name
			if !cv.Joined {
				label += " (left)"
			}
		}
		cell := tview.NewTableCell(tview.Escape(label)).SetExpansion(1)
		if cv.Unread > 0 && !cv.Room {
			cell.SetText(tview.Escape(fmt.Sprintf("%s (%d)", label, cv.Unread))).SetTextColor(tcell.ColorYellow)
		}
		t.SetCell(i, 0, cell)
		if k == s.current {
			sel = i
		}
	}
	if len(convs) == 0 {
		t.SetCell(0, 0, tview.NewTableCell("[::d]n: message someone\nj: join a room[::-]").SetSelectable(false))
		s.current = ""
	} else if sel < 0 {
		sel = len(convs) - 1
		s.current = s.convKeys[sel]
	}
	if sel >= 0 {
		t.Select(sel, 0)
	}
	ui.refreshChat(false)
}

// refreshChat shows the selected conversation.
func (ui *tui) refreshChat(force bool) {
	s := ui.sk
	c := ui.m.Soulseek()
	if c == nil || s.current == "" {
		s.chatView.SetText("")
		s.chatView.SetTitle(" Chat ")
		s.input.SetLabel(" Say: ")
		return
	}
	convs, _ := c.chat.Conversations()
	for _, cv := range convs {
		if cv.key() != s.current {
			continue
		}
		var b strings.Builder
		for _, l := range cv.Lines {
			fmt.Fprintf(&b, "[gray]%s[-] ", l.At.Format("15:04"))
			switch {
			case l.From == "" && l.Own && cv.Room:
				b.WriteString("[gray]" + tview.Escape(l.Text) + "[-]\n")
				continue
			case l.Own:
				b.WriteString("[green::b]you[-::-]: ")
			default:
				b.WriteString("[::b]" + tview.Escape(l.From) + "[::-]: ")
			}
			b.WriteString(tview.Escape(l.Text) + "\n")
		}
		title := " " + strings.TrimLeft(cv.key(), "@") + " "
		if cv.Room {
			title = fmt.Sprintf(" %s · %d users ", cv.key(), len(cv.Users))
			s.input.SetLabel(" Say in " + cv.key() + ": ")
		} else {
			s.input.SetLabel(" To " + cv.Name + ": ")
		}
		s.chatView.SetTitle(title)
		s.chatView.SetText(b.String())
		s.chatView.ScrollToEnd()
		s.shown = s.current
		c.chat.markRead(s.current)
		return
	}
}

func (ui *tui) slskSend() {
	s := ui.sk
	c := ui.m.Soulseek()
	text := strings.TrimSpace(s.input.GetText())
	if c == nil || text == "" || s.current == "" {
		return
	}
	name := strings.TrimLeft(s.current, "#@")
	var err error
	if strings.HasPrefix(s.current, "#") {
		err = c.Say(name, text)
	} else {
		err = c.SendPrivate(name, text)
	}
	if err != nil {
		ui.message("Not sent", err.Error())
		return
	}
	s.input.SetText("")
	s.chatSeq = -1
	ui.refreshSoulseek()
}

// slskJoinRoom offers the server's room list, biggest first.
func (ui *tui) slskJoinRoom() {
	c := ui.m.Soulseek()
	if c == nil {
		return
	}
	join := func(room string) {
		if room == "" {
			return
		}
		if err := c.JoinRoom(room); err != nil {
			ui.message("Can't join", err.Error())
			return
		}
		ui.sk.current, ui.sk.chatSeq = chatKey(room, true), -1
		ui.update(func(cfg *Config) {
			if !containsString(cfg.Soulseek.Rooms, room) {
				cfg.Soulseek.Rooms = append(cfg.Soulseek.Rooms, room)
			}
		})
	}
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("Type a room name…", "", 0, func() {
		ui.closeModal("rooms")
		ui.askText("Join a Soulseek room", "Its name (a new name makes a new room)", "", join)
	})
	for i, r := range c.chat.Rooms() {
		if i >= 300 {
			break
		}
		r := r
		list.AddItem(tview.Escape(fmt.Sprintf("%s (%d)", r.Name, r.Users)), "", 0, func() {
			ui.closeModal("rooms")
			join(r.Name)
		})
	}
	list.SetDoneFunc(func() { ui.closeModal("rooms") })
	list.SetBorder(true).SetTitle(" Join a room (Esc closes) ")
	ui.openModal("rooms", list, 60, 24)
}

func (ui *tui) slskPortCheck() {
	s := ui.sk
	s.portNote = "[yellow]checking the port…[-]"
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.ctx, 30*time.Second)
		defer cancel()
		ok, port, err := ui.m.CheckSoulseekPort(ctx)
		ui.app.QueueUpdateDraw(func() {
			switch {
			case err != nil:
				s.portNote = "[orange]port check: " + tview.Escape(err.Error()) + "[-]"
			case ok:
				s.portNote = fmt.Sprintf("[green]port %d is open to others[-]", port)
			default:
				s.portNote = fmt.Sprintf("[orange]port %d is closed to others[-] (users behind closed ports can't reach you; forward it in your router or use UPnP and the \"direct\" route)", port)
			}
		})
	}()
}

func (ui *tui) focusSoulseek() {
	ui.app.SetFocus(ui.sk.transfers)
}
