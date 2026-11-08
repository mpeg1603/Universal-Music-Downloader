package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/encoding/charmap"
)

// Logging in to sites that need an account (RuTracker.org, say), the way Jackett's
// definitions describe it. Logins are rare on purpose: once, then again only when the site
// sends a request back to its login page; a failed login isn't retried for half an hour
// (repeated failures make sites ask for a captcha, or lock the account).

const (
	cgLoginKeep  = 12 * time.Hour   // a session is trusted this long without a new login
	cgLoginPause = 30 * time.Minute // after a failed login
)

// NeedsLogin says whether the indexer needs your account.
func (d *cgDef) NeedsLogin() bool { return d.Login != nil }

// ensureLogin logs in when the site needs it and the session isn't there (again: force).
func (c *cardigann) ensureLogin(ctx context.Context, force bool) error {
	l := c.def.Login
	if l == nil {
		return nil
	}
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if !force && !c.loggedIn.IsZero() && time.Since(c.loggedIn) < cgLoginKeep {
		return nil
	}
	if c.loginErr != nil && time.Since(c.loginTry) < cgLoginPause {
		return c.loginErr
	}
	c.loginTry = time.Now()
	c.inLogin = true
	c.loginMu.Unlock()
	err := c.login(ctx, l)
	c.loginMu.Lock()
	c.inLogin = false
	if ctx.Err() != nil {
		return ctx.Err() // not the site's fault: try again next time
	}
	c.loginErr = err
	if err == nil {
		c.loggedIn = time.Now()
		Logf("Torrent: logged in to %s", c.def.Name)
	} else {
		c.loggedIn = time.Time{}
		Logf("Torrent: %v", err)
	}
	return err
}

func (c *cardigann) login(ctx context.Context, l *cgLogin) error {
	vars := c.vars("")
	method := strings.ToLower(FirstNonEmpty(l.Method, "form"))
	required := []string{"username", "password"}
	if method == "cookie" {
		required = []string{"cookie"}
	}
	for _, s := range c.def.Settings {
		if v, _ := c.config[s.Name].(string); ContainsString(required, s.Name) && strings.TrimSpace(v) == "" {
			return failf("%s needs your account: Sources → Torrent → Enter → Choose indexers → %s → Enter", c.def.Name, c.def.Name)
		}
	}
	page := c.site + strings.TrimLeft(c.render(l.Path, vars), "/")
	var data []byte
	var err error
	switch method {
	case "cookie":
		u, _ := url.Parse(c.site)
		var cookies []*http.Cookie
		for _, part := range strings.Split(fmt.Sprint(c.config["cookie"]), ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
				cookies = append(cookies, &http.Cookie{Name: k, Value: v})
			}
		}
		c.http.Jar.SetCookies(u, cookies)
	case "post":
		values := url.Values{}
		for k, v := range l.Inputs {
			values.Set(k, c.render(v, vars))
		}
		data, err = c.fetch(ctx, "POST", page, strings.NewReader(c.encodeForm(values)))
	case "form":
		var form []byte
		if form, err = c.fetch(ctx, "GET", page, nil); err != nil {
			return fmt.Errorf("%s: can't open its login page: %w", c.def.Name, err)
		}
		doc, perr := goquery.NewDocumentFromReader(bytes.NewReader(form))
		if perr != nil {
			return perr
		}
		f := doc.Find(FirstNonEmpty(l.Form, "form")).First()
		values := url.Values{}
		f.Find("input[name]").Each(func(_ int, in *goquery.Selection) {
			name, _ := in.Attr("name")
			typ, _ := in.Attr("type")
			if typ == "checkbox" || typ == "radio" {
				if _, on := in.Attr("checked"); !on {
					return
				}
			}
			v, _ := in.Attr("value")
			values.Set(name, v)
		})
		for k, v := range l.Inputs {
			values.Set(k, c.render(v, vars))
		}
		action, _ := f.Attr("action")
		target := page
		if p := FirstNonEmpty(c.render(l.SubmitPath, vars), action); p != "" {
			if base, err := url.Parse(page); err == nil {
				if ref, err := url.Parse(p); err == nil {
					target = base.ResolveReference(ref).String()
				}
			}
		}
		data, err = c.fetch(ctx, "POST", target, strings.NewReader(c.encodeForm(values)))
	}
	if err != nil {
		return fmt.Errorf("%s: logging in: %w", c.def.Name, err)
	}
	if data != nil {
		doc, perr := goquery.NewDocumentFromReader(bytes.NewReader(data))
		if perr == nil {
			if sel := l.Captcha.Selector; sel != "" && doc.Find(sel).Length() > 0 {
				return blockedf("%s asks for a captcha (after failed logins): log in once at %s in your browser, check the username and password, and save the Torrent settings again", c.def.Name, c.site)
			}
			for _, e := range l.Error {
				if s := doc.Find(e.Selector).First(); s.Length() > 0 {
					msg := strings.Join(strings.Fields(s.Text()), " ")
					return failf("%s refused the login: %s", c.def.Name, FirstNonEmpty(FitWidth(msg, 160), "wrong username or password?"))
				}
			}
		}
	}
	// The test: a page only logged-in users see.
	if l.Test.Path != "" || l.Test.Selector != "" || l.Test.Contains != "" {
		page := data
		if l.Test.Path != "" {
			if page, err = c.fetch(ctx, "GET", c.site+strings.TrimLeft(c.render(l.Test.Path, vars), "/"), nil); err != nil {
				return fmt.Errorf("%s: checking the login: %w", c.def.Name, err)
			}
		}
		ok := true
		if l.Test.Contains != "" {
			ok = bytes.Contains(page, []byte(l.Test.Contains))
		}
		if ok && l.Test.Selector != "" {
			doc, perr := goquery.NewDocumentFromReader(bytes.NewReader(page))
			ok = perr == nil && doc.Find(l.Test.Selector).Length() > 0
		}
		if !ok {
			return failf("%s didn't accept the login (wrong username or password?)", c.def.Name)
		}
	}
	return nil
}

// encodeForm encodes form or query values in the site's character set (RuTracker reads
// windows-1251).
func (c *cardigann) encodeForm(values url.Values) string {
	enc := strings.ToLower(c.def.Encoding)
	if enc != "windows-1251" && enc != "cp1251" {
		return values.Encode()
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	e := charmap.Windows1251.NewEncoder()
	for _, k := range keys {
		for _, v := range values[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			if s, err := e.String(v); err == nil {
				v = s
			}
			b.WriteString(url.QueryEscape(k) + "=" + url.QueryEscape(v))
		}
	}
	return b.String()
}

// fetchFile downloads a file from the site (a .torrent) with the site's login, as it is.
func (c *cardigann) fetchFile(ctx context.Context, u string) ([]byte, error) {
	if err := c.ensureLogin(ctx, false); err != nil {
		return nil, err
	}
	if err := c.pace.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	if c.flare != nil {
		c.flare.prepare(req)
	} else {
		req.Header.Set("User-Agent", browserUA)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, unavailablef("%s: %s", c.def.Name, describeNetErr(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if isChallenge(data) {
		if c.flare == nil {
			return nil, blockedf("%s shows a browser check (Cloudflare) for the torrent file; set FlareSolverr in Sources → Torrent to get past it", c.def.Name)
		}
		if err := c.flare.clear(ctx, c.site); err != nil {
			return nil, err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		c.flare.prepare(req)
		resp2, err := c.http.Do(req)
		if err != nil {
			return nil, unavailablef("%s: %s", c.def.Name, describeNetErr(err))
		}
		defer resp2.Body.Close()
		resp = resp2
		if data, err = io.ReadAll(io.LimitReader(resp.Body, 32<<20)); err != nil {
			return nil, err
		}
	}
	if c.loginPath != "" && resp.Request != nil && resp.Request.URL.Path == c.loginPath {
		c.loginMu.Lock()
		c.loggedIn = time.Time{}
		c.loginMu.Unlock()
		return nil, failf("%s wants a new login to download “%s”; it's done with the next search", c.def.Name, u)
	}
	if resp.StatusCode != 200 {
		return nil, unavailablef("%s answered HTTP %d for the torrent file", c.def.Name, resp.StatusCode)
	}
	return data, nil
}
