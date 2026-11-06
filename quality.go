package main

import (
	"fmt"
	"strings"
)

// Audio quality, used to decide whether a copy in your library is good enough and whether a
// download would be an improvement on it.

type Quality struct {
	Codec      string `json:"codec,omitempty"` // ffprobe-style name: mp3, aac, opus, vorbis, flac, alac, ...
	Lossless   bool   `json:"lossless,omitempty"`
	Kbps       int    `json:"kbps,omitempty"` // average bitrate; 0 when unknown
	SampleRate int    `json:"sample_rate,omitempty"`
	BitDepth   int    `json:"bit_depth,omitempty"`
}

var losslessCodecs = map[string]bool{"flac": true, "alac": true, "ape": true, "wavpack": true, "tta": true,
	"pcm_s16le": true, "pcm_s24le": true, "pcm_s32le": true, "pcm_s16be": true, "pcm_s24be": true,
	"pcm_s32be": true, "pcm_f32le": true, "pcm_u8": true, "mlp": true, "truehd": true, "wav": true, "aiff": true}

// codecEfficiency converts a lossy codec's bitrate into roughly equivalent MP3 kbps: Opus at
// 160 kbps sounds about as good as MP3 at 256.
var codecEfficiency = map[string]float64{"mp3": 1.0, "aac": 1.25, "vorbis": 1.25, "opus": 1.6, "wmav2": 0.9, "wmav1": 0.8, "mp2": 0.8}

func isLosslessCodec(codec string) bool {
	return losslessCodecs[codec] || strings.HasPrefix(codec, "pcm_")
}

func newQuality(codec string, kbps, sampleRate, bitDepth int) Quality {
	codec = strings.ToLower(codec)
	return Quality{Codec: codec, Lossless: isLosslessCodec(codec), Kbps: kbps, SampleRate: sampleRate, BitDepth: bitDepth}
}

func (q Quality) known() bool { return q.Codec != "" || q.Kbps > 0 }

// Score orders qualities: lossless above any lossy file, hi-res above CD quality; lossy files
// by MP3-equivalent bitrate.
func (q Quality) Score() float64 {
	if q.Lossless {
		s := 10000.0
		if q.BitDepth > 16 {
			s += float64(q.BitDepth-16) * 100
		}
		if q.SampleRate > 48000 {
			s += float64(q.SampleRate) / 1000
		}
		return s
	}
	f, ok := codecEfficiency[q.Codec]
	if !ok {
		f = 1
	}
	return float64(q.Kbps) * f
}

func (q Quality) String() string {
	if !q.known() {
		return "?"
	}
	name := codecDisplay(q.Codec)
	if q.Lossless {
		if q.BitDepth > 0 && q.SampleRate > 0 {
			return fmt.Sprintf("%s %d/%s", name, q.BitDepth, khz(q.SampleRate))
		}
		return name
	}
	if q.Kbps > 0 {
		return fmt.Sprintf("%s %d", name, q.Kbps)
	}
	return name
}

func khz(rate int) string {
	if rate%1000 == 0 {
		return fmt.Sprint(rate / 1000)
	}
	return fmt.Sprintf("%.1f", float64(rate)/1000)
}

func codecDisplay(c string) string {
	switch c {
	case "mp3", "aac", "flac", "alac", "ape":
		return strings.ToUpper(c)
	case "opus":
		return "Opus"
	case "vorbis":
		return "Vorbis"
	case "wavpack":
		return "WavPack"
	case "wmav1", "wmav2":
		return "WMA"
	case "":
		return "?"
	}
	if strings.HasPrefix(c, "pcm_") {
		return "PCM"
	}
	return c
}

// A Threshold is the quality at which a library copy counts as good enough: such tracks are not
// downloaded again.
type Threshold string

var thresholds = []struct {
	Name  Threshold
	Label string
	Min   float64 // minimum score; lossy files get 8% slack for VBR averages
}{
	{"lossless", "lossless only (FLAC, ALAC, WAV, ...)", 10000},
	{"320", "MP3 320 kbps or equivalent (AAC/Vorbis 256, Opus 192)", 300},
	{"256", "MP3 256 / V0 or equivalent", 225},
	{"192", "MP3 192 or equivalent", 175},
	{"128", "MP3 128 or equivalent", 115},
	{"any", "any copy counts", 0},
}

func thresholdNames() []string {
	var out []string
	for _, t := range thresholds {
		out = append(out, string(t.Name))
	}
	return out
}

func validThreshold(s string) bool {
	for _, t := range thresholds {
		if string(t.Name) == s {
			return true
		}
	}
	return false
}

func (t Threshold) min() float64 {
	for _, x := range thresholds {
		if x.Name == t {
			return x.Min
		}
	}
	return 10000
}

// meets reports whether q is good enough. An unknown quality counts as good enough only for "any".
func (t Threshold) meets(q Quality) bool {
	if t == "any" {
		return true
	}
	if !q.known() {
		return false
	}
	return q.Score() >= t.min()
}

// better reports whether candidate is a real improvement on existing: lossless over lossy, or a
// clearly higher bitrate (15% more, so a 256 kbps file doesn't replace a 245 kbps one).
func better(candidate, existing Quality) bool {
	if !existing.known() {
		return candidate.known()
	}
	if !candidate.known() {
		return false
	}
	if candidate.Lossless != existing.Lossless {
		return candidate.Lossless
	}
	if candidate.Lossless {
		return candidate.Score() > existing.Score()
	}
	return candidate.Score() > existing.Score()*1.15
}

// mightBeBetter is better for a candidate whose quality is only estimated: unknown counts as
// worth a try, since the file is checked again after downloading.
func mightBeBetter(estimate, existing Quality) bool {
	if !estimate.known() {
		return true
	}
	return better(estimate, existing)
}
