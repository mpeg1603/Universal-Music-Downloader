package core

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// Songs listed under a name the sources don't use: the artist written in another alphabet
// ("Alyans" for "Альянс"), a compilation's "Various Artists" instead of the real performer,
// the subtitle in brackets being the name the song goes by. "Find under other names" searches
// for such a song by names you pick (suggestions made from its own, or typed in). The file
// found is still tagged and filed with the song's own names, and its length still has to fit.

// NameVariant is one name to search for a song by.
type NameVariant struct {
	Artist string `json:"artist,omitempty"` // "": any artist, as long as the title and the length fit
	Title  string `json:"title"`
	Why    string `json:"why,omitempty"` // for suggestions: how it was made
}

func (v NameVariant) String() string {
	if v.Artist == "" {
		return v.Title + " (any artist)"
	}
	return v.Artist + " - " + v.Title
}

// track is t as searched for under this name.
func (v NameVariant) track(t *Track) *Track {
	c := *t
	c.Title = FirstNonEmpty(strings.TrimSpace(v.Title), t.Title)
	c.AlbumArtist, c.ISRC = "", "" // an ISRC search finds the listing that wasn't found
	if a := strings.TrimSpace(v.Artist); a != "" {
		c.Artists = []string{a}
	} else {
		c.Artists, c.anyArtist = []string{""}, true
	}
	return &c
}

// otherNames keeps the names chosen for songs (data/other-names.json).
type otherNames struct {
	once  sync.Once
	mu    sync.Mutex
	path  string
	byKey map[string][]NameVariant
}

func (o *otherNames) load() {
	o.once.Do(func() {
		o.byKey = map[string][]NameVariant{}
		if data, err := os.ReadFile(o.path); err == nil {
			json.Unmarshal(data, &o.byKey)
		}
	})
}

func (o *otherNames) get(key string) []NameVariant {
	o.load()
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]NameVariant(nil), o.byKey[key]...)
}

func (o *otherNames) set(key string, names []NameVariant) error {
	o.load()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(names) == 0 {
		delete(o.byKey, key)
	} else {
		o.byKey[key] = names
	}
	data, err := json.MarshalIndent(o.byKey, "", " ")
	if err != nil {
		return err
	}
	return writeFileAtomic(o.path, data)
}

// --- suggestions ------------------------------------------------------------------------------

// otherScript is s in the other alphabet (Cyrillic ↔ Latin), or "". Latin is only written in
// Cyrillic when translit says so (see looksTransliterated).
func otherScript(s string, translit bool) string {
	switch {
	case hasCyrillic(s):
		return toLatin(s)
	case translit && hasLatin(s):
		return capWords(toCyrillic(s))
	}
	return ""
}

var (
	// Letter groups that Russian (or Ukrainian, Belarusian) names written in Latin letters have
	// and English words rarely do.
	reTranslit = regexp.MustCompile(`zh|kh|shch|sch|ts[aeiouy]|y[aueo]|iy\b|ij\b|yy\b|yj\b|aya\b|oye\b|[^aeiou]ye`)
	// Common English words: a name with one is English, not transliterated.
	reEnglish = wordRe(`the|a|an|and|of|you|your|my|me|i|to|in|on|for|with|love|is|are|be|it|this|that|all|what|we|no|not|don t|im`)
)

// looksTransliterated says whether Latin text looks like a Cyrillic name written in Latin
// letters ("Kosmicheskij Pinbol", "Alyans").
func looksTransliterated(s string) bool {
	n := normalize(s)
	return n != "" && !hasCyrillic(s) && reTranslit.MatchString(n) && !reEnglish.MatchString(n)
}

// capWords capitalizes the first letter of each word that's all lowercase.
func capWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		r := []rune(w)
		if len(r) > 0 && strings.ToLower(w) == w {
			r[0] = unicode.ToUpper(r[0])
			words[i] = string(r)
		}
	}
	return strings.Join(words, " ")
}

// nameSuggestions are the other names a song might go by, most likely first.
func nameSuggestions(t *Track) []NameVariant {
	artist := ""
	if len(t.Artists) > 0 {
		artist = t.Artists[0]
	}
	title := searchTitle(t.Title)
	var out []NameVariant
	seen := map[string]bool{strings.ToLower(artist + "\x00" + title): true}
	add := func(a, ti, why string) {
		a, ti = strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(ti), " ")
		k := strings.ToLower(a + "\x00" + ti)
		if ti == "" || seen[k] || len(out) >= 12 {
			return
		}
		seen[k] = true
		out = append(out, NameVariant{Artist: a, Title: ti, Why: why})
	}
	va := variousArtists(artist)
	if va {
		add("", title, "it's listed under “"+FirstNonEmpty(artist, "no artist")+"”; the title and length have to fit")
	}
	// The other alphabet: Cyrillic names written in Latin, or the other way round.
	// (One part in Cyrillic and one in Latin: the Latin one is likely the transliteration.)
	translit := hasCyrillic(artist) != hasCyrillic(title) || looksTransliterated(artist+" "+title)
	altA, altT := otherScript(artist, translit), otherScript(title, translit)
	if !va && altA != "" {
		add(altA, title, "the artist in the other alphabet")
		if altT != "" {
			add(altA, altT, "both in the other alphabet")
		}
	}
	if altT != "" {
		add(artist, altT, "the title in the other alphabet")
	}
	// A subtitle in brackets, or after " - ", as the name it goes by: "Memo Boy (brian is the
	// most beautiful)".
	for _, m := range reBrackets.FindAllStringSubmatch(baseTitle(t.Title), -1) {
		inner := strings.TrimSpace(m[1])
		if inner == "" || reNoise.MatchString(inner) || reVersionWords.MatchString(inner) || versionMarks(normalize(inner)) != 0 {
			continue
		}
		add(inner, title, "the words in brackets as the artist")
	}
	if parts := reSpacedDash.Split(t.Title, 2); len(parts) == 2 {
		sub := strings.TrimSpace(parts[1])
		if sub != "" && !reNoise.MatchString(sub) && !reVersionWords.MatchString(sub) && versionMarks(normalize(sub)) == 0 {
			add(sub, searchTitle(parts[0]), "what follows “ - ” as the artist")
		}
	}
	// Someone else credited.
	for _, a := range t.Artists[min(1, len(t.Artists)):] {
		add(a, title, "another of its credited artists")
	}
	if aa := t.AlbumArtist; aa != "" && !variousArtists(aa) {
		add(aa, title, "the album's artist")
	}
	if !va && artist != "" {
		add(title, artist, "artist and title swapped")
		add("", title, "only the title and length have to fit")
	}
	return out
}

// --- the backend ------------------------------------------------------------------------------

// NameSuggestions are other names to search for a song by, and the ones chosen for it before.
func (m *Manager) NameSuggestions(key string) (suggested, chosen []NameVariant) {
	j := m.job(key)
	if j == nil {
		return nil, nil
	}
	j.mu.Lock()
	t := j.Track
	chosen = append([]NameVariant(nil), j.alt...)
	j.mu.Unlock()
	return nameSuggestions(t), chosen
}

// SearchUnderOtherNames searches for a song again, by the names given instead of its own (none:
// by its own again). They're kept for the song, for its later searches too.
func (m *Manager) SearchUnderOtherNames(key string, names []NameVariant) error {
	j := m.job(key)
	if j == nil {
		return fmt.Errorf("that song isn't in the list anymore")
	}
	var clean []NameVariant
	for _, v := range names {
		v.Artist, v.Title = strings.TrimSpace(v.Artist), strings.TrimSpace(v.Title)
		if v.Title == "" {
			continue
		}
		if len(clean) == 8 {
			return fmt.Errorf("at most 8 names at a time: each one is another search on every source")
		}
		clean = append(clean, v)
	}
	if err := m.names.set(key, clean); err != nil {
		return err
	}
	j.mu.Lock()
	j.alt = clean
	t := j.Track
	j.mu.Unlock()
	if len(clean) > 0 {
		var list []string
		for _, v := range clean {
			list = append(list, v.String())
		}
		Logf("%s: searching under other names: %s", t, strings.Join(list, "; "))
	}
	m.Restart(key)
	return nil
}
