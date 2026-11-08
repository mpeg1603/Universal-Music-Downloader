package gui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"universal-music-downloader/internal/core"

	qt "github.com/mappu/miqt/qt6"
	"github.com/mappu/miqt/qt6/mainthread"
)

// pickIndexers lists the built-in torrent indexers: on or off, and the account of those that
// need one (RuTracker.org).
func (a *app) pickIndexers() {
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle("Torrent sites")
	d.Resize(900, 620)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(18, 14, 18, 14)
	box.SetSpacing(10)
	box.AddWidget(text("The torrent sites the app searches. The ones marked ★ are on by default. Sites that keep failing are left out for a while by themselves.", "subtitle").QWidget)
	top := qt.NewQHBoxLayout2()
	find := qt.NewQLineEdit2()
	find.SetPlaceholderText("Find a site by name or address")
	find.SetClearButtonEnabled(true)
	music := qt.NewQCheckBox3("Only those with music")
	music.SetChecked(true)
	top.AddWidget2(find.QWidget, 1)
	top.AddWidget(music.QWidget)
	box.AddLayout(top.QLayout)
	table := table("On", "Site", "Language", "Music", "Searches", "Last problem")
	table.HorizontalHeader().SetSectionResizeMode2(1, qt.QHeaderView__Stretch)
	table.SetColumnWidth(0, 34)
	table.SetColumnWidth(5, 220)
	box.AddWidget2(table.QWidget, 1)
	info := qt.NewQLabel2()
	info.SetWordWrap(true)
	box.AddWidget(info.QWidget)

	var rows []core.IndexerStatus
	fill := func() {
		r := table.CurrentRow()
		rows = rows[:0]
		q := strings.ToLower(strings.TrimSpace(find.Text()))
		for _, x := range a.m.Indexers() {
			if music.IsChecked() && !x.Music && !x.On {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(x.Name+" "+x.ID+" "+x.Site), q) {
				continue
			}
			rows = append(rows, x)
		}
		table.SetRowCount(len(rows))
		settings := a.m.Config().Indexers.Settings
		for i, x := range rows {
			on, name := "·", x.Name
			if x.On {
				on = "✓"
			}
			if x.Default {
				name += " ★"
			}
			if x.Login {
				if len(settings[x.ID]) > 0 {
					name += " 🔑"
				} else {
					name += " (needs your account)"
				}
			}
			m := ""
			if x.Music {
				m = "♪"
			}
			searches, problem := "", ""
			if x.Health != nil {
				searches = fmt.Sprintf("%d (%d errors)", x.Health.Searches, x.Health.Errors)
				problem = x.Health.LastErr
			}
			for c, text := range []string{on, name, x.Language, m, searches, problem} {
				table.SetItem(i, c, cell(text))
			}
		}
		if r >= 0 && r < len(rows) {
			table.SelectRow(r)
		}
	}
	selected := func() *core.IndexerStatus {
		if r := table.CurrentRow(); r >= 0 && r < len(rows) {
			return &rows[r]
		}
		return nil
	}
	table.OnCurrentCellChanged(func(int, int, int, int) {
		if x := selected(); x != nil {
			text := x.Name + " · " + x.Site + "\n" + x.Description
			if x.Login {
				text += "\nNeeds your account on the site: use Your account."
			}
			info.SetText(text)
		}
	})
	toggle := func() {
		x := selected()
		if x == nil {
			return
		}
		if x.Login && !x.On && len(a.m.Config().Indexers.Settings[x.ID]) == 0 {
			a.indexerAccount(*x)
		} else if err := a.m.SetIndexer(x.ID, !x.On); err != nil {
			a.message("Torrent sites", err.Error())
		}
		fill()
	}
	table.OnCellDoubleClicked(func(int, int) { toggle() })
	find.OnTextChanged(func(string) { fill() })
	music.OnToggled(func(bool) { fill() })

	buttons := qt.NewQHBoxLayout2()
	add := func(text string, do func()) *qt.QPushButton {
		b := button(text, "", do)
		buttons.AddWidget(b.QWidget)
		return b
	}
	add("On / off", toggle)
	add("Your account…", func() {
		if x := selected(); x != nil {
			if !x.Login {
				a.message("Torrent sites", x.Name+" needs no account.")
				return
			}
			a.indexerAccount(*x)
			fill()
		}
	})
	var update *qt.QPushButton
	update = add("Get the latest site list", func() {
		update.SetEnabled(false)
		info.SetText("Fetching the latest list of sites…")
		go func() {
			ctx, cancel := context.WithTimeout(a.m.Context(), 3*time.Minute)
			defer cancel()
			n, err := a.m.RefreshIndexers(ctx)
			mainthread.Start(func() {
				update.SetEnabled(true)
				if err != nil {
					info.SetText(err.Error())
				} else {
					info.SetText(fmt.Sprintf("%d sites new or changed.", n))
				}
				fill()
			})
		}()
	})
	buttons.AddStretch()
	add("Close", func() { d.Accept() })
	box.AddLayout(buttons.QLayout)
	fill()
	d.Exec()
}

// indexerAccount asks for an indexer's settings (a login), saves them and turns it on.
func (a *app) indexerAccount(x core.IndexerStatus) {
	have := a.m.Config().Indexers.Settings[x.ID]
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle(x.Name + ": your account")
	form := qt.NewQFormLayout(d.QWidget)
	fields := map[string]*qt.QLineEdit{}
	for _, s := range x.Settings {
		e := qt.NewQLineEdit3(have[s.Name])
		if s.Secret {
			e.SetEchoMode(qt.QLineEdit__Password)
		}
		form.AddRow3(s.Label, e.QWidget)
		fields[s.Name] = e
	}
	form.AddRowWithWidget(text("Your account on "+x.Site+". It's kept in the settings file, readable only by you. The app logs in once and keeps "+
		"the session; a failed login isn't tried again for 30 minutes, because sites lock accounts or ask for captchas after many.", "hint").QWidget)
	buttons := qt.NewQHBoxLayout2()
	save := button("Save and turn on", "primary", func() {
		values := map[string]string{}
		for k, e := range fields {
			values[k] = e.Text()
		}
		if err := a.m.SetIndexerSettings(x.ID, values); err != nil {
			a.message(x.Name, err.Error())
			return
		}
		d.Accept()
	})
	save.SetDefault(true)
	buttons.AddWidget(save.QWidget)
	if len(have) > 0 {
		forget := button("Forget the account", "danger", func() {
			if err := a.m.SetIndexerSettings(x.ID, nil); err != nil {
				a.message(x.Name, err.Error())
				return
			}
			d.Accept()
		})
		buttons.AddWidget(forget.QWidget)
	}
	buttons.AddWidget(button("Cancel", "", func() { d.Reject() }).QWidget)
	form.AddRowWithLayout(buttons.QLayout)
	d.Resize(560, 220)
	d.Exec()
}
