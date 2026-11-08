package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/encoding/charmap"
	"gopkg.in/yaml.v3"
)

// Built-in torrent indexers: Jackett's site definitions ("Cardigann" YAML files, see
// indexers.go), read and run here the way Jackett runs them: the search page is built from
// the definition's templates, and the results are picked out of the HTML, JSON or XML answer
// with its selectors and filters.

type cgDef struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Language     string   `yaml:"language"`
	Type         string   `yaml:"type"`
	Encoding     string   `yaml:"encoding"`
	Links        []string `yaml:"links"`
	RequestDelay float64  `yaml:"requestDelay"`
	Caps         struct {
		CategoryMappings []struct {
			ID   string `yaml:"id"`
			Cat  string `yaml:"cat"`
			Desc string `yaml:"desc"`
		} `yaml:"categorymappings"`
	} `yaml:"caps"`
	Settings []struct {
		Name    string `yaml:"name"`
		Type    string `yaml:"type"`
		Default any    `yaml:"default"`
	} `yaml:"settings"`
	Login    *yaml.Node `yaml:"login"`
	Search   cgSearch   `yaml:"search"`
	Download struct {
		Selectors []cgSelector `yaml:"selectors"`
	} `yaml:"download"`
}

type cgSearch struct {
	Paths []struct {
		Path       string            `yaml:"path"`
		Method     string            `yaml:"method"`
		Inputs     map[string]string `yaml:"inputs"`
		Categories []string          `yaml:"categories"`
		Response   struct {
			Type string `yaml:"type"`
		} `yaml:"response"`
	} `yaml:"paths"`
	Inputs          map[string]string `yaml:"inputs"`
	Headers         map[string]any    `yaml:"headers"`
	KeywordsFilters []cgFilter        `yaml:"keywordsfilters"`
	Rows            struct {
		Selector string     `yaml:"selector"`
		After    int        `yaml:"after"`
		Filters  []cgFilter `yaml:"filters"`
	} `yaml:"rows"`
	Fields yaml.Node `yaml:"fields"` // in order: later fields use earlier ones
}

type cgFilter struct {
	Name string `yaml:"name"`
	Args any    `yaml:"args"`
}

type cgSelector struct {
	Selector  string            `yaml:"selector"`
	Attribute string            `yaml:"attribute"`
	Remove    string            `yaml:"remove"`
	Filters   []cgFilter        `yaml:"filters"`
	Text      *string           `yaml:"text"`
	Optional  bool              `yaml:"optional"`
	Default   *string           `yaml:"default"`
	Case      map[string]string `yaml:"case"`
}

type cgField struct {
	name string
	sel  cgSelector
}

// cardigann is an indexer run from a definition.
type cardigann struct {
	def    *cgDef
	fields []cgField
	config map[string]any // the settings' defaults (.Config)
	site   string         // the site's address, ending in /
	cats   map[string]int // site category → Torznab category
	audio  []string       // the site's music categories
	http   *http.Client
	flare  *flareSolverr
	pace   *rateLimiter
}

// Torznab category numbers for Cardigann category names.
var torznabCats = map[string]int{"Console": 1000, "Movies": 2000, "Audio": 3000, "Audio/MP3": 3010, "Audio/Video": 3020,
	"Audio/Audiobook": 3030, "Audio/Lossless": 3040, "Audio/Other": 3050, "Audio/Foreign": 3060, "PC": 4000, "TV": 5000,
	"XXX": 6000, "Books": 7000, "Other": 8000}

func torznabCat(name string) int {
	if n, ok := torznabCats[name]; ok {
		return n
	}
	if i := strings.IndexByte(name, '/'); i > 0 {
		return torznabCats[name[:i]] + 1
	}
	return 8000
}

func parseCardigann(data []byte) (*cgDef, []cgField, error) {
	var d cgDef
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, nil, err
	}
	if d.ID == "" || len(d.Links) == 0 || len(d.Search.Paths) == 0 {
		return nil, nil, fmt.Errorf("incomplete definition")
	}
	var fields []cgField
	f := d.Search.Fields
	for i := 0; i+1 < len(f.Content); i += 2 {
		var sel cgSelector
		v := f.Content[i+1]
		if v.Kind == yaml.ScalarNode { // "field: selector"
			sel.Selector = v.Value
		} else if err := v.Decode(&sel); err != nil {
			return nil, nil, fmt.Errorf("field %s: %w", f.Content[i].Value, err)
		}
		fields = append(fields, cgField{f.Content[i].Value, sel})
	}
	return &d, fields, nil
}

// musicCats are a definition's music categories (its site IDs).
func (d *cgDef) musicCats() []string {
	var out []string
	for _, m := range d.Caps.CategoryMappings {
		if strings.HasPrefix(m.Cat, "Audio") && m.Cat != "Audio/Audiobook" && m.Cat != "Audio/Video" {
			out = append(out, m.ID)
		}
	}
	return out
}

func newCardigann(data []byte, flare *flareSolverr, route string) (*cardigann, error) {
	d, fields, err := parseCardigann(data)
	if err != nil {
		return nil, err
	}
	c := &cardigann{def: d, fields: fields, config: map[string]any{}, cats: map[string]int{}, audio: d.musicCats(), flare: flare,
		http: &http.Client{Timeout: 30 * time.Second, Transport: router.Transport(route), CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme == "magnet" || len(via) > 8 {
				return http.ErrUseLastResponse
			}
			return nil
		}}}
	c.site = strings.TrimRight(d.Links[0], "/") + "/"
	for _, s := range d.Settings {
		switch s.Type {
		case "checkbox":
			if b, _ := s.Default.(bool); b {
				c.config[s.Name] = "True"
			} else {
				c.config[s.Name] = ""
			}
		case "info", "info_cookie", "info_flaresolverr", "info_useragent", "info_category_8000":
		default:
			c.config[s.Name] = fmt.Sprint(firstOf(s.Default, ""))
		}
	}
	c.config["sitelink"] = c.site
	for _, m := range d.Caps.CategoryMappings {
		c.cats[m.ID] = torznabCat(m.Cat)
	}
	gap := time.Duration(max(d.RequestDelay, 1) * float64(time.Second))
	c.pace = &rateLimiter{interval: gap}
	return c, nil
}

// --- templates and filters ------------------------------------------------------------------

var cgFuncs = template.FuncMap{
	"re_replace": func(s any, pattern, repl string) string {
		re, err := cgRegexp(pattern)
		if err != nil {
			return fmt.Sprint(s)
		}
		return re.ReplaceAllString(fmt.Sprint(s), cgReplacement(repl))
	},
	"join": func(list any, sep string) string {
		switch l := list.(type) {
		case []string:
			return strings.Join(l, sep)
		case []any:
			var out []string
			for _, x := range l {
				out = append(out, fmt.Sprint(x))
			}
			return strings.Join(out, sep)
		}
		return fmt.Sprint(list)
	},
}

var cgTemplates sync.Map // text → *template.Template

// render fills in a definition's template.
func (c *cardigann) render(text string, vars map[string]any) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var t *template.Template
	if v, ok := cgTemplates.Load(text); ok {
		t = v.(*template.Template)
	} else {
		var err error
		t, err = template.New("").Funcs(cgFuncs).Option("missingkey=zero").Parse(goTemplate(text))
		if err != nil {
			debugf("%s: template %q: %v", c.def.ID, text, err)
			return text
		}
		cgTemplates.Store(text, t)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, vars); err != nil {
		debugf("%s: template %q: %v", c.def.ID, text, err)
	}
	return strings.ReplaceAll(b.String(), "<no value>", "")
}

var (
	reHyphenKey = regexp.MustCompile(`\.(Config|Result|Query)\.([A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)+)`)
	reAction    = regexp.MustCompile(`\{\{.*?\}\}`)
	reQuoted    = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// goTemplate makes Jackett's template text Go's: settings with hyphens in their names are
// looked up with index, and quoted regular expressions ("\s+") become raw strings.
func goTemplate(text string) string {
	text = reHyphenKey.ReplaceAllString(text, `(index .$1 "$2")`)
	return reAction.ReplaceAllStringFunc(text, func(action string) string {
		return reQuoted.ReplaceAllStringFunc(action, func(q string) string {
			inner := q[1 : len(q)-1]
			if !strings.Contains(inner, `\`) || strings.Contains(inner, "`") {
				return q
			}
			return "`" + strings.ReplaceAll(inner, `\"`, `"`) + "`"
		})
	})
}

// cgRegexp compiles a .NET-style pattern as far as Go's regexps allow.
func cgRegexp(p string) (*regexp.Regexp, error) {
	p = strings.NewReplacer(`\p{IsCJKUnifiedIdeographs}`, `\p{Han}`, `\p{IsCyrillic}`, `\p{Cyrillic}`).Replace(p)
	return regexp.Compile(p)
}

// cgReplacement turns .NET's $1 into Go's ${1}.
var reDollar = regexp.MustCompile(`\$(\d+)`)

func cgReplacement(s string) string { return reDollar.ReplaceAllString(s, "$${$1}") }

func filterArgs(a any) []string {
	switch v := a.(type) {
	case nil:
		return nil
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return []string{fmt.Sprint(a)}
}

func (c *cardigann) applyFilters(s string, filters []cgFilter, vars map[string]any) string {
	for _, f := range filters {
		args := filterArgs(f.Args)
		arg := func(i int) string {
			if i < len(args) {
				return c.render(args[i], vars)
			}
			return ""
		}
		switch f.Name {
		case "replace":
			s = strings.ReplaceAll(s, arg(0), arg(1))
		case "re_replace":
			if re, err := cgRegexp(arg(0)); err == nil {
				s = re.ReplaceAllString(s, cgReplacement(arg(1)))
			}
		case "regexp":
			if re, err := cgRegexp(arg(0)); err == nil {
				if m := re.FindStringSubmatch(s); len(m) > 1 {
					s = m[1]
				} else if m != nil {
					s = m[0]
				} else {
					s = ""
				}
			}
		case "split":
			n, _ := strconv.Atoi(arg(1))
			parts := strings.Split(s, arg(0))
			if n < 0 {
				n += len(parts)
			}
			if n >= 0 && n < len(parts) {
				s = parts[n]
			}
		case "trim":
			if a := arg(0); a != "" {
				s = strings.Trim(s, a)
			} else {
				s = strings.TrimSpace(s)
			}
		case "prepend":
			s = arg(0) + s
		case "append":
			s += arg(0)
		case "tolower":
			s = strings.ToLower(s)
		case "toupper":
			s = strings.ToUpper(s)
		case "urldecode":
			if u, err := url.QueryUnescape(s); err == nil {
				s = u
			}
		case "urlencode":
			s = url.QueryEscape(s)
		case "htmldecode":
			s = html.UnescapeString(s)
		case "htmlencode":
			s = html.EscapeString(s)
		case "querystring":
			if u, err := url.Parse(s); err == nil {
				s = u.Query().Get(arg(0))
			}
		case "validfilename":
			s = strings.Map(func(r rune) rune {
				if strings.ContainsRune(`<>:"/\|?*`, r) {
					return '_'
				}
				return r
			}, s)
		case "diacritics":
			s = foldAccents(s)
		case "strdump", "hexdump", "dateparse", "timeparse", "timeago", "reltime", "fuzzytime", "validate", "andmatch":
			// dates aren't used here; andmatch narrows results, which the matcher does anyway
		default:
			debugf("%s: filter %q isn't supported", c.def.ID, f.Name)
		}
	}
	return s
}

func foldAccents(s string) string { return accentFolder.Replace(s) }

// --- searching ------------------------------------------------------------------------------

func (c *cardigann) vars(keywords string) map[string]any {
	return map[string]any{
		"Keywords":   keywords,
		"Query":      map[string]any{"Keywords": keywords, "Q": keywords},
		"Categories": c.audio,
		"Config":     c.config,
		"True":       "True",
		"False":      "",
		"Today":      map[string]any{"Year": time.Now().Year()},
		"Result":     map[string]any{},
	}
}

// search runs a music search on the site.
func (c *cardigann) search(ctx context.Context, query string) ([]torrentResult, error) {
	keywords := c.applyFilters(query, c.def.Search.KeywordsFilters, c.vars(query))
	vars := c.vars(keywords)
	var out []torrentResult
	var firstErr error
	tried := 0
	for _, p := range c.def.Search.Paths {
		if len(p.Categories) > 0 && !overlaps(p.Categories, c.audio) {
			continue
		}
		if tried++; tried > 2 { // the first pages are enough, and the site is spared
			break
		}
		results, err := c.searchPath(ctx, p.Path, p.Method, p.Inputs, p.Response.Type, vars)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out = append(out, results...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		if ContainsString(b, x) {
			return true
		}
	}
	return false
}

func (c *cardigann) searchPath(ctx context.Context, path, method string, inputs map[string]string, kind string, vars map[string]any) ([]torrentResult, error) {
	u, err := url.Parse(c.site)
	if err != nil {
		return nil, err
	}
	ref, err := url.Parse(c.render(path, vars))
	if err != nil {
		return nil, fmt.Errorf("bad search path: %w", err)
	}
	u = u.ResolveReference(ref)
	values := url.Values{}
	raw := ""
	for _, in := range []map[string]string{c.def.Search.Inputs, inputs} {
		for k, v := range in {
			val := c.render(v, vars)
			if k == "$raw" {
				raw += val
				continue
			}
			values.Set(k, val)
		}
	}
	var body io.Reader
	if strings.EqualFold(method, "post") {
		body = strings.NewReader(values.Encode() + raw)
	} else {
		q := u.Query()
		for k, vs := range values {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
		if raw != "" {
			if u.RawQuery != "" {
				u.RawQuery += "&"
			}
			u.RawQuery += raw
		}
	}
	data, err := c.fetch(ctx, strings.ToUpper(FirstNonEmpty(method, "get")), u.String(), body)
	if err != nil {
		return nil, err
	}
	if kind == "json" {
		return c.parseJSON(data, vars)
	}
	return c.parseHTML(data, kind == "xml", vars)
}

// fetch gets a page of the site, through FlareSolverr when the site answers with a challenge.
func (c *cardigann) fetch(ctx context.Context, method, u string, body io.Reader) ([]byte, error) {
	if err := c.pace.wait(ctx); err != nil {
		return nil, err
	}
	var bodyData []byte
	if body != nil {
		bodyData, _ = io.ReadAll(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(bodyData))
	if err != nil {
		return nil, err
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range c.def.Search.Headers {
		req.Header.Set(k, fmt.Sprint(firstOf(filterArgs(v), "")))
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	resp.Body.Close()
	if isChallenge(data) || resp.StatusCode == 403 && bytes.Contains(data, []byte("cloudflare")) {
		if c.flare == nil {
			return nil, blockedf("%s shows a browser check (Cloudflare); set FlareSolverr in Sources → Torrent to get past it", c.def.Name)
		}
		if method != "GET" {
			return nil, blockedf("%s shows a browser check", c.def.Name)
		}
		if data, err = c.flare.get(ctx, u); err != nil {
			return nil, err
		}
	} else if resp.StatusCode >= 400 {
		return nil, unavailablef("%s answered HTTP %d", c.def.Name, resp.StatusCode)
	}
	if enc := strings.ToLower(c.def.Encoding); enc == "windows-1251" || enc == "cp1251" {
		if d, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
			data = d
		}
	}
	return data, nil
}

const browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"

// fieldValue works out one field of a result.
func (c *cardigann) fieldValue(sel cgSelector, find func(cgSelector) (string, bool), vars map[string]any) (string, bool) {
	var v string
	switch {
	case sel.Text != nil:
		v = c.render(*sel.Text, vars)
	case len(sel.Case) > 0:
		found := false
		for s, val := range sel.Case {
			if _, ok := find(cgSelector{Selector: s}); ok {
				v, found = c.render(val, vars), true
				break
			}
		}
		if !found {
			return c.def0(sel, vars)
		}
	default:
		var ok bool
		if v, ok = find(sel); !ok {
			return c.def0(sel, vars)
		}
	}
	return strings.TrimSpace(c.applyFilters(v, sel.Filters, vars)), true
}

func (c *cardigann) def0(sel cgSelector, vars map[string]any) (string, bool) {
	if sel.Default != nil {
		return c.render(*sel.Default, vars), true
	}
	return "", sel.Optional
}

func (c *cardigann) rowResult(find func(cgSelector) (string, bool), vars map[string]any) (torrentResult, bool) {
	res := map[string]any{}
	vars["Result"] = res
	for _, f := range c.fields {
		v, ok := c.fieldValue(f.sel, find, vars)
		if !ok && !f.sel.Optional && (f.name == "title" || f.name == "download" || f.name == "magnet" && res["download"] == nil) {
			if f.name != "magnet" {
				return torrentResult{}, false
			}
		}
		res[f.name] = v
	}
	get := func(k string) string { s, _ := res[k].(string); return s }
	r := torrentResult{Title: get("title"), Seeders: -1, Indexer: c.def.ID}
	abs := func(s string) string {
		if s == "" || strings.HasPrefix(s, "magnet:") {
			return s
		}
		if u, err := url.Parse(c.site); err == nil {
			if ref, err := url.Parse(s); err == nil {
				return u.ResolveReference(ref).String()
			}
		}
		return s
	}
	r.Details, r.Link, r.Magnet = abs(get("details")), abs(get("download")), get("magnet")
	if strings.HasPrefix(r.Link, "magnet:") {
		r.Magnet, r.Link = r.Link, ""
	}
	r.InfoHash = strings.ToLower(get("infohash"))
	if r.Magnet == "" && len(r.InfoHash) == 40 {
		r.Magnet = "magnet:?xt=urn:btih:" + r.InfoHash + "&dn=" + url.QueryEscape(r.Title)
	}
	if m := reBTIH.FindStringSubmatch(r.Magnet); m != nil && r.InfoHash == "" {
		r.InfoHash = m[1]
	}
	if s := get("seeders"); s != "" {
		r.Seeders, _ = strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	}
	r.Size = parseSize(get("size"))
	if cat := get("category"); cat != "" {
		if n, ok := c.cats[cat]; ok {
			r.Categories = []int{n}
		}
	}
	if r.Title == "" || (r.Link == "" && r.Magnet == "") {
		return torrentResult{}, false
	}
	return r, true
}

var reSize = regexp.MustCompile(`(?i)([\d.,]+)\s*([kmgt]?i?b|bytes?)?`)

// parseSize reads "1.5 GB", "700 MiB", "1,234 KB" or a byte count.
func parseSize(s string) int64 {
	m := reSize.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	num := strings.ReplaceAll(m[1], ",", "")
	if strings.Count(m[1], ",") == 1 && !strings.Contains(m[1], ".") && len(m[1])-strings.Index(m[1], ",") != 4 {
		num = strings.ReplaceAll(m[1], ",", ".") // a decimal comma
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	unit := strings.ToLower(m[2])
	mult := map[byte]float64{'k': 1 << 10, 'm': 1 << 20, 'g': 1 << 30, 't': 1 << 40}
	if len(unit) > 0 {
		if f, ok := mult[unit[0]]; ok {
			v *= f
		}
	}
	return int64(v)
}

func (c *cardigann) parseHTML(data []byte, isXML bool, vars map[string]any) ([]torrentResult, error) {
	if isXML { // the HTML parser takes <link> as a void element
		data = bytes.ReplaceAll(bytes.ReplaceAll(data, []byte("<link>"), []byte("<xlink>")), []byte("</link>"), []byte("</xlink>"))
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	fix := func(sel string) string {
		if isXML {
			return regexp.MustCompile(`\blink\b`).ReplaceAllString(sel, "xlink")
		}
		return sel
	}
	rowSel := fix(c.render(c.def.Search.Rows.Selector, vars))
	var out []torrentResult
	rows := doc.Find(rowSel)
	if rows.Length() == 0 && bytes.Contains(data, []byte("<title>Just a moment")) {
		return nil, blockedf("%s shows a browser check", c.def.Name)
	}
	rows.Each(func(i int, row *goquery.Selection) {
		find := func(sel cgSelector) (string, bool) {
			s := row
			if sel.Selector != "" {
				s = row.Find(fix(c.render(sel.Selector, vars)))
				if s.Length() == 0 {
					if row.Is(fix(sel.Selector)) {
						s = row
					} else {
						return "", false
					}
				}
			}
			s = s.First()
			if sel.Remove != "" {
				s = s.Clone()
				s.Find(sel.Remove).Remove()
			}
			if sel.Attribute != "" {
				v, ok := s.Attr(sel.Attribute)
				return v, ok
			}
			return s.Text(), true
		}
		if r, ok := c.rowResult(find, vars); ok {
			out = append(out, r)
		}
	})
	if after := c.def.Search.Rows.After; after > 0 {
		debugf("%s: rows with 'after' aren't merged", c.def.ID)
	}
	return out, nil
}

// jsonPath follows a selector ("data.items", "$") into a JSON value.
func jsonPath(v any, path string) any {
	path = strings.TrimSpace(path)
	if i := strings.Index(path, ":"); i >= 0 {
		path = path[:i] // :has(...) filters aren't supported; the matcher picks anyway
	}
	path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			v = x[part]
		case []any:
			if n, err := strconv.Atoi(strings.Trim(part, "[]")); err == nil && n < len(x) {
				v = x[n]
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return v
}

func (c *cardigann) parseJSON(data []byte, vars map[string]any) ([]torrentResult, error) {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, failf("%s sent an unreadable answer", c.def.Name)
	}
	rows, _ := jsonPath(doc, c.render(c.def.Search.Rows.Selector, vars)).([]any)
	var out []torrentResult
	for _, row := range rows {
		find := func(sel cgSelector) (string, bool) {
			v := jsonPath(row, c.render(sel.Selector, vars))
			if v == nil {
				return "", false
			}
			if f, ok := v.(float64); ok {
				return strconv.FormatFloat(f, 'f', -1, 64), true
			}
			return fmt.Sprint(v), true
		}
		if r, ok := c.rowResult(find, vars); ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// downloadLink finds the torrent or magnet link on a details page, with the definition's
// download selectors.
func (c *cardigann) downloadLink(ctx context.Context, page []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	vars := c.vars("")
	for _, sel := range c.def.Download.Selectors {
		s := doc.Find(c.render(sel.Selector, vars)).First()
		if s.Length() == 0 {
			continue
		}
		v := s.Text()
		if sel.Attribute != "" {
			v, _ = s.Attr(sel.Attribute)
		}
		if v = strings.TrimSpace(c.applyFilters(v, sel.Filters, vars)); v != "" {
			if strings.HasPrefix(v, "magnet:") {
				return v
			}
			if u, err := url.Parse(c.site); err == nil {
				if ref, err := url.Parse(v); err == nil {
					return u.ResolveReference(ref).String()
				}
			}
			return v
		}
	}
	return ""
}

// --- FlareSolverr ---------------------------------------------------------------------------

// flareSolverr gets past Cloudflare's browser checks the way Jackett does: a FlareSolverr
// service (github.com/FlareSolverr/FlareSolverr) loads the page in a real browser, and its
// cookies and user agent are reused for a while.
type flareSolverr struct {
	url  string
	http *http.Client
	mu   sync.Mutex
	ua   string
	jar  map[string][]*http.Cookie // by host
}

func newFlareSolverr(u string) *flareSolverr {
	if strings.TrimSpace(u) == "" {
		return nil
	}
	return &flareSolverr{url: strings.TrimRight(u, "/") + "/v1", http: &http.Client{Timeout: 90 * time.Second}, jar: map[string][]*http.Cookie{}}
}

func (f *flareSolverr) prepare(req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	req.Header.Set("User-Agent", FirstNonEmpty(f.ua, browserUA))
	for _, ck := range f.jar[req.URL.Host] {
		req.AddCookie(ck)
	}
}

func (f *flareSolverr) get(ctx context.Context, u string) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{"cmd": "request.get", "url": u, "maxTimeout": 60000})
	req, err := http.NewRequestWithContext(ctx, "POST", f.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return nil, unavailablef("FlareSolverr at %s: %s", f.url, describeNetErr(err))
	}
	defer resp.Body.Close()
	var out struct {
		Status   string
		Message  string
		Solution struct {
			Response  string
			UserAgent string `json:"userAgent"`
			Cookies   []struct {
				Name, Value string
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Status != "ok" {
		return nil, unavailablef("FlareSolverr couldn't get past the check: %s", FirstNonEmpty(out.Message, fmt.Sprint(err)))
	}
	host := ""
	if pu, err := url.Parse(u); err == nil {
		host = pu.Host
	}
	f.mu.Lock()
	f.ua = out.Solution.UserAgent
	var jar []*http.Cookie
	for _, ck := range out.Solution.Cookies {
		jar = append(jar, &http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	f.jar[host] = jar
	f.mu.Unlock()
	return []byte(out.Solution.Response), nil
}
