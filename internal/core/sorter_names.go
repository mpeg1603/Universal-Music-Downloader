package core

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Text helpers of Sort & merge: comparison keys, reading file and folder names, and names that
// are safe on every filesystem.

const (
	sortYear = `(?:19|20)\d{2}`
	sortDash = `[-–—]`
)

var (
	reSortFeatParen     = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:feat\.?|ft\.?|featuring|with)\s[^)\]]*[)\]]`)
	reSortFeatTail      = regexp.MustCompile(`(?i)\s+(?:feat\.?|ft\.?|featuring)\s.*$`)
	reSortJoiners       = regexp.MustCompile(`(?i)\s*(?:,|;|/|&|\s(?:and|x|vs\.?|with)\s)\s*`)
	reSortRemasterParen = regexp.MustCompile(`(?i)\s*[(\[][^)\]]*\bremaster(?:ed)?\b[^)\]]*[)\]]`)
	reSortRemasterDash  = regexp.MustCompile(`(?i)\s+` + sortDash + `\s+[^-–—]*\bremaster(?:ed)?\b[^-–—]*$`)
	reSortSingleMark    = regexp.MustCompile(`(?i)\s*(?:` + sortDash + `\s*single|[(\[]\s*single\s*[)\]])\s*$`)
	reSortEdition       = regexp.MustCompile(`(?i)\s*[(\[][^)\]]*\b(?:deluxe|edition|remaster(?:ed)?|expanded|bonus|anniversary|special|version)\b[^)\]]*[)\]]\s*$`)
	reSortURL           = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?[\w-]+\.(?:com|net|org|ru|info|biz|su|ua|io)\b\S*`)
	reSortTrackHolder   = regexp.MustCompile(`^(?:track|трек|дорожка|piste|audio ?track)\s*\d+$`)
	reSortJunkBracket   = regexp.MustCompile(`(?i)\s*[(\[{][^)\]}]*\b(?:official|video|audio|lyrics?|hq|hd|\d*\s*kbps|mp3|flac|free download|visuali[sz]er|clip|www)\b[^)\]}]*[)\]}]`)
	reSortSpaces        = regexp.MustCompile(`\s+`)
	reSortNameSep       = regexp.MustCompile(`\s+` + sortDash + `\s+`)
	reSortSceneSep      = regexp.MustCompile(`\s*-\s*`)
	reSortDiscTrack     = regexp.MustCompile(`^(\d)[-.](\d{2})(?:\s*[-._)]\s*|\s+)(.+)$`)
	reSortNumDot        = regexp.MustCompile(`^(\d{1,3})\s*[._)]\s*(.+)$`)
	reSortNumDash       = regexp.MustCompile(`^(\d{1,3})\s*-(\s*)(.+)$`)
	reSortNumZero       = regexp.MustCompile(`^(0\d|\d{3})\s+(.+)$`)
	reSortAllDigits     = regexp.MustCompile(`^\d{1,3}$`)
	reSortDiscDir       = regexp.MustCompile(`(?i)^(?:cd|disc|disk|диск)\s*[-_.]?\s*(\d{1,2})(?:\s*(?:of|из|/)\s*\d{1,2})?$`)
	reSortDiscSuffix    = regexp.MustCompile(`(?i)\s*[(\[]?\s*(?:cd|disc|disk|диск)\s*(\d{1,2})\s*[)\]]?\s*$`)
	reSortDirJunk       = regexp.MustCompile(`(?i)\s*[(\[{][^)\]}]*\b(?:flac|mp3|aac|ogg|alac|wav|ape|lossless|web|vinyl|\d*\s*kbps|vbr|cbr|v0|v2|hi ?res|\d{2,3}|\d{2} ?[-/] ?\d{2,3}|24 ?bit|16 ?bit)\b[^)\]}]*[)\]}]`)

	reSortFolderPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^(?P<artist>.+?)\s+` + sortDash + `\s+[(\[]?(?P<year>` + sortYear + `)[)\]]?\s+` + sortDash + `\s+(?P<album>.+)$`),
		regexp.MustCompile(`^(?P<artist>.+?)\s+` + sortDash + `\s+(?P<album>.+?)\s*[(\[](?P<year>` + sortYear + `)[)\]]$`),
		regexp.MustCompile(`^[(\[]?(?P<year>` + sortYear + `)[)\]]?\s*(?:` + sortDash + `|\.)?\s+(?P<album>[^(\[].*)$`),
		regexp.MustCompile(`^(?P<album>.+?)\s*[(\[](?P<year>` + sortYear + `)[)\]]$`),
		regexp.MustCompile(`^(?P<artist>.+?)\s+` + sortDash + `\s+(?P<album>.+)$`),
	}

	sortPlaceholders = setOf("unknown", "unknown artist", "unknown album", "unknown title", "untitled", "no title", "no artist",
		"no album", "n a", "none", "null", "artist", "album", "title", "track",
		"неизвестен", "неизвестный исполнитель", "неизвестный альбом", "без названия")
	sortVarious = setOf("various artists", "various", "va", "v a", "сборник", "разные исполнители", "varios artistas",
		"verschiedene interpreten", "artistes divers")
	sortGenericDirs = setOf("music", "my music", "музыка", "mp3", "flac", "audio", "songs", "tracks", "download", "downloads",
		"загрузки", "new folder", "новая папка", "unsorted", "misc", "other", "temp", "tmp", "singles",
		"unknown", "unknown artist", "unknown album", "various", "various artists", "va", "playlist",
		"playlists", "collection", "library", "itunes", "itunes media", "media", "desktop", "torrents",
		"albums", "artists")
	sortReserved = setOf("con", "prn", "aux", "nul", "com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9")
	sortFolder  = sync.Pool{New: func() any { c := cases.Fold(); return &c }}
	sortIllegal = strings.NewReplacer("/", "-", `\`, "-", ":", " -", "*", "", "?", "", `"`, "'", "<", "", ">", "", "|", "-")
)

func setOf(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}

// sortFold is a key that ignores case, accents, punctuation and spacing ("AC/DC" == "ac-dc").
func sortFold(text string, dropArticle bool) string {
	var clean strings.Builder
	for _, r := range norm.NFKD.String(text) {
		if !unicode.Is(unicode.Mn, r) {
			clean.WriteRune(r)
		}
	}
	caser := sortFolder.Get().(*cases.Caser) // a Caser can't be shared while in use
	lowered := strings.ReplaceAll(caser.String(clean.String()), "&", " and ")
	sortFolder.Put(caser)
	var b strings.Builder
	space := true
	for _, r := range lowered {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	s := strings.TrimSpace(b.String())
	if dropArticle {
		s = strings.TrimPrefix(s, "the ")
	}
	if s == "" {
		return strings.ToLower(strings.TrimSpace(text))
	}
	return s
}

func sortPrimaryArtist(artist string) string {
	s := strings.TrimSpace(reSortFeatTail.ReplaceAllString(reSortFeatParen.ReplaceAllString(artist, ""), ""))
	return FirstNonEmpty(s, strings.TrimSpace(artist))
}

func sortLeadArtist(artist string) string {
	s := sortPrimaryArtist(artist)
	return FirstNonEmpty(strings.TrimSpace(reSortJoiners.Split(s, 2)[0]), s)
}

func sortArtistKey(artist string) string { return sortFold(sortPrimaryArtist(artist), true) }

// sortTitleKey keys a song: featured artists and "remastered" notes don't make a different song,
// but "Live", "Extended Mix", "Acoustic" and the like do and are kept.
func sortTitleKey(title string) string {
	s := reSortFeatTail.ReplaceAllString(reSortFeatParen.ReplaceAllString(title, ""), "")
	s = reSortRemasterDash.ReplaceAllString(reSortRemasterParen.ReplaceAllString(s, ""), "")
	return sortFold(FirstNonEmpty(strings.TrimSpace(s), title), false)
}

func sortAlbumKey(album string) string {
	return sortFold(FirstNonEmpty(strings.TrimSpace(reSortSingleMark.ReplaceAllString(album, "")), album), false)
}

func sortStripEdition(album string) string {
	s := reSortSingleMark.ReplaceAllString(album, "")
	for {
		stripped := reSortEdition.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = stripped
	}
	return FirstNonEmpty(strings.TrimSpace(s), strings.TrimSpace(album))
}

func sortIsSingleTitle(album string) bool { return reSortSingleMark.MatchString(album) }

func sortIsVarious(name string) bool { return name != "" && sortVarious[sortFold(name, false)] }

// sortIsPlaceholder is for tags that say nothing: "Unknown Artist", "Track 05", a bare URL, "-".
func sortIsPlaceholder(value string) bool {
	key := sortFold(value, false)
	alnum := false
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			alnum = true
			break
		}
	}
	if !alnum || sortPlaceholders[key] || reSortTrackHolder.MatchString(key) {
		return true
	}
	trimmed := strings.TrimSpace(value)
	return reSortURL.FindString(trimmed) == trimmed
}

// sortSimilar is fuzzy equality of two keys; short keys must match exactly.
func sortSimilar(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if min(len([]rune(a)), len([]rune(b))) < 8 {
		return false
	}
	return ratio(a, b) >= 88
}

// sortSameArtist is true for "Linkin Park" and "linkin park", "The Doors" and "Doors", "Linkin
// Park & Jay-Z" and "Linkin Park".
func sortSameArtist(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ka, kb := sortArtistKey(a), sortArtistKey(b)
	return sortSimilar(ka, kb) || sortFold(sortLeadArtist(a), true) == kb || ka == sortFold(sortLeadArtist(b), true)
}

// sortSafeName makes a name into one path component valid on Linux and Windows filesystems.
func sortSafeName(name, fallback string) string {
	var b strings.Builder
	for _, r := range norm.NFC.String(name) {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(reSortSpaces.ReplaceAllString(sortIllegal.Replace(b.String()), " "))
	if strings.HasPrefix(s, "...") {
		s = "…" + s[3:] // "...And Justice for All" must not become a hidden folder
	}
	s = strings.TrimRight(strings.TrimLeft(s, "."), ". ")
	if sortReserved[strings.ToLower(strings.TrimSpace(strings.SplitN(s, ".", 2)[0]))] {
		s = "_" + s
	}
	if len(s) > 200 {
		cut := 200
		for cut > 0 && (s[cut]&0xC0) == 0x80 {
			cut--
		}
		s = strings.TrimRight(s[:cut], ". ")
	}
	return FirstNonEmpty(s, fallback)
}

func sortSmartCase(text string) string {
	if text == "" || text != strings.ToLower(text) || !strings.ContainsFunc(text, unicode.IsLetter) {
		return text
	}
	var b strings.Builder
	start := true
	for _, r := range text {
		if start {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(r)
		}
		start = r == ' ' || r == '\t' || r == '(' || r == '[' || r == '/' || r == '-'
	}
	return b.String()
}

// sortSplitNumber splits "01 - Title" (0, 1, "Title"), "1-03 Title" (1, 3, "Title");
// "99 Problems" stays whole.
func sortSplitNumber(text string) (disc, track int, rest string) {
	if m := reSortDiscTrack.FindStringSubmatch(text); m != nil {
		d, _ := strconv.Atoi(m[1])
		t, _ := strconv.Atoi(m[2])
		return d, t, m[3]
	}
	// A number counts as a track number only with a separator after it, or when zero-padded,
	// so titles such as "99 Problems" survive.
	var num, tail string
	if m := reSortNumDot.FindStringSubmatch(text); m != nil {
		num, tail = m[1], m[2]
	} else if m := reSortNumDash.FindStringSubmatch(text); m != nil && (m[2] != "" || !unicode.IsDigit(rune(m[3][0]))) {
		num, tail = m[1], m[3]
	} else if m := reSortNumZero.FindStringSubmatch(text); m != nil {
		num, tail = m[1], m[2]
	}
	if num != "" {
		n, _ := strconv.Atoi(num)
		if n > 100 && n%100 != 0 { // "101 Title" is disc 1, track 1
			return n / 100, n % 100, tail
		}
		return 0, n, tail
	}
	return 0, 0, text
}

type sortNameHint struct {
	artist, album, title string
	track, disc          int
}

// sortParseFilename guesses from names like "01 - Title", "Artist - Title",
// "Artist - Album - 03 - Title", "1-04. Title" or scene-style "01-artist_name-title-grp".
func sortParseFilename(stem string) sortNameHint {
	scene := !strings.Contains(stem, " ") && strings.Contains(stem, "_")
	text := stem
	if scene {
		text = strings.ReplaceAll(stem, "_", " ")
	}
	text = reSortJunkBracket.ReplaceAllString(reSortURL.ReplaceAllString(text, " "), " ")
	text = strings.Trim(strings.TrimSpace(reSortSpaces.ReplaceAllString(text, " ")), " -_")
	sep := reSortNameSep
	if scene {
		sep = reSortSceneSep
	}
	var h sortNameHint
	var rest string
	h.disc, h.track, rest = sortSplitNumber(text)
	var parts []string
	for _, p := range sep.Split(rest, -1) {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if scene && len(parts) >= 3 {
		parts = parts[:len(parts)-1] // trailing release-group tag
	}
	if len(parts) == 0 {
		return h
	}
	var before []string
	after := parts
	if h.track == 0 {
		// "Artist - 03 - Title", "Artist - Album - 03 - Title", "Artist - 03. Title"
		for i := 1; i < len(parts); i++ {
			if reSortAllDigits.MatchString(parts[i]) && i+1 < len(parts) {
				h.track, _ = strconv.Atoi(parts[i])
				before, after = parts[:i], parts[i+1:]
				break
			}
			if d, tr, remainder := sortSplitNumber(parts[i]); tr != 0 {
				h.disc, h.track = d, tr
				before, after = parts[:i], append([]string{remainder}, parts[i+1:]...)
				break
			}
		}
	}
	switch {
	case len(before) > 0:
		h.artist, h.album, h.title = before[0], strings.Join(before[1:], " - "), strings.Join(after, " - ")
	case len(parts) == 1:
		h.title = parts[0]
	default:
		h.artist, h.title = parts[0], strings.Join(parts[1:], " - ")
	}
	h.artist, h.album, h.title = sortSmartCase(h.artist), sortSmartCase(h.album), sortSmartCase(h.title)
	return h
}

type sortFolderHint struct {
	artist, album, year, parentArtist string
	disc                              int
	albumLike, singles                bool
}

func sortCleanDir(name string) string {
	s := name
	if !strings.Contains(name, " ") {
		s = strings.ReplaceAll(name, "_", " ")
	}
	s = reSortDirJunk.ReplaceAllString(s, "")
	return strings.Trim(strings.TrimSpace(reSortSpaces.ReplaceAllString(s, " ")), " -")
}

// sortParseFolders reads hints from the folders between the source root and the file, like
// ["Linkin Park", "2000 - Hybrid Theory", "CD1"] or ["Deftones - White Pony (2000) [FLAC]"].
func sortParseFolders(dirs []string) sortFolderHint {
	var h sortFolderHint
	if len(dirs) > 0 {
		if m := reSortDiscDir.FindStringSubmatch(strings.TrimSpace(dirs[len(dirs)-1])); m != nil {
			h.disc, _ = strconv.Atoi(m[1])
			dirs = dirs[:len(dirs)-1]
		}
	}
	if len(dirs) == 0 {
		return h
	}
	name := sortCleanDir(dirs[len(dirs)-1])
	if m := reSortDiscSuffix.FindStringSubmatchIndex(name); m != nil {
		if h.disc == 0 {
			h.disc, _ = strconv.Atoi(name[m[2]:m[3]])
		}
		name = strings.Trim(strings.TrimSpace(name[:m[0]]), " -")
	}
	if sortFold(name, false) == "singles" && len(dirs) >= 2 {
		h.singles = true
		h.parentArtist = sortCleanDir(dirs[len(dirs)-2])
		return h
	}
	if name == "" || sortGenericDirs[sortFold(name, false)] {
		return h
	}
	matched := false
	for _, re := range reSortFolderPatterns {
		m := re.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		for i, group := range re.SubexpNames() {
			switch group {
			case "artist":
				h.artist = strings.TrimSpace(m[i])
			case "album":
				h.album = strings.TrimSpace(m[i])
			case "year":
				h.year = m[i]
			}
		}
		h.albumLike, matched = true, true
		break
	}
	if !matched {
		h.album = name
	}
	if len(dirs) >= 2 {
		if parent := sortCleanDir(dirs[len(dirs)-2]); parent != "" && !sortGenericDirs[sortFold(parent, false)] {
			h.parentArtist = parent
		}
	}
	return h
}
