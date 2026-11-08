package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var Formats = []string{"original", "flac", "mp3", "opus", "m4a"}

var extForFormat = map[string]string{"mp3": ".mp3", "opus": ".opus", "m4a": ".m4a", "flac": ".flac"}
var extForCodec = map[string]string{"mp3": ".mp3", "aac": ".m4a", "alac": ".m4a", "opus": ".opus", "vorbis": ".ogg", "flac": ".flac"}
var copyable = map[string][]string{".mp3": {"mp3"}, ".m4a": {"aac", "alac"}, ".opus": {"opus"}, ".ogg": {"vorbis"}, ".flac": {"flac"}}
var encodeArgs = map[string][]string{
	".mp3":  {"-c:a", "libmp3lame", "-q:a", "0"},
	".m4a":  {"-c:a", "aac", "-b:a", "256k"},
	".opus": {"-c:a", "libopus", "-b:a", "192k"},
	".ogg":  {"-c:a", "libvorbis", "-q:a", "6"},
	".flac": {"-c:a", "flac"},
}

func targetExtension(format, codec string) string {
	if format == "original" {
		if ext, ok := extForCodec[codec]; ok {
			return ext
		}
		return ".flac"
	}
	return extForFormat[format]
}

// probe returns the audio codec, duration (seconds) and quality of a media file.
func probe(ctx context.Context, tools *Tools, path string) (string, float64, Quality, error) {
	if tools.FFprobe == "" {
		return "", 0, Quality{}, errNoFFmpeg()
	}
	out, err := command(ctx, tools.FFprobe, "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,bits_per_raw_sample,bits_per_sample,bit_rate,sample_fmt:format=duration,bit_rate",
		"-of", "json", path).Output()
	if err != nil {
		return "", 0, Quality{}, failf("ffprobe couldn't read the downloaded file")
	}
	var data ffprobeOutput
	if err := json.Unmarshal(out, &data); err != nil || len(data.Streams) == 0 {
		return "", 0, Quality{}, failf("the downloaded file contains no audio")
	}
	d, q := data.quality()
	return data.Streams[0].CodecName, d, q, nil
}

func ffmetaEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", `\`+"\n").Replace(s)
}

// flacPicture builds the METADATA_BLOCK_PICTURE value Ogg/Opus players read cover art from.
func flacPicture(img []byte) string {
	mime := "image/jpeg"
	if bytes.HasPrefix(img, []byte("\x89PNG")) {
		mime = "image/png"
	}
	var b bytes.Buffer
	put := func(v uint32) { binary.Write(&b, binary.BigEndian, v) }
	put(3) // front cover
	put(uint32(len(mime)))
	b.WriteString(mime)
	put(5)
	b.WriteString("Cover")
	put(0) // width, height, depth, colours: unknown
	put(0)
	put(0)
	put(0)
	put(uint32(len(img)))
	b.Write(img)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

// convertAndTag writes src's audio to dest (format chosen by dest's extension), copying the
// stream when possible, and tags it with the track's metadata and cover.
func convertAndTag(ctx context.Context, tools *Tools, src, dest, codec string, t *Track, cover string) error {
	ext := strings.ToLower(filepath.Ext(dest))
	tags := []string{
		"title=" + t.Title,
		"artist=" + strings.Join(t.Artists, "; "),
		"album_artist=" + t.AlbumArtistStr(),
		"album=" + t.Album,
	}
	if t.TrackNumber > 0 {
		n := strconv.Itoa(t.TrackNumber)
		if t.TotalTracks > 0 {
			n += "/" + strconv.Itoa(t.TotalTracks)
		}
		tags = append(tags, "track="+n, "disc="+strconv.Itoa(max(t.DiscNumber, 1)))
	}
	if t.ReleaseDate != "" {
		tags = append(tags, "date="+t.ReleaseDate)
	}
	if t.ISRC != "" {
		tags = append(tags, "ISRC="+t.ISRC)
	}
	if t.SpotifyURL != "" {
		tags = append(tags, "comment="+t.SpotifyURL)
	}
	var img []byte
	if cover != "" {
		img, _ = os.ReadFile(cover)
	}
	ogg := ext == ".opus" || ext == ".ogg"
	if ogg && img != nil {
		tags = append(tags, "METADATA_BLOCK_PICTURE="+flacPicture(img))
	}
	var meta strings.Builder
	meta.WriteString(";FFMETADATA1\n")
	for _, kv := range tags {
		k, v, _ := strings.Cut(kv, "=")
		meta.WriteString(k + "=" + ffmetaEscape(v) + "\n")
	}
	metaFile := dest + ".ffmeta"
	if err := os.WriteFile(metaFile, []byte(meta.String()), 0o644); err != nil {
		return err
	}
	defer os.Remove(metaFile)

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", src, "-f", "ffmetadata", "-i", metaFile}
	if img != nil && !ogg {
		args = append(args, "-i", cover, "-map", "0:a:0", "-map", "2:v:0", "-c:v", "copy", "-disposition:v:0", "attached_pic")
	} else {
		args = append(args, "-map", "0:a:0")
	}
	args = append(args, "-map_metadata", "1", "-map_metadata:s:a:0", "-1")
	codecArgs := encodeArgs[ext]
	for _, c := range copyable[ext] {
		if c == codec {
			codecArgs = []string{"-c:a", "copy"}
		}
	}
	args = append(args, codecArgs...)
	switch ext {
	case ".mp3":
		args = append(args, "-id3v2_version", "3", "-write_id3v1", "0")
	case ".m4a":
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, dest)
	if tools.FFmpeg == "" {
		return errNoFFmpeg()
	}
	var stderr bytes.Buffer
	cmd := command(ctx, tools.FFmpeg, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cover != "" && ctx.Err() == nil {
			return convertAndTag(ctx, tools, src, dest, codec, t, "") // unusual cover image format
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return failf("ffmpeg couldn't convert the file: %s", msg)
	}
	return nil
}

// CoverCache downloads each album cover once per run.
type CoverCache struct {
	dir     string
	http    *HTTP
	mu      sync.Mutex
	entries map[string]*coverEntry
}

type coverEntry struct {
	once sync.Once
	path string
}

func newCoverCache(dir string) *CoverCache {
	return &CoverCache{dir: dir, http: newHTTP("cover art", 0), entries: map[string]*coverEntry{}}
}

func (c *CoverCache) get(ctx context.Context, url string) string {
	if url == "" {
		return ""
	}
	c.mu.Lock()
	e := c.entries[url]
	if e == nil {
		e = &coverEntry{}
		c.entries[url] = e
	}
	c.mu.Unlock()
	e.once.Do(func() {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		status, body, err := c.http.get(cctx, url)
		if err != nil || status != 200 || !strings.HasPrefix(http.DetectContentType(body), "image/") {
			return
		}
		sum := sha1.Sum([]byte(url))
		p := filepath.Join(c.dir, hex.EncodeToString(sum[:8])+".img")
		if os.MkdirAll(c.dir, 0o755) == nil && os.WriteFile(p, body, 0o644) == nil {
			e.path = p
		}
	})
	return e.path
}
