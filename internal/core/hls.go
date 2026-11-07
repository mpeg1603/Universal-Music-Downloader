package core

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Downloading an HLS stream (.m3u8) into one file: its segments, decrypted where they use
// AES-128, one after the other. VK serves its audio this way.

type hlsSegment struct {
	url    string
	keyURL string
	iv     []byte
}

func hlsAttrs(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				end = len(rest) - 1
			}
			v, rest = rest[1:end+1], rest[min(end+2, len(rest)):]
		} else {
			v, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.ToUpper(strings.TrimSpace(k))] = v
		s = strings.TrimPrefix(strings.TrimSpace(rest), ",")
	}
	return out
}

// parseHLS reads a media playlist; for a master playlist it returns the variant with the
// highest bandwidth instead.
func parseHLS(base, text string) (segments []hlsSegment, variant string, err error) {
	b, err := url.Parse(base)
	if err != nil {
		return nil, "", err
	}
	resolve := func(ref string) string {
		u, err := b.Parse(strings.TrimSpace(ref))
		if err != nil {
			return ref
		}
		return u.String()
	}
	var keyURL string
	var keyIV []byte
	seq := 0
	bestBW := -1
	pendingBW := -1
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			seq, _ = strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			a := hlsAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			switch strings.ToUpper(a["METHOD"]) {
			case "", "NONE":
				keyURL, keyIV = "", nil
			case "AES-128":
				keyURL, keyIV = resolve(a["URI"]), nil
				if iv := strings.TrimPrefix(strings.TrimPrefix(a["IV"], "0x"), "0X"); iv != "" {
					keyIV, _ = hex.DecodeString(iv)
				}
			default:
				return nil, "", fmt.Errorf("unsupported HLS encryption %s", a["METHOD"])
			}
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			a := hlsAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			pendingBW, _ = strconv.Atoi(a["BANDWIDTH"])
		case strings.HasPrefix(line, "#"):
		default:
			if pendingBW >= 0 {
				if pendingBW > bestBW {
					bestBW, variant = pendingBW, resolve(line)
				}
				pendingBW = -1
				continue
			}
			seg := hlsSegment{url: resolve(line), keyURL: keyURL}
			if keyURL != "" {
				seg.iv = keyIV
				if seg.iv == nil {
					seg.iv = make([]byte, 16)
					binary.BigEndian.PutUint64(seg.iv[8:], uint64(seq))
				}
			}
			segments = append(segments, seg)
			seq++
		}
	}
	return segments, variant, nil
}

// hlsDownload saves the stream at playlistURL into dest.
func hlsDownload(ctx context.Context, h *HTTP, playlistURL, dest string) error {
	var segments []hlsSegment
	u := playlistURL
	for depth := 0; ; depth++ {
		status, body, err := h.get(ctx, u)
		if err != nil {
			return err
		}
		if status != 200 {
			return failf("%s: the stream's playlist answered HTTP %d", h.Name, status)
		}
		segs, variant, err := parseHLS(u, string(body))
		if err != nil {
			return failf("%s: %v", h.Name, err)
		}
		if variant == "" || depth > 2 {
			segments = segs
			break
		}
		u = variant
	}
	if len(segments) == 0 {
		return failf("%s: the stream has no segments", h.Name)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	keys := map[string][]byte{}
	task := taskFrom(ctx)
	var done int64
	for i, s := range segments {
		status, data, err := h.get(ctx, s.url)
		if err != nil {
			return err
		}
		if status != 200 {
			return unavailablef("%s: a stream segment answered HTTP %d", h.Name, status)
		}
		if err := bw.take(ctx, len(data)); err != nil {
			return err
		}
		if s.keyURL != "" {
			key := keys[s.keyURL]
			if key == nil {
				st, k, err := h.get(ctx, s.keyURL)
				if err != nil {
					return err
				}
				if st != 200 || len(k) != 16 {
					return failf("%s: couldn't get the stream's key (HTTP %d)", h.Name, st)
				}
				keys[s.keyURL], key = k, k
			}
			if data, err = decryptAES128CBC(data, key, s.iv); err != nil {
				return failf("%s: %v", h.Name, err)
			}
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		done += int64(len(data))
		task.setProgress(done, done*int64(len(segments))/int64(i+1))
	}
	return f.Close()
}

func decryptAES128CBC(data, key, iv []byte) ([]byte, error) {
	if len(data)%aes.BlockSize != 0 {
		return nil, errors.New("encrypted segment has a bad length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	if n := len(out); n > 0 { // PKCS#7 padding
		if p := int(out[n-1]); p > 0 && p <= aes.BlockSize && p <= n {
			out = out[:n-p]
		}
	}
	return out, nil
}
