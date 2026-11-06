package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Yandex Music, with your own account's OAuth token. Full tracks need a Plus subscription:
// FLAC where Yandex has it (with Plus), otherwise MP3 at up to 320 kbps. Without Plus Yandex
// only serves previews, which are rejected. By default Yandex is reached directly, not through
// a VPN (see netroute.go).

const (
	yandexAPIDefault   = "https://api.music.yandex.net"
	yandexAppClient    = "YandexMusicAndroid/24023621"
	yandexDesktopApp   = "YandexMusicDesktopAppWindows/5.95.0" // the client the signing key belongs to
	yandexFileInfoKey  = "kzqU4XhfCaY6B6JTHODeq5"
	yandexDownloadSalt = "XGRlBW9FXlekgbPrRHuSiA"
	// The address that gives you a token: log in, then copy access_token from the address bar.
	yandexTokenURL = "https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d"
)

var yandexAPI = yandexAPIDefault // tests point it elsewhere

var yandexCodecs = []string{"flac", "flac-mp4", "aac-mp4", "he-aac-mp4", "aac", "he-aac", "mp3"}

type Yandex struct {
	token string
	http  *HTTP
}

func newYandex(token, route string) *Yandex {
	return &Yandex{token: strings.TrimSpace(token), http: newRoutedHTTP("Yandex Music", 250*time.Millisecond, route)}
}

func (y *Yandex) Name() string        { return "yandex" }
func (y *Yandex) Label() string       { return "Yandex Music" }
func (y *Yandex) MaxQuality() Quality { return Quality{Codec: "flac", Lossless: true} }

func (y *Yandex) api(ctx context.Context, path string, params url.Values, client string, out any) error {
	u := yandexAPI + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	hdr := map[string]string{"X-Yandex-Music-Client": client, "Accept-Language": "en"}
	if y.token != "" {
		hdr["Authorization"] = "OAuth " + y.token
	}
	status, body, err := y.http.do(ctx, request{url: u, headers: hdr})
	if err != nil {
		return err
	}
	switch {
	case status == 401 || status == 403:
		return blockedf("Yandex Music rejected your token (HTTP %d); get a new one in Sources → Yandex Music", status)
	case status == 451:
		return blockedf("Yandex Music isn't available from this connection (HTTP 451); keep its route on \"direct\"")
	case status != 200:
		return failf("Yandex Music answered HTTP %d", status)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return failf("Yandex Music sent an unreadable answer")
	}
	if resp.Error != nil {
		return failf("Yandex Music: %s", firstNonEmpty(resp.Error.Message, resp.Error.Name))
	}
	return json.Unmarshal(resp.Result, out)
}

// yandexID accepts the numbers and strings Yandex uses for ids.
type yandexID string

func (i *yandexID) UnmarshalJSON(b []byte) error {
	*i = yandexID(strings.Trim(string(b), `"`))
	return nil
}

func (y *Yandex) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	var res struct {
		Tracks struct {
			Results []struct {
				ID         yandexID `json:"id"`
				Title      string   `json:"title"`
				Version    string   `json:"version"`
				DurationMs float64  `json:"durationMs"`
				Available  *bool    `json:"available"`
				Artists    []struct {
					ID   yandexID `json:"id"`
					Name string   `json:"name"`
				} `json:"artists"`
				Albums []struct {
					ID    yandexID `json:"id"`
					Title string   `json:"title"`
				} `json:"albums"`
			} `json:"results"`
		} `json:"tracks"`
	}
	q := t.Artists[0] + " " + searchTitle(t.Title)
	if err := y.api(ctx, "/search", url.Values{"text": {q}, "type": {"track"}, "page": {"0"}, "nocorrect": {"false"}}, yandexAppClient, &res); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range res.Tracks.Results {
		if r.Available != nil && !*r.Available {
			continue
		}
		title := r.Title
		if r.Version != "" {
			title += " (" + r.Version + ")"
		}
		var names []string
		for _, a := range r.Artists {
			names = append(names, a.Name)
		}
		c := Candidate{Source: "yandex", Title: title, Artist: strings.Join(names, ", "), Duration: r.DurationMs / 1000,
			Ref: string(r.ID), ArtistIsCredit: true, URL: "https://music.yandex.ru/track/" + string(r.ID)}
		if len(r.Albums) > 0 {
			c.Album = r.Albums[0].Title
			c.URL = fmt.Sprintf("https://music.yandex.ru/album/%s/track/%s", r.Albums[0].ID, r.ID)
		}
		if len(r.Artists) > 0 {
			c.ArtistURL = "https://music.yandex.ru/artist/" + string(r.Artists[0].ID)
		}
		out = append(out, c)
	}
	return out, nil
}

type yandexFile struct {
	Quality   string   `json:"quality"`
	Codec     string   `json:"codec"`
	Bitrate   int      `json:"bitrate"`
	Transport string   `json:"transport"`
	Key       string   `json:"key"`
	URL       string   `json:"url"`
	URLs      []string `json:"urls"`
}

func (y *Yandex) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	f, err := y.fileInfo(ctx, c.Ref)
	if err == nil {
		return y.fetchFile(ctx, f, workdir)
	}
	if isBlocked(err) {
		return "", err
	}
	debugf("yandex: get-file-info for %s failed (%v); trying download-info", c.Ref, err)
	return y.legacyDownload(ctx, c.Ref, workdir)
}

// fileInfo asks for the best file Yandex has (FLAC with Plus).
func (y *Yandex) fileInfo(ctx context.Context, id string) (*yandexFile, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	quality, transport := "lossless", "raw"
	mac := hmac.New(sha256.New, []byte(yandexFileInfoKey))
	mac.Write([]byte(ts + id + quality + strings.Join(yandexCodecs, "") + transport))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	sign = sign[:len(sign)-1]
	var res struct {
		DownloadInfo yandexFile `json:"downloadInfo"`
	}
	err := y.api(ctx, "/get-file-info", url.Values{"ts": {ts}, "trackId": {id}, "quality": {quality},
		"codecs": {strings.Join(yandexCodecs, ",")}, "transports": {transport}, "sign": {sign}}, yandexDesktopApp, &res)
	if err != nil {
		return nil, err
	}
	f := res.DownloadInfo
	if f.URL == "" && len(f.URLs) > 0 {
		f.URL = f.URLs[0]
	}
	if f.URL == "" {
		return nil, failf("no file")
	}
	if strings.Contains(f.Quality, "preview") {
		return nil, failf("Yandex Music only offers a preview of this track (does your account have Plus?)")
	}
	return &f, nil
}

func (y *Yandex) fetchFile(ctx context.Context, f *yandexFile, workdir string) (string, error) {
	p, err := y.http.download(ctx, f.URL, workdir, "yandex-raw")
	if err != nil {
		return "", err
	}
	ext := ".mp3"
	switch {
	case f.Codec == "flac":
		ext = ".flac"
	case strings.HasSuffix(f.Codec, "-mp4"):
		ext = ".m4a"
	case strings.Contains(f.Codec, "aac"):
		ext = ".aac"
	}
	dest := filepath.Join(workdir, "yandex"+ext)
	if f.Transport == "encraw" && f.Key != "" {
		return dest, decryptCTRFile(p, dest, f.Key)
	}
	return dest, os.Rename(p, dest)
}

func decryptCTRFile(src, dst, hexKey string) error {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return failf("bad decryption key from Yandex Music")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return failf("bad decryption key from Yandex Music")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, cipher.StreamReader{S: cipher.NewCTR(block, make([]byte, aes.BlockSize)), R: in})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	os.Remove(src)
	return err
}

// legacyDownload uses the older download-info API (MP3 up to 320 kbps).
func (y *Yandex) legacyDownload(ctx context.Context, id, workdir string) (string, error) {
	var infos []struct {
		Codec           string `json:"codec"`
		Bitrate         int    `json:"bitrateInKbps"`
		Preview         bool   `json:"preview"`
		DownloadInfoURL string `json:"downloadInfoUrl"`
	}
	if err := y.api(ctx, "/tracks/"+id+"/download-info", nil, yandexAppClient, &infos); err != nil {
		return "", err
	}
	sort.SliceStable(infos, func(i, j int) bool {
		if (infos[i].Codec == "mp3") != (infos[j].Codec == "mp3") {
			return infos[i].Codec == "mp3"
		}
		return infos[i].Bitrate > infos[j].Bitrate
	})
	for _, in := range infos {
		if in.Preview || in.DownloadInfoURL == "" {
			continue
		}
		status, body, err := y.http.do(ctx, request{url: in.DownloadInfoURL, headers: map[string]string{"Authorization": "OAuth " + y.token}})
		if err != nil {
			return "", err
		}
		if status != 200 {
			continue
		}
		var d struct {
			Host string `xml:"host"`
			Path string `xml:"path"`
			TS   string `xml:"ts"`
			S    string `xml:"s"`
		}
		if xml.Unmarshal(body, &d) != nil || d.Path == "" {
			continue
		}
		sum := md5.Sum([]byte(yandexDownloadSalt + strings.TrimPrefix(d.Path, "/") + d.S))
		link := fmt.Sprintf("https://%s/get-mp3/%s/%s%s", d.Host, hex.EncodeToString(sum[:]), d.TS, d.Path)
		p, err := y.http.download(ctx, link, workdir, "yandex")
		if err == nil && !strings.HasSuffix(p, ".mp3") {
			np := strings.TrimSuffix(p, filepath.Ext(p)) + ".mp3"
			if os.Rename(p, np) == nil {
				p = np
			}
		}
		return p, err
	}
	return "", failf("Yandex Music only offers a preview of this track (does your account have Plus?)")
}
