package core

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

// ffmpeg and ffprobe, when they aren't installed: the app fetches the FFmpeg project's
// recommended static builds (FFmpeg-Builds on GitHub, the LGPL variant), checks them against
// the release's checksums, and keeps just the two programs in the cache. Like yt-dlp, but
// fetched once (they don't go stale the way yt-dlp does).

var ffmpegReleases = "https://github.com/FFmpeg-Builds/FFmpeg-Builds/releases/download/latest/"

// ffmpegPlatform is the builds' name for this system.
func ffmpegPlatform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "linux64", nil
	case "linux/arm64":
		return "linuxarm64", nil
	case "windows/amd64":
		return "win64", nil
	case "windows/arm64":
		return "winarm64", nil
	}
	return "", fmt.Errorf("no ready-made ffmpeg for %s/%s: install ffmpeg yourself", runtime.GOOS, runtime.GOARCH)
}

var reFFmpegAsset = regexp.MustCompile(`^ffmpeg-n(\d+)\.(\d+)-latest-([a-z0-9]+)-lgpl-[0-9.]+\.(tar\.xz|zip)$`)

// pickFFmpegAsset chooses, from the release's checksum file, the newest stable LGPL build
// for platform; it returns the file's name and checksum.
func pickFFmpegAsset(sums, platform string) (name, sum string) {
	best := -1
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		file := strings.TrimPrefix(f[1], "*")
		m := reFFmpegAsset.FindStringSubmatch(file)
		if m == nil || m[3] != platform {
			continue
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if v := major*1000 + minor; v > best {
			best, name, sum = v, file, strings.ToLower(f[0])
		}
	}
	return name, sum
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// fetchedFFmpeg returns the ffmpeg and ffprobe fetched before, if they're there.
func fetchedFFmpeg(dir string) (string, string, bool) {
	ff, fp := filepath.Join(dir, exeName("ffmpeg")), filepath.Join(dir, exeName("ffprobe"))
	if _, err := os.Stat(ff); err != nil {
		return "", "", false
	}
	if _, err := os.Stat(fp); err != nil {
		return "", "", false
	}
	return ff, fp, true
}

// fetchFFmpeg downloads ffmpeg and ffprobe into dir.
func fetchFFmpeg(ctx context.Context, dir string, logf func(string, ...any)) (string, string, error) {
	platform, err := ffmpegPlatform()
	if err != nil {
		return "", "", err
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	get := func(u string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		return resp, nil
	}
	resp, err := get(ffmpegReleases + "checksums.sha256")
	if err != nil {
		return "", "", fmt.Errorf("can't get ffmpeg's checksums: %w", err)
	}
	sums, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil {
		return "", "", err
	}
	name, want := pickFFmpegAsset(string(sums), platform)
	if name == "" {
		return "", "", fmt.Errorf("no ffmpeg build for %s in the latest FFmpeg-Builds release", platform)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	resp, err = get(ffmpegReleases + name)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	logf("Downloading ffmpeg (%s, %s)…", name, FmtBytes(resp.ContentLength))
	archive, err := os.CreateTemp(dir, "ffmpeg-download-*")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, h), resp.Body); err != nil {
		return "", "", fmt.Errorf("downloading ffmpeg: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", "", fmt.Errorf("the downloaded ffmpeg doesn't match its checksum")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	wanted := map[string]bool{exeName("ffmpeg"): true, exeName("ffprobe"): true}
	got := map[string]bool{}
	put := func(base string, r io.Reader) error {
		dest := filepath.Join(dir, base)
		tmp := dest + ".tmp"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, r); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		got[base] = true
		return os.Rename(tmp, dest)
	}
	// Only <top>/bin/ffmpeg and <top>/bin/ffprobe are taken from the archive.
	isWanted := func(p string) (string, bool) {
		base := path.Base(p)
		return base, wanted[base] && path.Base(path.Dir(p)) == "bin"
	}
	if strings.HasSuffix(name, ".zip") {
		info, err := archive.Stat()
		if err != nil {
			return "", "", err
		}
		zr, err := zip.NewReader(archive, info.Size())
		if err != nil {
			return "", "", err
		}
		for _, f := range zr.File {
			if base, ok := isWanted(f.Name); ok {
				rc, err := f.Open()
				if err != nil {
					return "", "", err
				}
				err = put(base, rc)
				rc.Close()
				if err != nil {
					return "", "", err
				}
			}
		}
	} else {
		xr, err := xz.NewReader(bufio.NewReader(archive))
		if err != nil {
			return "", "", err
		}
		tr := tar.NewReader(xr)
		for {
			hd, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", "", fmt.Errorf("unpacking ffmpeg: %w", err)
			}
			if base, ok := isWanted(hd.Name); ok && hd.Typeflag == tar.TypeReg {
				if err := put(base, tr); err != nil {
					return "", "", err
				}
			}
		}
	}
	if len(got) != len(wanted) {
		return "", "", fmt.Errorf("ffmpeg's archive %s has no bin/ffmpeg and bin/ffprobe", name)
	}
	ff, fp, _ := fetchedFFmpeg(dir)
	logf("ffmpeg is ready (%s)", dir)
	return ff, fp, nil
}
