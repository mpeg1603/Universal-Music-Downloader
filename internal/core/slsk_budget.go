package core

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Soulseek server bans clients that search too much ("You have been banned for 30
// minutes. This is usually the result of doing too many operations at once… Do not quickly
// repeat a search."). It publishes no numbers; one search every 5 seconds got this app banned
// after about 170 searches (14 minutes), twice, while answering others' searches for half an
// hour did not. So searches are budgeted, the same search isn't sent twice within an hour (its
// answers are kept), and a ban is waited out in full, across restarts too.

// slskSearchBudget spaces searches out and caps how many go out in 10 minutes and in an hour.
type slskSearchBudget struct {
	mu      sync.Mutex
	gap     time.Duration // between two searches
	perHour int           // in any hour; a quarter of it in any 10 minutes
	next    time.Time     // the earliest the next search may go
	sent    []time.Time   // the last hour's searches (and slots already given out)
}

func newSlskSearchBudget(gap time.Duration, perHour int) *slskSearchBudget {
	return &slskSearchBudget{gap: gap, perHour: perHour}
}

func (b *slskSearchBudget) set(gap time.Duration, perHour int) {
	b.mu.Lock()
	b.gap, b.perHour = gap, perHour
	b.mu.Unlock()
}

func (b *slskSearchBudget) burst() int { return max(1, b.perHour/4) }

// slot is the earliest time from now a search may go out. Callers hold mu.
func (b *slskSearchBudget) slot(now time.Time) time.Time {
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(b.sent) && !b.sent[i].After(cut) {
		i++
	}
	b.sent = b.sent[i:]
	t := now
	if b.next.After(t) {
		t = b.next
	}
	// The searches that must age out first: the hour's, then the 10 minutes'.
	if n := len(b.sent); n >= b.perHour {
		t = later(t, b.sent[n-b.perHour].Add(time.Hour))
	}
	if n, k := len(b.sent), b.burst(); n >= k {
		t = later(t, b.sent[n-k].Add(10*time.Minute))
	}
	return t
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// backlog is how long a search started now would wait for its turn.
func (b *slskSearchBudget) backlog() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return max(0, time.Until(b.slot(time.Now())))
}

// wait takes the next slot and waits for it.
func (b *slskSearchBudget) wait(ctx context.Context) error {
	b.mu.Lock()
	t := b.slot(time.Now())
	b.sent = append(b.sent, t)
	b.next = t.Add(b.gap)
	b.mu.Unlock()
	select {
	case <-time.After(time.Until(t)):
		return nil
	case <-ctx.Done():
		b.mu.Lock()
		for i := len(b.sent) - 1; i >= 0; i-- { // give the slot back
			if b.sent[i].Equal(t) {
				b.sent = append(b.sent[:i], b.sent[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		return ctx.Err()
	}
}

// --- answers kept, so a search isn't repeated --------------------------------------------------

const (
	slskSearchKeep   = time.Hour
	slskSearchesKept = 400 // searches kept
)

type slskCachedSearch struct {
	resps []slskResponse
	at    time.Time
	done  chan struct{} // closed when the search has finished
	err   error
}

type slskSearchCache struct {
	mu sync.Mutex
	m  map[string]*slskCachedSearch
}

func searchCacheKey(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// get returns the search for text: one kept or under way (wait on done), or, with mine
// true, a new entry the caller must fill in and finish.
func (c *slskSearchCache) get(text string) (s *slskCachedSearch, mine bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*slskCachedSearch{}
	}
	k := searchCacheKey(text)
	if s := c.m[k]; s != nil {
		select {
		case <-s.done:
			if s.err == nil && time.Since(s.at) < slskSearchKeep {
				return s, false
			}
		default:
			return s, false // under way
		}
	}
	if len(c.m) >= slskSearchesKept {
		var oldK string
		var oldT time.Time
		for k, v := range c.m {
			select {
			case <-v.done:
				if oldK == "" || v.at.Before(oldT) {
					oldK, oldT = k, v.at
				}
			default:
			}
		}
		delete(c.m, oldK)
	}
	s = &slskCachedSearch{done: make(chan struct{})}
	c.m[k] = s
	return s, true
}

func (c *slskSearchCache) finish(s *slskCachedSearch, resps []slskResponse, err error) {
	c.mu.Lock()
	s.resps, s.err, s.at = resps, err, time.Now()
	c.mu.Unlock()
	close(s.done)
}

// --- bans -----------------------------------------------------------------------------------

var reSlskBan = regexp.MustCompile(`(?i)banned for (\d+) (minute|hour)`)

// slskBanLength reads a server message that says you're banned, and for how long.
func slskBanLength(text string) (time.Duration, bool) {
	m := reSlskBan.FindStringSubmatch(text)
	if m == nil {
		if strings.Contains(strings.ToLower(text), "you have been banned") {
			return time.Hour, true
		}
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	unit := time.Minute
	if strings.EqualFold(m[2], "hour") {
		unit = time.Hour
	}
	return time.Duration(n) * unit, true
}

// loadBan reads when a ban saved by an earlier run ends.
func loadBan(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil || time.Until(t) <= 0 {
		return time.Time{}
	}
	return t
}

func saveBan(path string, until time.Time) {
	if path != "" {
		writeFileAtomic(path, []byte(until.Format(time.RFC3339)+"\n"))
	}
}
