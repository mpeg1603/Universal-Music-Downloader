package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Testing a source or a torrent site by hand: one real, small request, to see whether it answers.

// ConnectionCheck is how a test went.
type ConnectionCheck struct {
	Name   string
	OK     bool
	Took   time.Duration
	Detail string
}

const (
	checkTime  = 100 * time.Second // FlareSolverr and a login can take a minute
	checkQuery = "Beatles"
)

// checkFeed makes one search on a torrent site.
func checkFeed(ctx context.Context, f *Torznab) ConnectionCheck {
	c := ConnectionCheck{Name: f.Name}
	select {
	case f.sem <- struct{}{}:
	case <-ctx.Done():
		c.Detail = "it's too busy to test right now"
		return c
	}
	start := time.Now()
	res, err := f.search(ctx, checkQuery)
	<-f.sem
	c.Took = time.Since(start).Round(100 * time.Millisecond)
	switch {
	case err != nil:
		c.Detail = err.Error()
	case len(res) == 0:
		c.Detail = fmt.Sprintf("it answered in %s but listed nothing: the site may have changed", c.Took)
	default:
		c.OK = true
		c.Detail = fmt.Sprintf("works: %d results in %s", len(res), c.Took)
	}
	return c
}

// resume lets a site that's left out for failing be used again.
func (z *Torznab) resume() {
	z.mu.Lock()
	z.fails, z.until = 0, time.Time{}
	z.mu.Unlock()
}

func (m *Manager) torrentSource() *TorrentSource {
	m.srcMu.RLock()
	defer m.srcMu.RUnlock()
	return m.torrent
}

// CheckIndexer tests a built-in torrent site, on or off. A site that works and was left out
// for failing is used again.
func (m *Manager) CheckIndexer(ctx context.Context, id string) ConnectionCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTime)
	defer cancel()
	if s := m.torrentSource(); s != nil {
		for _, f := range s.feeds {
			if f.cg != nil && f.cg.def.ID == id {
				c := checkFeed(ctx, f)
				if c.OK {
					f.resume()
				}
				return c
			}
		}
	}
	data := m.indexerDefs.get(id)
	if data == nil {
		return ConnectionCheck{Name: id, Detail: "no such site"}
	}
	cfg := m.Config()
	cg, err := newCardigannWith(data, newFlareSolverr(cfg.Indexers.FlareSolverr), NormalizeRoute(cfg.Source("torrent").Route), cfg.Indexers.Settings[id])
	if err != nil {
		return ConnectionCheck{Name: id, Detail: err.Error()}
	}
	return checkFeed(ctx, newBuiltinFeed(cg))
}

// CheckSource tests a source that's on. Soulseek's connection is only looked at (its searches
// are scarce), and the torrent source is every site it uses.
func (m *Manager) CheckSource(ctx context.Context, name string) ConnectionCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTime)
	defer cancel()
	c := ConnectionCheck{Name: name}
	var src Source
	for _, s := range m.currentSources() {
		if s.Name() == name {
			src = s
		}
	}
	if src == nil {
		c.Detail = "it isn't on, or isn't set up"
		return c
	}
	c.Name = src.Label()
	switch s := src.(type) {
	case *Soulseek:
		st := s.c.State()
		c.OK = st.Online
		c.Detail = "signed in as " + st.User
		if !st.Online {
			c.Detail = FirstNonEmpty(st.Problem, "not connected")
		}
		return c
	case *TorrentSource:
		return checkTorrent(ctx, c, s)
	}
	start := time.Now()
	cands, err := src.Search(ctx, &Track{Key: "connection-test", Title: "Get Lucky", Artists: []string{"Daft Punk"}, Album: "Random Access Memories", DurationMs: 248000})
	c.Took = time.Since(start).Round(100 * time.Millisecond)
	switch {
	case err != nil:
		c.Detail = err.Error()
	default:
		c.OK = true
		c.Detail = fmt.Sprintf("works: %d results for a test song in %s", len(cands), c.Took)
	}
	return c
}

func checkTorrent(ctx context.Context, c ConnectionCheck, s *TorrentSource) ConnectionCheck {
	start := time.Now()
	results := make([]ConnectionCheck, len(s.feeds))
	var wg sync.WaitGroup
	for i, f := range s.feeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = checkFeed(ctx, f)
			if results[i].OK {
				f.resume()
			}
		}()
	}
	wg.Wait()
	c.Took = time.Since(start).Round(100 * time.Millisecond)
	var bad []string
	for _, r := range results {
		if r.OK {
			continue
		}
		bad = append(bad, r.Name)
	}
	c.OK = len(results) > len(bad)
	c.Detail = fmt.Sprintf("%d of %d sites work", len(results)-len(bad), len(results))
	if len(bad) > 0 {
		c.Detail += "; not: " + strings.Join(bad, ", ")
	}
	return c
}
