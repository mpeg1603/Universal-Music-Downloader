package main

import (
	"context"
	"sync"
	"time"
)

// Live progress of a track being worked on. Sources reach it through the context
// (taskFrom) to report what they're doing; every method is a no-op on a nil Task.
type Task struct {
	Track   *Track
	started time.Time

	mu       sync.Mutex
	stage    string // e.g. "searching Torrent", "downloading", "converting"
	note     string // e.g. "12 peers"
	done     int64
	total    int64 // 0: unknown
	speed    float64
	sampleAt time.Time
	sampleN  int64
	updated  time.Time
	// Set when the download is one file of a bigger one (a torrent).
	group, file string
	groupSpeed  float64
	onPark      func()
}

type taskKey struct{}

func withTask(ctx context.Context, t *Task) context.Context {
	return context.WithValue(ctx, taskKey{}, t)
}

func taskFrom(ctx context.Context) *Task {
	t, _ := ctx.Value(taskKey{}).(*Task)
	return t
}

// setStage starts a new step; progress from the previous step is cleared.
func (t *Task) setStage(stage string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stage == stage {
		return
	}
	t.stage, t.note, t.done, t.total, t.speed, t.sampleAt, t.sampleN = stage, "", 0, 0, 0, time.Time{}, 0
	t.group, t.file, t.groupSpeed = "", "", 0
}

func (t *Task) setNote(note string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.note = note
	t.mu.Unlock()
}

// setProgress records bytes done out of total (0 if unknown) and keeps a smoothed speed.
func (t *Task) setProgress(done, total int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	switch dt := now.Sub(t.sampleAt).Seconds(); {
	case t.sampleAt.IsZero() || done < t.sampleN:
		t.sampleAt, t.sampleN = now, done
	case dt >= 0.5:
		inst := float64(done-t.sampleN) / dt
		if t.speed == 0 {
			t.speed = inst
		} else {
			t.speed = 0.6*t.speed + 0.4*inst
		}
		t.sampleAt, t.sampleN = now, done
	}
	t.done, t.total, t.updated = done, total, now
}

// share marks the task's download as one file of a bigger download (a torrent, group).
// Torrents only report a file's progress in whole pieces, so the speed shown is the whole
// download's, counted once however many tracks share it.
func (t *Task) share(group, file string, speed float64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.group, t.file, t.groupSpeed, t.updated = group, file, speed, time.Now()
	t.mu.Unlock()
}

// park gives up the task's download slot while it only waits (e.g. for a torrent swarm),
// so other downloads can use it meanwhile.
func (t *Task) park() {
	if t == nil {
		return
	}
	t.mu.Lock()
	f := t.onPark
	t.onPark = nil
	t.mu.Unlock()
	if f != nil {
		f()
	}
}

// resume is called when the wait is over. Converting needs no download slot, so there's
// nothing to take back.
func (t *Task) resume(ctx context.Context) error { return nil }

type taskView struct {
	Stage, Note string
	Done, Total int64
	Speed       float64 // the group's when Group is set
	Group, File string
	Started     time.Time
}

func (t *Task) view() taskView {
	if t == nil {
		return taskView{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	speed := t.speed
	if t.group != "" {
		speed = t.groupSpeed
	}
	if time.Since(t.updated) > 3*time.Second {
		speed = 0
	}
	return taskView{Stage: t.stage, Note: t.note, Done: t.done, Total: t.total, Speed: speed,
		Group: t.group, File: t.file, Started: t.started}
}

// --- gates ------------------------------------------------------------------------------------

// gate limits how many jobs do something at once (search, download). Its limit can change
// at any time; waiting jobs are let through highest priority first.
type gate struct {
	mu      sync.Mutex
	limit   int
	used    int
	seq     int64
	waiters []*gateWaiter
}

type gateWaiter struct {
	prio int64
	seq  int64
	ch   chan struct{}
}

func newGate(limit int) *gate { return &gate{limit: max(1, limit)} }

func (g *gate) acquire(ctx context.Context, prio int64) error {
	g.mu.Lock()
	if g.used < g.limit && len(g.waiters) == 0 {
		g.used++
		g.mu.Unlock()
		return nil
	}
	g.seq++
	w := &gateWaiter{prio: prio, seq: g.seq, ch: make(chan struct{})}
	g.waiters = append(g.waiters, w)
	g.grantLocked()
	g.mu.Unlock()
	select {
	case <-w.ch:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		for i, x := range g.waiters {
			if x == w {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				g.mu.Unlock()
				return ctx.Err()
			}
		}
		g.mu.Unlock()
		g.release() // granted just now: pass it on
		return ctx.Err()
	}
}

func (g *gate) grantLocked() {
	for g.used < g.limit && len(g.waiters) > 0 {
		best := 0
		for i, w := range g.waiters {
			b := g.waiters[best]
			if w.prio > b.prio || (w.prio == b.prio && w.seq < b.seq) {
				best = i
			}
		}
		w := g.waiters[best]
		g.waiters = append(g.waiters[:best], g.waiters[best+1:]...)
		g.used++
		close(w.ch)
	}
}

func (g *gate) release() {
	g.mu.Lock()
	g.used--
	g.grantLocked()
	g.mu.Unlock()
}

func (g *gate) setLimit(n int) {
	g.mu.Lock()
	g.limit = max(1, n)
	g.grantLocked()
	g.mu.Unlock()
}

// stats returns the slots in use, the limit and how many are waiting.
func (g *gate) stats() (used, limit, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used, g.limit, len(g.waiters)
}
