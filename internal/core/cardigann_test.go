package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		id, ok := defs.add(data)
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
