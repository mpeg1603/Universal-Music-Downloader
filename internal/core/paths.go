package core

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const defaultTemplate = "{album_artist}/{album}/{tracknum} - {title}"

var (
	reIllegal    = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
	reLeadDash   = regexp.MustCompile(`^\s*-\s+`)
	reservedName = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
)

func init() {
	for i := 1; i <= 9; i++ {
		reservedName[fmt.Sprintf("COM%d", i)] = true
		reservedName[fmt.Sprintf("LPT%d", i)] = true
	}
}

// sanitize makes one path component valid on Linux, macOS and Windows.
func sanitize(part string) string {
	part = strings.Trim(strings.Join(strings.Fields(reIllegal.ReplaceAllString(part, "_")), " "), " .")
	if reservedName[strings.ToUpper(part)] {
		part += "_"
	}
	if len(part) > 180 {
		cut := 180
		for cut > 0 && !utf8.RuneStart(part[cut]) {
			cut--
		}
		part = strings.TrimRight(part[:cut], " .")
	}
	if part == "" {
		return "_"
	}
	return part
}

func relativePath(t *Track, template, ext string) string {
	tracknum := ""
	if t.TrackNumber > 0 {
		if t.DiscNumber > 1 {
			tracknum = fmt.Sprintf("%d-", t.DiscNumber)
		}
		tracknum += fmt.Sprintf("%02d", t.TrackNumber)
	}
	album := t.Album
	if album == "" {
		album = "Unknown Album"
	}
	year := "0000"
	if len(t.ReleaseDate) >= 4 {
		year = t.ReleaseDate[:4]
	}
	slash := strings.NewReplacer("/", "_", `\`, "_")
	rendered := strings.NewReplacer(
		"{artist}", slash.Replace(t.Artists[0]),
		"{artists}", slash.Replace(t.ArtistStr()),
		"{album_artist}", slash.Replace(t.AlbumArtistStr()),
		"{album}", slash.Replace(album),
		"{title}", slash.Replace(t.Title),
		"{tracknum}", tracknum,
		"{year}", year,
	).Replace(template)
	var parts []string
	for _, p := range strings.FieldsFunc(rendered, func(r rune) bool { return r == '/' || r == '\\' }) {
		// An empty field leaves a dangling separator, e.g. " - Title" when there's no number.
		if p = reLeadDash.ReplaceAllString(p, ""); strings.TrimSpace(p) != "" {
			parts = append(parts, sanitize(p))
		}
	}
	if len(parts) == 0 {
		parts = []string{sanitize(t.Title)}
	}
	parts[len(parts)-1] += ext
	return strings.Join(parts, "/")
}
