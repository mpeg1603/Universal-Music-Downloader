package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VK's music, searched with your own account's token. VK only lets a few apps use its audio
// API; this uses the Kate Mobile app's identity, as other VK music tools do. Tracks are
// usually MP3 at up to 320 kbps, served as an HLS stream. By default VK is reached directly,
// not through a VPN (see netroute.go).

const (
	vkUserAgent    = "KateMobileAndroid/56 lite-460 (Android 4.4.2; SDK 19; x86; unknown Android SDK built for x86; en)"
	vkClientID     = "2685278"
	vkClientSecret = "lxhD8OD7dMsqtXIm5IUY"
	vkAPIVersion   = "5.131"
)

var vkAPIBase = "https://api.vk.com/method/" // tests point it elsewhere

type VK struct {
	token string
	http  *HTTP
}

func newVK(token, route string) *VK {
	h := newRoutedHTTP("VK", 400*time.Millisecond, route)
	h.ua = vkUserAgent
	return &VK{token: strings.TrimSpace(token), http: h}
}

func (v *VK) Name() string        { return "vk" }
func (v *VK) Label() string       { return "VK" }
func (v *VK) MaxQuality() Quality { return Quality{Codec: "mp3", Kbps: 320} }

type vkError struct {
	Code int    `json:"error_code"`
	Msg  string `json:"error_msg"`
}

func (v *VK) call(ctx context.Context, method string, params url.Values, out any) error {
	params.Set("access_token", v.token)
	params.Set("v", vkAPIVersion)
	params.Set("https", "1")
	status, body, err := v.http.do(ctx, request{method: "POST", url: vkAPIBase + method,
		body: []byte(params.Encode()), headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}})
	if err != nil {
		return err
	}
	if status != 200 {
		return unavailablef("VK answered HTTP %d", status)
	}
	var resp struct {
		Response json.RawMessage `json:"response"`
		Error    *vkError        `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return failf("VK sent an unreadable answer")
	}
	if e := resp.Error; e != nil {
		switch e.Code {
		case 5:
			return blockedf("VK rejected your token (%s); get a new one in Sources → VK", e.Msg)
		case 6:
			return unavailablef("VK: too many requests per second")
		case 9, 29:
			return blockedf("VK is rate-limiting us (%s)", e.Msg)
		case 14:
			return blockedf("VK wants a captcha solved; try again later (or open VK in a browser)")
		case 25, 3:
			return blockedf("VK doesn't allow this token to use music (%s); get a Kate Mobile token in Sources → VK", e.Msg)
		}
		return failf("VK: %s (error %d)", e.Msg, e.Code)
	}
	return json.Unmarshal(resp.Response, out)
}

func (v *VK) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	var res struct {
		Items []struct {
			ID       int64   `json:"id"`
			OwnerID  int64   `json:"owner_id"`
			Artist   string  `json:"artist"`
			Title    string  `json:"title"`
			Subtitle string  `json:"subtitle"`
			Duration float64 `json:"duration"`
			URL      string  `json:"url"`
			Album    *struct {
				Title string `json:"title"`
			} `json:"album"`
			MainArtists []struct {
				Name string `json:"name"`
				ID   string `json:"id"`
			} `json:"main_artists"`
		} `json:"items"`
	}
	q := t.Artists[0] + " " + searchTitle(t.Title)
	if err := v.call(ctx, "audio.search", url.Values{"q": {q}, "count": {"30"}, "sort": {"2"}, "auto_complete": {"1"}}, &res); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, it := range res.Items {
		if it.URL == "" { // not available (removed, or region-locked)
			continue
		}
		title := it.Title
		if it.Subtitle != "" {
			title += " (" + it.Subtitle + ")"
		}
		c := Candidate{Source: "vk", URL: fmt.Sprintf("https://vk.com/audio%d_%d", it.OwnerID, it.ID), Title: title,
			Artist: it.Artist, Duration: it.Duration, Ref: it.URL, ArtistIsCredit: true, Quality: Quality{Codec: "mp3", Kbps: 320}}
		if it.Album != nil {
			c.Album = it.Album.Title
		}
		if len(it.MainArtists) > 0 && it.MainArtists[0].ID != "" {
			c.ArtistURL = "https://vk.com/artist/" + it.MainArtists[0].ID
		}
		out = append(out, c)
	}
	return out, nil
}

func (v *VK) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	if strings.Contains(c.Ref, ".m3u8") {
		dest := filepath.Join(workdir, "vk.ts")
		return dest, hlsDownload(ctx, v.http, c.Ref, dest)
	}
	return v.http.download(ctx, c.Ref, workdir, "vk")
}

// vkLogin trades your VK login for a token (the password isn't stored). VK may answer that it
// needs a confirmation code (2FA): run it again with code.
func VkLogin(ctx context.Context, user, pass, code, route string) (token string, needCode bool, err error) {
	h := newRoutedHTTP("VK", 0, route)
	h.ua = vkUserAgent
	h.retries = 1
	params := url.Values{"grant_type": {"password"}, "client_id": {vkClientID}, "client_secret": {vkClientSecret},
		"username": {user}, "password": {pass}, "scope": {"audio,offline"}, "2fa_supported": {"1"}, "force_sms": {"1"}, "v": {vkAPIVersion}}
	if code != "" {
		params.Set("code", code)
	}
	status, body, err := h.do(ctx, request{method: "POST", url: "https://oauth.vk.com/token", body: []byte(params.Encode()),
		headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}})
	if err != nil {
		return "", false, err
	}
	var resp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
		Validation  string `json:"validation_type"`
		SID         string `json:"validation_sid"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return "", false, fmt.Errorf("VK answered HTTP %d", status)
	}
	switch {
	case resp.AccessToken != "":
		return resp.AccessToken, false, nil
	case resp.Error == "need_validation":
		if resp.SID != "" && resp.Validation != "2fa_app" {
			sms := url.Values{"sid": {resp.SID}, "v": {vkAPIVersion}}
			h.do(ctx, request{method: "POST", url: "https://api.vk.com/method/auth.validatePhone", body: []byte(sms.Encode()),
				headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}})
		}
		return "", true, nil
	case resp.Error == "need_captcha":
		return "", false, fmt.Errorf("VK wants a captcha solved for this login; log in to VK in a browser first, or paste a token instead")
	}
	return "", false, fmt.Errorf("VK refused the login: %s", FirstNonEmpty(resp.Description, resp.Error, strconv.Itoa(status)))
}
