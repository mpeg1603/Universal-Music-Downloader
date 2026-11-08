package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// External programs: ffmpeg/ffprobe (installed, or fetched: ffmpeg_fetch.go) and yt-dlp
// (fetched).

type Tools struct {
	FFmpeg, FFprobe, YtDlp string
	JSRuntimes             []string // passed to yt-dlp, which needs one for YouTube
}

// findBaseTools finds ffmpeg and ffprobe (installed, or fetched before into the cache) and a
// JavaScript runtime. Without ffmpeg, FFmpeg stays empty: the manager fetches it (fetchFFmpeg)
// before anything needs it. yt-dlp is fetched separately when a source needs it.
func findBaseTools() *Tools {
	t := &Tools{}
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 == nil && err2 == nil {
		t.FFmpeg, t.FFprobe = ff, fp
	} else if ff, fp, ok := fetchedFFmpeg(ffmpegDir()); ok {
		t.FFmpeg, t.FFprobe = ff, fp
	}
	for _, rt := range []string{"deno", "node", "bun"} {
		if _, err := exec.LookPath(rt); err == nil {
			t.JSRuntimes = append(t.JSRuntimes, rt)
		}
	}
	return t
}

func ffmpegDir() string { return filepath.Join(defaultCacheDir(), "bin", "ffmpeg") }

// errNoFFmpeg is what needs ffmpeg says when it's not there.
func errNoFFmpeg() error {
	return unavailablef("ffmpeg isn't available: it couldn't be downloaded (see the Log; it's tried again at the next start), and it isn't installed")
}

func lookPath(name string) (string, bool) {
	p, err := exec.LookPath(name)
	return p, err == nil
}

func ytdlpAsset() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "yt-dlp_linux", nil
	case "linux/arm64":
		return "yt-dlp_linux_aarch64", nil
	case "darwin/amd64", "darwin/arm64":
		return "yt-dlp_macos", nil
	case "windows/amd64":
		return "yt-dlp.exe", nil
	case "windows/arm64":
		return "yt-dlp_arm64.exe", nil
	}
	return "", fmt.Errorf("no official yt-dlp build for %s/%s; install yt-dlp and pass --yt-dlp", runtime.GOOS, runtime.GOARCH)
}

// ensureYtDlp keeps the official standalone yt-dlp build in dir, refreshing it weekly
// (YouTube changes often and old versions stop working). Downloads are checked against the
// release's SHA2-256SUMS file.
func ensureYtDlp(ctx context.Context, dir string, logf func(string, ...any)) (string, error) {
	asset, err := ytdlpAsset()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, asset)
	if info, err := os.Stat(dest); err == nil && time.Since(info.ModTime()) < 7*24*time.Hour {
		return dest, nil
	}
	logf("Downloading the latest yt-dlp…")
	base := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	client := &http.Client{Timeout: 5 * time.Minute}
	fetch := func(u string) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		return io.ReadAll(resp.Body)
	}
	sums, err := fetch(base + "SHA2-256SUMS")
	if err != nil {
		return keepOld(dest, err)
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return keepOld(dest, fmt.Errorf("%s missing from the release checksums", asset))
	}
	bin, err := fetch(base + asset)
	if err != nil {
		return keepOld(dest, err)
	}
	if got := sha256.Sum256(bin); hex.EncodeToString(got[:]) != want {
		return keepOld(dest, fmt.Errorf("checksum mismatch for the downloaded yt-dlp"))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", err
	}
	return dest, os.Rename(tmp, dest)
}

func keepOld(dest string, err error) (string, error) {
	if _, serr := os.Stat(dest); serr == nil {
		return dest, nil
	}
	return "", err
}

func defaultCacheDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "universal-music-downloader")
	}
	return filepath.Join(os.TempDir(), "universal-music-downloader")
}

// command is exec.CommandContext for the external programs a track's processing runs.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	ownProcessGroup(cmd)
	return cmd
}
