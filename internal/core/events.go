package core

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Change notifications for frontends: Watch calls back when a part of the app's state has
// changed, so a frontend redraws that part instead of polling all of it. Changes are noticed
// within a quarter second and each kind is sent at most once per check. Progress (bytes
// downloaded, speeds) changes all the time and isn't announced: read it with Overview and
// Queue on a timer while it's shown.

type EventKind string

const (
	EventQueue    EventKind = "queue"    // tracks were added, started, finished, moved
	EventSources  EventKind = "sources"  // a source's state (paused, problem, set up again)
	EventConfig   EventKind = "config"   // the settings were saved
	EventSoulseek EventKind = "soulseek" // the connection, chats, transfers
	EventLog      EventKind = "log"      // new log lines
)

type eventHub struct {
	mu      sync.Mutex
	subs    map[int]func(EventKind)
	next    int
	running bool
	stop    chan struct{}
}

// revisions count changes to the queue and the settings.
type revisions struct {
	queue, config atomic.Int64
}

// Watch calls f (from a goroutine of its own) for each kind of change; stop ends it.
func (m *Manager) Watch(f func(EventKind)) (stop func()) {
	h := &m.events
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[int]func(EventKind){}
	}
	id := h.next
	h.next++
	h.subs[id] = f
	if !h.running {
		h.running, h.stop = true, make(chan struct{})
		go m.watchLoop(h.stop, m.fingerprints()) // the state Watch was called in
	}
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subs, id)
		if len(h.subs) == 0 && h.running {
			close(h.stop)
			h.running = false
		}
	}
}

// fingerprints are cheap summaries of each part; a part changed when its fingerprint did.
func (m *Manager) fingerprints() map[EventKind]string {
	logSeq := AppLog.Seq()
	out := map[EventKind]string{
		EventQueue:  fmt.Sprint(m.revs.queue.Load()),
		EventConfig: fmt.Sprint(m.revs.config.Load()),
		EventLog:    fmt.Sprint(logSeq),
	}
	var src []string
	for _, s := range m.SourceStatus() {
		src = append(src, fmt.Sprint(s.Name, s.Enabled, s.Problem, s.Paused, s.Until, s.Reason, s.Note, s.Route))
	}
	out[EventSources] = fmt.Sprint(src)
	if c := m.slskClient(); c != nil {
		_, chatSeq := c.chat.Conversations()
		st := c.State()
		var tr []string
		for _, t := range m.SoulseekTransfers() {
			tr = append(tr, t.User+t.File+t.State)
		}
		out[EventSoulseek] = fmt.Sprint(chatSeq, st.Online, st.Connecting, st.Problem, st.Away, tr)
	}
	return out
}

func (m *Manager) watchLoop(stop chan struct{}, last map[EventKind]string) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		now := m.fingerprints()
		var changed []EventKind
		for _, k := range []EventKind{EventQueue, EventSources, EventConfig, EventSoulseek, EventLog} {
			if now[k] != last[k] {
				changed = append(changed, k)
			}
		}
		last = now
		if len(changed) == 0 {
			continue
		}
		m.events.mu.Lock()
		subs := make([]func(EventKind), 0, len(m.events.subs))
		for _, f := range m.events.subs {
			subs = append(subs, f)
		}
		m.events.mu.Unlock()
		for _, k := range changed {
			for _, f := range subs {
				f(k)
			}
		}
	}
}
