package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// A site like 1337x: a results table, and the torrent link on each result's details page.
const htmlDef = `
id: testsite
name: Test Site
type: public
encoding: UTF-8
links:
  - SITE/
caps:
  categorymappings:
    - {id: 22, cat: Audio/MP3, desc: "Music/MP3"}
    - {id: 23, cat: Audio/Lossless, desc: "Music/FLAC"}
    - {id: 42, cat: Movies, desc: "Movies"}
settings:
  - name: sort
    type: select
    default: seeders
  - name: disablesort
    type: checkbox
    default: false
download:
  selectors:
    - selector: a[href$=".torrent"]
      attribute: href
search:
  paths:
    - path: "{{ if and (.Keywords) (eq .Config.disablesort .False) }}sort-{{ end }}search/{{ .Keywords }}/{{ .Config.sort }}/1/"
  keywordsfilters:
    - name: re_replace
      args: ["\\s+", "+"]
  rows:
    selector: "tr:has(a[href^=\"/torrent/\"])"
  fields:
    title_default:
      selector: td.name a[href^="/torrent/"]
    title:
      text: "{{ .Result.title_default }}"
      filters:
        - name: replace
          args: ["-", " "]
    category_optional:
      optional: true
      selector: td.name a[href^="/sub/"]
      attribute: href
      filters:
        - name: split
          args: ["/", 2]
    category:
      text: "{{ if .Result.category_optional }}{{ .Result.category_optional }}{{ else }}42{{ end }}"
    details:
      selector: td.name a[href^="/torrent/"]
      attribute: href
    download:
      selector: td.name a[href^="/torrent/"]
      attribute: href
    size:
      selector: td.size
      remove: span
    seeders:
      selector: td.seeds
`

// A JSON API like The Pirate Bay's.
const jsonDef = `
id: testapi
name: Test API
type: public
links:
  - SITE/
caps:
  categorymappings:
    - {id: 100, cat: Audio}
    - {id: 101, cat: Audio/MP3}
search:
  paths:
    - path: "q.php?q={{ .Keywords }}&cat={{ join .Categories \",\" }}"
      response:
        type: json
  keywordsfilters:
    - name: tolower
  rows:
    selector: $
  fields:
    _id:
      selector: id
    title:
      selector: name
    category:
      selector: category
    details:
      text: "{{ .Config.sitelink }}description.php?id={{ .Result._id }}"
    infohash:
      selector: info_hash
    size:
      selector: size
    seeders:
      selector: seeders
`

func TestCardigann(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		switch {
		case strings.HasPrefix(r.URL.Path, "/sort-search/"):
			fmt.Fprint(w, `<table><tr><th>name</th></tr>
<tr><td class="name"><a href="/sub/23/0/">FLAC</a><a href="/torrent/1/Daft-Punk-Random-Access-Memories/">Daft-Punk-Random-Access-Memories-[FLAC]</a></td>
<td class="seeds">42</td><td class="size">512.5 MB<span>3</span></td></tr>
<tr><td class="name"><a href="/torrent/2/Some-Movie/">Some-Movie-2013</a></td><td class="seeds">7</td><td class="size">1.4 GB</td></tr>
</table>`)
		case r.URL.Path == "/torrent/1/Daft-Punk-Random-Access-Memories/":
			fmt.Fprint(w, `<ul><li><a href="/get/ram.torrent">Torrent</a></li></ul>`)
		case r.URL.Path == "/q.php":
			if r.URL.Query().Get("q") != "daft punk" || r.URL.Query().Get("cat") != "100,101" {
				t.Errorf("api query %v", r.URL.Query())
			}
			fmt.Fprint(w, `[{"id":"9","name":"Daft Punk - Discovery","category":"101","info_hash":"ABCDEF0123456789ABCDEF0123456789ABCDEF01","size":"123456789","seeders":"12"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c, err := newCardigann([]byte(strings.ReplaceAll(htmlDef, "SITE", srv.URL)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c.pace.interval = 0
	res, err := c.search(ctx, "daft punk")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || asked[0] != "/sort-search/daft+punk/seeders/1/" {
		t.Fatalf("%+v %v", res, asked)
	}
	r := res[0]
	if r.Title != "Daft Punk Random Access Memories [FLAC]" || r.Seeders != 42 || r.Size != 537395200 ||
		r.Link != srv.URL+"/torrent/1/Daft-Punk-Random-Access-Memories/" || len(r.Categories) != 1 || r.Categories[0] != 3040 || r.Indexer != "testsite" {
		t.Errorf("%+v", r)
	}
	if !notMusic(res[1].Categories) {
		t.Error("the movie should be left out")
	}
	page, _ := c.fetch(ctx, "GET", r.Link, nil)
	if link := c.downloadLink(ctx, page); link != srv.URL+"/get/ram.torrent" {
		t.Errorf("download link %q", link)
	}

	j, err := newCardigann([]byte(strings.ReplaceAll(jsonDef, "SITE", srv.URL)), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	j.pace.interval = 0
	res, err = j.search(ctx, "Daft Punk")
	if err != nil || len(res) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	r = res[0]
	if r.Title != "Daft Punk - Discovery" || r.InfoHash != "abcdef0123456789abcdef0123456789abcdef01" || !strings.HasPrefix(r.Magnet, "magnet:?xt=urn:btih:abcdef") ||
		r.Details != srv.URL+"/description.php?id=9" || r.Size != 123456789 || r.Seeders != 12 || r.Categories[0] != 3010 {
		t.Errorf("%+v", r)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1.5 GB": 1610612736, "700 MiB": 734003200, "1,234 KB": 1263616, "123": 123, "4,5 GB": 4831838208} {
		if got := parseSize(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}

// Every public Jackett definition reads, and its templates fill in (CARDIGANN_DEFS: a folder
// of Jackett's definition files).
func TestJackettDefinitionsLoad(t *testing.T) {
	dir := os.Getenv("CARDIGANN_DEFS")
	if dir == "" {
		t.Skip("CARDIGANN_DEFS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	defs := &indexerDefs{defs: map[string][]byte{}, info: map[string]IndexerInfo{}}
	music := 0
	for _, f := range files {
		data, _ := os.ReadFile(f)
		id, ok := defs.add(data, false)
		if !ok {
			continue
		}
		c, err := newCardigann(data, nil, "")
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		v := c.vars("daft punk")
		for _, p := range c.def.Search.Paths {
			if out := c.render(p.Path, v); strings.Contains(out, "{{") {
				t.Errorf("%s: path template left as is: %s", id, out)
			}
		}
		if defs.info[id].Music {
			music++
		}
	}
	t.Logf("%d public definitions without a login, %d with music", len(defs.defs), music)
	for _, id := range defaultIndexers {
		if defs.defs[id] == nil {
			t.Errorf("default indexer %s isn't among them", id)
		}
	}
}

// TestIndexerLive searches one real site once: CARDIGANN_DEFS=dir CARDIGANN_LIVE=id
// CARDIGANN_QUERY="artist album" (FLARESOLVERR=url optional). Skipped otherwise.
func TestIndexerLive(t *testing.T) {
	dir, id := os.Getenv("CARDIGANN_DEFS"), os.Getenv("CARDIGANN_LIVE")
	if dir == "" || id == "" {
		t.Skip("CARDIGANN_DEFS and CARDIGANN_LIVE not set")
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".yml"))
	if err != nil {
		t.Fatal(err)
	}
	defs := &indexerDefs{defs: map[string][]byte{}, info: map[string]IndexerInfo{}}
	if _, ok := defs.add(data, true); !ok {
		t.Fatalf("%s isn't kept (not public, or needs a login)", id)
	}
	t.Logf("%+v", defs.info[id])
	c, err := newCardigann(data, newFlareSolverr(os.Getenv("FLARESOLVERR")), "")
	if err != nil {
		t.Fatal(err)
	}
	q := FirstNonEmpty(os.Getenv("CARDIGANN_QUERY"), "Daft Punk Random Access Memories")
	res, err := c.search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d results for %q", len(res), q)
	for i, r := range res[:min(len(res), 8)] {
		t.Logf("%d. %s · %d seeders · %s · link %t magnet %t details %s", i+1, r.Title, r.Seeders, FmtBytes(r.Size), r.Link != "", r.Magnet != "", r.Details)
	}
	if len(res) == 0 {
		t.Error("nothing found")
	}
}

// RuTracker.org (the app's own definition): logging in (the form in windows-1251), searching
// its music forums, being logged out and logging in again, downloading a torrent with the
// login, and a captcha.
func TestRuTrackerLogin(t *testing.T) {
	def, err := ownIndexers.ReadFile("indexers/rutracker.yml")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	logins, sessions, captcha := 0, map[string]bool{}, false
	torrent := []byte("d8:announce3:url4:infod4:name4:testee")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		ck, _ := r.Cookie("bb_session")
		in := ck != nil && sessions[ck.Value]
		switch r.URL.Path {
		case "/forum/login.php":
			if r.Method != "POST" {
				w.Write([]byte(`<form action="login.php" method="post"></form>`))
				return
			}
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			user, _ := charmap.Windows1251.NewDecoder().String(form.Get("login_username"))
			if captcha {
				w.Write([]byte(`<input name="cap_sid" value="x">`))
				return
			}
			if user != "пользователь" || form.Get("login_password") != "secret" {
				msg, _ := charmap.Windows1251.NewEncoder().String(`<h4 class="warnColor1 tCenter mrg_16">Вы ввели неверное/неактивное имя пользователя или неверный пароль</h4>`)
				w.Write([]byte(msg))
				return
			}
			logins++
			id := fmt.Sprint("s", logins)
			sessions[id] = true
			http.SetCookie(w, &http.Cookie{Name: "bb_session", Value: id, Path: "/"})
			http.Redirect(w, r, "/forum/index.php", http.StatusFound)
		case "/forum/index.php":
			if in {
				w.Write([]byte(`<a id="logged-in-username" href="profile.php">пользователь</a>`))
			} else {
				w.Write([]byte(`<a href="login.php">Вход</a>`))
			}
		case "/forum/tracker.php":
			if !in {
				http.Redirect(w, r, "/forum/login.php?redirect=tracker.php", http.StatusFound)
				return
			}
			if f := strings.Split(r.URL.Query().Get("f"), ","); !ContainsString(f, "1224") || ContainsString(f, "185") {
				t.Errorf("forums searched: %s", f)
			}
			nm, _ := charmap.Windows1251.NewDecoder().String(r.URL.Query().Get("nm"))
			if nm != "Кино Группа крови" {
				t.Errorf("search %q", nm)
			}
			page, _ := charmap.Windows1251.NewEncoder().String(`<table id="tor-tbl"><tbody>
<tr id="trs-tr-1"><td></td><td></td><td class="f-name-col"><div class="f-name"><a href="tracker.php?f=1224">Русский рок (lossless)</a></div></td>
<td class="t-title-col"><div class="t-title"><a class="tLink" href="viewtopic.php?t=111">Кино - Группа крови - 1988, FLAC (tracks+.cue), lossless</a></div></td>
<td class="u-name-col">someone</td><td class="tor-size" data-ts_text="412345678"><a class="tr-dl" href="dl.php?t=111">393 MB</a></td>
<td><b class="seedmed">57</b></td><td class="leechmed">2</td><td>1000</td><td data-ts_text="1700000000">x</td></tr>
<tr><td colspan="10">Не найдено</td></tr>
</tbody></table>`)
			w.Write([]byte(page))
		case "/forum/dl.php":
			if !in {
				http.Redirect(w, r, "/forum/login.php", http.StatusFound)
				return
			}
			w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	data := strings.Replace(strings.Replace(string(def), "https://rutracker.org/", srv.URL+"/", 1), "  - https://rutracker.net/\n", "", 1)
	c, err := newCardigannWith([]byte(data), nil, "", map[string]string{"username": "пользователь", "password": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	c.pace = &rateLimiter{}
	ctx := context.Background()
	res, err := c.search(ctx, "Кино Группа крови")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Seeders != 57 || res[0].Size != 412345678 || res[0].Link != srv.URL+"/forum/dl.php?t=111" ||
		res[0].Details != srv.URL+"/forum/viewtopic.php?t=111" || len(res[0].Categories) != 1 || res[0].Categories[0] != 3040 {
		t.Fatalf("%+v", res)
	}
	got, err := c.fetchFile(ctx, res[0].Link)
	if err != nil || string(got) != string(torrent) {
		t.Fatalf("torrent: %q %v", got, err)
	}
	// The session ends: the next search logs in again by itself.
	mu.Lock()
	sessions = map[string]bool{}
	mu.Unlock()
	if res, err := c.search(ctx, "Кино Группа крови"); err != nil || len(res) != 1 || logins != 2 {
		t.Fatalf("after the session ended: %d results, %v, %d logins", len(res), err, logins)
	}

	// A wrong password: refused, and not tried again right away.
	bad, _ := newCardigannWith([]byte(data), nil, "", map[string]string{"username": "пользователь", "password": "wrong"})
	bad.pace = &rateLimiter{}
	if _, err := bad.search(ctx, "Кино Группа крови"); err == nil || !strings.Contains(err.Error(), "неверный пароль") {
		t.Errorf("wrong password: %v", err)
	}
	before := bad.loginTry
	bad.search(ctx, "Кино Группа крови")
	if !bad.loginTry.Equal(before) {
		t.Error("a failed login was tried again right away")
	}
	// No account set.
	none, _ := newCardigannWith([]byte(data), nil, "", nil)
	if _, err := none.search(ctx, "x"); err == nil || !strings.Contains(err.Error(), "needs your account") {
		t.Errorf("no account: %v", err)
	}
	// A captcha.
	mu.Lock()
	captcha = true
	mu.Unlock()
	cap, _ := newCardigannWith([]byte(data), nil, "", map[string]string{"username": "пользователь", "password": "secret"})
	cap.pace = &rateLimiter{}
	if _, err := cap.search(ctx, "Кино Группа крови"); err == nil || !strings.Contains(err.Error(), "captcha") {
		t.Errorf("captcha: %v", err)
	}
}

// TestRuTrackerLive logs in to the real RuTracker.org once, searches once and fetches the top
// result's torrent file: RUTRACKER_CREDS=file (username, password on two lines),
// FLARESOLVERR=url optional. Skipped otherwise.
func TestRuTrackerLive(t *testing.T) {
	path := os.Getenv("RUTRACKER_CREDS")
	if path == "" {
		t.Skip("RUTRACKER_CREDS not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatal("the file needs the username and the password on two lines")
	}
	def, _ := ownIndexers.ReadFile("indexers/rutracker.yml")
	c, err := newCardigannWith(def, newFlareSolverr(os.Getenv("FLARESOLVERR")), "",
		map[string]string{"username": strings.TrimSpace(lines[0]), "password": strings.TrimSpace(lines[1])})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	q := FirstNonEmpty(os.Getenv("CARDIGANN_QUERY"), "Кино Группа крови")
	res, err := c.search(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d results for %q", len(res), q)
	for i, r := range res[:min(len(res), 6)] {
		t.Logf("%d. %s · %d seeders · %s · %v", i+1, r.Title, r.Seeders, FmtBytes(r.Size), r.Categories)
	}
	if len(res) == 0 {
		t.Fatal("nothing found")
	}
	file, err := c.fetchFile(ctx, res[0].Link)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(file), "d") {
		t.Fatalf("not a torrent file: %q", file[:min(len(file), 80)])
	}
	t.Logf("torrent file of the top result: %d bytes", len(file))
}
