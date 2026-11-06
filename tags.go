package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Reading tags and stream details (codec, length, bitrate) of audio files without running
// ffprobe, which would take far too long on a large library. FLAC, MP3, MP4/M4A and Ogg
// (Vorbis/Opus) are read here; everything else falls back to ffprobe. Files are only ever
// opened for reading.

type tagInfo struct {
	Title, Artist, AlbumArtist, Album, ISRC string
	TrackNumber                             int
	Duration                                float64
	Q                                       Quality
}

var errUnsupported = errors.New("unsupported format")

func readTags(path string) (*tagInfo, error) {
	f, err := os.Open(path) // read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var info *tagInfo
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac":
		info, err = readFLAC(f, st.Size())
	case ".mp3":
		info, err = readMP3(f, st.Size())
	case ".m4a", ".mp4", ".m4b", ".alac", ".aac":
		info, err = readMP4(f, st.Size())
	case ".ogg", ".oga", ".opus":
		info, err = readOgg(f, st.Size())
	default:
		return nil, errUnsupported
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

// --- Vorbis comments (FLAC, Ogg) --------------------------------------------------------------

func parseVorbisComment(b []byte, info *tagInfo) {
	r := bytes.NewReader(b)
	var n uint32
	if binary.Read(r, binary.LittleEndian, &n) != nil || int64(n) > int64(r.Len()) {
		return
	}
	r.Seek(int64(n), io.SeekCurrent)
	var count uint32
	if binary.Read(r, binary.LittleEndian, &count) != nil {
		return
	}
	var artists, albumArtists []string
	for i := uint32(0); i < count; i++ {
		if binary.Read(r, binary.LittleEndian, &n) != nil || int64(n) > int64(r.Len()) {
			return
		}
		buf := make([]byte, n)
		io.ReadFull(r, buf)
		k, v, ok := strings.Cut(string(buf), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToUpper(k) {
		case "TITLE":
			info.Title = firstNonEmpty(info.Title, v)
		case "ARTIST":
			artists = append(artists, v)
		case "ALBUMARTIST", "ALBUM ARTIST", "ALBUM_ARTIST":
			albumArtists = append(albumArtists, v)
		case "ALBUM":
			info.Album = firstNonEmpty(info.Album, v)
		case "ISRC":
			info.ISRC = firstNonEmpty(info.ISRC, v)
		case "TRACKNUMBER":
			if info.TrackNumber == 0 {
				info.TrackNumber = leadingInt(v)
			}
		}
	}
	info.Artist = strings.Join(artists, "; ")
	info.AlbumArtist = strings.Join(albumArtists, "; ")
}

func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// --- FLAC -------------------------------------------------------------------------------------

func skipID3v2(f io.ReadSeeker) (int64, error) {
	var h [10]byte
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return 0, err
	}
	if string(h[:3]) != "ID3" {
		f.Seek(0, io.SeekStart)
		return 0, nil
	}
	size := int64(syncsafe(h[6:10])) + 10
	if h[5]&0x10 != 0 {
		size += 10
	}
	_, err := f.Seek(size, io.SeekStart)
	return size, err
}

func syncsafe(b []byte) uint32 {
	return uint32(b[0]&0x7f)<<21 | uint32(b[1]&0x7f)<<14 | uint32(b[2]&0x7f)<<7 | uint32(b[3]&0x7f)
}

func readFLAC(f *os.File, size int64) (*tagInfo, error) {
	start, err := skipID3v2(f)
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return nil, errors.New("not a FLAC file")
	}
	info := &tagInfo{}
	var rate, bps int
	var samples int64
	pos := start + 4
	for {
		var h [4]byte
		if _, err := io.ReadFull(f, h[:]); err != nil {
			return nil, err
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		pos += 4 + n
		switch typ {
		case 0: // STREAMINFO
			b := make([]byte, n)
			if _, err := io.ReadFull(f, b); err != nil || n < 18 {
				return nil, errors.New("bad STREAMINFO")
			}
			x := binary.BigEndian.Uint64(b[10:18])
			rate = int(x >> 44)
			bps = int((x>>36)&0x1f) + 1
			samples = int64(x & 0xfffffffff)
		case 4: // VORBIS_COMMENT
			if n > 16<<20 {
				return nil, errors.New("comment block too big")
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(f, b); err != nil {
				return nil, err
			}
			parseVorbisComment(b, info)
		default:
			if _, err := f.Seek(n, io.SeekCurrent); err != nil {
				return nil, err
			}
		}
		if last {
			break
		}
	}
	if rate > 0 && samples > 0 {
		info.Duration = float64(samples) / float64(rate)
	}
	kbps := 0
	if info.Duration > 0 {
		kbps = int(float64(size-pos) * 8 / info.Duration / 1000)
	}
	info.Q = newQuality("flac", kbps, rate, bps)
	return info, nil
}

// --- MP3 --------------------------------------------------------------------------------------

func decodeID3Text(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	enc, b := b[0], b[1:]
	var s string
	switch enc {
	case 1, 2: // UTF-16 with BOM, UTF-16BE
		be := enc == 2
		if len(b) >= 2 && b[0] == 0xfe && b[1] == 0xff {
			be, b = true, b[2:]
		} else if len(b) >= 2 && b[0] == 0xff && b[1] == 0xfe {
			be, b = false, b[2:]
		}
		u := make([]uint16, 0, len(b)/2)
		for i := 0; i+1 < len(b); i += 2 {
			if be {
				u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
			} else {
				u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
			}
		}
		// Values are separated by NULs (ID3v2.4); a BOM may follow each separator.
		s = strings.ReplaceAll(string(utf16.Decode(u)), "\ufeff", "")
	case 3:
		s = string(b)
	default: // ISO-8859-1
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		s = string(r)
	}
	var parts []string
	for _, p := range strings.Split(s, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

func unsync(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xff, 0x00}, []byte{0xff})
}

// parseID3v2 reads the tag at the start of the file and returns its total size.
func parseID3v2(f io.ReadSeeker, info *tagInfo) (int64, float64) {
	var h [10]byte
	f.Seek(0, io.SeekStart)
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:3]) != "ID3" {
		return 0, 0
	}
	ver, flags := h[3], h[5]
	size := int64(syncsafe(h[6:10]))
	total := size + 10
	if flags&0x10 != 0 {
		total += 10
	}
	if size > 64<<20 {
		return total, 0
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return total, 0
	}
	if flags&0x80 != 0 && ver < 4 {
		data = unsync(data)
	}
	if flags&0x40 != 0 && len(data) >= 4 { // extended header
		n := int(binary.BigEndian.Uint32(data[:4]))
		if ver == 4 {
			n = int(syncsafe(data[:4]))
		} else {
			n += 4
		}
		if n < len(data) {
			data = data[n:]
		}
	}
	var lengthMs float64
	idLen, hdrLen := 4, 10
	if ver == 2 {
		idLen, hdrLen = 3, 6
	}
	for len(data) >= hdrLen && data[0] != 0 {
		id := string(data[:idLen])
		var n int
		var fflags byte
		switch ver {
		case 2:
			n = int(data[3])<<16 | int(data[4])<<8 | int(data[5])
		case 3:
			n = int(binary.BigEndian.Uint32(data[4:8]))
			fflags = data[9]
		default:
			n = int(syncsafe(data[4:8]))
			fflags = data[9]
		}
		if n < 0 || hdrLen+n > len(data) {
			break
		}
		body := data[hdrLen : hdrLen+n]
		data = data[hdrLen+n:]
		if ver == 4 && fflags&0x02 != 0 {
			body = unsync(body)
		}
		if ver == 4 && fflags&0x01 != 0 && len(body) >= 4 { // data length indicator
			body = body[4:]
		}
		if ver >= 3 && fflags&0x0c != 0 { // compressed or encrypted
			continue
		}
		switch id {
		case "TIT2", "TT2":
			info.Title = decodeID3Text(body)
		case "TPE1", "TP1":
			info.Artist = decodeID3Text(body)
		case "TPE2", "TP2":
			info.AlbumArtist = decodeID3Text(body)
		case "TALB", "TAL":
			info.Album = decodeID3Text(body)
		case "TSRC", "TRC":
			info.ISRC = decodeID3Text(body)
		case "TXXX", "TXX": // user-defined text: description, then value
			if desc, val, ok := strings.Cut(decodeID3Text(body), "; "); ok && strings.EqualFold(desc, "ISRC") && info.ISRC == "" {
				info.ISRC = val
			}
		case "TRCK", "TRK":
			info.TrackNumber = leadingInt(decodeID3Text(body))
		case "TLEN", "TLE":
			if v, err := strconv.ParseFloat(decodeID3Text(body), 64); err == nil {
				lengthMs = v
			}
		}
	}
	return total, lengthMs / 1000
}

var (
	mp3Bitrates = [2][3][16]int{ // [MPEG1, MPEG2/2.5][layer I, II, III][index]
		{{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
			{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
			{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}},
		{{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
			{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}},
	}
	mp3Rates = [3][3]int{{44100, 48000, 32000}, {22050, 24000, 16000}, {11025, 12000, 8000}}
)

type mpegFrame struct {
	mpeg1           bool
	version         int // 0: MPEG1, 1: MPEG2, 2: MPEG2.5
	layer           int // 1..3
	kbps, rate      int
	mono            bool
	samplesPerFrame int
}

func parseMPEGHeader(h []byte) (mpegFrame, bool) {
	if len(h) < 4 || h[0] != 0xff || h[1]&0xe0 != 0xe0 {
		return mpegFrame{}, false
	}
	verBits, layerBits := (h[1]>>3)&3, (h[1]>>1)&3
	brIdx, srIdx := h[2]>>4, (h[2]>>2)&3
	if verBits == 1 || layerBits == 0 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return mpegFrame{}, false
	}
	fr := mpegFrame{layer: int(4 - layerBits)}
	switch verBits {
	case 3:
		fr.version, fr.mpeg1 = 0, true
	case 2:
		fr.version = 1
	default:
		fr.version = 2
	}
	table := 1
	if fr.mpeg1 {
		table = 0
	}
	fr.kbps = mp3Bitrates[table][fr.layer-1][brIdx]
	fr.rate = mp3Rates[fr.version][srIdx]
	fr.mono = h[3]>>6 == 3
	switch {
	case fr.layer == 1:
		fr.samplesPerFrame = 384
	case fr.layer == 3 && !fr.mpeg1:
		fr.samplesPerFrame = 576
	default:
		fr.samplesPerFrame = 1152
	}
	return fr, true
}

func readMP3(f *os.File, size int64) (*tagInfo, error) {
	info := &tagInfo{}
	tagEnd, tlen := parseID3v2(f, info)
	end := size
	var v1 [128]byte
	if size > 128 {
		if _, err := f.ReadAt(v1[:], size-128); err == nil && string(v1[:3]) == "TAG" {
			end -= 128
			if info.Title == "" {
				info.Title = strings.TrimRight(string(v1[3:33]), "\x00 ")
				info.Artist = firstNonEmpty(info.Artist, strings.TrimRight(string(v1[33:63]), "\x00 "))
				info.Album = firstNonEmpty(info.Album, strings.TrimRight(string(v1[63:93]), "\x00 "))
			}
		}
	}
	buf := make([]byte, 64<<10)
	n, _ := f.ReadAt(buf, tagEnd)
	buf = buf[:n]
	off := -1
	var fr mpegFrame
	for i := 0; i+4 <= len(buf); i++ {
		if buf[i] != 0xff {
			continue
		}
		if x, ok := parseMPEGHeader(buf[i:]); ok {
			fr, off = x, i
			break
		}
	}
	if off < 0 {
		return nil, errors.New("no MPEG audio frame found")
	}
	audio := end - tagEnd - int64(off)
	frame := buf[off:]
	// Xing/Info (LAME) or VBRI header: frame count, hence the exact length.
	sideInfo := 17
	switch {
	case fr.mpeg1 && !fr.mono:
		sideInfo = 32
	case !fr.mpeg1 && fr.mono:
		sideInfo = 9
	}
	frames, vbrBytes := int64(0), int64(0)
	if x := 4 + sideInfo; len(frame) >= x+16 && (string(frame[x:x+4]) == "Xing" || string(frame[x:x+4]) == "Info") {
		flags := binary.BigEndian.Uint32(frame[x+4 : x+8])
		p := x + 8
		if flags&1 != 0 {
			frames = int64(binary.BigEndian.Uint32(frame[p : p+4]))
			p += 4
		}
		if flags&2 != 0 && len(frame) >= p+4 {
			vbrBytes = int64(binary.BigEndian.Uint32(frame[p : p+4]))
		}
	} else if len(frame) >= 36+18 && string(frame[36:40]) == "VBRI" {
		vbrBytes = int64(binary.BigEndian.Uint32(frame[36+10 : 36+14]))
		frames = int64(binary.BigEndian.Uint32(frame[36+14 : 36+18]))
	}
	switch {
	case frames > 0 && fr.rate > 0:
		info.Duration = float64(frames) * float64(fr.samplesPerFrame) / float64(fr.rate)
	case tlen > 0:
		info.Duration = tlen
	case fr.kbps > 0:
		info.Duration = float64(audio) * 8 / float64(fr.kbps*1000)
	}
	kbps := fr.kbps
	if info.Duration > 0 {
		b := audio
		if vbrBytes > 0 {
			b = vbrBytes
		}
		kbps = int(float64(b) * 8 / info.Duration / 1000)
	}
	codec := "mp3"
	if fr.layer == 2 {
		codec = "mp2"
	}
	info.Q = newQuality(codec, kbps, fr.rate, 0)
	return info, nil
}

// --- MP4 / M4A --------------------------------------------------------------------------------

type mp4Reader struct {
	f                   *os.File
	info                *tagInfo
	timescale, duration uint64
	mdat                int64
	codec               string
	rate                int
	depth               int
	esdsKbps            int
}

func readMP4(f *os.File, size int64) (*tagInfo, error) {
	m := &mp4Reader{f: f, info: &tagInfo{}}
	if err := m.boxes(0, size, 0); err != nil {
		return nil, err
	}
	if m.codec == "" {
		return nil, errors.New("no audio track found")
	}
	if m.timescale > 0 {
		m.info.Duration = float64(m.duration) / float64(m.timescale)
	}
	kbps := m.esdsKbps
	if m.info.Duration > 0 && m.mdat > 0 {
		kbps = int(float64(m.mdat) * 8 / m.info.Duration / 1000)
	}
	m.info.Q = newQuality(m.codec, kbps, m.rate, m.depth)
	return m.info, nil
}

func (m *mp4Reader) boxes(start, end int64, depth int) error {
	if depth > 12 {
		return nil
	}
	for pos := start; pos+8 <= end; {
		var h [16]byte
		if _, err := m.f.ReadAt(h[:8], pos); err != nil {
			return err
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		typ := string(h[4:8])
		hdr := int64(8)
		switch size {
		case 1:
			if _, err := m.f.ReadAt(h[8:16], pos+8); err != nil {
				return err
			}
			size, hdr = int64(binary.BigEndian.Uint64(h[8:16])), 16
		case 0:
			size = end - pos
		}
		if size < hdr || pos+size > end {
			if depth == 0 && typ == "mdat" { // truncated file
				size = end - pos
			} else {
				return nil
			}
		}
		body, bodyEnd := pos+hdr, pos+size
		switch typ {
		case "moov", "trak", "mdia", "minf", "stbl", "udta", "ilst":
			m.boxes(body, bodyEnd, depth+1)
		case "meta":
			var peek [8]byte
			m.f.ReadAt(peek[:], body)
			if string(peek[4:8]) == "hdlr" { // QuickTime style, no version/flags
				m.boxes(body, bodyEnd, depth+1)
			} else {
				m.boxes(body+4, bodyEnd, depth+1)
			}
		case "mdat":
			m.mdat += size - hdr
		case "mvhd":
			b := m.read(body, min(bodyEnd-body, 32))
			if len(b) >= 20 && b[0] == 0 {
				m.timescale = uint64(binary.BigEndian.Uint32(b[12:16]))
				m.duration = uint64(binary.BigEndian.Uint32(b[16:20]))
			} else if len(b) >= 32 && b[0] == 1 {
				m.timescale = uint64(binary.BigEndian.Uint32(b[20:24]))
				m.duration = binary.BigEndian.Uint64(b[24:32])
			}
		case "stsd":
			if m.codec == "" {
				m.sampleEntry(body+8, bodyEnd)
			}
		case "\xa9nam", "\xa9ART", "aART", "\xa9alb", "trkn", "----":
			m.ilstItem(typ, body, bodyEnd)
		}
		pos += size
	}
	return nil
}

func (m *mp4Reader) read(off, n int64) []byte {
	if n <= 0 || n > 1<<20 {
		return nil
	}
	b := make([]byte, n)
	k, _ := m.f.ReadAt(b, off)
	return b[:k]
}

func (m *mp4Reader) sampleEntry(pos, end int64) {
	b := m.read(pos, min(end-pos, 4096))
	if len(b) < 36 {
		return
	}
	size := int(binary.BigEndian.Uint32(b[:4]))
	switch string(b[4:8]) {
	case "mp4a":
		m.codec = "aac"
	case "alac":
		m.codec = "alac"
	case "fLaC":
		m.codec = "flac"
	case "Opus":
		m.codec = "opus"
	case ".mp3":
		m.codec = "mp3"
	case "ac-3":
		m.codec = "ac3"
	case "ec-3":
		m.codec = "eac3"
	default:
		return
	}
	// AudioSampleEntry: 8 header, 6 reserved, 2 data ref, 8 reserved, 2 channels, 2 sample size,
	// 4 reserved, 4 sample rate (16.16).
	m.depth = int(binary.BigEndian.Uint16(b[26:28]))
	m.rate = int(binary.BigEndian.Uint32(b[32:36]) >> 16)
	if m.codec != "alac" && m.codec != "flac" {
		m.depth = 0
	}
	// esds: the decoder config descriptor's average bitrate.
	if i := bytes.Index(b[36:min(size, len(b))], []byte("esds")); i >= 0 {
		d := b[36+i+8:]
		if j := bytes.IndexByte(d, 0x04); j >= 0 && len(d) > j+14 { // DecoderConfigDescriptor tag
			p := j + 1
			for p < len(d) && d[p]&0x80 != 0 { // size bytes
				p++
			}
			p++
			if len(d) >= p+13 {
				m.esdsKbps = int(binary.BigEndian.Uint32(d[p+9:p+13]) / 1000)
			}
		}
	}
}

func (m *mp4Reader) ilstItem(typ string, pos, end int64) {
	b := m.read(pos, min(end-pos, 64<<10))
	var name, value string
	for len(b) >= 8 {
		n := int(binary.BigEndian.Uint32(b[:4]))
		if n < 8 || n > len(b) {
			break
		}
		child, payload := string(b[4:8]), b[8:n]
		switch child {
		case "name":
			if len(payload) > 4 {
				name = string(payload[4:])
			}
		case "data":
			if len(payload) >= 8 {
				if typ == "trkn" && len(payload) >= 12 {
					m.info.TrackNumber = int(binary.BigEndian.Uint16(payload[10:12]))
				} else {
					value = strings.TrimSpace(string(payload[8:]))
				}
			}
		}
		b = b[n:]
	}
	switch typ {
	case "\xa9nam":
		m.info.Title = value
	case "\xa9ART":
		m.info.Artist = value
	case "aART":
		m.info.AlbumArtist = value
	case "\xa9alb":
		m.info.Album = value
	case "----":
		if strings.EqualFold(name, "ISRC") {
			m.info.ISRC = value
		}
	}
}

// --- Ogg (Vorbis, Opus) -----------------------------------------------------------------------

// oggPackets reads the first packets of an Ogg stream (up to limit bytes in all).
func oggPackets(r io.Reader, want int, limit int) ([][]byte, int64, error) {
	var packets [][]byte
	var cur []byte
	var read int64
	for len(packets) < want {
		var h [27]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return packets, read, err
		}
		if string(h[:4]) != "OggS" {
			return packets, read, errors.New("not an Ogg page")
		}
		segs := make([]byte, h[26])
		if _, err := io.ReadFull(r, segs); err != nil {
			return packets, read, err
		}
		read += 27 + int64(len(segs))
		for _, s := range segs {
			chunk := make([]byte, s)
			if _, err := io.ReadFull(r, chunk); err != nil {
				return packets, read, err
			}
			read += int64(s)
			if len(cur)+len(chunk) <= limit {
				cur = append(cur, chunk...)
			}
			if s < 255 {
				packets = append(packets, cur)
				cur = nil
				if len(packets) == want {
					return packets, read, nil
				}
			}
		}
	}
	return packets, read, nil
}

func readOgg(f *os.File, size int64) (*tagInfo, error) {
	packets, headerBytes, err := oggPackets(io.NewSectionReader(f, 0, size), 2, 4<<20)
	if len(packets) < 2 {
		if err == nil {
			err = errors.New("Ogg headers missing")
		}
		return nil, err
	}
	info := &tagInfo{}
	id, comment := packets[0], packets[1]
	var rate, nominal, preskip int
	codec := ""
	switch {
	case len(id) >= 30 && string(id[1:7]) == "vorbis":
		codec = "vorbis"
		rate = int(binary.LittleEndian.Uint32(id[12:16]))
		nominal = int(int32(binary.LittleEndian.Uint32(id[20:24])))
		if len(comment) > 7 && string(comment[1:7]) == "vorbis" {
			parseVorbisComment(comment[7:], info)
		}
	case len(id) >= 19 && string(id[:8]) == "OpusHead":
		codec = "opus"
		preskip = int(binary.LittleEndian.Uint16(id[10:12]))
		rate = 48000
		if len(comment) > 8 && string(comment[:8]) == "OpusTags" {
			parseVorbisComment(comment[8:], info)
		}
	default:
		return nil, errUnsupported
	}
	// The last page's granule position gives the length.
	tail := min(size, 64<<10)
	buf := make([]byte, tail)
	f.ReadAt(buf, size-tail)
	if i := bytes.LastIndex(buf, []byte("OggS")); i >= 0 && len(buf) >= i+14 {
		granule := int64(binary.LittleEndian.Uint64(buf[i+6 : i+14]))
		if granule > 0 && rate > 0 {
			info.Duration = float64(granule-int64(preskip)) / float64(rate)
		}
	}
	kbps := nominal / 1000
	if info.Duration > 0 {
		kbps = int(float64(size-headerBytes) * 8 / info.Duration / 1000)
	}
	q := newQuality(codec, kbps, rate, 0)
	if codec == "opus" {
		q.SampleRate = 0 // Opus always decodes at 48 kHz; the rate says nothing about quality
	}
	info.Q = q
	return info, nil
}
