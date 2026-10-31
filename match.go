package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Deciding whether a search result is the same recording as a track from the library.

const (
	minTitle  = 80.0
	minArtist = 70.0
)

type Candidate struct {
	Source    string
	URL       string
	Title     string
	Artist    string
	Album     string
	ArtistURL string  // the uploader/artist page on that source
	Duration  float64 // seconds, 0 when unknown
	// ArtistIsCredit is true when Artist is the performer credit (Bandcamp band, YouTube Music
	// credits). Then the artist's name in the title doesn't count: that's how covers are named.
	ArtistIsCredit bool
}

var accentFolder = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "ā", "a", "ă", "a", "ą", "a",
	"ç", "c", "ć", "c", "ĉ", "c", "ċ", "c", "č", "c", "ď", "d", "đ", "d", "ð", "d",
	"è", "e", "é", "e", "ê", "e", "ë", "e", "ē", "e", "ĕ", "e", "ė", "e", "ę", "e", "ě", "e",
	"ĝ", "g", "ğ", "g", "ġ", "g", "ģ", "g", "ĥ", "h", "ħ", "h",
	"ì", "i", "í", "i", "î", "i", "ï", "i", "ĩ", "i", "ī", "i", "ĭ", "i", "į", "i", "ı", "i",
	"ĵ", "j", "ķ", "k", "ĺ", "l", "ļ", "l", "ľ", "l", "ŀ", "l", "ł", "l",
	"ñ", "n", "ń", "n", "ņ", "n", "ň", "n",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o", "ō", "o", "ŏ", "o", "ő", "o",
	"ŕ", "r", "ŗ", "r", "ř", "r", "ś", "s", "ŝ", "s", "ş", "s", "š", "s", "ș", "s",
	"ţ", "t", "ť", "t", "ŧ", "t", "ț", "t",
	"ù", "u", "ú", "u", "û", "u", "ü", "u", "ũ", "u", "ū", "u", "ŭ", "u", "ů", "u", "ű", "u", "ų", "u",
	"ŵ", "w", "ý", "y", "ÿ", "y", "ŷ", "y", "ź", "z", "ż", "z", "ž", "z",
	"ß", "ss", "æ", "ae", "œ", "oe", "þ", "th",
	"&", " and ", "'", "", "’", "",
)

// normalize lowercases, strips accents and punctuation, and collapses whitespace.
func normalize(s string) string {
	s = accentFolder.Replace(strings.ToLower(s))
	var b strings.Builder
	space := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case unicode.Is(unicode.Mn, r): // combining accent
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

var (
	reFeatBracket = regexp.MustCompile(`(?i)[(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s[^)\]]*[)\]]`)
	reFeatTail    = regexp.MustCompile(`(?i)\s(?:feat\.?|ft\.?|featuring)\s.*$`)
	reBrackets    = regexp.MustCompile(`[(\[{【]([^)\]}】]*)[)\]}】]`)
	// Bracketed parts containing these words are packaging, not part of the song title.
	reNoise = regexp.MustCompile(`(?i)\b(?:official|video|audio|lyrics?|visuali[sz]er|hd|hq|4k|remaster(?:ed)?|` +
		`explicit|clean|mv|m/v|music video|full song|color coded|out now|premiere|album version|single version|original mix)\b`)
	// The same words trailing an un-bracketed upload title ("song official video"), on normalized text.
	reNoiseTail    = regexp.MustCompile(`(?: (?:official(?: music)?(?: video| audio)?|lyric video|lyrics|audio|video|hd|hq|4k|visuali[sz]er))+$`)
	reChannelNoise = regexp.MustCompile(`(?i)\s*-\s*topic$|vevo$|\s+official$|\s+music$|\s+records?$`)
	reSpacedDash   = regexp.MustCompile(`\s+-\s+`)
	reTitleParts   = regexp.MustCompile(`\s[-–—|]\s`)
)

func wordRe(alts string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^| )(?:` + alts + `)(?: |$)`)
}

// Words that mark a different version of a song. A candidate containing one is rejected
// unless the track's own title/album contains it too. Matched against normalize()d text.
var versionMarkers = []struct {
	name string
	re   *regexp.Regexp
}{
	{"live recording", wordRe(`live`)},
	{"remix", wordRe(`re ?mix|remixed|rmx|bootleg`)},
	{"cover", wordRe(`cover|covered|tribute`)},
	{"karaoke version", wordRe(`karaoke`)},
	{"instrumental", wordRe(`instrumental|backing track`)},
	{"a cappella version", wordRe(`a ?cap+el+a`)},
	{"acoustic version", wordRe(`acoustic|unplugged`)},
	{"sped-up/slowed edit", wordRe(`sped up|speed up|slowed|reverb|nightcore|daycore|8d|bass ?boost(?:ed)?`)},
	{"extended/looped version", wordRe(`extended|[0-9]+ ?hours?`)},
	{"demo", wordRe(`demo`)},
	{"preview or snippet", wordRe(`preview|snippet|teaser`)},
	{"reaction or tutorial", wordRe(`reaction|tutorial|lesson|how to play`)},
	{"orchestral/piano version", wordRe(`orchestral|piano version|guitar version`)},
	{"mashup", wordRe(`mash ?up`)},
}

// baseTitle drops a " - Remastered 2011"/" - Radio Edit" suffix and featured artists.
func baseTitle(title string) string {
	title = reSpacedDash.Split(title, 2)[0]
	title = reFeatBracket.ReplaceAllString(title, " ")
	return strings.TrimSpace(reFeatTail.ReplaceAllString(title, ""))
}

// searchTitle is the title to put in search queries: base title without bracketed extras.
func searchTitle(title string) string {
	b := baseTitle(title)
	if s := strings.Join(strings.Fields(reBrackets.ReplaceAllString(b, " ")), " "); s != "" {
		return s
	}
	return b
}

func cleanTitle(title string, candidate bool) string {
	title = reFeatBracket.ReplaceAllString(title, " ")
	title = reBrackets.ReplaceAllStringFunc(title, func(m string) string {
		inner := reBrackets.FindStringSubmatch(m)[1]
		if reNoise.MatchString(inner) {
			return " "
		}
		return " " + inner + " "
	})
	text := normalize(reFeatTail.ReplaceAllString(title, ""))
	if candidate {
		if t := reNoiseTail.ReplaceAllString(text, ""); t != "" {
			text = t
		}
	}
	return text
}

func fmtDuration(seconds float64) string {
	if seconds <= 0 {
		return "?:??"
	}
	s := int(math.Round(seconds))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func durationTolerance(seconds float64) float64 { return max(10, seconds*0.06) }

func containsPhrase(haystack, needle string) bool {
	return needle != "" && strings.Contains(" "+haystack+" ", " "+needle+" ")
}

func versionConflict(t *Track, c *Candidate) string {
	ours := normalize(t.Title + " " + t.Album)
	theirs := normalize(c.Title)
	for _, m := range versionMarkers {
		if m.re.MatchString(theirs) && !m.re.MatchString(ours) {
			return m.name
		}
	}
	return ""
}

func normalizedArtists(t *Track) []string {
	var out []string
	for _, a := range t.Artists {
		if n := normalize(a); n != "" && n != "unknown artist" {
			out = append(out, n)
		}
	}
	return out
}

func titleScore(t *Track, c *Candidate) float64 {
	targets := map[string]bool{cleanTitle(t.Title, false): true, cleanTitle(baseTitle(t.Title), false): true}
	artists := normalizedArtists(t)
	parts := []string{c.Title}
	// Uploads are often titled "Artist - Title" (or "Title | Artist"); try each side too.
	if reTitleParts.MatchString(c.Title) {
		parts = append(parts, reTitleParts.Split(c.Title, -1)...)
	}
	variants := map[string]float64{} // cleaned variant -> penalty
	add := func(v string, penalty float64) {
		if old, ok := variants[v]; v != "" && (!ok || penalty < old) {
			variants[v] = penalty
		}
	}
	for _, part := range parts {
		// Also try without any bracketed extras ("Song (Live)"), slightly penalised; version
		// words like live/remix are policed separately by versionConflict.
		for _, alt := range []struct {
			text    string
			penalty float64
		}{{part, 0}, {reBrackets.ReplaceAllString(part, " "), 5}} {
			cleaned := cleanTitle(alt.text, true)
			add(cleaned, alt.penalty)
			for _, a := range artists {
				if containsPhrase(cleaned, a) {
					add(strings.Join(strings.Fields(strings.ReplaceAll(" "+cleaned+" ", " "+a+" ", " ")), " "), alt.penalty)
				}
			}
		}
	}
	best := 0.0
	for target := range targets {
		if target == "" {
			continue
		}
		for v, penalty := range variants {
			best = max(best, max(ratio(target, v), tokenSortRatio(target, v))-penalty)
		}
	}
	return best
}

func artistScore(t *Track, c *Candidate) float64 {
	names := normalizedArtists(t)
	if len(names) == 0 {
		return 100
	}
	var channels []string
	for _, ch := range []string{normalize(c.Artist), normalize(reChannelNoise.ReplaceAllString(c.Artist, ""))} {
		if ch != "" {
			channels = append(channels, ch)
		}
	}
	title := normalize(c.Title)
	best := 0.0
	for _, n := range names {
		for _, ch := range channels {
			best = max(best, tokenSetRatio(n, ch), ratio(strings.ReplaceAll(n, " ", ""), strings.ReplaceAll(ch, " ", "")))
		}
		if !c.ArtistIsCredit && containsPhrase(title, n) {
			best = max(best, 90)
		}
	}
	return best
}

type Match struct {
	Cand   Candidate
	Score  float64
	Reject string // why it was rejected; empty when accepted
}

func evaluate(t *Track, c Candidate) Match {
	ts := titleScore(t, &c)
	as := artistScore(t, &c)
	rough := 0.55*ts + 0.45*as
	if m := versionConflict(t, &c); m != "" {
		return Match{c, rough, "looks like a " + m}
	}
	if ts < minTitle {
		return Match{c, rough, "different title"}
	}
	if as < minArtist {
		who := c.Artist
		if who == "" {
			who = "unknown uploader"
		}
		return Match{c, rough, "different artist (" + who + ")"}
	}
	d := 50.0
	if c.Duration > 0 && t.DurationMs > 0 {
		diff := math.Abs(c.Duration - t.Duration())
		tol := durationTolerance(t.Duration())
		if diff > tol {
			return Match{c, rough, fmt.Sprintf("length %s vs %s expected", fmtDuration(c.Duration), fmtDuration(t.Duration()))}
		}
		d = 100 * (1 - diff/tol)
	}
	score := 0.45*ts + 0.35*as + 0.20*d
	if c.Album != "" && t.Album != "" && tokenSetRatio(cleanTitle(t.Album, false), cleanTitle(c.Album, false)) >= 90 {
		score += 3
	}
	return Match{c, math.Round(score*10) / 10, ""}
}

// rank splits candidates into accepted (best first) and rejected (closest first).
func rank(t *Track, cands []Candidate) (accepted, rejected []Match) {
	for _, c := range cands {
		m := evaluate(t, c)
		if m.Reject == "" {
			accepted = append(accepted, m)
		} else {
			rejected = append(rejected, m)
		}
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].Score > accepted[j].Score })
	sort.SliceStable(rejected, func(i, j int) bool { return rejected[i].Score > rejected[j].Score })
	return
}

func describeRejection(rejected []Match) string {
	if len(rejected) == 0 {
		return "no results"
	}
	best := rejected[0]
	by := ""
	if best.Cand.Artist != "" {
		by = " by " + best.Cand.Artist
	}
	plural := "s"
	if len(rejected) == 1 {
		plural = ""
	}
	return fmt.Sprintf("no match in %d result%s; closest was “%s”%s (%s)", len(rejected), plural, best.Cand.Title, by, best.Reject)
}
