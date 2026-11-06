package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Name-your-price releases whose artist asks for an email address: Bandcamp emails the download
// link. bandcampMail keeps track of which releases were asked for and the links received, saved
// to disk so a restart doesn't ask Bandcamp again. The links come from your mailbox (Gmail via
// Google sign-in, or IMAP), or you paste them from the emails yourself.

// mailFetcher reads Bandcamp's emails received since a time, without changing the mailbox.
type mailFetcher interface {
	fetch(ctx context.Context, since time.Time) ([][]byte, error)
}

type imapMailbox struct{ host, user, pass string }

func (b imapMailbox) fetch(ctx context.Context, since time.Time) ([][]byte, error) {
	return fetchBandcampMails(ctx, b.host, b.user, b.pass, since.Add(-24*time.Hour)) // IMAP searches by date only
}

type bandcampMail struct {
	path string

	mu        sync.Mutex
	Requested map[string]time.Time `json:"requested"` // release -> when Bandcamp was asked to email it
	Links     map[string]string    `json:"links"`     // release -> download page from the email
	Seen      map[string]string    `json:"seen"`      // link -> release ("" if it didn't open)

	cfg     BandcampCfg
	fetcher mailFetcher
	problem string // why the mailbox can't be read (not set up fully)
	http    *HTTP
	waiters map[string][]chan string
	polling bool
	lastErr string
	onLink  func() // a link arrived: jobs waiting for one can go on
}

func newBandcampMail(path string) *bandcampMail {
	m := &bandcampMail{path: path, Requested: map[string]time.Time{}, Links: map[string]string{},
		Seen: map[string]string{}, waiters: map[string][]chan string{}, http: newRoutedHTTP("Bandcamp", 500*time.Millisecond, "system")}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, m); err != nil {
			debugf("reading %s: %v", path, err)
		}
		for _, mp := range []*map[string]string{&m.Links, &m.Seen} {
			if *mp == nil {
				*mp = map[string]string{}
			}
		}
		if m.Requested == nil {
			m.Requested = map[string]time.Time{}
		}
	}
	return m
}

// configure applies the settings; fetcher is nil when you paste the links yourself.
func (m *bandcampMail) configure(cfg BandcampCfg, route string, fetcher mailFetcher, problem string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg, m.fetcher, m.problem, m.lastErr = cfg, fetcher, problem, ""
	m.http = newRoutedHTTP("Bandcamp", 500*time.Millisecond, route)
}

func (m *bandcampMail) settings() (BandcampCfg, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg, m.fetcher != nil
}

func (m *bandcampMail) saveLocked() {
	b, err := json.MarshalIndent(m, "", " ")
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(m.path), 0o755); err == nil {
			tmp := m.path + ".tmp"
			if err = os.WriteFile(tmp, b, 0o600); err == nil {
				err = os.Rename(tmp, m.path)
			}
		}
	}
	if err != nil {
		debugf("saving %s: %v", m.path, err)
	}
}

func (m *bandcampMail) link(key string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Links[key]
}

func (m *bandcampMail) requestedAt(key string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Requested[key]
}

func (m *bandcampMail) markRequested(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Requested[key] = time.Now()
	m.saveLocked()
}

// dropLink forgets a link that no longer works, so the release is asked for again.
func (m *bandcampMail) dropLink(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Links, key)
	delete(m.Requested, key)
	m.saveLocked()
}

// pending is how many releases were asked for and have no link yet.
func (m *bandcampMail) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.Requested {
		if m.Links[k] == "" {
			n++
		}
	}
	return n
}

// status: releases waiting for their link, and what's wrong with the mailbox, if anything.
func (m *bandcampMail) status() (pending int, problem string) {
	n := m.pending()
	m.mu.Lock()
	defer m.mu.Unlock()
	return n, firstNonEmpty(m.problem, m.lastErr)
}

func (m *bandcampMail) wait(key string) chan string {
	ch := make(chan string, 1)
	m.mu.Lock()
	m.waiters[key] = append(m.waiters[key], ch)
	m.mu.Unlock()
	return ch
}

func (m *bandcampMail) forget(key string, ch chan string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.waiters[key]
	for i, c := range list {
		if c == ch {
			m.waiters[key] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(m.waiters[key]) == 0 {
		delete(m.waiters, key)
	}
}

// add takes a download link from an email: it reads which release it is for, keeps it and
// hands it to whoever waits for it. Links already known are skipped.
func (m *bandcampMail) add(ctx context.Context, link string) (key string, fresh bool, err error) {
	m.mu.Lock()
	key, seen := m.Seen[link]
	hc := m.http
	m.mu.Unlock()
	if seen && key != "" {
		return key, false, nil
	}
	key, err = linkItem(ctx, hc, link)
	if err != nil {
		if ctx.Err() == nil && !isUnavailable(err) {
			m.mu.Lock()
			m.Seen[link] = ""
			m.saveLocked()
			m.mu.Unlock()
		}
		return "", false, err
	}
	m.mu.Lock()
	m.Seen[link] = key
	fresh = m.Links[key] != link
	m.Links[key] = link
	for _, ch := range m.waiters[key] {
		select {
		case ch <- link:
		default:
		}
	}
	m.saveLocked()
	cb := m.onLink
	m.mu.Unlock()
	if fresh && cb != nil {
		cb()
	}
	return key, fresh, nil
}

// AddText takes the download links found in pasted text (the links themselves, or whole
// emails). It returns how many links were found and how many were new.
func (m *bandcampMail) AddText(ctx context.Context, text string) (found, added int, errs []string) {
	for _, link := range findBandcampLinks(text) {
		found++
		_, fresh, err := m.add(ctx, link)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("%s: %v", link, err))
		case fresh:
			added++
		}
	}
	return found, added, errs
}

var reAnyURL = regexp.MustCompile(`https?://[^\s"'<>]+`)

// findBandcampLinks finds Bandcamp download links in text: plain, in HTML, or wrapped in a
// redirect link (such as Google's ?q=…).
func findBandcampLinks(text string) []string {
	text = html.UnescapeString(text)
	seen := map[string]bool{}
	var out []string
	add := func(l string) {
		l = strings.TrimRight(l, ".,;)]")
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	for _, l := range reBandcampDownload.FindAllString(text, -1) {
		add(l)
	}
	for _, u := range reAnyURL.FindAllString(text, -1) {
		pu, err := url.Parse(strings.TrimRight(u, ".,;)]"))
		if err != nil {
			continue
		}
		for _, vs := range pu.Query() {
			for _, v := range vs {
				if l := reBandcampDownload.FindString(v); l != "" && l == v {
					add(l)
				}
			}
		}
	}
	return out
}

// poll reads the mailbox every 20 seconds while someone waits for a link.
func (m *bandcampMail) poll() {
	m.mu.Lock()
	if m.polling || m.fetcher == nil {
		m.mu.Unlock()
		return
	}
	m.polling = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.polling = false
		m.mu.Unlock()
	}()
	for {
		m.mu.Lock()
		n, f := len(m.waiters), m.fetcher
		var since time.Time
		for k := range m.waiters {
			if at := m.Requested[k]; !at.IsZero() && (since.IsZero() || at.Before(since)) {
				since = at
			}
		}
		m.mu.Unlock()
		if n == 0 || f == nil {
			return
		}
		if since.IsZero() {
			since = time.Now()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		msgs, err := f.fetch(ctx, since.Add(-time.Hour))
		m.mu.Lock()
		m.lastErr = ""
		if err != nil {
			m.lastErr = err.Error()
		}
		m.mu.Unlock()
		if err != nil {
			logf("Bandcamp email: %v", err)
		}
		for _, raw := range msgs {
			for _, link := range bandcampLinks(raw) {
				if _, _, err := m.add(ctx, link); err != nil {
					debugf("bandcamp email link %s: %v", link, err)
				}
			}
		}
		cancel()
		time.Sleep(20 * time.Second)
	}
}

// linkItem reads which release a download page is for.
func linkItem(ctx context.Context, hc *HTTP, link string) (string, error) {
	status, body, err := hc.get(ctx, link)
	if err != nil {
		return "", err
	}
	if isChallenge(body) {
		return "", blockedf("Bandcamp is showing its bot check page to this connection")
	}
	var blob struct {
		DigitalItems []struct {
			ItemID json.Number `json:"item_id"`
			Type   string      `json:"type"`
		} `json:"digital_items"`
	}
	if !dataAttr(body, "data-blob", &blob) || len(blob.DigitalItems) == 0 {
		if status != 200 {
			return "", fmt.Errorf("the link doesn't open (HTTP %d); it may have expired", status)
		}
		return "", errors.New("no download on that page; it may have expired")
	}
	id, _ := blob.DigitalItems[0].ItemID.Int64()
	return itemKey(blob.DigitalItems[0].Type, id), nil
}
