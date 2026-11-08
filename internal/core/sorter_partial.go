package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Sort & merge leaves alone anything that isn't finished: this app's downloads, and those of
// a browser, torrent client or Soulseek client writing into the same folders.

// sortPartialMarks are the endings of unfinished downloads. Besides the file with such a name,
// a file of the same name without the ending is unfinished too ("song.flac" beside
// "song.flac.aria2").
var sortPartialMarks = setOf(partialSuffix, ".partial", ".crdownload", ".download", ".tmp", ".temp", ".!qb", ".!ut", ".!bt",
	".aria2", ".ytdl", ".incomplete", ".opdownload", ".filepart", ".bc!", ".umd-sort-tmp")

// reSortPartialName: "song.temp.m4a" (yt-dlp), "song.part.flac"; and Nicotine+'s unfinished
// downloads, "INCOMPLETE" and a checksum before the name.
var reSortPartialName = regexp.MustCompile(`(?i)\.(?:part|partial|tmp|temp)\.[^.]+$|^(?-i:INCOMPLETE)[0-9a-fA-F]{6,}`)

// sortSettle: a file written to this recently may still be growing. It's sorted next time.
var sortSettle = 2 * time.Minute

// sortLooksPartial tells unfinished downloads by their names.
func sortLooksPartial(name string) bool {
	return strings.HasPrefix(name, ".") || reSortPartialName.MatchString(name)
}

func sortStillWritten(st os.FileInfo) bool {
	age := time.Since(st.ModTime())
	return age >= 0 && age < sortSettle
}

// sortPartialCheck is the last look at a file before it's moved: the folder may have changed
// since the plan was made. It keeps the listing of the last folder, moves being in folder order.
type sortPartialCheck struct {
	dir   string
	names map[string]bool
}

func (c *sortPartialCheck) unfinished(path string) bool {
	base := filepath.Base(path)
	if sortLooksPartial(base) {
		return true
	}
	if dir := filepath.Dir(path); dir != c.dir || c.names == nil {
		c.dir, c.names = dir, map[string]bool{}
		if list, err := os.ReadDir(dir); err == nil {
			for _, e := range list {
				c.names[strings.ToLower(e.Name())] = true
			}
		}
	}
	base = strings.ToLower(base)
	for mark := range sortPartialMarks {
		if c.names[base+mark] {
			return true
		}
	}
	return false
}
