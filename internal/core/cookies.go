package core

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Your YouTube login, for yt-dlp. yt-dlp saves its cookie jar back into the --cookies file
// when it exits, and YouTube's answers expire or rotate login cookies; run on your own
// cookies.txt, every run rewrote it, and the login was soon gone from the file for good.
// So yt-dlp never gets your file: each run gets its own copy, deleted afterwards.
//
// Cookies from a browser are read once (with yt-dlp, which knows how to decrypt them) into
// a private snapshot, refreshed every few minutes, so a live browser session's rotated
// cookies are picked up; runs get copies of that snapshot. Only YouTube's, Google's and
// SoundCloud's cookies are kept.

const cookieRefresh = 10 * time.Minute

var cookieDomains = []string{"youtube.com", "google.com", "soundcloud.com"}

type ytCookies struct {
	file    string // your cookies.txt (only read)
	browser string // yt-dlp's --cookies-from-browser value, e.g. "chromium"
	bin     string // yt-dlp, to read the browser's cookies
	dir     string // private folder for the snapshot and the per-run copies

	mu      sync.Mutex
	snap    []byte // the browser's cookies (Netscape format), or the file's
	snapAt  time.Time
	snapErr error
	seq     int
}

func newYtCookies(file, browser, bin, dir string) *ytCookies {
	if file == "" && browser == "" {
		return nil
	}
	return &ytCookies{file: ExpandHome(file), browser: strings.TrimSpace(browser), bin: bin, dir: dir}
}

// describe says where the cookies come from, for messages.
func (c *ytCookies) describe() string {
	if c.browser != "" {
		return "your " + c.browser + " browser's cookies"
	}
	return "the cookies file " + c.file
}

// load returns the cookies to hand out, reading them again when they're old.
func (c *ytCookies) load(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := time.Since(c.snapAt) < cookieRefresh
	if c.snap != nil && fresh {
		return c.snap, nil
	}
	if c.snapErr != nil && time.Since(c.snapAt) < time.Minute {
		return c.snap, c.snapErr // don't try again on every run
	}
	var data []byte
	var err error
	if c.browser != "" {
		data, err = c.fromBrowser(ctx)
	} else {
		data, err = os.ReadFile(c.file)
		if err != nil {
			err = fmt.Errorf("can't read the cookies file: %w", err)
		}
	}
	c.snapAt = time.Now()
	if err != nil {
		c.snapErr = err
		return c.snap, err // the last good ones, if any
	}
	c.snap, c.snapErr = keepDomains(data, cookieDomains), nil
	return c.snap, nil
}

// fromBrowser has yt-dlp export the browser's cookies into a private temporary file.
func (c *ytCookies) fromBrowser(ctx context.Context) ([]byte, error) {
	if c.bin == "" {
		return nil, fmt.Errorf("yt-dlp is needed to read the browser's cookies")
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return nil, err
	}
	tmp := filepath.Join(c.dir, fmt.Sprintf("export-%d.txt", time.Now().UnixNano()))
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	// Without a URL yt-dlp exits with a usage error, after writing the cookie jar.
	var stderr bytes.Buffer
	cmd := command(ctx, c.bin, "--ignore-config", "--cookies-from-browser", c.browser, "--cookies", tmp)
	cmd.Stderr = &stderr
	cmd.Run()
	data, err := os.ReadFile(tmp)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		msg := lastErrorLine(stderr.String())
		if msg == "" {
			msg = "yt-dlp exported nothing"
		}
		return nil, fmt.Errorf("couldn't read %s's cookies: %s", c.browser, msg)
	}
	return data, nil
}

func lastErrorLine(stderr string) string {
	msg := ""
	for _, line := range strings.Split(reANSI.ReplaceAllString(stderr, ""), "\n") {
		if s, ok := strings.CutPrefix(strings.TrimSpace(line), "ERROR:"); ok {
			msg = strings.TrimSpace(s)
		}
	}
	return msg
}

// copyFor writes a copy of the cookies for one yt-dlp run; remove deletes it afterwards.
func (c *ytCookies) copyFor(ctx context.Context) (path string, remove func(), err error) {
	data, err := c.load(ctx)
	if data == nil {
		return "", func() {}, err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return "", func() {}, err
	}
	c.mu.Lock()
	c.seq++
	n := c.seq
	c.mu.Unlock()
	path = filepath.Join(c.dir, fmt.Sprintf("run-%d-%d.txt", os.Getpid(), n))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", func() {}, err
	}
	return path, func() { os.Remove(path) }, nil
}

// check reads the cookies and says what's wrong with them for YouTube ("" when they hold a login).
func (c *ytCookies) check(ctx context.Context) string {
	data, err := c.load(ctx)
	if err != nil && data == nil {
		return err.Error()
	}
	if !hasYouTubeLogin(data) {
		if c.browser != "" {
			return fmt.Sprintf("%s have no YouTube login: log in to YouTube in %s", c.describe(), c.browser)
		}
		return fmt.Sprintf("%s has no YouTube login in it (it expired or was rotated out): export it again from a "+
			"private window you then close, or better, choose your browser in YouTube's settings", c.describe())
	}
	return ""
}

// botCheckAdvice is what to say when YouTube asks to sign in.
func (c *ytCookies) botCheckAdvice(ctx context.Context) string {
	if c == nil {
		return "in Sources, open YouTube's settings and choose a browser where you're logged in to YouTube"
	}
	if problem := c.check(ctx); problem != "" {
		return problem
	}
	return "even with your login (" + c.describe() + "); YouTube may be flagging this connection: try another connection for it in its settings"
}

// cookieLines calls f with the domain, name and value of each cookie in a Netscape cookie file.
func cookieLines(data []byte, f func(line, domain, name, value string)) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		raw := strings.TrimPrefix(line, "#HttpOnly_")
		if strings.HasPrefix(raw, "#") || strings.TrimSpace(raw) == "" {
			f(line, "", "", "")
			continue
		}
		fields := strings.Split(raw, "\t")
		if len(fields) < 7 {
			f(line, "", "", "")
			continue
		}
		f(line, strings.ToLower(fields[0]), fields[5], fields[6])
	}
}

// keepDomains drops the cookies of other sites (comments and the header stay).
func keepDomains(data []byte, domains []string) []byte {
	var b bytes.Buffer
	cookieLines(data, func(line, domain, _, _ string) {
		if domain != "" && !matchesDomain(domain, domains) {
			return
		}
		b.WriteString(line)
		b.WriteByte('\n')
	})
	return b.Bytes()
}

func matchesDomain(domain string, domains []string) bool {
	d := strings.TrimPrefix(domain, ".")
	for _, want := range domains {
		if d == want || strings.HasSuffix(d, "."+want) {
			return true
		}
	}
	return false
}

// hasYouTubeLogin tells whether the cookies log in to YouTube: yt-dlp needs LOGIN_INFO and one
// of the SAPISID cookies on youtube.com.
func hasYouTubeLogin(data []byte) bool {
	have := map[string]bool{}
	cookieLines(data, func(_, domain, name, value string) {
		if value != "" && matchesDomain(domain, []string{"youtube.com"}) {
			have[name] = true
		}
	})
	return have["LOGIN_INFO"] && (have["SAPISID"] || have["__Secure-3PAPISID"] || have["__Secure-1PAPISID"])
}
