package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBandwidthCap(t *testing.T) {
	b := &Bandwidth{}
	b.SetCap(2e6) // 2 MB/s
	src := bytes.NewReader(make([]byte, 3e6))
	start := time.Now()
	n, err := io.Copy(io.Discard, b.Reader(context.Background(), src))
	took := time.Since(start)
	if err != nil || n != 3e6 {
		t.Fatal(n, err)
	}
	// 3 MB at 2 MB/s, less the initial burst.
	if took < 1300*time.Millisecond || took > 2200*time.Millisecond {
		t.Errorf("3 MB took %s at 2 MB/s", took)
	}
	b.SetCap(0)
	start = time.Now()
	io.Copy(io.Discard, b.Reader(context.Background(), bytes.NewReader(make([]byte, 20e6))))
	if time.Since(start) > 500*time.Millisecond {
		t.Error("unlimited is limited")
	}
	// Torrents take their share: the rest of the cap is left for the others.
	b.SetCap(10e6)
	for i := 0; i < 10; i++ {
		b.sample(6e6)
	}
	if b.rate < 3.9e6 || b.rate > 4.1e6 {
		t.Errorf("own rate %.0f with torrents at 6 MB/s of 10", b.rate)
	}
}

func TestLocalProxyTunnelsAndCounts(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 100000))
	}))
	defer srv.Close()
	p, err := startLocalProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	before := bw.bytes.Load()
	pu, _ := url.Parse(p.URL(routeSystem))
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 100000 {
		t.Fatalf("got %d bytes", len(data))
	}
	if got := bw.bytes.Load() - before; got < 100000 {
		t.Errorf("counted %d bytes", got)
	}
	// Without the secret it refuses.
	bad, _ := url.Parse(strings.Replace(p.URL(routeSystem), p.secret, "wrong", 1))
	tr2 := srv.Client().Transport.(*http.Transport).Clone()
	tr2.Proxy = http.ProxyURL(bad)
	if _, err := (&http.Client{Transport: tr2}).Get(srv.URL); err == nil {
		t.Error("proxy accepted a wrong password")
	}
}

func TestHLSDownloadDecrypts(t *testing.T) {
	key := make([]byte, 16)
	rand.Read(key)
	plain := [][]byte{bytes.Repeat([]byte("A"), 5000), bytes.Repeat([]byte("B"), 4000), bytes.Repeat([]byte("C"), 3000)}
	encrypt := func(data []byte, seq int) []byte {
		iv := make([]byte, 16)
		binary.BigEndian.PutUint64(iv[8:], uint64(seq))
		pad := aes.BlockSize - len(data)%aes.BlockSize
		data = append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
		block, _ := aes.NewCipher(key)
		out := make([]byte, len(data))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, data)
		return out
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=128000\nlow/index.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=320000\nhigh/index.m3u8\n")
		case "/high/index.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-KEY:METHOD=AES-128,URI=\"/key.pub\"\n#EXTINF:10,\nseg0.ts\n"+
				"#EXT-X-KEY:METHOD=NONE\n#EXTINF:10,\nseg1.ts\n#EXT-X-KEY:METHOD=AES-128,URI=\"/key.pub\"\n#EXTINF:10,\nseg2.ts\n#EXT-X-ENDLIST\n")
		case "/key.pub":
			w.Write(key)
		case "/high/seg0.ts":
			w.Write(encrypt(plain[0], 0))
		case "/high/seg1.ts":
			w.Write(plain[1])
		case "/high/seg2.ts":
			w.Write(encrypt(plain[2], 2))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "out.ts")
	if err := hlsDownload(context.Background(), newHTTP("test", 0), srv.URL+"/master.m3u8", dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, bytes.Join(plain, nil)) {
		t.Errorf("got %d bytes, want %d", len(got), 12000)
	}
}

func TestSOCKS5Route(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello") }))
	defer target.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() { // a minimal SOCKS5 server with user/password login
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 512)
				io.ReadFull(c, buf[:2])
				io.ReadFull(c, buf[:buf[1]])
				c.Write([]byte{5, 2})
				io.ReadFull(c, buf[:2])
				ulen := int(buf[1])
				io.ReadFull(c, buf[:ulen])
				user := string(buf[:ulen])
				io.ReadFull(c, buf[:1])
				plen := int(buf[0])
				io.ReadFull(c, buf[:plen])
				pass := string(buf[:plen])
				if user != "u" || pass != "p" {
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{1, 0})
				io.ReadFull(c, buf[:4])
				var host string
				switch buf[3] {
				case 1:
					io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					io.ReadFull(c, buf[:1])
					n := int(buf[0])
					io.ReadFull(c, buf[:n])
					host = string(buf[:n])
				}
				io.ReadFull(c, buf[:2])
				port := binary.BigEndian.Uint16(buf[:2])
				up, err := net.Dial("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}()
		}
	}()
	route := "socks5://u:p@" + ln.Addr().String()
	if err := validRoute(route); err != nil {
		t.Fatal(err)
	}
	h := newRoutedHTTP("test", 0, route)
	status, body, err := h.get(context.Background(), target.URL)
	if err != nil || status != 200 || string(body) != "hello" {
		t.Errorf("%d %q %v", status, body, err)
	}
	if _, err := router.Dial(context.Background(), "socks5://u:wrong@"+ln.Addr().String(), "tcp", "127.0.0.1:1"); err == nil {
		t.Error("wrong password accepted")
	}
}

func TestVKSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Header.Get("User-Agent") != vkUserAgent || r.Form.Get("access_token") != "tok" {
			fmt.Fprint(w, `{"error":{"error_code":5,"error_msg":"User authorization failed"}}`)
			return
		}
		fmt.Fprintf(w, `{"response":{"count":3,"items":[
			{"id":1,"owner_id":-2,"artist":"Interpol","title":"NYC","duration":260,"url":"%s/a.mp3","album":{"title":"Turn On The Bright Lights"}},
			{"id":2,"owner_id":-2,"artist":"Interpol","title":"NYC","subtitle":"Live","duration":270,"url":"%s/b.m3u8"},
			{"id":3,"owner_id":-2,"artist":"Interpol","title":"NYC","duration":260,"url":""}]}}`, "http://x", "http://x")
	}))
	defer srv.Close()
	old := vkAPIBase
	vkAPIBase = srv.URL + "/"
	defer func() { vkAPIBase = old }()
	tr := &Track{Title: "NYC", Artists: []string{"Interpol"}, DurationMs: 260000}
	cands, err := newVK("tok", routeSystem).Search(context.Background(), tr)
	if err != nil || len(cands) != 2 || cands[1].Title != "NYC (Live)" || cands[0].Album != "Turn On The Bright Lights" || cands[0].URL != "https://vk.com/audio-2_1" {
		t.Fatalf("%+v %v", cands, err)
	}
	if acc, _ := rank(tr, cands); len(acc) != 1 || acc[0].Cand.Ref != "http://x/a.mp3" {
		t.Errorf("accepted %+v", acc)
	}
	if _, err := newVK("bad", routeSystem).Search(context.Background(), tr); !isBlocked(err) {
		t.Errorf("bad token: %v", err)
	}
}

func TestYandexSource(t *testing.T) {
	audio := []byte("fLaC-pretend-audio")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"result":{"tracks":{"results":[{"id":123,"title":"NYC","version":"Remastered","durationMs":260000,
				"artists":[{"id":7,"name":"Interpol"}],"albums":[{"id":9,"title":"Turn On The Bright Lights"}]}]}}}`)
		case "/get-file-info":
			q := r.URL.Query()
			if r.Header.Get("Authorization") != "OAuth tok" || r.Header.Get("X-Yandex-Music-Client") != yandexDesktopApp ||
				q.Get("trackId") != "123" || q.Get("sign") == "" || strings.HasSuffix(q.Get("sign"), "=") {
				w.WriteHeader(401)
				return
			}
			fmt.Fprintf(w, `{"result":{"downloadInfo":{"quality":"lossless","codec":"flac","bitrate":0,"transport":"raw","url":"%s/file"}}}`, srv.URL)
		case "/file":
			w.Write(audio)
		}
	}))
	defer srv.Close()
	old := yandexAPI
	yandexAPI = srv.URL
	defer func() { yandexAPI = old }()
	y := newYandex("tok", routeSystem)
	tr := &Track{Title: "NYC", Artists: []string{"Interpol"}, DurationMs: 260000}
	cands, err := y.Search(context.Background(), tr)
	if err != nil || len(cands) != 1 || cands[0].Title != "NYC (Remastered)" || cands[0].Ref != "123" {
		t.Fatalf("%+v %v", cands, err)
	}
	p, err := y.Download(context.Background(), cands[0], tr, t.TempDir())
	if err != nil || filepath.Ext(p) != ".flac" {
		t.Fatal(p, err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, audio) {
		t.Errorf("file %q", got)
	}
	if _, err := newYandex("bad", routeSystem).Download(context.Background(), cands[0], tr, t.TempDir()); !isBlocked(err) {
		t.Errorf("bad token: %v", err)
	}
}

// A slow indexer must not hold up searches; one that keeps failing is left out.
func TestTorrentSearchDoesntWaitForSlowIndexers(t *testing.T) {
	slow := make(chan struct{})
	defer close(slow)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/fast/"):
			fmt.Fprint(w, `<rss><channel><item><title>Interpol - Antics</title><link>http://x/1.torrent</link><attr name="seeders" value="3"/></item></channel></rss>`)
		case strings.Contains(r.URL.Path, "/broken/"):
			w.WriteHeader(400)
		default:
			select {
			case <-slow:
			case <-r.Context().Done():
			}
		}
	}))
	defer srv.Close()
	feeds := []*Torznab{
		newTorznabFeed("fast", srv.URL+"/fast/api", "k", 3*time.Second),
		newTorznabFeed("slow", srv.URL+"/slow/api", "k", 3*time.Second),
		newTorznabFeed("broken", srv.URL+"/broken/api", "k", 3*time.Second),
	}
	src := newTorrentSource(feeds, "", t.TempDir(), "flac", time.Minute, 3*time.Second)
	src.grace = 300 * time.Millisecond
	for i := 0; i < 3; i++ {
		start := time.Now()
		res, err := src.searchAll(context.Background(), fmt.Sprintf("interpol antics %d", i))
		if err != nil || len(res) != 1 {
			t.Fatalf("search %d: %v %v", i, res, err)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("search %d took %s", i, took)
		}
	}
	st := map[string]feedStatus{}
	for _, f := range src.feedStatus() {
		st[f.Name] = f
	}
	if st["broken"].OK || st["broken"].Errors != 3 || !st["fast"].OK || st["fast"].Searches != 3 {
		t.Errorf("health: %+v", st)
	}
	// Cached on disk: a new source (next run) answers without asking the indexers.
	src2 := newTorrentSource([]*Torznab{newTorznabFeed("broken", srv.URL+"/broken/api", "k", time.Second)}, "", src.cacheDir[:len(src.cacheDir)-len("/torrent-searches")], "flac", time.Minute)
	if res, err := src2.searchAll(context.Background(), "interpol antics 0"); err != nil || len(res) != 1 {
		t.Errorf("cached: %v %v", res, err)
	}
}

func TestImportSemicolonArtists(t *testing.T) {
	lib := newLibrary()
	csv := "Track URI,Track Name,Artist Name(s),Album Name\n" +
		"spotify:track:4TRJBNyjz1F6sFG5OeCFP2,Pretend,\"Woe, Is Me\",Number[s]\n" +
		"spotify:track:7CR0CfMEC6BZBuYLaKSbu9,Synth,Артек Электроника;Misha Fomin,Album\n"
	if err := importCSV(lib, "Mix", strings.NewReader(csv)); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, tr := range lib.Tracks {
		got[tr.Title] = tr.Artists
	}
	if fmt.Sprint(got["Pretend"]) != "[Woe, Is Me]" || fmt.Sprint(got["Synth"]) != "[Артек Электроника Misha Fomin]" {
		t.Errorf("artists: %q", got)
	}
}
