package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The Soulseek tab's Browse, Users and Interests pages.

// --- browse -------------------------------------------------------------------------------------

func (ui *tui) buildSlskBrowse() tview.Primitive {
	s := ui.sk
	s.browseUser = tview.NewInputField().SetLabel(" Browse user: ").SetFieldWidth(30).SetPlaceholder("their name, Enter")
	s.folders = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.folders.SetBorder(true).SetTitle(" Folders (Enter shows · d downloads the folder) ")
	s.files = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.files.SetBorder(true).SetTitle(" Files (Enter downloads) ")
	s.browseUser.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEnter:
			ui.slskBrowse(s.browseUser.GetText())
		case tcell.KeyTab, tcell.KeyEscape, tcell.KeyDown:
			ui.app.SetFocus(s.folders)
		}
	})
	s.folders.SetSelectionChangedFunc(func(row, col int) {
		if row >= 1 && row-1 < len(s.folderList) {
			s.selFolder = s.folderList[row-1]
			ui.fillBrowseFiles()
		}
	})
	s.folders.SetSelectedFunc(func(row, col int) { ui.app.SetFocus(s.files) })
	s.folders.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(s.files)
			return nil
		case ev.Rune() == '/':
			ui.app.SetFocus(s.browseUser)
			return nil
		case ev.Rune() == 'd' && s.selFolder != "":
			ui.slskDownloadBrowsed(s.selFolder)
			return nil
		}
		return ui.slskKeys(ev, s.browsedUser)
	})
	s.files.SetSelectedFunc(func(row, col int) {
		if row >= 1 && row-1 < len(s.folderFiles) {
			r := s.folderFiles[row-1]
			ui.m.SoulseekDownload(r.User, r.Path, r.Size)
			s.files.SetTitle(" [green]queued " + tview.Escape(baseName(r.Path)) + "[-] ")
		}
	})
	s.files.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyEscape:
			ui.app.SetFocus(s.folders)
			return nil
		case ev.Rune() == 'd' && s.selFolder != "":
			ui.slskDownloadBrowsed(s.selFolder)
			return nil
		}
		return ui.slskKeys(ev, s.browsedUser)
	})
	lists := tview.NewFlex().
		AddItem(s.folders, 0, 2, true).
		AddItem(s.files, 0, 3, false)
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(s.browseUser, 1, 0, false).
		AddItem(lists, 0, 1, true)
}

// slskBrowse fetches a user's file list and shows the Browse page.
func (ui *tui) slskBrowse(user string) {
	s := ui.sk
	if user = strings.TrimSpace(user); user == "" || s.browsing {
		return
	}
	s.browsing = true
	s.browseUser.SetText(user)
	ui.slskShow(2)
	s.folders.SetTitle(" Asking " + tview.Escape(user) + " for their file list… ")
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.Context(), 4*time.Minute)
		defer cancel()
		files, err := ui.m.SoulseekBrowse(ctx, user)
		ui.app.QueueUpdateDraw(func() {
			s.browsing = false
			if err != nil {
				s.folders.SetTitle(" Folders ")
				ui.message("Browse "+user, err.Error())
				return
			}
			s.browsed, s.browsedUser = files, user
			byFolder := map[string]int{}
			for _, f := range files {
				byFolder[f.Folder()]++
			}
			s.folderList = s.folderList[:0]
			for f := range byFolder {
				s.folderList = append(s.folderList, f)
			}
			sort.Slice(s.folderList, func(i, j int) bool { return strings.ToLower(s.folderList[i]) < strings.ToLower(s.folderList[j]) })
			t := s.folders
			t.Clear()
			headerRow(t, "Folder", "Files")
			for i, f := range s.folderList {
				t.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(strings.ReplaceAll(f, `\`, "/"))).SetExpansion(1).SetMaxWidth(80))
				t.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprint(byFolder[f])).SetAlign(tview.AlignRight))
			}
			t.SetTitle(fmt.Sprintf(" %s shares %d files in %d folders (Enter shows · d downloads the folder) ", tview.Escape(user), len(files), len(s.folderList)))
			if len(s.folderList) > 0 {
				t.Select(1, 0)
				s.selFolder = s.folderList[0]
				ui.fillBrowseFiles()
			}
			ui.app.SetFocus(t)
		})
	}()
}

func (ui *tui) fillBrowseFiles() {
	s := ui.sk
	s.folderFiles = s.folderFiles[:0]
	for _, f := range s.browsed {
		if f.Folder() == s.selFolder {
			s.folderFiles = append(s.folderFiles, f)
		}
	}
	t := s.files
	t.Clear()
	headerRow(t, "File", "Size", "Quality", "Length")
	for i, r := range s.folderFiles {
		length := ""
		if r.Duration > 0 {
			length = core.FmtDuration(float64(r.Duration))
		}
		t.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(baseName(r.Path))).SetExpansion(1).SetMaxWidth(80))
		t.SetCell(i+1, 1, tview.NewTableCell(core.FmtBytes(r.Size)).SetAlign(tview.AlignRight))
		t.SetCell(i+1, 2, tview.NewTableCell(slskQualityText(r)))
		t.SetCell(i+1, 3, tview.NewTableCell(length).SetAlign(tview.AlignRight))
	}
	t.SetTitle(" " + tview.Escape(baseName(s.selFolder)) + " (Enter downloads · d the whole folder) ")
}

// slskDownloadBrowsed downloads a browsed folder and its subfolders.
func (ui *tui) slskDownloadBrowsed(folder string) {
	s := ui.sk
	var list []core.SlskResult
	for _, f := range s.browsed {
		if f.Folder() == folder || strings.HasPrefix(f.Folder(), folder+`\`) {
			list = append(list, f)
		}
	}
	ui.confirm(fmt.Sprintf("Download %d files of %s's folder %s?", len(list), s.browsedUser, baseName(folder)), "Download", func() {
		for _, f := range list {
			ui.m.SoulseekDownload(f.User, f.Path, f.Size)
		}
	})
}

// --- users --------------------------------------------------------------------------------------

type slskPerson struct {
	list string // buddies, banned, ignored ("" a heading)
	user core.SlskUser
}

func (ui *tui) buildSlskUsers() tview.Primitive {
	s := ui.sk
	s.people = tview.NewTable().SetSelectable(true, false)
	s.people.SetBorder(true).SetTitle(" Buddies, banned and ignored users (a adds a buddy · x takes off the list) ")
	s.userInfo = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetScrollable(true)
	s.userInfo.SetBorder(true).SetTitle(" User info (i) ")
	s.people.SetSelectedFunc(func(row, col int) {
		if row >= 0 && row < len(s.peopleRows) && s.peopleRows[row].list != "" {
			ui.slskUserInfo(s.peopleRows[row].user.Name)
		}
	})
	s.people.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.people.GetSelection()
		var p *slskPerson
		if row >= 0 && row < len(s.peopleRows) && s.peopleRows[row].list != "" {
			p = &s.peopleRows[row]
		}
		switch {
		case ev.Rune() == 'a':
			ui.askText("Add a buddy", "Their Soulseek name (you see when they're online)", "", func(v string) {
				if err := ui.m.SoulseekSetUser("buddies", v, true); err != nil {
					ui.message("Soulseek", err.Error())
				}
			})
			return nil
		case ev.Rune() == 'x' && p != nil:
			ui.m.SoulseekSetUser(p.list, p.user.Name, false)
			return nil
		}
		user := ""
		if p != nil {
			user = p.user.Name
		}
		return ui.slskKeys(ev, user)
	})
	return tview.NewFlex().
		AddItem(s.people, 0, 1, true).
		AddItem(s.userInfo, 0, 1, false)
}

func (ui *tui) fillPeople() {
	s := ui.sk
	cfg := ui.m.Config().Soulseek
	row, _ := s.people.GetSelection()
	s.people.Clear()
	s.peopleRows = s.peopleRows[:0]
	r := 0
	section := func(title, list string, users []core.SlskUser) {
		s.people.SetCell(r, 0, tview.NewTableCell("[::b]"+title+"[::-]").SetSelectable(false))
		s.peopleRows = append(s.peopleRows, slskPerson{})
		r++
		for _, u := range users {
			status, color := u.StatusText(), tcell.ColorGray
			switch u.Status {
			case 2:
				color = tcell.ColorGreen
			case 1:
				color = tcell.ColorYellow
			}
			extra := ""
			if u.Files > 0 {
				extra = fmt.Sprintf("%d files · %s", u.Files, core.FmtRate(float64(u.Speed)))
			}
			s.people.SetCell(r, 0, tview.NewTableCell("  "+tview.Escape(u.Name)).SetExpansion(1))
			s.people.SetCell(r, 1, tview.NewTableCell(status).SetTextColor(color))
			s.people.SetCell(r, 2, tview.NewTableCell(extra).SetTextColor(tcell.ColorGray))
			s.peopleRows = append(s.peopleRows, slskPerson{list: list, user: u})
			r++
		}
		if len(users) == 0 {
			s.people.SetCell(r, 0, tview.NewTableCell("  [::d]none[::-]").SetSelectable(false))
			s.peopleRows = append(s.peopleRows, slskPerson{})
			r++
		}
	}
	plain := func(names []string) []core.SlskUser {
		var out []core.SlskUser
		for _, n := range names {
			out = append(out, core.SlskUser{Name: n, Status: -1})
		}
		return out
	}
	section("Buddies", "buddies", ui.m.SoulseekBuddies())
	section("Banned (can't download from you or see your files)", "banned", plain(cfg.Banned))
	section("Ignored (their messages are hidden)", "ignored", plain(cfg.Ignored))
	if row <= 0 || row >= r {
		row = 1
	}
	s.people.Select(row, 0)
}

// slskUserInfo shows a user's details on the Users page.
func (ui *tui) slskUserInfo(user string) {
	s := ui.sk
	ui.slskShow(3)
	s.userInfo.SetTitle(" " + tview.Escape(user) + " ")
	s.userInfo.SetText("[yellow]Asking " + tview.Escape(user) + "…[-]")
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.Context(), 70*time.Second)
		defer cancel()
		u, err := ui.m.SoulseekUserInfo(ctx, user)
		likes, hates, _ := ui.m.SoulseekUserInterests(ctx, user)
		ui.app.QueueUpdateDraw(func() {
			var b strings.Builder
			fmt.Fprintf(&b, "[::b]%s[::-] · %s", tview.Escape(u.Name), u.StatusText())
			if u.Privileged {
				b.WriteString(" · [yellow]privileged[-]")
			}
			if u.Country != "" {
				b.WriteString(" · " + u.Country)
			}
			fmt.Fprintf(&b, "\nShares %d files in %d folders · uploads at %s on average\n", u.Files, u.Folders, core.FmtRate(float64(u.Speed)))
			if err == nil {
				free := "no free upload slot"
				if u.FreeSlot {
					free = "a free upload slot"
				}
				fmt.Fprintf(&b, "%d upload slots, %d queued, %s\n", u.Slots, u.Queue, free)
				if u.Description != "" {
					b.WriteString("\n" + tview.Escape(u.Description) + "\n")
				}
			} else {
				b.WriteString("\n[orange]" + tview.Escape(err.Error()) + "[-]\n")
			}
			if len(likes) > 0 {
				b.WriteString("\n[green]Likes:[-] " + tview.Escape(strings.Join(likes, ", ")) + "\n")
			}
			if len(hates) > 0 {
				b.WriteString("[red]Dislikes:[-] " + tview.Escape(strings.Join(hates, ", ")) + "\n")
			}
			b.WriteString("\n[::d]m message · b browse their files · u more[::-]")
			s.userInfo.SetText(b.String())
		})
	}()
}

// --- interests ----------------------------------------------------------------------------------

type slskLike struct {
	item string
	hate bool
}

func (ui *tui) buildSlskInterests() tview.Primitive {
	s := ui.sk
	s.likes = tview.NewTable().SetSelectable(true, false)
	s.likes.SetBorder(true).SetTitle(" Your interests (a like · h dislike · x remove) ")
	s.recs = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.recs.SetBorder(true)
	s.similar = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	s.similar.SetBorder(true).SetTitle(" Similar users (Enter: user menu) ")
	add := func(hate bool) {
		title, help := "Something you like", "an artist, a genre, anything; people who like it too find you, and you them"
		if hate {
			title, help = "Something you dislike", "recommendations steer away from it"
		}
		ui.askText(title, help, "", func(v string) {
			if err := ui.m.SoulseekSetInterest(v, hate, false); err != nil {
				ui.message("Soulseek", err.Error())
			}
			ui.fillLikes()
		})
	}
	s.likes.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.likes.GetSelection()
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(s.recs)
			return nil
		case ev.Rune() == 'a' || ev.Rune() == 'h':
			add(ev.Rune() == 'h')
			return nil
		case ev.Rune() == 'x' && row >= 0 && row < len(s.likeRows):
			l := s.likeRows[row]
			ui.m.SoulseekSetInterest(l.item, l.hate, true)
			ui.fillLikes()
			return nil
		case ev.Key() == tcell.KeyEnter && row >= 0 && row < len(s.likeRows):
			ui.loadRecs(s.likeRows[row].item)
			return nil
		}
		return ui.slskKeys(ev, "")
	})
	s.recs.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.recs.GetSelection()
		var rec *core.SlskRecommendation
		if row >= 1 && row-1 < len(s.recRows) {
			rec = &s.recRows[row-1]
		}
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(s.similar)
			return nil
		case ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(s.likes)
			return nil
		case ev.Rune() == 'g':
			s.globalRecs = !s.globalRecs
			ui.loadRecs("")
			return nil
		case ev.Rune() == 'r':
			ui.loadRecs(s.recsFor)
			return nil
		case ev.Key() == tcell.KeyEnter && rec != nil:
			ui.loadRecs(rec.Item)
			return nil
		case (ev.Rune() == '+' || ev.Rune() == 'a') && rec != nil:
			ui.m.SoulseekSetInterest(rec.Item, false, false)
			ui.fillLikes()
			return nil
		case ev.Rune() == 's' && rec != nil:
			ui.slskShow(0)
			ui.sk.find.SetText(rec.Item)
			ui.slskSearch(rec.Item)
			return nil
		}
		return ui.slskKeys(ev, "")
	})
	s.similar.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		row, _ := s.similar.GetSelection()
		user := ""
		if row >= 1 && row-1 < len(s.simRows) {
			user = s.simRows[row-1].Name
		}
		switch {
		case ev.Key() == tcell.KeyTab:
			ui.app.SetFocus(s.likes)
			return nil
		case ev.Key() == tcell.KeyBacktab:
			ui.app.SetFocus(s.recs)
			return nil
		case ev.Key() == tcell.KeyEnter && user != "":
			ui.slskUserMenu(user)
			return nil
		}
		return ui.slskKeys(ev, user)
	})
	return tview.NewFlex().
		AddItem(s.likes, 0, 1, false).
		AddItem(s.recs, 0, 2, true).
		AddItem(s.similar, 0, 1, false)
}

func (ui *tui) fillLikes() {
	s := ui.sk
	cfg := ui.m.Config().Soulseek
	s.likes.Clear()
	s.likeRows = s.likeRows[:0]
	for _, x := range cfg.Likes {
		s.likes.SetCell(len(s.likeRows), 0, tview.NewTableCell("[green]+[-] "+tview.Escape(x)).SetExpansion(1))
		s.likeRows = append(s.likeRows, slskLike{item: x})
	}
	for _, x := range cfg.Hates {
		s.likes.SetCell(len(s.likeRows), 0, tview.NewTableCell("[red]−[-] "+tview.Escape(x)).SetExpansion(1))
		s.likeRows = append(s.likeRows, slskLike{item: x, hate: true})
	}
	if len(s.likeRows) == 0 {
		s.likes.SetCell(0, 0, tview.NewTableCell("[::d]a: add something you like[::-]").SetSelectable(false))
	}
}

// loadRecs loads recommendations and similar users: yours ("") or for an item.
func (ui *tui) loadRecs(item string) {
	s := ui.sk
	s.recsFor = item
	title := " Recommendations for you (g: everyone's · Enter: more like it · + like it · s search for it) "
	switch {
	case item != "":
		title = " People who like " + tview.Escape(item) + " also like (r: yours again) "
	case s.globalRecs:
		title = " What everyone likes (g: yours) "
	}
	s.recs.SetTitle(title)
	s.recs.Clear()
	s.recs.SetCell(0, 0, tview.NewTableCell("[yellow]asking the server…[-]").SetSelectable(false))
	global := s.globalRecs
	go func() {
		ctx, cancel := context.WithTimeout(ui.m.Context(), 30*time.Second)
		defer cancel()
		var recs []core.SlskRecommendation
		var users []core.SlskUserScore
		var err error
		if item != "" {
			recs, err = ui.m.SoulseekItemRecommendations(ctx, item)
			users, _ = ui.m.SoulseekSimilarUsers(ctx, item)
		} else {
			recs, err = ui.m.SoulseekRecommendations(ctx, global)
			users, _ = ui.m.SoulseekSimilarUsers(ctx, "")
		}
		ui.app.QueueUpdateDraw(func() {
			if s.recsFor != item {
				return
			}
			s.recRows, s.simRows = recs, users
			s.recs.Clear()
			headerRow(s.recs, "Interest", "People")
			for i, r := range recs {
				color := tcell.ColorDefault
				if r.Score < 0 {
					color = tcell.ColorRed
				}
				s.recs.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(r.Item)).SetExpansion(1).SetTextColor(color))
				s.recs.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprint(r.Score)).SetAlign(tview.AlignRight))
			}
			if err != nil {
				s.recs.SetCell(1, 0, tview.NewTableCell("[orange]"+tview.Escape(err.Error())+"[-]").SetSelectable(false))
			} else if len(recs) == 0 {
				s.recs.SetCell(1, 0, tview.NewTableCell("[::d]nothing yet: add some interests (a)[::-]").SetSelectable(false))
			}
			s.similar.Clear()
			headerRow(s.similar, "User", "")
			for i, u := range users {
				s.similar.SetCell(i+1, 0, tview.NewTableCell(tview.Escape(u.Name)).SetExpansion(1))
				if u.Score > 0 {
					s.similar.SetCell(i+1, 1, tview.NewTableCell(fmt.Sprint(u.Score)).SetAlign(tview.AlignRight))
				}
			}
			if len(recs) > 0 {
				s.recs.Select(1, 0)
			}
		})
	}()
}

// slskTheirInterests shows a user's interests on the Users page.
func (ui *tui) slskTheirInterests(user string) { ui.slskUserInfo(user) }
