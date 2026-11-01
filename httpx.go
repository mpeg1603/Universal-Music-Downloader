package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"

// SourceError is an expected failure; its message ends up in the failure report.
// Unavailable marks network trouble, so the track is retried automatically next run.
type SourceError struct {
	Msg         string
	Unavailable bool
}

func (e *SourceError) Error() string { return e.Msg }

func failf(format string, args ...any) error {
	return &SourceError{Msg: fmt.Sprintf(format, args...)}
}

func unavailablef(format string, args ...any) error {
	return &SourceError{Msg: fmt.Sprintf(format, args...), Unavailable: true}
}

func isUnavailable(err error) bool {
	var se *SourceError
	return errors.As(err, &se) && se.Unavailable
}

func describeNetErr(err error) string {
	var netErr net.Error
	var tlsErr *tls.RecordHeaderError
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timed out"
	case errors.As(err, &tlsErr), strings.Contains(err.Error(), "tls:"):
		return "TLS connection failed"
	default:
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return "DNS lookup failed"
		}
		return "connection failed"
	}
}

// rateLimiter spaces requests at least `interval` apart across goroutines.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (r *rateLimiter) wait(ctx context.Context) error {
	if r.interval <= 0 {
		return nil
	}
	r.mu.Lock()
	now := time.Now()
	start := r.next
	if start.Before(now) {
		start = now
	}
	r.next = start.Add(r.interval)
	r.mu.Unlock()
	select {
	case <-time.After(time.Until(start)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type HTTP struct {
	Name    string
	client  *http.Client
	limiter *rateLimiter
	retries int
}

func newHTTP(name string, interval time.Duration) *HTTP {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &HTTP{
		Name:    name,
		client:  &http.Client{Transport: transport},
		limiter: &rateLimiter{interval: interval},
		retries: 4,
	}
}

type request struct {
	method  string
	url     string
	body    []byte
	headers map[string]string
}

// do sends a request and returns the whole body, retrying network errors, 429 and 5xx.
func (h *HTTP) do(ctx context.Context, req request) (int, []byte, error) {
	if req.method == "" {
		req.method = "GET"
	}
	for attempt := 0; ; attempt++ {
		if err := h.limiter.wait(ctx); err != nil {
			return 0, nil, err
		}
		rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		r, err := http.NewRequestWithContext(rctx, req.method, req.url, bytes.NewReader(req.body))
		if err != nil {
			cancel()
			return 0, nil, err
		}
		r.Header.Set("User-Agent", userAgent)
		for k, v := range req.headers {
			r.Header.Set(k, v)
		}
		resp, err := h.client.Do(r)
		var body []byte
		if err == nil {
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
		cancel()
		retryable := err != nil || resp.StatusCode == 429 || resp.StatusCode >= 500
		if !retryable {
			return resp.StatusCode, body, nil
		}
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		if attempt == h.retries-1 {
			if err != nil {
				return 0, nil, unavailablef("%s unreachable (%s)", h.Name, describeNetErr(err))
			}
			return 0, nil, unavailablef("%s answered HTTP %d", h.Name, resp.StatusCode)
		}
		delay := time.Duration(1<<attempt) * time.Second
		if err == nil {
			if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
				delay = min(time.Duration(s)*time.Second, 2*time.Minute)
			}
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}
}

func (h *HTTP) get(ctx context.Context, u string) (int, []byte, error) {
	return h.do(ctx, request{url: u})
}

func (h *HTTP) getJSON(ctx context.Context, u string, out any) error {
	status, body, err := h.get(ctx, u)
	if err != nil {
		return err
	}
	if status != 200 {
		return failf("%s answered HTTP %d", h.Name, status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return failf("%s sent an unreadable answer", h.Name)
	}
	return nil
}

func (h *HTTP) postJSON(ctx context.Context, u string, payload any, headers map[string]string, out any) error {
	body, _ := json.Marshal(payload)
	hdr := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		hdr[k] = v
	}
	status, resp, err := h.do(ctx, request{method: "POST", url: u, body: body, headers: hdr})
	if err != nil {
		return err
	}
	if status != 200 {
		return failf("%s answered HTTP %d", h.Name, status)
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return failf("%s sent an unreadable answer", h.Name)
	}
	return nil
}

// download streams url into dir/<stem><ext>, taking the extension from the response.
func (h *HTTP) download(ctx context.Context, u, dir, stem string) (string, error) {
	for attempt := 0; ; attempt++ {
		if err := h.limiter.wait(ctx); err != nil {
			return "", err
		}
		p, err := h.downloadOnce(ctx, u, dir, stem)
		if err == nil || !isUnavailable(err) || ctx.Err() != nil || attempt == h.retries-1 {
			return p, err
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
}

func (h *HTTP) downloadOnce(ctx context.Context, u, dir, stem string) (string, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	r.Header.Set("User-Agent", userAgent)
	resp, err := h.client.Do(r)
	if err != nil {
		return "", unavailablef("%s download failed (%s)", h.Name, describeNetErr(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 410:
		return "", failf("%s refused the download (HTTP %d)", h.Name, resp.StatusCode)
	case resp.StatusCode != 200:
		return "", unavailablef("%s download failed (HTTP %d)", h.Name, resp.StatusCode)
	}
	dest := filepath.Join(dir, stem+extensionFor(resp, u))
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return "", err
	}
	var body io.Reader = resp.Body
	if task := taskFrom(ctx); task != nil {
		body = &countingReader{r: resp.Body, task: task, total: max(resp.ContentLength, 0)}
	}
	_, err = io.Copy(f, body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return "", unavailablef("%s download broke off (%s)", h.Name, describeNetErr(err))
	}
	return dest, os.Rename(part, dest)
}

// countingReader reports download progress to a task.
type countingReader struct {
	r           io.Reader
	task        *Task
	done, total int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	c.task.setProgress(c.done, c.total)
	return n, err
}

var reFilename = regexp.MustCompile(`filename\*=UTF-8''([^;]+)|filename="?([^";]+)"?`)

func extensionFor(resp *http.Response, u string) string {
	name := ""
	if m := reFilename.FindStringSubmatch(resp.Header.Get("Content-Disposition")); m != nil {
		name = m[1] + m[2]
		if n, err := url.PathUnescape(name); err == nil {
			name = n
		}
	} else if pu, err := url.Parse(u); err == nil {
		name = pu.Path
	}
	if ext := strings.ToLower(path.Ext(name)); ext != "" && len(ext) <= 6 {
		return ext
	}
	ctype, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if exts, _ := mime.ExtensionsByType(ctype); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}
