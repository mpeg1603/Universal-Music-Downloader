package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"unicode/utf8"
)

// The Soulseek wire format (see Nicotine+'s doc/SLSKPROTOCOL.md): little-endian integers,
// strings and byte arrays prefixed with their uint32 length, and messages framed as
// [uint32 length][code][contents]. The code is a uint32 for server and peer messages and a
// uint8 for peer-init and distributed ones; file connections carry no framing.

// slskMsg builds a message's contents.
type slskMsg struct{ b []byte }

func (m *slskMsg) u8(v uint8) *slskMsg { m.b = append(m.b, v); return m }
func (m *slskMsg) u32(v uint32) *slskMsg {
	m.b = binary.LittleEndian.AppendUint32(m.b, v)
	return m
}
func (m *slskMsg) i32(v int32) *slskMsg { return m.u32(uint32(v)) }
func (m *slskMsg) u64(v uint64) *slskMsg {
	m.b = binary.LittleEndian.AppendUint64(m.b, v)
	return m
}
func (m *slskMsg) boolean(v bool) *slskMsg {
	if v {
		return m.u8(1)
	}
	return m.u8(0)
}
func (m *slskMsg) str(s string) *slskMsg {
	m.u32(uint32(len(s)))
	m.b = append(m.b, s...)
	return m
}
func (m *slskMsg) raw(p []byte) *slskMsg { m.b = append(m.b, p...); return m }

// slskRead reads a message's contents. A short or malformed message sets err and every read
// after it returns zero values, so parsers check err once at the end.
type slskRead struct {
	b   []byte
	off int
	err error
}

var errSlskShort = errors.New("message ends early")

func (r *slskRead) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.off+n > len(r.b) {
		r.err = errSlskShort
		return nil
	}
	p := r.b[r.off : r.off+n]
	r.off += n
	return p
}

func (r *slskRead) u8() uint8 {
	if p := r.take(1); p != nil {
		return p[0]
	}
	return 0
}
func (r *slskRead) u16() uint16 {
	if p := r.take(2); p != nil {
		return binary.LittleEndian.Uint16(p)
	}
	return 0
}
func (r *slskRead) u32() uint32 {
	if p := r.take(4); p != nil {
		return binary.LittleEndian.Uint32(p)
	}
	return 0
}
func (r *slskRead) i32() int32 { return int32(r.u32()) }
func (r *slskRead) u64() uint64 {
	if p := r.take(8); p != nil {
		return binary.LittleEndian.Uint64(p)
	}
	return 0
}
func (r *slskRead) boolean() bool { return r.u8() != 0 }

// str reads a string: UTF-8, or Latin-1 from old clients.
func (r *slskRead) str() string {
	n := r.u32()
	p := r.take(int(n))
	if p == nil {
		return ""
	}
	if utf8.Valid(p) {
		return string(p)
	}
	rs := make([]rune, len(p))
	for i, c := range p {
		rs[i] = rune(c)
	}
	return string(rs)
}

func (r *slskRead) bytes() []byte { return r.take(int(r.u32())) }

// ip reads an IPv4 address, sent as a little-endian number (so its bytes come reversed).
func (r *slskRead) ip() net.IP {
	p := r.take(4)
	if p == nil {
		return net.IPv4zero
	}
	return net.IPv4(p[3], p[2], p[1], p[0])
}

// fileSize reads a uint64 size; Soulseek NS sends sizes over 2 GiB with garbage in the upper half.
func (r *slskRead) fileSize() uint64 {
	if r.err == nil && r.off+8 <= len(r.b) && r.b[r.off+7] == 255 {
		v := r.u32()
		r.take(4)
		return uint64(v)
	}
	return r.u64()
}

func (r *slskRead) more() bool { return r.err == nil && r.off < len(r.b) }

// rest returns the unread part.
func (r *slskRead) rest() []byte {
	if r.err != nil {
		return nil
	}
	p := r.b[r.off:]
	r.off = len(r.b)
	return p
}

func putIP(m *slskMsg, ip net.IP) {
	v := ip.To4()
	if v == nil {
		v = net.IPv4zero.To4()
	}
	m.raw([]byte{v[3], v[2], v[1], v[0]})
}

// --- framing --------------------------------------------------------------------------------

// slskFrame makes a framed message whose code takes codeSize bytes (4 or 1).
func slskFrame(code uint32, codeSize int, body []byte) []byte {
	out := make([]byte, 0, 4+codeSize+len(body))
	out = binary.LittleEndian.AppendUint32(out, uint32(codeSize+len(body)))
	if codeSize == 1 {
		out = append(out, byte(code))
	} else {
		out = binary.LittleEndian.AppendUint32(out, code)
	}
	return append(out, body...)
}

// readSlskFrame reads one framed message; messages over limit bytes end the connection.
func readSlskFrame(r *bufio.Reader, codeSize int, limit uint32) (uint32, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n < uint32(codeSize) {
		return 0, nil, fmt.Errorf("bad message length %d", n)
	}
	if n > limit {
		return 0, nil, fmt.Errorf("message of %d bytes is over the %d limit", n, limit)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}
	if codeSize == 1 {
		return uint32(buf[0]), buf[1:], nil
	}
	return binary.LittleEndian.Uint32(buf), buf[4:], nil
}

// --- compression ----------------------------------------------------------------------------

func slskCompress(p []byte) []byte {
	var b bytes.Buffer
	w, _ := zlib.NewWriterLevel(&b, 4)
	w.Write(p)
	w.Close()
	return b.Bytes()
}

// slskDecompress inflates at most limit bytes (a "zip bomb" ends with an error).
func slskDecompress(p []byte, limit int64) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(p))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, errors.New("compressed message too large")
	}
	return out, nil
}

// --- files ----------------------------------------------------------------------------------

// File attribute codes.
const (
	slskAttrBitrate    = 0
	slskAttrDuration   = 1
	slskAttrVBR        = 2
	slskAttrSampleRate = 4
	slskAttrBitDepth   = 5
)

type slskAttr struct{ Code, Value uint32 }

// slskFile is a file in a search result (Name is the full path) or a folder listing (the
// base name).
type slskFile struct {
	Name  string
	Size  int64
	Attrs []slskAttr
}

func (f slskFile) attr(code uint32) int {
	for _, a := range f.Attrs {
		if a.Code == code {
			return int(a.Value)
		}
	}
	return 0
}

func readSlskFiles(r *slskRead, max int) []slskFile {
	n := int(r.u32())
	if n < 0 || n > max {
		r.err = fmt.Errorf("%d files is too many", n)
		return nil
	}
	out := make([]slskFile, 0, min(n, 1024))
	for i := 0; i < n && r.err == nil; i++ {
		r.u8() // always 1
		f := slskFile{Name: r.str(), Size: int64(r.fileSize())}
		r.str() // extension: obsolete
		na := int(r.u32())
		if na > 64 {
			r.err = errSlskShort
			break
		}
		for j := 0; j < na; j++ {
			f.Attrs = append(f.Attrs, slskAttr{r.u32(), r.u32()})
		}
		out = append(out, f)
	}
	return out
}

func writeSlskFile(m *slskMsg, f slskFile) {
	m.u8(1).str(f.Name).u64(uint64(f.Size)).str("").u32(uint32(len(f.Attrs)))
	for _, a := range f.Attrs {
		m.u32(a.Code).u32(a.Value)
	}
}
