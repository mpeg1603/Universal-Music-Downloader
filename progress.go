package main

import (
	"context"
	"sync"
	"time"
)

// Live progress of the tracks being worked on (read by the terminal UI), and the worker
// slots that limit how many tracks are worked on at once.

// Task is one track in progress. Sources reach it through the context (taskFrom) to report
// what they're doing; every method is a no-op on a nil Task.
type Task struct {
	Track   *Track
	started time.Time
	slot    *slotHandle

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

// park frees the task's worker slot while it only waits (e.g. for a torrent swarm), so other
// tracks can be searched meanwhile. resume takes a slot back, ahead of tracks not started yet.
func (t *Task) park() {
	if t != nil {
		t.slot.park()
	}
}

func (t *Task) resume(ctx context.Context) error {
	if t == nil {
		return nil
	}
	return t.slot.resume(ctx)
}

type taskView struct {
	Track       *Track
	Stage, Note string
	Done, Total int64
	Speed       float64 // the group's when Group is set
	Group, File string
	Started     time.Time
}

func (t *Task) view() taskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	speed := t.speed
	if t.group != "" {
		speed = t.groupSpeed
	}
	if time.Since(t.updated) > 3*time.Second {
		speed = 0
	}
	return taskView{Track: t.Track, Stage: t.stage, Note: t.note, Done: t.done, Total: t.total, Speed: speed,
		Group: t.group, File: t.file, Started: t.started}
}

// Board lists the tasks in progress, oldest first.
type Board struct {
	mu    sync.Mutex
	tasks []*Task
}

func (b *Board) add(t *Task) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.tasks = append(b.tasks, t)
	b.mu.Unlock()
}

func (b *Board) remove(t *Task) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, x := range b.tasks {
		if x == t {
			b.tasks = append(b.tasks[:i], b.tasks[i+1:]...)
			return
		}
	}
}

func (b *Board) snapshot() []taskView {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	tasks := append([]*Task(nil), b.tasks...)
	b.mu.Unlock()
	out := make([]taskView, len(tasks))
	for i, t := range tasks {
		out[i] = t.view()
	}
	return out
}

// --- worker slots ---------------------------------------------------------------------------

// slotPool is a semaphore with two queues: tasks resuming after a wait go before new ones.
type slotPool struct {
	mu        sync.Mutex
	free      int
	parked    int
	maxParked int
	resuming  []chan struct{}
	starting  []chan struct{}
}

func newSlotPool(size, maxParked int) *slotPool {
	return &slotPool{free: size, maxParked: maxParked}
}

func (p *slotPool) acquire(ctx context.Context, resuming bool) error {
	p.mu.Lock()
	if p.free > 0 {
		p.free--
		p.mu.Unlock()
		return nil
	}
	w := make(chan struct{})
	if resuming {
		p.resuming = append(p.resuming, w)
	} else {
		p.starting = append(p.starting, w)
	}
	p.mu.Unlock()
	select {
	case <-w:
		return nil
	case <-ctx.Done():
		p.mu.Lock()
		removed := removeWaiter(&p.resuming, w) || removeWaiter(&p.starting, w)
		p.mu.Unlock()
		if !removed { // granted just now: hand it on
			p.release()
		}
		return ctx.Err()
	}
}

func removeWaiter(list *[]chan struct{}, w chan struct{}) bool {
	for i, x := range *list {
		if x == w {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return true
		}
	}
	return false
}

func (p *slotPool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, q := range []*[]chan struct{}{&p.resuming, &p.starting} {
		if len(*q) > 0 {
			w := (*q)[0]
			*q = (*q)[1:]
			close(w)
			return
		}
	}
	p.free++
}

func (p *slotPool) tryPark() bool {
	p.mu.Lock()
	if p.parked >= p.maxParked {
		p.mu.Unlock()
		return false
	}
	p.parked++
	p.mu.Unlock()
	p.release()
	return true
}

func (p *slotPool) unparked() {
	p.mu.Lock()
	p.parked--
	p.mu.Unlock()
}

// slotHandle is one task's hold on the pool; only that task's goroutine uses it.
type slotHandle struct {
	pool   *slotPool
	held   bool
	parked bool
}

func (h *slotHandle) park() {
	if h != nil && h.held && h.pool.tryPark() {
		h.held, h.parked = false, true
	}
}

func (h *slotHandle) resume(ctx context.Context) error {
	if h == nil || !h.parked {
		return nil
	}
	if err := h.pool.acquire(ctx, true); err != nil {
		return err
	}
	h.pool.unparked()
	h.held, h.parked = true, false
	return nil
}

func (h *slotHandle) done() {
	if h == nil {
		return
	}
	if h.held {
		h.pool.release()
		h.held = false
	}
	if h.parked {
		h.pool.unparked()
		h.parked = false
	}
}
