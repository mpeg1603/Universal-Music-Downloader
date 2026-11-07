package tui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"spotify-downloader/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The Soulseek tab: the connection and what's shared, searching the network and downloading
// files or whole folders by hand, transfers both ways, and chat.

type slskUI struct {
	info      *tview.TextView
	find      *tview.InputField
	results   *tview.Table
	res       []core.SlskResult
	searching bool
	transfers *tview.Table
	xfers     []core.SlskTransferView
	convs     *tview.Table
	chatView  *tview.TextView
	input     *tview.InputField
	convKeys  []string
	current   string // the conversation shown
	chatSeq   int64
	portNote  string
}

func (ui *tui) buildSoulseek() tview.Primitive {
	s := &slskUI{chatSeq: -1}
	ui.sk = s
	s.info = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	s.info.SetBorder(true).SetTitle(" Soulseek ")
	s.find = tview.NewInputField().SetLabel(" Search Soulseek: ").SetFieldWidth(0).SetPlaceholder("words in file and folder names, Enter searches")
	s.results = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.results.SetBorder(true).SetTitle(" Results (Enter downloads the file · d the whole folder · m message the user) ")
	s.transfers = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.transfers.SetBorder(true).SetTitle(" Transfers ")
	s.convs = tview.NewTable().SetSelectable(true, false)
	s.convs.SetBorder(true).SetTitle(" Chats ")
	s.chatView = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	s.chatView.SetBorder(true)
	s.input = tview.NewInputField().SetLabel(" Say: ").SetFieldWidth(0)

	order := []tview.Primitive{s.find, s.results, s.transfers, s.convs, s.input}
	cycle := func(from tview.Primitive, step int) {
		for i, p := range order {
			if p == from {
				ui.app.SetFocus(order[(i+step+len(order))%len(order)])
				return
			}
		}
	}
	s.find.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEnter:
			ui.slskSearch(s.find.GetText())
		case tcell.KeyTab, tcell.KeyEscape, tcell.KeyDown:
			cycle(s.find, 1)
		case tcell.KeyBacktab:
			cycle(s.find, -1)
		}
	})
	s.input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			ui.slskSend()
		case tcell.KeyEscape:
			ui.app.SetFocus(s.convs)
		case tcell.KeyTab:
			cycle(s.input, 1)
		case tcell.KeyBacktab:
			cycle(s.input, -1)
		}
	})
	s.convs.SetSelectionChangedFunc(func(row, col int) {
		if row >= 0 && row < len(s.convKeys) {
			s.current = s.convKeys[row]
			ui.refreshChat()
		}
	})
	s.convs.SetSelectedFunc(func(row, col int) { ui.app.SetFocus(s.input) })
	message := func(user string) {
		ui.m.SoulseekOpenChat(user)
		s.current, s.chatSeq = core.ChatKey(user, false), -1
		ui.refreshSoulseek()
		ui.app.SetFocus(s.input)
	}
	// keys common to the lists
	keys := func(from tview.Primitive, ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyTab:
			cycle(from, 1)
			return nil
		case ev.Key() == tcell.KeyBacktab:
			cycle(from, -1)
			return nil
		case ev.Key() != tcell.KeyRune:
			return ev
		}
		switch ev.Rune() {
		case '/':
			ui.app.SetFocus(s.find)
		case 'R':
			ui.m.SoulseekReconnect()
		case 'o':
			ui.slskPortCheck()
		case 'n':
			ui.askText("Message a Soulseek user", "Their username", "", func(v string) {
				if v != "" {
					message(v)
				}
			})
		case 'j':
			ui.slskJoinRoom()
		default:
			return ev
		}
		return nil
	}
	s.results.SetSelectedFunc(func(row, col int) {
		if row >= 1 && row-1 < len(s.res) {
			r := s.res[row-1]
			ui.m.SoulseekDownload(r.User, r.Path, r.Size)
			s.results.SetTitle(" [green]queued " + tview.Escape(baseName(r.Path)) + " from " + tview.Escape(r.User) + "[-] ")
		}
	})
	s.results.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.results.GetSelection()
		var r *core.SlskResult
		if row >= 1 && row-1 < len(s.res) {
			r = &s.res[row-1]
		}
		switch {
		case ev.Rune() == 'd' && r != nil:
			folder, user := r.Folder(), r.User
			ui.confirm(fmt.Sprintf("Download all of %s's folder %s?", user, baseName(folder)), "Download", func() {
				if err := ui.m.SoulseekDownloadFolder(user, folder); err != nil {
					ui.message("Soulseek", err.Error())
					return
				}
				s.results.SetTitle(" [green]asked " + tview.Escape(user) + " for the folder " + tview.Escape(baseName(folder)) + "[-] ")
			})
			return nil
		case ev.Rune() == 'm' && r != nil:
			message(r.User)
			return nil
		case ev.Key() == tcell.KeyUp:
			if row <= 1 {
				ui.app.SetFocus(s.find)
				return nil
			}
		}
		return keys(s.results, ev)
	})
	s.transfers.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.transfers.GetSelection()
		var x *core.SlskTransferView
		if row >= 1 && row-1 < len(s.xfers) {
			x = &s.xfers[row-1]
		}
		switch {
		case ev.Rune() == 'x' && x != nil && x.Up:
			ui.m.SoulseekAbortUpload(x.User, x.File)
			return nil
		case ev.Rune() == 'x' && x != nil:
			ui.message("Soulseek", "A download stops when its track is cancelled (Queue tab, c), or when you quit.")
			return nil
		case ev.Rune() == 'm' && x != nil:
			message(x.User)
			return nil
		case ev.Rune() == 'B' && x != nil:
			user := x.User
			ui.confirm("Ban "+user+"? They can't download from you or see your files any more.", "Ban", func() {
				if err := ui.m.SoulseekBan(user); err != nil {
					ui.message("Can't ban", err.Error())
				}
			})
			return nil
		}
		return keys(s.transfers, ev)
	})
	s.convs.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Rune() == 'l' && s.current != "" {
			ui.m.SoulseekCloseChat(s.current)
			s.current, s.chatSeq = "", -1
			ui.refreshSoulseek()
			return nil
		}
		return keys(s.convs, ev)
	})
	search := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.find, 1, 0, false).
		AddItem(s.results, 0, 1, true)
	middle := tview.NewFlex().
		AddItem(search, 0, 3, true).
		AddItem(s.transfers, 0, 2, false)
	chat := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.chatView, 0, 1, false).
		AddItem(s.input, 1, 0, false)
	bottom := tview.NewFlex().
		AddItem(s.convs, 26, 0, false).
		AddItem(chat, 0, 1, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.info, 7, 0, false).
		AddItem(middle, 0, 3, true).
		AddItem(bottom, 0, 2, false)
}

func baseName(p string) string { return path.Base(strings.ReplaceAll(p, `\`, "/")) }

// slskSearch searches the network in the background (searches are paced; it may wait its turn).
func (ui *tui) slskSearch(text string) {
	s := ui.sk
	text = strings.TrimSpace(text)
	if text == "" || s.searching {
		return
	}
	s.searching = true
	s.results.SetTitle(" Searching (searches are spaced out, as the network asks)… ")
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.Context(), 2*time.Minute)
		defer cancel()
		res, err := ui.m.SoulseekSearch(ctx, text)
		ui.app.QueueUpdateDraw(func() {
			s.searching = false
			if err != nil {
				s.results.SetTitle(" Results ")
				ui.message("Soulseek search", err.Error())
				return
			}
			s.res = res
			ui.fillSlskResults()
			s.results.SetTitle(fmt.Sprintf(" %d files for %q (Enter downloads · d the whole folder · m message) ", len(res), text))
			if len(res) > 0 {
				s.results.Select(1, 0)
				ui.app.SetFocus(s.results)
			}
		})
	}()
}

func slskQualityText(r core.SlskResult) string {
	ext := strings.ToUpper(strings.TrimPrefix(path.Ext(baseName(r.Path)), "."))
	switch {
	case r.BitDepth > 0 && r.SampleRate > 0:
		return fmt.Sprintf("%s %d/%.1f", ext, r.BitDepth, float64(r.SampleRate)/1000)
	case r.Bitrate > 0:
		return fmt.Sprintf("%s %d", ext, r.Bitrate)
	}
	return ext
}

func (ui *tui) fillSlskResults() {
	s := ui.sk
	t := s.results
	t.Clear()
	headerRow(t, "User", "File", "Size", "Quality", "Length", "Slot", "Speed")
	for i, r := range s.res {
		file := baseName(r.Folder()) + "/" + baseName(r.Path)
		slot, scolor := "free", tcell.ColorGreen
		if !r.FreeSlot {
			slot, scolor = fmt.Sprintf("queue %d", r.Queue), tcell.ColorYellow
		}
		length := ""
		if r.Duration > 0 {
			length = core.FmtDuration(float64(r.Duration))
		}
		cells := []*tview.TableCell{
			tview.NewTableCell(tview.Escape(r.User)).SetMaxWidth(18),
			tview.NewTableCell(tview.Escape(file)).SetExpansion(1).SetMaxWidth(90),
			tview.NewTableCell(core.FmtBytes(r.Size)).SetAlign(tview.AlignRight),
			tview.NewTableCell(slskQualityText(r)),
			tview.NewTableCell(length).SetAlign(tview.AlignRight),
			tview.NewTableCell(slot).SetTextColor(scolor),
			tview.NewTableCell(core.FmtRate(float64(r.Speed))).SetAlign(tview.AlignRight),
		}
		for c, cell := range cells {
			t.SetCell(i+1, c, cell)
		}
	}
	if len(s.res) == 0 {
		t.SetCell(1, 1, tview.NewTableCell("[gray]nothing found[-]").SetSelectable(false))
	}
}

func (ui *tui) refreshSoulseek() {
	s := ui.sk
	v := ui.m.SoulseekStatus()
	var b strings.Builder
	if !v.On {
		if !v.Configured {
			b.WriteString("[yellow]Soulseek is off.[-] Sources tab → Soulseek → Enter: your Soulseek username and password (a name nobody " +
				"uses yet makes a new account), or import them from Nicotine+ there; then turn the source on (space).")
		} else {
			b.WriteString("[yellow]Soulseek is off.[-] Turn the Soulseek source on in the Sources tab (space) to log in as " + tview.Escape(v.Username) + ".")
		}
		s.info.SetText(b.String())
		s.transfers.Clear()
		s.xfers = nil
		ui.refreshChats()
		return
	}
	switch {
	case v.Online:
		fmt.Fprintf(&b, "[green]● online[-] as [::b]%s[::-] on %s since %s · your address %s", tview.Escape(v.User), tview.Escape(v.Server),
			v.Since.Format("15:04"), v.PublicIP)
		if v.Privileges > 0 {
			fmt.Fprintf(&b, " · [yellow]privileged[-] for %s", core.FmtWait(v.Privileges.Round(time.Hour)))
		}
	case v.Connecting:
		b.WriteString("[yellow]◌ connecting to " + tview.Escape(v.Server) + "…[-]")
	default:
		b.WriteString("[red]● offline: " + tview.Escape(core.FirstNonEmpty(v.Problem, "not connected")) + "[-]")
		if v.Permanent {
			b.WriteString(" (R reconnects)")
		}
	}
	b.WriteString("\n")
	if v.Port > 0 {
		fmt.Fprintf(&b, "Listening on port %d", v.Port)
		if v.PortNote != "" {
			b.WriteString(" · " + tview.Escape(v.PortNote))
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
	switch {
	case v.NoShare:
		b.WriteString("[orange]Not sharing[-] (many users don't send files to people who share nothing)")
	case v.ShareScanning:
		b.WriteString("[yellow]Reading the shared folders…[-]")
	case v.ShareScanned.IsZero():
		b.WriteString("[yellow]The shared folders are read once your library scan is done[-]")
	default:
		fmt.Fprintf(&b, "Sharing %d files in %d folders (%s)", v.SharedFiles, v.SharedFolders, tview.Escape(strings.Join(v.ShareRoots, ", ")))
		if v.ShareProblem != "" {
			b.WriteString(" · [orange]" + tview.Escape(v.ShareProblem) + "[-]")
		}
	}
	fmt.Fprintf(&b, " · answered %d of %d searches", v.SearchHits, v.SearchReqs)
	if v.Parent != "" {
		fmt.Fprintf(&b, " · search network via %s (level %d)", tview.Escape(v.Parent), v.BranchLevel)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Downloads: %d of %d at once", v.DlUsed, v.DlLimit)
	if v.DlWaiting > 0 {
		fmt.Fprintf(&b, " (+%d waiting)", v.DlWaiting)
	}
	fmt.Fprintf(&b, " · a search every %s", core.FmtWait(v.SearchEvery))
	if v.SearchBacklog > time.Second {
		fmt.Fprintf(&b, " (next free in %s)", core.FmtWait(v.SearchBacklog.Round(time.Second)))
	}
	fmt.Fprintf(&b, " · Uploads: %d of %d slots", v.UpActive, v.UpSlots)
	if v.UpSpeed > 0 {
		b.WriteString(", " + core.FmtRate(v.UpSpeed))
	}
	if v.UpQueued > 0 {
		fmt.Fprintf(&b, ", %d queued", v.UpQueued)
	}
	if v.UpLimitKBps > 0 {
		fmt.Fprintf(&b, " (limit %d KB/s)", v.UpLimitKBps)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Wishlist: %d tracks no source had, one searched every %s", v.WishTracks, core.FmtWait(v.WishEvery))
	if !v.WishNext.IsZero() && v.Online {
		fmt.Fprintf(&b, " (next %s)", v.WishNext.Format("15:04"))
	}
	if v.MOTD != "" {
		b.WriteString("\n[::d]" + tview.Escape(core.FitWidth(strings.ReplaceAll(v.MOTD, "\n", " "), 300)) + "[::-]")
	}
	s.info.SetText(b.String())
	ui.fillTransfers()
	ui.refreshChats()
}

func (ui *tui) fillTransfers() {
	s := ui.sk
	s.xfers = ui.m.SoulseekTransfers()
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
		file := baseName(path.Dir(strings.ReplaceAll(x.File, `\`, "/"))) + "/" + baseName(x.File)
		prog := ""
		if x.Total > 0 && x.Done > 0 {
			frac := float64(x.Done) / float64(x.Total)
			prog = fmt.Sprintf("%s %3.0f%%", tviewBar(frac, 8), frac*100)
		} else if x.Total > 0 {
			prog = core.FmtBytes(x.Total)
		}
		spd := ""
		if x.Speed > 0 && (x.State == "sending" || x.State == "downloading") {
			spd = core.FmtRate(x.Speed)
		}
		scolor := tcell.ColorDefault
		switch {
		case x.State == "done", strings.HasPrefix(x.State, "done:"):
			scolor = tcell.ColorGreen
		case strings.HasPrefix(x.State, "failed"), x.State == "cancelled", x.State == "stopped":
			scolor = tcell.ColorGray
		case x.State == "sending", x.State == "downloading":
			scolor = tcell.ColorYellow
		}
		cells := []*tview.TableCell{
			tview.NewTableCell(dir).SetTextColor(color),
			tview.NewTableCell(tview.Escape(user)).SetMaxWidth(18),
			tview.NewTableCell(tview.Escape(file)).SetExpansion(1).SetMaxWidth(60),
			tview.NewTableCell(tview.Escape(x.State)).SetTextColor(scolor).SetMaxWidth(30),
			tview.NewTableCell(prog),
			tview.NewTableCell(spd).SetAlign(tview.AlignRight),
		}
		for c, cell := range cells {
			t.SetCell(i+1, c, cell)
		}
	}
	if len(s.xfers) == 0 {
		t.SetCell(1, 2, tview.NewTableCell("[::d]nothing yet[::-]").SetSelectable(false))
	}
}

func (ui *tui) refreshChats() {
	s := ui.sk
	convs, seq := ui.m.SoulseekChats()
	if seq == s.chatSeq {
		if s.current != "" && ui.current == "soulseek" {
			ui.m.SoulseekMarkRead(s.current) // read while shown
		}
		return
	}
	s.chatSeq = seq
	s.convKeys = s.convKeys[:0]
	t := s.convs
	t.Clear()
	sel := -1
	for i, cv := range convs {
		k := cv.Key()
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
		t.SetCell(0, 0, tview.NewTableCell("[::d]n: message someone[::-]").SetSelectable(false))
		t.SetCell(1, 0, tview.NewTableCell("[::d]j: join a room[::-]").SetSelectable(false))
		s.current = ""
	} else if sel < 0 {
		sel = len(convs) - 1
		s.current = s.convKeys[sel]
	}
	if sel >= 0 {
		t.Select(sel, 0)
	}
	ui.refreshChat()
}

// refreshChat shows the selected conversation.
func (ui *tui) refreshChat() {
	s := ui.sk
	if s.current == "" {
		s.chatView.SetText("")
		s.chatView.SetTitle(" Chat ")
		s.input.SetLabel(" Say: ")
		return
	}
	convs, _ := ui.m.SoulseekChats()
	for _, cv := range convs {
		if cv.Key() != s.current {
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
		if cv.Room {
			s.chatView.SetTitle(fmt.Sprintf(" %s · %d users ", cv.Key(), len(cv.Users)))
			s.input.SetLabel(" Say in " + cv.Key() + ": ")
		} else {
			s.chatView.SetTitle(" " + cv.Name + " ")
			s.input.SetLabel(" To " + cv.Name + ": ")
		}
		s.chatView.SetText(b.String())
		s.chatView.ScrollToEnd()
		ui.m.SoulseekMarkRead(s.current)
		return
	}
}

func (ui *tui) slskSend() {
	s := ui.sk
	text := strings.TrimSpace(s.input.GetText())
	if text == "" || s.current == "" {
		return
	}
	if err := ui.m.SoulseekSay(s.current, text); err != nil {
		ui.message("Not sent", err.Error())
		return
	}
	s.input.SetText("")
	s.chatSeq = -1
	ui.refreshSoulseek()
}

// slskJoinRoom offers the server's room list, biggest first.
func (ui *tui) slskJoinRoom() {
	join := func(room string) {
		if room == "" {
			return
		}
		if err := ui.m.SoulseekJoinRoom(room); err != nil {
			ui.message("Can't join", err.Error())
			return
		}
		ui.sk.current, ui.sk.chatSeq = core.ChatKey(room, true), -1
	}
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("Type a room name…", "", 0, func() {
		ui.closeModal("rooms")
		ui.askText("Join a Soulseek room", "Its name (a new name makes a new room)", "", join)
	})
	for i, r := range ui.m.SoulseekRooms() {
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
		ctx, cancel := context.WithTimeout(ui.m.Context(), 30*time.Second)
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

// focusSoulseek starts on the results, so number keys still switch tabs (/ goes to the search).
func (ui *tui) focusSoulseek() {
	ui.app.SetFocus(ui.sk.results)
}
