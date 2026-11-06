package main

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

// The native tag readers must agree with ffprobe.
func TestReadTagsMatchesFFprobe(t *testing.T) {
	tools := needFFmpeg(t)
	dir := t.TempDir()
	cases := []struct {
		name  string
		args  []string
		codec string
		lossy bool
	}{
		{"cbr.mp3", []string{"-c:a", "libmp3lame", "-b:a", "192k"}, "mp3", true},
		{"vbr.mp3", []string{"-c:a", "libmp3lame", "-q:a", "0"}, "mp3", true},
		{"v23.mp3", []string{"-c:a", "libmp3lame", "-b:a", "128k", "-id3v2_version", "3"}, "mp3", true},
		{"cd.flac", []string{"-c:a", "flac"}, "flac", false},
		{"hires.flac", []string{"-c:a", "flac", "-sample_fmt", "s32", "-ar", "96000"}, "flac", false},
		{"aac.m4a", []string{"-c:a", "aac", "-b:a", "256k"}, "aac", true},
		{"alac.m4a", []string{"-c:a", "alac"}, "alac", false},
		{"vorbis.ogg", []string{"-c:a", "libvorbis", "-q:a", "6"}, "vorbis", true},
		{"opus.opus", []string{"-c:a", "libopus", "-b:a", "160k"}, "opus", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.name)
			args := []string{"-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "anoisesrc=d=12:c=pink:a=0.3",
				"-metadata", "title=Ünïcode Title (Live)", "-metadata", "artist=Artist One; Artist Two", "-metadata", "album=The Album",
				"-metadata", "album_artist=Album Artist", "-metadata", "ISRC=USAB12345678", "-metadata", "track=7/12"}
			args = append(append(args, c.args...), p)
			if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("%v %s", err, out)
			}
			got, err := readTags(p)
			if err != nil {
				t.Fatal(err)
			}
			want, err := probeTags(context.Background(), tools, p)
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "Ünïcode Title (Live)" || got.Album != "The Album" || got.AlbumArtist != "Album Artist" || got.TrackNumber != 7 {
				t.Errorf("tags: %+v", got)
			}
			if got.Artist != "Artist One; Artist Two" {
				t.Errorf("artist %q", got.Artist)
			}
			if c.name != "vorbis.ogg" && c.name != "opus.opus" && c.codec != "alac" && c.codec != "aac" && got.ISRC != "USAB12345678" {
				t.Errorf("ISRC %q", got.ISRC)
			}
			if math.Abs(got.Duration-want.Duration) > 0.15 || math.Abs(got.Duration-12) > 0.2 {
				t.Errorf("duration %.3f, ffprobe %.3f", got.Duration, want.Duration)
			}
			if got.Q.Codec != c.codec || got.Q.Lossless == c.lossy {
				t.Errorf("quality %+v", got.Q)
			}
			if c.lossy && want.Q.Kbps > 0 && math.Abs(float64(got.Q.Kbps-want.Q.Kbps)) > float64(want.Q.Kbps)*0.12 {
				t.Errorf("bitrate %d, ffprobe %d", got.Q.Kbps, want.Q.Kbps)
			}
			if !c.lossy && (got.Q.BitDepth != want.Q.BitDepth || got.Q.SampleRate != want.Q.SampleRate) {
				t.Errorf("lossless %+v, ffprobe %+v", got.Q, want.Q)
			}
		})
	}
}

func TestGuessFromPath(t *testing.T) {
	for _, c := range []struct{ path, artist, title, album string }{
		{"/m/Burial/Untrue/02 - Archangel.mp3", "Burial", "Archangel", "Untrue"},
		{"/m/Burial/2007 - Untrue/02. Archangel.flac", "Burial", "Archangel", "Untrue"},
		{"/m/Misc/Interpol - NYC.mp3", "Interpol", "NYC", "Misc"},
		{"/m/Interpol - Antics/1-03 Narc.mp3", "Interpol", "Narc", "Interpol - Antics"},
	} {
		f := &LibFile{Path: c.path}
		guessFromPath(f)
		if f.Artist != c.artist || f.Title != c.title || f.Album != c.album {
			t.Errorf("%s: %q / %q / %q", c.path, f.Artist, f.Title, f.Album)
		}
	}
}

func TestQualityThresholds(t *testing.T) {
	flac := Quality{Codec: "flac", Lossless: true, BitDepth: 16, SampleRate: 44100}
	hires := Quality{Codec: "flac", Lossless: true, BitDepth: 24, SampleRate: 96000}
	v0 := Quality{Codec: "mp3", Kbps: 245}
	mp3320 := Quality{Codec: "mp3", Kbps: 320}
	opus := Quality{Codec: "opus", Kbps: 130}
	aac := Quality{Codec: "aac", Kbps: 256}
	cases := []struct {
		th   Threshold
		q    Quality
		want bool
	}{
		{"lossless", flac, true}, {"lossless", mp3320, false},
		{"320", mp3320, true}, {"320", Quality{Codec: "mp3", Kbps: 310}, true}, {"320", v0, false}, {"320", aac, true},
		{"256", v0, true}, {"256", opus, false}, {"192", opus, true},
		{"any", Quality{}, true}, {"128", Quality{}, false},
	}
	for _, c := range cases {
		if got := c.th.meets(c.q); got != c.want {
			t.Errorf("%s meets %s: %v", c.th, c.q, got)
		}
	}
	if !better(flac, mp3320) || better(mp3320, flac) || !better(hires, flac) || better(flac, flac) {
		t.Error("lossless ordering")
	}
	if better(Quality{Codec: "mp3", Kbps: 256}, v0) || !better(mp3320, Quality{Codec: "mp3", Kbps: 192}) || !better(opus, Quality{Codec: "mp3", Kbps: 128}) {
		t.Error("lossy ordering")
	}
	if !mightBeBetter(Quality{}, mp3320) || mightBeBetter(opus, mp3320) {
		t.Error("estimates")
	}
	if s := hires.String(); s != "FLAC 24/96" {
		t.Errorf("String: %s", s)
	}
}
