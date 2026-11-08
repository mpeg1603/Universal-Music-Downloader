package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The Soulseek client's place in the app: started while the Soulseek source is on and has an
// account, given the shared folders, and fed the wishlist (tracks no source had).

// setupSoulseek starts, adjusts or stops the client to match the settings.
func (m *Manager) setupSoulseek(cfg Config) {
	sc := cfg.Source("soulseek")
	m.srcMu.Lock()
	old, oldKey := m.slsk, m.slskKey
	m.srcMu.Unlock()
	if !sc.Enabled || !cfg.Soulseek.Configured() {
		if old != nil {
			m.srcMu.Lock()
			m.slsk, m.slskSrc, m.slskKey = nil, nil, ""
			m.slskClosed = time.Now()
			m.srcMu.Unlock()
			old.Close()
			Logf("Soulseek: logged out")
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
	c := newSlskClient(cfg.Soulseek, NormalizeRoute(sc.Route), m.shares)
	c.banFile = filepath.Join(m.dataDir, "soulseek-ban.txt")
	if old != nil {
		old.Close()
		old.mu.Lock()
		c.bannedTill = old.bannedTill
		old.mu.Unlock()
	}
	// A new client keeps the search budget and answers, and logs in at least 30 seconds
	// after the last one logged out.
	m.srcMu.Lock()
	if m.slskBudget == nil {
		m.slskBudget, m.slskAnswers = c.budget, c.answers
	}
	c.budget, c.answers = m.slskBudget, m.slskAnswers
	if old != nil {
		m.slskClosed = time.Now()
	}
	c.notBefore = m.slskClosed.Add(30 * time.Second)
	m.srcMu.Unlock()
	c.budget.set(cfg.Soulseek.SearchInterval(), cfg.Soulseek.SearchesPerHour())
	c.setWishlist(m.soulseekWish)
	src := newSoulseekSource(c, cfg.Format)
	src.jump, src.goodSeeder = m.mayJump, m.seederAffinity
	m.srcMu.Lock()
	m.slsk, m.slskKey, m.slskSrc = c, key, src
	m.srcMu.Unlock()
	m.shareFolders(cfg, false)
	c.Start()
}

// slskClient returns the running client (nil when the source is off).
func (m *Manager) slskClient() *slskClient {
	m.srcMu.RLock()
	defer m.srcMu.RUnlock()
	return m.slsk
}

// shareFolders scans the shared folders again when they changed (or always, with force).
func (m *Manager) shareFolders(cfg Config, force bool) {
	if !force && !m.ready.Load() {
		return // the first scan comes after the library's (see setup), whose details it uses
	}
	roots := cfg.ShareRoots()
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
			Logf("%s", msg)
		}
		if c := m.slskClient(); c != nil {
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
			Logf("Soulseek wishlist: %s turned up (%s from %s); trying it", t, accepted[0].Cand.Quality, FirstNonEmpty(strings.SplitN(accepted[0].Cand.Ref, "\x00", 2)[0], "?"))
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
	c := m.slskClient()
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
	return FirstNonEmpty(st.Problem, "offline"), ""
}

// CheckSoulseekPort asks the Soulseek site whether our port is reachable from the internet.
func (m *Manager) CheckSoulseekPort(ctx context.Context) (bool, int, error) {
	c := m.slskClient()
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

// --- seeder affinity ------------------------------------------------------------------------

// seederAffinity: a user just sent us a file fast and in good quality. While they're there,
// their shared files are looked through (once in a while) for other tracks in the queue, and
// those are lined up to come from them, one after another.
func (m *Manager) seederAffinity(user string, speed float64) {
	m.srcMu.Lock()
	src, c := m.slskSrc, m.slsk
	if m.browsed == nil {
		m.browsed = map[string]time.Time{}
	}
	if src == nil || c == nil || time.Since(m.browsed[user]) < 30*time.Minute || m.browsing >= 2 {
		m.srcMu.Unlock()
		return
	}
	m.browsed[user] = time.Now()
	m.browsing++
	m.srcMu.Unlock()
	defer func() {
		m.srcMu.Lock()
		m.browsing--
		m.srcMu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(m.ctx, 4*time.Minute)
	defer cancel()
	files, err := c.Browse(ctx, user)
	if err != nil {
		debugf("Soulseek: browsing %s: %v", user, err)
		return
	}
	n := m.lineUp(src, user, files)
	if n > 0 {
		Logf("Soulseek: %s sends fast (%s) and shares %d more tracks of your queue; getting them from there", user, FmtRate(speed), n)
	}
}

// lineUp finds queued tracks among a user's files and has them downloaded from that user.
func (m *Manager) lineUp(src *Soulseek, user string, files []slskFile) int {
	// Files by the folder names in their paths ("Deftones", "Deftones - White Pony"), so
	// only tracks whose artist has a folder there are looked at.
	byDir := map[string][]int{}
	for i, f := range files {
		parts := strings.Split(f.Name, `\`)
		seen := map[string]bool{}
		for _, p := range parts[:max(0, len(parts)-1)] {
			for _, k := range []string{normalize(p), normalize(strings.SplitN(p, " - ", 2)[0])} {
				if k != "" && !seen[k] {
					seen[k] = true
					byDir[k] = append(byDir[k], i)
				}
			}
		}
	}
	m.jobsMu.Lock()
	jobs := append([]*Job(nil), m.order...)
	m.jobsMu.Unlock()
	lined := 0
	for _, j := range jobs {
		j.mu.Lock()
		st, t := j.state, j.Track
		j.mu.Unlock()
		if st != JobQueued && st != JobWaiting && st != JobFailed {
			continue
		}
		var idx []int
		for _, a := range append(append([]string{}, t.Artists...), t.AlbumArtist) {
			idx = append(idx, byDir[normalize(a)]...)
		}
		if len(idx) == 0 {
			continue
		}
		resp := slskResponse{User: user, FreeSlot: true}
		for _, i := range idx {
			resp.Files = append(resp.Files, files[i])
		}
		cands := src.candidates(t, []slskResponse{resp})
		if accepted, _ := rank(t, cands); len(accepted) == 0 {
			continue
		}
		for i := range cands {
			cands[i].Unsearched = true // if it doesn't work out, Soulseek is searched as usual
		}
		src.remember(t.Key, cands)
		m.preferSource(t.Key, "soulseek")
		if lined++; lined >= 500 {
			break
		}
	}
	return lined
}

// preferSource lines a track up for a source: the ones before it are passed over this round
// and it goes to the front of the queue.
func (m *Manager) preferSource(key, name string) {
	j := m.job(key)
	if j == nil {
		return
	}
	tried := map[string]bool{}
	for _, s := range m.currentSources() {
		if s.Name() == name {
			break
		}
		tried[s.Name()] = true
	}
	j.mu.Lock()
	if j.state == JobQueued || j.state == JobWaiting || j.state == JobFailed {
		j.tried, j.state, j.lastErr, j.retryAt = tried, JobQueued, "", time.Time{}
		j.bump = m.bumpSeq.Add(1)
	}
	j.mu.Unlock()
	m.wake()
}
