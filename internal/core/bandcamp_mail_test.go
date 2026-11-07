package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A fake Bandcamp: a name-your-price track that asks for an email address, the emailed
// download page, and the file behind it.
type fakeBandcamp struct {
	srv       *httptest.Server
	requested atomic.Int32
	address   atomic.Value
}

func newFakeBandcamp(t *testing.T) *fakeBandcamp {
	f := &fakeBandcamp{}
	attr := func(name, json string) string { return name + `="` + html.EscapeString(json) + `"` }
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/track/song":
			fmt.Fprintf(w, `<html><div %s></div></html>`, attr("data-tralbum",
				`{"current":{"id":5,"type":"track","minimum_price":0},"trackinfo":[{"title":"Song","track_num":1,"duration":4,"title_link":"/track/song"}]}`))
		case r.URL.Path == "/email_download" && r.Method == "POST":
			r.ParseForm()
			if r.Form.Get("item_id") != "5" || r.Form.Get("item_type") != "track" {
				t.Errorf("email_download form: %v", r.Form)
			}
			f.address.Store(r.Form.Get("address"))
			f.requested.Add(1)
			io.WriteString(w, `{"ok":true}`)
		case r.URL.Path == "/download" && r.URL.Query().Get("id") == "1":
			fmt.Fprintf(w, `<html><div %s></div></html>`, attr("data-blob",
				`{"digital_items":[{"item_id":5,"type":"track","downloads":{"flac":{"url":"`+f.srv.URL+`/download/track?enc=flac"}}}]}`))
		case r.URL.Path == "/download":
			http.NotFound(w, r)
		case r.URL.Path == "/statdownload/track":
			fmt.Fprintf(w, `{"result":"ok","download_url":"%s/file.flac"}`, f.srv.URL)
		case r.URL.Path == "/file.flac":
			w.Header().Set("Content-Type", "audio/flac")
			io.WriteString(w, "FLACDATA")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBandcamp) link() string { return f.srv.URL + "/download?id=1&sig=abc" }

// With no mailbox: Bandcamp is asked once (also across restarts), the track waits, and a
// pasted link lets it download.
func TestBandcampEmailedLinkPasted(t *testing.T) {
	fb := newFakeBandcamp(t)
	dir := t.TempDir()
	store := filepath.Join(dir, "bandcamp-emails.json")
	cfg := BandcampCfg{Email: "me@example.com", Mailbox: "paste"}
	newMail := func() (*bandcampMail, *atomic.Int32) {
		mb := newBandcampMail(store)
		mb.configure(cfg, "system", nil, "")
		var arrived atomic.Int32
		mb.onLink = func() { arrived.Add(1) }
		return mb, &arrived
	}
	mb, arrived := newMail()
	tr := MakeTrack("Song", []string{"Artist"}, "", 4)
	cand := Candidate{Source: "bandcamp", URL: fb.srv.URL + "/track/song", Title: "Song", Artist: "Artist", Duration: 4}
	download := func(mb *bandcampMail) (string, error) {
		b := newBandcamp(filepath.Join(dir, "cache"), "original", "system", mb)
		work := t.TempDir()
		return b.Download(context.Background(), cand, tr, work)
	}

	for i := 0; i < 2; i++ {
		_, err := download(mb)
		if _, ok := isPending(err); !ok {
			t.Fatalf("try %d: want a pending error, got %v", i, err)
		}
		if !strings.Contains(err.Error(), "me@example.com") || !strings.Contains(err.Error(), "paste") {
			t.Errorf("the reason should say where the link went and how to paste it: %v", err)
		}
	}
	if n := fb.requested.Load(); n != 1 {
		t.Fatalf("Bandcamp was asked %d times, want once", n)
	}
	if a := fb.address.Load(); a != "me@example.com" {
		t.Errorf("address sent: %v", a)
	}
	// After a restart it isn't asked again.
	mb, arrived = newMail()
	if _, err := download(mb); err == nil || fb.requested.Load() != 1 {
		t.Fatalf("after a restart: err %v, asked %d times", err, fb.requested.Load())
	}
	if n, _ := mb.Status(); n != 1 {
		t.Errorf("waiting releases: %d", n)
	}

	// Pasting: the whole email text works too; an expired link is reported.
	found, added, errs := mb.addTextWith(context.Background(), "Hi! Your download: "+fb.link()+"\nand "+fb.srv.URL+"/download?id=9")
	if found != 2 || added != 1 || len(errs) != 1 {
		t.Fatalf("pasted: %d found, %d added, errors %v", found, added, errs)
	}
	if arrived.Load() != 1 {
		t.Errorf("tracks waiting for the link weren't told")
	}
	if n, _ := mb.Status(); n != 0 {
		t.Errorf("still waiting for %d", n)
	}
	file, err := download(mb)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(file); string(b) != "FLACDATA" {
		t.Errorf("downloaded %q", b)
	}
	// Pasting the same link again changes nothing.
	if _, added, _ := mb.addTextWith(context.Background(), fb.link()); added != 0 {
		t.Error("the same link counted twice")
	}
}

// addTextWith is AddText for links on the test server (the real one only takes bandcamp.com).
func (m *bandcampMail) addTextWith(ctx context.Context, text string) (found, added int, errs []string) {
	re := regexp.MustCompile(`http://127\.0\.0\.1:\d+/download\?[^\s]+`)
	for _, link := range re.FindAllString(text, -1) {
		found++
		_, fresh, err := m.add(ctx, link)
		switch {
		case err != nil:
			errs = append(errs, err.Error())
		case fresh:
			added++
		}
	}
	return
}

type fakeMailbox struct {
	link  func() string
	calls atomic.Int32
}

func (f *fakeMailbox) fetch(ctx context.Context, since time.Time) ([][]byte, error) {
	f.calls.Add(1)
	raw := "From: Bandcamp <noreply@bandcamp.com>\r\nSubject: Your download\r\nContent-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n\r\n<a href=3D\"" + strings.ReplaceAll(f.link(), "=", "=3D") + "\">Download</a>\r\n"
	return [][]byte{[]byte(raw)}, nil
}

// With a mailbox: the link is picked up from Bandcamp's email.
func TestBandcampEmailedLinkFromMailbox(t *testing.T) {
	fb := newFakeBandcamp(t)
	old := reBandcampDownload
	reBandcampDownload = regexp.MustCompile(`http://127\.0\.0\.1:\d+/download\?[^\s"'<>]+`)
	defer func() { reBandcampDownload = old }()
	dir := t.TempDir()
	mb := newBandcampMail(filepath.Join(dir, "bandcamp-emails.json"))
	box := &fakeMailbox{link: fb.link}
	mb.configure(BandcampCfg{Email: "me@example.com", Mailbox: "imap"}, "system", box, "")
	b := newBandcamp(filepath.Join(dir, "cache"), "original", "system", mb)
	tr := MakeTrack("Song", []string{"Artist"}, "", 4)
	cand := Candidate{Source: "bandcamp", URL: fb.srv.URL + "/track/song", Title: "Song", Artist: "Artist", Duration: 4}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	file, err := b.Download(ctx, cand, tr, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(file); string(data) != "FLACDATA" || fb.requested.Load() != 1 || box.calls.Load() == 0 {
		t.Errorf("file %q, asked %d times, mailbox read %d times", data, fb.requested.Load(), box.calls.Load())
	}
}

func TestFindBandcampLinks(t *testing.T) {
	link := "https://bandcamp.com/download?from=email&id=123&payment_id=456&sig=abc&type=album"
	text := "Thanks!\n<a href=\"" + html.EscapeString(link) + "\">Download</a>\n" +
		"https://www.google.com/url?q=" + url.QueryEscape(link) + "&sa=D\n" + link + "."
	got := FindBandcampLinks(text)
	if len(got) != 1 || got[0] != link {
		t.Errorf("links: %q", got)
	}
}

// A track waiting for Bandcamp's emailed link waits (no worse source is used meanwhile) and
// goes on as soon as a link arrives.
func TestPendingSourceWaitsForYou(t *testing.T) {
	tools := NeedFFmpeg(t)
	a := GetLucky()
	a.Key, a.DurationMs = "a", 4000
	bc := &FakeSource{SrcName: "bandcamp", T: t, Results: map[string][]Candidate{"a": Found("bandcamp", "Get Lucky", "Daft Punk", 4)},
		downloadErr: pendingf(6*time.Hour, "Bandcamp emails the download link to me@example.com: paste it")}
	yt := &FakeSource{SrcName: "youtube", T: t, Results: map[string][]Candidate{"a": Found("youtube", "Get Lucky", "Daft Punk", 4)}}
	m := TestManager(t, tools, []Source{bc, yt}, TestLibrary(a), nil)
	views := RunManager(t, m)
	v := views["a"]
	if v.State != JobWaiting || !strings.Contains(v.Reason, "paste it") {
		t.Fatalf("state %s (%s), want waiting for the link", v.State, v.Reason)
	}
	if yt.searched.Load() != 0 {
		t.Error("a worse source was tried while waiting for Bandcamp's link")
	}
	if n := m.healthOf("bandcamp"); n.fails != 0 || !n.until.IsZero() {
		t.Error("waiting for you shouldn't count against the source")
	}
	bc.downloadErr = nil
	m.bandcampLinkArrived()
	WaitFor(t, func() bool { v, _ := m.JobView("a"); return v.State == JobDone })
	if v, _ := m.JobView("a"); v.Rec.Source != "bandcamp" {
		t.Errorf("downloaded from %s", v.Rec.Source)
	}
}

// Gmail: sign in in the browser (here: a fake Google) and read Bandcamp's emails.
func TestGmailSignInAndFetch(t *testing.T) {
	var challenge, refreshed atomic.Value
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch {
		case r.URL.Path == "/token" && r.Form.Get("grant_type") == "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge.Load() ||
				r.Form.Get("client_secret") != "sec" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			io.WriteString(w, `{"access_token":"acc1","refresh_token":"ref1","expires_in":3600}`)
		case r.URL.Path == "/token" && r.Form.Get("grant_type") == "refresh_token":
			if r.Form.Get("refresh_token") != "ref1" {
				http.Error(w, `{"error":"invalid_grant"}`, 400)
				return
			}
			refreshed.Store(true)
			io.WriteString(w, `{"access_token":"acc2","expires_in":3600}`)
		case strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && r.URL.Path == "/gmail/v1/users/me/profile":
			io.WriteString(w, `{"emailAddress":"me@gmail.com"}`)
		case r.Header.Get("Authorization") == "Bearer acc2" && r.URL.Path == "/gmail/v1/users/me/messages":
			if !strings.Contains(r.URL.Query().Get("q"), "from:bandcamp.com") {
				t.Errorf("query: %s", r.URL.RawQuery)
			}
			io.WriteString(w, `{"messages":[{"id":"m1"}]}`)
		case r.Header.Get("Authorization") == "Bearer acc2" && r.URL.Path == "/gmail/v1/users/me/messages/m1":
			raw := base64.URLEncoding.EncodeToString([]byte("From: noreply@bandcamp.com\r\n\r\nhttps://bandcamp.com/download?id=1&sig=x\r\n"))
			fmt.Fprintf(w, `{"raw":%q}`, raw)
		default:
			http.Error(w, `{"error":{"message":"unexpected"}}`, 403)
		}
	}))
	defer google.Close()
	oldAuth, oldToken, oldAPI := googleAuthURL, googleTokenURL, gmailAPI
	googleAuthURL, googleTokenURL, gmailAPI = google.URL+"/auth", google.URL+"/token", google.URL
	defer func() { googleAuthURL, googleTokenURL, gmailAPI = oldAuth, oldToken, oldAPI }()

	// The "browser": checks what's asked for and comes back with a code.
	browser := func(authURL string) {
		u, _ := url.Parse(authURL)
		q := u.Query()
		if q.Get("scope") != gmailScope || q.Get("client_id") != "cid" || q.Get("code_challenge_method") != "S256" {
			t.Errorf("sign-in asks for: %s", authURL)
		}
		challenge.Store(q.Get("code_challenge"))
		go func() {
			resp, err := http.Get(q.Get("redirect_uri") + "?code=the-code&state=" + url.QueryEscape(q.Get("state")))
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	refresh, account, err := GmailSignIn(ctx, "cid", "sec", browser)
	if err != nil || refresh != "ref1" || account != "me@gmail.com" {
		t.Fatalf("sign-in: %q %q %v", refresh, account, err)
	}
	box := newGmailBox(BandcampCfg{GmailClientID: "cid", GmailClientSecret: "sec", GmailToken: refresh})
	msgs, err := box.fetch(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(msgs) != 1 || refreshed.Load() != true {
		t.Fatalf("fetch: %d messages, %v", len(msgs), err)
	}
	if links := bandcampLinks(msgs[0]); len(links) != 1 {
		t.Errorf("links in the email: %v", links)
	}
	// An expired sign-in says what to do.
	_, err = newGmailBox(BandcampCfg{GmailClientID: "cid", GmailClientSecret: "sec", GmailToken: "old"}).fetch(ctx, time.Now())
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Errorf("expired sign-in: %v", err)
	}
}
