package core

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestPickFFmpegAsset(t *testing.T) {
	sums := `aa11  ffmpeg-master-latest-linux64-lgpl.tar.xz
bb22  ffmpeg-n8.1-latest-linux64-lgpl-8.1.tar.xz
cc33  ffmpeg-n9.0-latest-linux64-lgpl-9.0.tar.xz
dd44  ffmpeg-n9.0-latest-linux64-gpl-9.0.tar.xz
ee55  ffmpeg-n9.0-latest-win64-lgpl-9.0.zip
ff66  ffmpeg-n9.0-latest-linux64-lgpl-shared-9.0.tar.xz
`
	if name, sum := pickFFmpegAsset(sums, "linux64"); name != "ffmpeg-n9.0-latest-linux64-lgpl-9.0.tar.xz" || sum != "cc33" {
		t.Errorf("linux64: %s %s", name, sum)
	}
	if name, _ := pickFFmpegAsset(sums, "win64"); name != "ffmpeg-n9.0-latest-win64-lgpl-9.0.zip" {
		t.Errorf("win64: %s", name)
	}
	if name, _ := pickFFmpegAsset(sums, "linuxarm64"); name != "" {
		t.Errorf("linuxarm64: %s", name)
	}
}

func TestFetchFFmpeg(t *testing.T) {
	platform, err := ffmpegPlatform()
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("tar.xz platforms only")
	}
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for _, f := range []struct{ name, body string }{
		{"ffmpeg-n9.0-latest/doc/ffmpeg", "not this one"},
		{"ffmpeg-n9.0-latest/bin/ffmpeg", "#!/bin/sh\necho ffmpeg\n"},
		{"ffmpeg-n9.0-latest/bin/ffprobe", "#!/bin/sh\necho ffprobe\n"},
		{"ffmpeg-n9.0-latest/bin/ffplay", "not needed"},
	} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	var archive bytes.Buffer
	xw, _ := xz.NewWriter(&archive)
	xw.Write(tarBuf.Bytes())
	xw.Close()
	sum := sha256.Sum256(archive.Bytes())
	name := fmt.Sprintf("ffmpeg-n9.0-latest-%s-lgpl-9.0.tar.xz", platform)
	bad := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.sha256":
			s := hex.EncodeToString(sum[:])
			if bad {
				s = "00" + s[2:]
			}
			fmt.Fprintf(w, "%s  %s\n", s, name)
		case "/" + name:
			w.Write(archive.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := ffmpegReleases
	ffmpegReleases = srv.URL + "/"
	defer func() { ffmpegReleases = old }()

	dir := t.TempDir()
	ff, fp, err := fetchFFmpeg(context.Background(), dir, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ff); string(b) != "#!/bin/sh\necho ffmpeg\n" {
		t.Errorf("ffmpeg: %q", b)
	}
	if b, _ := os.ReadFile(fp); string(b) != "#!/bin/sh\necho ffprobe\n" {
		t.Errorf("ffprobe: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("left in the folder: %v", entries)
	}
	if _, _, ok := fetchedFFmpeg(dir); !ok {
		t.Error("not found again")
	}
	bad = true
	if _, _, err := fetchFFmpeg(context.Background(), t.TempDir(), t.Logf); err == nil {
		t.Error("a checksum mismatch should fail")
	}
}
