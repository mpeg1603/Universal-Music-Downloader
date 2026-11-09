package core

import (
	"strings"
	"unicode"
)

// Russian names are written in both alphabets, and catalogs and uploaders each pick one: a band
// that's "Chernikovskaya Hata" on Deezer is "Черниковская хата" in the search box.

var cyrToLat = map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z",
	'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	'і': "i", 'ї': "i", 'є': "e", 'ґ': "g", 'ў': "u"}

var latDigraphs = []struct{ lat, cyr string }{{"shch", "щ"}, {"sch", "щ"}, {"sh", "ш"}, {"ch", "ч"}, {"zh", "ж"}, {"kh", "х"},
	{"ts", "ц"}, {"yu", "ю"}, {"ya", "я"}, {"yo", "ё"}, {"ye", "е"}, {"iy", "ий"}, {"yy", "ый"}}

var latToCyr = map[rune]string{'a': "а", 'b': "б", 'c': "к", 'd': "д", 'e': "е", 'f': "ф", 'g': "г", 'h': "х", 'i': "и", 'j': "й",
	'k': "к", 'l': "л", 'm': "м", 'n': "н", 'o': "о", 'p': "п", 'q': "к", 'r': "р", 's': "с", 't': "т", 'u': "у", 'v': "в",
	'w': "в", 'x': "кс", 'y': "ы", 'z': "з"}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func hasLatin(s string) bool {
	for _, r := range s {
		if r < 128 && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// toLatin writes Cyrillic in Latin letters and leaves everything else alone.
func toLatin(s string) string {
	if !hasCyrillic(s) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		low := unicode.ToLower(r)
		out, ok := cyrToLat[low]
		switch {
		case !ok:
			b.WriteRune(r)
		case low != r && out != "":
			b.WriteString(strings.ToUpper(out[:1]) + out[1:])
		default:
			b.WriteString(out)
		}
	}
	return b.String()
}

// toCyrillic is the reverse, for names only ever searched for: a best guess.
func toCyrillic(s string) string {
	out := []rune(strings.ToLower(s))
	var b strings.Builder
	prevVowel := false
	for i := 0; i < len(out); {
		matched := false
		for _, d := range latDigraphs {
			if strings.HasPrefix(string(out[i:]), d.lat) {
				b.WriteString(d.cyr)
				i += len([]rune(d.lat))
				prevVowel, matched = strings.ContainsAny(d.lat[:1], "yaeiou"), true
				break
			}
		}
		if matched {
			continue
		}
		r := out[i]
		switch c, ok := latToCyr[r]; {
		case ok && r == 'y' && (prevVowel || i+1 == len(out) || !unicode.IsLetter(out[i+1])):
			b.WriteRune('й')
		case ok:
			b.WriteString(c)
		default:
			b.WriteRune(r)
		}
		prevVowel = strings.ContainsRune("aeiou", r)
		i++
	}
	return b.String()
}

var latFold = strings.NewReplacer("kh", "h", "ks", "x", "iy", "y", "yi", "y", "ij", "y", "j", "y", "ph", "f", "w", "v", "ye", "e")

// foldName is a name in the form spellings across alphabets and small typos meet in: lowercase,
// Latin letters, the usual transliteration choices evened out, and doubled letters single.
func foldName(s string) string {
	out := latFold.Replace(normalize(toLatin(s)))
	var b strings.Builder
	var last rune
	for _, r := range out {
		if r != last || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
		last = r
	}
	return b.String()
}

// altScript is a search text in the other alphabet, or "" when there's no point. Tags
// ("artist:", "year:") and everything that isn't a name are left alone.
func altScript(text string, latinToo bool) string {
	var conv func(string) string
	switch {
	case hasCyrillic(text):
		conv = toLatin
	case latinToo && hasLatin(text):
		conv = toCyrillic
	default:
		return ""
	}
	locs := reTag.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return conv(text)
	}
	var b strings.Builder
	at := 0
	for _, l := range locs {
		b.WriteString(conv(text[at:l[0]]) + text[l[0]:l[1]])
		at = l[1]
	}
	b.WriteString(conv(text[at:]))
	return b.String()
}

// transliteratedPair is a track and a candidate in Latin letters, for comparing them.
func transliteratedPair(t *Track, c *Candidate) (*Track, *Candidate) {
	lt, lc := *t, *c
	lt.Title, lt.Album, lc.Title, lc.Artist, lc.Album = toLatin(t.Title), toLatin(t.Album), toLatin(c.Title), toLatin(c.Artist), toLatin(c.Album)
	lt.Artists = make([]string, len(t.Artists))
	for i, a := range t.Artists {
		lt.Artists[i] = toLatin(a)
	}
	return &lt, &lc
}

// differentAlphabets says whether the title or the artist is in Cyrillic on one side only.
func differentAlphabets(t *Track, c *Candidate) bool {
	ours := strings.Join(t.Artists, " ")
	return hasCyrillic(t.Title) != hasCyrillic(c.Title) || hasCyrillic(ours) != hasCyrillic(c.Artist)
}

// transliteratedQueries are extra searches for a track whose name mixes alphabets or could be
// written in the other one: uploads are often titled in just one.
func transliteratedQueries(t *Track, queries []string) []string {
	artist, title := "", searchTitle(t.Title)
	if len(t.Artists) > 0 {
		artist = t.Artists[0]
	}
	var out []string
	add := func(q string) {
		q = strings.Join(strings.Fields(q), " ")
		for _, x := range append(queries, out...) {
			if strings.EqualFold(x, q) {
				return
			}
		}
		if q != "" {
			out = append(out, q)
		}
	}
	mixed := hasCyrillic(title) != hasCyrillic(artist)
	if hasCyrillic(artist) || hasCyrillic(title) {
		add(toLatin(artist) + " " + toLatin(title))
	}
	if mixed {
		a, tt := artist, title
		if !hasCyrillic(a) {
			a = toCyrillic(a)
		}
		if !hasCyrillic(tt) {
			tt = toCyrillic(tt)
		}
		add(a + " " + tt)
	}
	return out
}
