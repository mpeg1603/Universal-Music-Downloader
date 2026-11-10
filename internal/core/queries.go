package core

import (
	"context"
	"regexp"
	"strings"
)

// The searches to try for a track on sites that list single songs (YouTube, SoundCloud,
// Bandcamp, …), most promising first. One search for "Artist Title" misses a lot: the site
// may credit the album artist, or list the song under another credited artist, or name a
// remix or live version only in the full title. Each next search is only made when the
// ones before found nothing the matcher accepts.

// reVersionWords finds the words of a title that name a version: "The Midnight Remix",
// "Live at Wembley", "Radio Edit".
var reVersionWords = regexp.MustCompile(`(?i)\b(?:re-?mix|mix|edit|version|live|acoustic|instrumental|demo|dub|rework|vip|bootleg|cover|session|unplugged)\b`)

// versionedTitle is the title with its version part ("Missing Out The Midnight Remix") when
// the title has one that the plain search title leaves out; "" otherwise.
func versionedTitle(title string) string {
	plain := searchTitle(title)
	full := reFeatBracket.ReplaceAllString(title, " ")
	full = strings.TrimSpace(reFeatTail.ReplaceAllString(full, ""))
	full = strings.NewReplacer("(", " ", ")", " ", "[", " ", "]", " ", " - ", " ").Replace(full)
	full = strings.Join(strings.Fields(full), " ")
	if strings.EqualFold(full, plain) || !reVersionWords.MatchString(strings.TrimPrefix(strings.ToLower(full), strings.ToLower(plain))) {
		return ""
	}
	return full
}

func variousArtists(name string) bool {
	switch normalize(name) {
	case "various artists", "various", "va", "unknown artist", "":
		return true
	}
	return false
}

// songQueries lists up to max searches for t: artist + title (with its version, if any), the
// album artist + title, title + the other credited artists, title + album.
func songQueries(t *Track, max int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(parts ...string) {
		q := strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
		if k := strings.ToLower(q); q != "" && !seen[k] && len(out) < max {
			seen[k] = true
			out = append(out, q)
		}
	}
	title := searchTitle(t.Title)
	artist := ""
	if len(t.Artists) > 0 {
		artist = t.Artists[0]
	}
	if v := versionedTitle(t.Title); v != "" {
		add(artist, v)
	}
	add(artist, title)
	if symbolTitle(t.Title) != "" && t.Album != "" {
		// Search engines drop "*" or "...": the album narrows "M83 *" down to the song.
		add(artist, baseAlbum(t.Album), title)
	}
	if aa := t.AlbumArtist; aa != "" && !strings.EqualFold(aa, artist) && !variousArtists(aa) {
		add(aa, title)
	}
	if len(t.Artists) > 1 {
		add(title, strings.Join(t.Artists[1:min(len(t.Artists), 3)], " "))
	}
	if album := baseAlbum(t.Album); album != "" && !strings.EqualFold(normalize(album), normalize(title)) {
		add(title, album)
	}
	return out
}

// searchUntilFound runs the searches in order until one finds a result the matcher accepts,
// and returns everything found so far (each result once, by URL). An error on the first
// search is returned; on a later one, what was found before is.
func searchUntilFound(ctx context.Context, t *Track, queries []string, search func(q string) ([]Candidate, error)) ([]Candidate, error) {
	var out []Candidate
	seen := map[string]bool{}
	for i, q := range queries {
		cands, err := search(q)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			if i == 0 || isBlocked(err) || isBusy(err) || len(out) == 0 {
				return nil, err // busy: not an answer, so the track comes back for the rest
			}
			debugf("search %q failed (%v); going on with the %d results found before", q, err, len(out))
			return out, nil
		}
		for _, c := range cands {
			if k := c.URL + "\x00" + c.Ref; !seen[k] {
				seen[k] = true
				out = append(out, c)
			}
		}
		if accepted, _ := rank(t, out); len(accepted) > 0 {
			if i > 0 {
				debugf("%s: found with the search %q", t, q)
			}
			return out, nil
		}
	}
	return out, nil
}
