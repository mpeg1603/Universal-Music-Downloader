package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Gmail without an app password: you sign in with Google in your browser and allow read-only
// access to your mail (gmail.readonly: the app can't send, change or delete anything). Google
// only lets registered apps ask for that, so you register your own (free, a few minutes, see
// the README) and give its client ID and secret. The sign-in is kept as a refresh token in
// config.json.

var (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	gmailAPI       = "https://gmail.googleapis.com"
)

const gmailScope = "https://www.googleapis.com/auth/gmail.readonly"

var gmailHTTP = &http.Client{Timeout: 45 * time.Second}

type gmailBox struct {
	clientID, secret, refresh string

	mu     sync.Mutex
	access string
	expiry time.Time
}

func newGmailBox(cfg BandcampCfg) *gmailBox {
	return &gmailBox{clientID: cfg.GmailClientID, secret: cfg.GmailClientSecret, refresh: cfg.GmailToken}
}

type googleToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func googleTokenRequest(ctx context.Context, form url.Values) (googleToken, error) {
	var t googleToken
	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := gmailHTTP.Do(req)
	if err != nil {
		return t, unavailablef("Google unreachable (%s)", describeNetErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, &t); err != nil {
		return t, fmt.Errorf("Google answered HTTP %d", resp.StatusCode)
	}
	switch {
	case t.Error == "invalid_grant":
		return t, errors.New("the Gmail sign-in has expired or was revoked: sign in again (Sources → Bandcamp). " +
			"If this happens every week, set your Google Cloud app's publishing status to \"In production\"")
	case t.Error == "invalid_client" || t.Error == "unauthorized_client":
		return t, fmt.Errorf("Google doesn't accept the client ID/secret (%s)", FirstNonEmpty(t.Description, t.Error))
	case t.Error != "":
		return t, fmt.Errorf("Google: %s", FirstNonEmpty(t.Description, t.Error))
	case t.AccessToken == "":
		return t, fmt.Errorf("Google answered HTTP %d without a token", resp.StatusCode)
	}
	return t, nil
}

func (g *gmailBox) token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access != "" && time.Until(g.expiry) > time.Minute {
		return g.access, nil
	}
	if g.refresh == "" {
		return "", errors.New("not signed in to Gmail (Sources → Bandcamp → Sign in with Google)")
	}
	t, err := googleTokenRequest(ctx, url.Values{"client_id": {g.clientID}, "client_secret": {g.secret},
		"refresh_token": {g.refresh}, "grant_type": {"refresh_token"}})
	if err != nil {
		return "", err
	}
	g.access, g.expiry = t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn)*time.Second)
	return g.access, nil
}

func gmailGet(ctx context.Context, token, path string, q url.Values, out any) error {
	u := gmailAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := gmailHTTP.Do(req)
	if err != nil {
		return unavailablef("Gmail unreachable (%s)", describeNetErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(body, &e)
		msg := FirstNonEmpty(e.Error.Message, http.StatusText(resp.StatusCode))
		if resp.StatusCode == 403 && strings.Contains(msg, "has not been used") {
			msg = "the Gmail API isn't enabled in your Google Cloud project (APIs & Services → Library → Gmail API → Enable)"
		}
		return fmt.Errorf("Gmail: %s (HTTP %d)", msg, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// fetch returns Bandcamp's emails received since the given time (spam included: they
// sometimes end up there), as raw messages.
func (g *gmailBox) fetch(ctx context.Context, since time.Time) ([][]byte, error) {
	tok, err := g.token(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	q := url.Values{"q": {fmt.Sprintf("from:bandcamp.com after:%d", since.Unix())}, "maxResults": {"50"},
		"includeSpamTrash": {"true"}}
	if err := gmailGet(ctx, tok, "/gmail/v1/users/me/messages", q, &list); err != nil {
		return nil, err
	}
	var out [][]byte
	for _, m := range list.Messages {
		var msg struct {
			Raw string `json:"raw"`
		}
		if err := gmailGet(ctx, tok, "/gmail/v1/users/me/messages/"+url.PathEscape(m.ID), url.Values{"format": {"raw"}}, &msg); err != nil {
			return out, err
		}
		raw, err := base64.URLEncoding.DecodeString(msg.Raw)
		if err != nil {
			raw, err = base64.RawURLEncoding.DecodeString(msg.Raw)
		}
		if err == nil {
			out = append(out, raw)
		}
	}
	return out, nil
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// gmailSignIn lets you sign in with Google in your browser (show gets the address to open)
// and returns the refresh token and the Gmail address. Google sends the browser back to a
// one-off local address (127.0.0.1) the app listens on until you're done.
func GmailSignIn(ctx context.Context, clientID, secret string, show func(authURL string)) (refresh, account string, err error) {
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == "" || secret == "" {
		return "", "", errors.New("enter your Google OAuth client ID and secret first (see the README: Gmail)")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	verifier, state := randomToken(), randomToken()
	sum := sha256.Sum256([]byte(verifier))
	type answer struct{ code, err string }
	got := make(chan answer, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		a := answer{code: q.Get("code"), err: q.Get("error")}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if a.code != "" {
			io.WriteString(w, "<html><body><h3>Signed in. You can close this tab and go back to spotify-downloader.</h3></body></html>")
		} else {
			io.WriteString(w, "<html><body><h3>Sign-in didn't complete ("+html.EscapeString(a.err)+"). Go back to spotify-downloader.</h3></body></html>")
		}
		select {
		case got <- a:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	authURL := googleAuthURL + "?" + url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {gmailScope},
		"access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}.Encode()
	show(authURL)
	var a answer
	select {
	case a = <-got:
	case <-ctx.Done():
		return "", "", ctx.Err()
	case <-time.After(10 * time.Minute):
		return "", "", errors.New("no answer from the browser within 10 minutes")
	}
	if a.code == "" {
		if a.err == "access_denied" {
			return "", "", errors.New("access wasn't allowed in the browser")
		}
		return "", "", fmt.Errorf("Google: %s", a.err)
	}
	t, err := googleTokenRequest(ctx, url.Values{"client_id": {clientID}, "client_secret": {secret}, "code": {a.code},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "grant_type": {"authorization_code"}})
	if err != nil {
		return "", "", err
	}
	if t.RefreshToken == "" {
		return "", "", errors.New("Google didn't give a lasting sign-in (no refresh token); remove the app's access at myaccount.google.com/permissions and sign in again")
	}
	var prof struct {
		Email string `json:"emailAddress"`
	}
	if err := gmailGet(ctx, t.AccessToken, "/gmail/v1/users/me/profile", nil, &prof); err != nil {
		return "", "", err
	}
	return t.RefreshToken, prof.Email, nil
}

// openBrowser opens an address in your default browser (best effort).
func OpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
