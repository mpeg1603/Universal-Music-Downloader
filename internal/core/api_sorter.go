package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What frontends use of Sort & merge. One job runs at a time: planning, applying or undoing. A
// plan stays in memory only while it's being looked at; it's on disk too, with its journal, so
// an applied plan can still be undone after a restart.

// SortView is the state of Sort & merge.
type SortView struct {
	Busy        bool   // planning, applying or undoing
	Doing       string // "plan", "apply" or "undo"
	Stage       string
	Done, Total int
	Left        time.Duration // about how long this stage has to go; 0 when it can't be told
	CanSkip     bool          // the online lookups are running, and SortSkipLookups ends them
	Err         string
	Plan        *SortPlanView // the plan being looked at
	Result      *SortResult   // how the last apply or undo went
	ResultWas   string        // "apply" or "undo"
}

// SortPlanView is a plan's outline.
type SortPlanView struct {
	ID, Created, Dir string
	Options          SortOptions
	Summary          SortSummary
	Text             string
}

// SortPlanInfo is a plan in the history.
type SortPlanInfo struct {
	ID, Created, Dir string
	Moves            int
	Applied          bool // has something to undo
	Partly           bool // applying stopped before the end
	Options          SortOptions
}

type sorter struct {
	mu     sync.Mutex
	view   SortView
	plan   *sortPlan
	cancel context.CancelFunc
	skip   context.CancelFunc // ends the online lookups

	stage      string // what Left is worked out from
	stageAt    time.Time
	stageFirst int
}

func (m *Manager) sortDir() string { return filepath.Join(m.dataDir, "sort-plans") }

// SortDefaults suggests what to sort: your library folders and the downloads folder, into your
// first library folder, with the duplicates next to it.
func (m *Manager) SortDefaults() SortOptions {
	cfg := m.Config()
	o := SortOptions{Tolerance: 3}
	o.Sources = append(o.Sources, cfg.LibraryDirs...)
	if !ContainsString(o.Sources, cfg.OutputDir) {
		o.Sources = append(o.Sources, cfg.OutputDir)
	}
	if len(cfg.LibraryDirs) > 0 {
		o.Dest = cfg.LibraryDirs[0]
		o.DuplicatesDir = sortDefaultDuplicates(o.Dest)
	}
	return o
}

// sortDefaultDuplicates is "Duplicates" next to the library, or inside it if it's a whole disk.
func sortDefaultDuplicates(dest string) string {
	dest = filepath.Clean(dest)
	parent := filepath.Dir(dest)
	if parent == dest || deviceOf(sortNearestDir(parent)) != deviceOf(sortNearestDir(dest)) {
		return filepath.Join(dest, "Duplicates")
	}
	return filepath.Join(parent, "Duplicates")
}

func (o *SortOptions) check() error {
	var sources []string
	for _, s := range o.Sources {
		if s = CleanPath(s); s == "" {
			continue
		}
		st, err := os.Stat(s)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("%s isn't a folder", s)
		}
		if !ContainsString(sources, s) {
			sources = append(sources, s)
		}
	}
	if len(sources) == 0 {
		return errors.New("choose at least one folder to sort")
	}
	o.Sources = sources
	if o.Dest = CleanPath(o.Dest); o.Dest == "" {
		return errors.New("choose where the sorted music goes")
	}
	if o.DuplicatesDir = CleanPath(o.DuplicatesDir); o.DuplicatesDir == "" {
		o.DuplicatesDir = sortDefaultDuplicates(o.Dest)
	}
	if o.Dest == o.DuplicatesDir || isWithin(o.Dest, o.DuplicatesDir) {
		return errors.New("the sorted music can't go inside the duplicates folder")
	}
	if o.Tolerance <= 0 {
		o.Tolerance = 3
	}
	return nil
}

func (m *Manager) SortStatus() SortView {
	s := &m.sort
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view
	if v.Result != nil {
		r := *v.Result
		v.Result = &r
	}
	return v
}

func (m *Manager) sortBegin(doing string) (context.Context, bool) {
	s := &m.sort
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.view.Busy {
		return nil, false
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.cancel = cancel
	s.view.Busy, s.view.Doing, s.view.Err, s.view.Stage, s.view.Done, s.view.Total, s.view.Left = true, doing, "", "Starting", 0, 0, 0
	s.stage = ""
	return ctx, true
}

func (m *Manager) sortProgress(stage string, done, total int) {
	s := &m.sort
	s.mu.Lock()
	if s.stage != stage || done < s.stageFirst {
		s.stage, s.stageAt, s.stageFirst = stage, time.Now(), done
	}
	s.view.Stage, s.view.Done, s.view.Total, s.view.Left = stage, done, total, 0
	if el := time.Since(s.stageAt); total > done && done > s.stageFirst && el > 3*time.Second {
		s.view.Left = time.Duration(float64(el) / float64(done-s.stageFirst) * float64(total-done))
	}
	s.mu.Unlock()
	m.revs.sort.Add(1)
}

func (m *Manager) sortSkippable(skip context.CancelFunc) {
	s := &m.sort
	s.mu.Lock()
	s.skip, s.view.CanSkip = skip, skip != nil
	s.mu.Unlock()
	m.revs.sort.Add(1)
}

// SortSkipLookups ends the online lookups of the plan being made: what's still missing is filed
// from the tags and names alone. What was asked so far stays remembered.
func (m *Manager) SortSkipLookups() {
	s := &m.sort
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skip != nil {
		s.skip()
		s.view.CanSkip = false
	}
}

func (m *Manager) sortEnd(err error) {
	s := &m.sort
	s.mu.Lock()
	s.view.Busy, s.view.Stage, s.view.Left, s.view.CanSkip, s.cancel, s.skip = false, "", 0, false, nil, nil
	if err != nil && !errors.Is(err, context.Canceled) {
		s.view.Err = err.Error()
	}
	s.mu.Unlock()
	m.revs.sort.Add(1)
	releaseMemory()
}

// SortCancel stops what's running: a plan is dropped; applying stops after the current file
// (applying again continues).
func (m *Manager) SortCancel() {
	s := &m.sort
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// SortPlan starts making a plan, in the background. It only reads.
func (m *Manager) SortPlan(o SortOptions) error {
	if err := o.check(); err != nil {
		return err
	}
	ctx, ok := m.sortBegin("plan")
	if !ok {
		return errors.New("Sort & merge is busy")
	}
	m.sort.mu.Lock()
	m.sort.plan, m.sort.view.Plan, m.sort.view.Result, m.sort.view.ResultWas = nil, nil, nil, ""
	m.sort.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		p, err := sortMakePlan(ctx, o, m.dataDir, m.tools, m.deezer, m.sortSkippable, m.sortProgress)
		if err == nil {
			dir := filepath.Join(m.sortDir(), p.ID)
			if err = p.save(dir); err == nil {
				s := &m.sort
				s.mu.Lock()
				s.plan = p
				s.view.Plan = &SortPlanView{ID: p.ID, Created: p.Created, Dir: dir, Options: p.Options, Summary: p.Summary, Text: p.text()}
				s.mu.Unlock()
				Logf("Sort & merge: planned %d moves, %d duplicates (%s)", len(p.Moves), p.Summary.DupFiles, dir)
			}
		}
		m.sortEnd(err)
	}()
	return nil
}

// SortMoves lists the current plan's moves: of one kind ("" is all) whose path contains text.
func (m *Manager) SortMoves(kind, text string) []SortMove {
	s := &m.sort
	s.mu.Lock()
	p := s.plan
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	needle := strings.ToLower(strings.TrimSpace(text))
	out := make([]SortMove, 0, len(p.Moves))
	for _, mv := range p.Moves {
		if kind != "" && mv.Kind != kind {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(mv.Src+"\x00"+mv.Dst), needle) {
			continue
		}
		out = append(out, mv)
	}
	return out
}

// SortDuplicates lists the current plan's duplicate songs.
func (m *Manager) SortDuplicates() []SortDuplicate {
	s := &m.sort
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.plan == nil {
		return nil
	}
	return s.plan.Duplicates
}

// SortDiscard forgets the plan being looked at.
func (m *Manager) SortDiscard() {
	s := &m.sort
	s.mu.Lock()
	if s.view.Plan != nil && !s.view.Busy { // never applied: there's nothing to keep it for
		if _, err := os.Stat(filepath.Join(s.view.Plan.Dir, sortJournal)); os.IsNotExist(err) {
			os.RemoveAll(s.view.Plan.Dir)
		}
	}
	s.plan, s.view.Plan, s.view.Result, s.view.ResultWas, s.view.Err = nil, nil, nil, "", ""
	s.mu.Unlock()
	m.revs.sort.Add(1)
	releaseMemory()
}

func (m *Manager) sortLoad(id string) (*sortPlan, string, error) {
	dir := filepath.Join(m.sortDir(), filepath.Base(id))
	data, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		return nil, "", fmt.Errorf("that plan is gone: %w", err)
	}
	var p sortPlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", err
	}
	return &p, dir, nil
}

// SortApply carries out a plan ("" is the one being looked at; another continues an unfinished
// one). Downloads are paused meanwhile.
func (m *Manager) SortApply(id string) error {
	s := &m.sort
	s.mu.Lock()
	p := s.plan
	s.mu.Unlock()
	dir := ""
	if id == "" {
		if p == nil {
			return errors.New("make a plan first")
		}
		dir = filepath.Join(m.sortDir(), p.ID)
	} else {
		var err error
		if p, dir, err = m.sortLoad(id); err != nil {
			return err
		}
	}
	ctx, ok := m.sortBegin("apply")
	if !ok {
		return errors.New("Sort & merge is busy")
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		wasPaused := m.paused.Load()
		m.SetPaused(true)
		defer m.SetPaused(wasPaused)
		res, moved, err := sortApply(ctx, p, dir, m.sortProgress)
		if err == nil {
			m.sortFollow(moved)
			Logf("Sort & merge: %d files done, %d problems (%s)", res.Done, res.ProblemCount, dir)
			s.mu.Lock()
			s.view.Result, s.view.ResultWas = &res, "apply"
			if !res.Stopped {
				s.plan, s.view.Plan = nil, nil
			}
			s.mu.Unlock()
			if !res.Stopped {
				m.sortAddToLibrary(p.Options.Dest)
			}
			m.Rescan()
		}
		m.sortEnd(err)
	}()
	return nil
}

// sortAddToLibrary makes the sorted folder one of your library folders, if it isn't already.
func (m *Manager) sortAddToLibrary(dest string) {
	cfg := m.Config()
	for _, d := range cfg.LibraryDirs {
		if d == dest || isWithin(dest, d) {
			return
		}
	}
	if err := m.AddLibraryDir(dest); err != nil {
		Logf("Sort & merge: couldn't add %s to your library: %v", dest, err)
	}
}

// SortUndo puts back what an applied plan did.
func (m *Manager) SortUndo(id string) error {
	p, dir, err := m.sortLoad(id)
	if err != nil {
		return err
	}
	ctx, ok := m.sortBegin("undo")
	if !ok {
		return errors.New("Sort & merge is busy")
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		wasPaused := m.paused.Load()
		m.SetPaused(true)
		defer m.SetPaused(wasPaused)
		res, moved, err := sortUndo(ctx, p, dir, m.sortProgress)
		if err == nil {
			m.sortFollow(moved)
			Logf("Sort & merge: put back %d files, %d problems", res.Done, res.ProblemCount)
			m.sort.mu.Lock()
			m.sort.view.Result, m.sort.view.ResultWas = &res, "undo"
			m.sort.mu.Unlock()
			m.Rescan()
		}
		m.sortEnd(err)
	}()
	return nil
}

// sortFollow updates what the app remembers about files that moved, so it doesn't think they
// were lost and download them again.
func (m *Manager) sortFollow(moved map[string]string) {
	if len(moved) == 0 {
		return
	}
	for _, r := range m.state.all() {
		dst, a := moved[r.Path]
		lib, b := moved[r.LibPath]
		if !a && !b {
			continue
		}
		if a {
			r.Path = dst
		}
		if b {
			r.LibPath = lib
		}
		m.state.put(r)
		if j := m.job(r.Key); j != nil {
			j.mu.Lock()
			if j.rec != nil {
				rec := *j.rec
				if a {
					rec.Path = dst
				}
				if b {
					rec.LibPath = lib
				}
				j.rec = &rec
			}
			j.mu.Unlock()
		}
	}
	m.revs.queue.Add(1)
}

// SortDeletePlans removes plans from the history; an applied plan can't be undone afterwards.
// The plan being looked at, or anything while Sort & merge is running, stays. It says how many
// went.
func (m *Manager) SortDeletePlans(ids []string) (int, error) {
	s := &m.sort
	s.mu.Lock()
	busy := s.view.Busy
	current := ""
	if s.view.Plan != nil {
		current = filepath.Base(s.view.Plan.Dir)
	}
	s.mu.Unlock()
	if busy {
		return 0, errors.New("Sort & merge is running; try again when it's done")
	}
	n := 0
	for _, id := range ids {
		id = filepath.Base(id)
		if id == current || id == "." || id == string(filepath.Separator) {
			continue
		}
		dir := filepath.Join(m.sortDir(), id)
		if _, err := os.Stat(filepath.Join(dir, "info.json")); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return n, err
		}
		n++
	}
	m.revs.sort.Add(1)
	return n, nil
}

// SortHistory lists earlier plans, newest first.
func (m *Manager) SortHistory() []SortPlanInfo {
	entries, err := os.ReadDir(m.sortDir())
	if err != nil {
		return nil
	}
	var out []SortPlanInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(m.sortDir(), e.Name())
		var info SortPlanInfo
		data, err := os.ReadFile(filepath.Join(dir, "info.json"))
		if err != nil || json.Unmarshal(data, &info) != nil {
			continue
		}
		info.Dir = dir
		_, journalErr := os.Stat(filepath.Join(dir, sortJournal))
		_, finishedErr := os.Stat(filepath.Join(dir, sortFinished))
		info.Applied = journalErr == nil
		info.Partly = journalErr == nil && finishedErr != nil
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}
