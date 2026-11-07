package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The Soulseek client's place in the app: started while the Soulseek source is on and has an
// account, given the shared folders, and fed the wishlist (tracks no source had).

// setupSoulseek starts, adjusts or stops the client to match the settings.
func (m *Manager) setupSoulseek(cfg Config) {
	sc := cfg.source("soulseek")
	m.srcMu.Lock()
	old, oldKey := m.slsk, m.slskKey
	m.srcMu.Unlock()
	if !sc.Enabled || !cfg.Soulseek.configured() {
		if old != nil {
			m.srcMu.Lock()
			m.slsk, m.slskSrc, m.slskKey = nil, nil, ""
			m.srcMu.Unlock()
			old.Close()
			logf("Soulseek: logged out")
		}
		return
	}
	key := cfg.Soulseek.connKey(sc.Route)
	if old != nil && oldKey == key {
		old.apply(cfg.Soulseek)
		m.srcMu.RLock()
		m.slskSrc.setFormat(cfg.Format)
		m.srcMu.RUnlock()
		m.shareFolders(cfg, false)
		return
	}
	if old != nil {
		old.Close()
	}
	c := newSlskClient(cfg.Soulseek, normalizeRoute(sc.Route), m.shares)
	c.setWishlist(m.soulseekWish)
	m.srcMu.Lock()
	m.slsk, m.slskKey, m.slskSrc = c, key, newSoulseekSource(c, cfg.Format)
	m.srcMu.Unlock()
	m.shareFolders(cfg, false)
	c.Start()
}

// Soulseek returns the running client (nil when the source is off).
func (m *Manager) Soulseek() *slskClient {
	m.srcMu.RLock()
	defer m.srcMu.RUnlock()
	return m.slsk
}

// shareFolders scans the shared folders again when they changed (or always, with force).
func (m *Manager) shareFolders(cfg Config, force bool) {
	if !force && !m.ready.Load() {
		return // the first scan comes after the library's (see setup), whose details it uses
	}
	roots := cfg.shareRoots()
	key := fmt.Sprint(roots)
	if cfg.Soulseek.NoShare {
		key = ""
	}
	m.srcMu.Lock()
	same := m.shareKey == key
	m.shareKey = key
	c := m.slsk
	m.srcMu.Unlock()
	if (same && !force) || c == nil {
		if c != nil {
			c.sendShareCounts()
		}
		return
	}
	if cfg.Soulseek.NoShare {
		roots = nil
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		start := time.Now()
		m.shares.scan(m.ctx, roots, m.shareMeta(cfg))
		if m.ctx.Err() != nil {
			return
		}
		st := m.shares.Status()
		if len(roots) > 0 {
			msg := fmt.Sprintf("Soulseek: sharing %d files in %d folders (read in %s)", st.Files, st.Folders, time.Since(start).Round(100*time.Millisecond))
			if st.Problem != "" {
				msg += "; " + st.Problem
			}
			logf("%s", msg)
		}
		if c := m.Soulseek(); c != nil {
			c.sendShareCounts()
		}
	}()
}

// shareMeta tells what the app knows about a file's audio: from the library scan, or from
// the download that made it.
func (m *Manager) shareMeta(cfg Config) func(string) (Quality, float64, bool) {
	byPath := map[string]Quality{}
	if m.state != nil {
		for _, r := range m.state.all() {
			if r.Path != "" && r.Status == "downloaded" {
				byPath[r.Path] = r.Quality
			}
		}
	}
	return func(p string) (Quality, float64, bool) {
		if m.index != nil {
			if f := m.index.lookup(p); f != nil {
				return f.Q, f.Duration, true
			}
		}
		q, ok := byPath[p]
		return q, 0, ok
	}
}

// soulseekWish picks the next track for a wishlist search: tracks no source had, Soulseek
// included, in turn. When an answer fits, the track is tried on Soulseek again with it.
func (m *Manager) soulseekWish() (string, func([]slskResponse)) {
	m.jobsMu.Lock()
	n := len(m.order)
	var t *Track
	for i := 0; i < n; i++ {
		j := m.order[(m.wishPos+i)%n]
		j.mu.Lock()
		ok := j.state == JobFailed && j.tried["soulseek"]
		tr := j.Track
		j.mu.Unlock()
		if ok {
			t = tr
			m.wishPos = (m.wishPos + i + 1) % n
			break
		}
	}
	m.jobsMu.Unlock()
	if t == nil {
		return "", nil
	}
	qs := songQueries(t, 1)
	if len(qs) == 0 {
		return "", nil
	}
	return queryText(qs[0]), func(resps []slskResponse) {
		m.srcMu.RLock()
		src := m.slskSrc
		m.srcMu.RUnlock()
		if src == nil {
			return
		}
		cands := src.candidates(t, resps)
		if accepted, _ := rank(t, cands); len(accepted) > 0 {
			logf("Soulseek wishlist: %s turned up (%s from %s); trying it", t, accepted[0].Cand.Quality, firstNonEmpty(strings.SplitN(accepted[0].Cand.Ref, "\x00", 2)[0], "?"))
			src.remember(t.Key, cands)
			m.retryOn(t.Key, "soulseek")
		} else {
			debugf("Soulseek wishlist: %d answers for %s, none fits", len(resps), t)
		}
	}
}

// WishlistSize counts the tracks on the Soulseek wishlist.
func (m *Manager) WishlistSize() int {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	n := 0
	for _, j := range m.order {
		j.mu.Lock()
		if j.state == JobFailed && j.tried["soulseek"] {
			n++
		}
		j.mu.Unlock()
	}
	return n
}

// retryOn queues a track that wasn't found again, for one source only.
func (m *Manager) retryOn(key, name string) {
	j := m.job(key)
	if j == nil {
		return
	}
	j.mu.Lock()
	if j.state != JobFailed {
		j.mu.Unlock()
		return
	}
	tried := map[string]bool{}
	for _, s := range m.currentSources() {
		if s.Name() != name {
			tried[s.Name()] = true
		}
	}
	j.tried, j.state, j.lastErr, j.retryAt, j.rounds = tried, JobQueued, "", time.Time{}, 0
	j.bump = m.bumpSeq.Add(1)
	j.mu.Unlock()
	m.wake()
}

// soulseekStatus is the Sources tab's view of the client.
func (m *Manager) soulseekStatus() (problem, note string) {
	c := m.Soulseek()
	if c == nil {
		return "", ""
	}
	st := c.State()
	switch {
	case st.Online:
		return "", ""
	case st.Connecting:
		return "", "connecting to the Soulseek server"
	}
	return firstNonEmpty(st.Problem, "offline"), ""
}

// CheckSoulseekPort asks the Soulseek site whether our port is reachable from the internet.
func (m *Manager) CheckSoulseekPort(ctx context.Context) (bool, int, error) {
	c := m.Soulseek()
	if c == nil {
		return false, 0, fmt.Errorf("Soulseek is off")
	}
	port := c.listenPort()
	if port == 0 {
		return false, 0, fmt.Errorf("not listening on any port")
	}
	ok, err := checkSoulseekPort(ctx, c.route, port)
	return ok, port, err
}
