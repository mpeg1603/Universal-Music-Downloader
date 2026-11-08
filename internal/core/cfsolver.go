package core

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// Cloudflare's "checking your browser" pages, which some torrent sites show, are left to
// FlareSolverr (github.com/FlareSolverr/FlareSolverr) when you run it, as Jackett does: it
// loads the page, and its cookies and user agent are reused for a while. Without it, such a
// site is paused like any site that pushes back.

type challengeSolver interface {
	// clear gets past the check for u's site; prepare then sends its cookies with requests.
	clear(ctx context.Context, u string) error
	prepare(req *http.Request)
	Name() string
}

// solverCookies are the cookies and user agent a solved check left, by host.
type solverCookies struct {
	mu  sync.Mutex
	ua  string
	jar map[string][]*http.Cookie
	at  map[string]time.Time
}

func (s *solverCookies) prepare(req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req.Header.Set("User-Agent", FirstNonEmpty(s.ua, browserUA))
	for _, ck := range s.jar[req.URL.Hostname()] {
		req.AddCookie(ck)
	}
}

func (s *solverCookies) set(host, ua string, cookies []*http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jar == nil {
		s.jar, s.at = map[string][]*http.Cookie{}, map[string]time.Time{}
	}
	if ua != "" {
		s.ua = ua
	}
	s.jar[host], s.at[host] = cookies, time.Now()
}

// fresh: the host's check was solved within the last few minutes (a second check this soon
// means the cookies don't help, and solving again won't either).
func (s *solverCookies) fresh(host string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.at[host]) < 2*time.Minute
}
