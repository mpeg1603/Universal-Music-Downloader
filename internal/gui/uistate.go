package gui

import (
	"encoding/json"
	"os"
	"path/filepath"

	qt "github.com/mappu/miqt/qt6"
)

// StateFile is where the window remembers its size and column widths; set by the program.
var StateFile string

type uiState struct {
	Width     int              `json:"width,omitempty"`
	Height    int              `json:"height,omitempty"`
	Maximized bool             `json:"maximized,omitempty"`
	Columns   map[string][]int `json:"columns,omitempty"`

	timer *qt.QTimer
}

func loadUIState() *uiState {
	s := &uiState{}
	if StateFile != "" {
		if data, err := os.ReadFile(StateFile); err == nil {
			json.Unmarshal(data, s)
		}
	}
	if s.Columns == nil {
		s.Columns = map[string][]int{}
	}
	return s
}

func (s *uiState) save() {
	if StateFile == "" {
		return
	}
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(StateFile), 0o755)
	tmp := StateFile + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, StateFile)
	}
}

// columnsChanged notes a table's widths; saved a moment later, when you've stopped dragging.
func (s *uiState) columnsChanged(a *app, t *dataTable) {
	widths := make([]int, len(t.cols))
	for i := range t.cols {
		widths[i] = t.view.ColumnWidth(i)
	}
	s.Columns[t.name] = widths
	if s.timer == nil {
		if a.win == nil {
			return
		}
		s.timer = qt.NewQTimer2(a.win.QObject)
		s.timer.SetSingleShot(true)
		s.timer.OnTimeout(s.save)
	}
	s.timer.Start(1200)
}

// throttle runs f when poked, at most once per interval: right away if it's been a while,
// else once when the interval is up.
type throttle struct {
	timer  *qt.QTimer
	idle   bool
	pokeFn func()
}

func newThrottle(parent *qt.QObject, ms int, f func()) *throttle {
	t := &throttle{timer: qt.NewQTimer2(parent), idle: true}
	t.timer.SetInterval(ms)
	pending := false
	t.timer.OnTimeout(func() {
		if pending {
			pending = false
			f()
			return
		}
		t.idle = true
		t.timer.Stop()
	})
	t.pokeFn = func() {
		if t.idle {
			t.idle = false
			t.timer.Start(ms)
			f()
			return
		}
		pending = true
	}
	return t
}

func (t *throttle) poke() { t.pokeFn() }
