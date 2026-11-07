package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Soulseek, through your own slskd (https://github.com/slskd/slskd): a file-sharing network
// where people share their music folders, often in FLAC. slskd runs the Soulseek client with
// your account; this talks to its web API (address + API key) and picks up the finished
// files from slskd's downloads folder (slskd keeps its copy there).

type SlskdConfig struct {
	URL       string `json:"url,omitempty"`       // e.g. http://localhost:5030
	APIKey    string `json:"api_key,omitempty"`   // one of slskd's web.authentication.api_keys
	Downloads string `json:"downloads,omitempty"` // slskd's directories.downloads
}

func (c SlskdConfig) configured() bool { return c.URL != "" && c.APIKey != "" }

func defaultSlskdDownloads() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "slskd", "downloads")
}

type Slskd struct {
	base      string
	key       string
	downloads string
	lossless  bool
	client    *http.Client
	postMu    sync.Mutex    // slskd takes one new search at a time
	poll      time.Duration // how often to look at searches and transfers
	// queueWait: give up on a user who hasn't started sending the file after this long.
	queueWait time.Duration
}

func newSlskd(cfg SlskdConfig, format, route string) *Slskd {
	return &Slskd{base: strings.TrimRight(cfg.URL, "/"), key: cfg.APIKey,
		downloads: firstNonEmpty(cleanPath(cfg.Downloads), defaultSlskdDownloads()),
		lossless:  format == "flac" || format == "original",
		client:    &http.Client{Transport: router.Transport(route), Timeout: 30 * time.Second},
		poll:      time.Second, queueWait: 10 * time.Minute}
}

func (s *Slskd) Name() string        { return "slskd" }
func (s *Slskd) Label() string       { return "Soulseek" }
func (s *Slskd) MaxQuality() Quality { return Quality{Codec: "flac", Lossless: true} }

// call sends a request to slskd's API; out (if not nil) receives the JSON answer.
func (s *Slskd) call(ctx context.Context, method, p string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+"/api/v0/"+p, rd)
	if err != nil {
		return 0, failf("bad slskd address: %v", err)
	}
	req.Header.Set("X-API-Key", s.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, unavailablef("slskd unreachable at %s (%s)", s.base, describeNetErr(err))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return resp.StatusCode, unavailablef("slskd refused the API key (HTTP %d); check it in Sources → Soulseek", resp.StatusCode)
	case resp.StatusCode == 429:
		return resp.StatusCode, unavailablef("slskd is busy (HTTP 429)")
	case resp.StatusCode >= 500:
		return resp.StatusCode, unavailablef("slskd answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	case resp.StatusCode >= 300:
		return resp.StatusCode, failf("slskd answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data[:min(len(data), 200)])))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, failf("slskd sent an unreadable answer")
		}
	}
	return resp.StatusCode, nil
}

type slskdFile struct {
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	BitRate    int    `json:"bitRate"`
	BitDepth   int    `json:"bitDepth"`
	SampleRate int    `json:"sampleRate"`
	Length     int    `json:"length"`
	IsLocked   bool   `json:"isLocked"`
}

type slskdResponse struct {
	Username          string      `json:"username"`
	Files             []slskdFile `json:"files"`
	HasFreeUploadSlot bool        `json:"hasFreeUploadSlot"`
	QueueLength       int64       `json:"queueLength"`
	UploadSpeed       int64       `json:"uploadSpeed"`
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *Slskd) Search(ctx context.Context, t *Track) ([]Candidate, error) {
	// Soulseek matches every word against file paths, so plain words work best.
	var queries []string
	for _, q := range songQueries(t, 2) {
		queries = append(queries, queryText(q))
	}
	return searchUntilFound(ctx, t, queries, func(q string) ([]Candidate, error) {
		responses, err := s.search(ctx, q)
		if err != nil {
			return nil, err
		}
		return s.candidates(t, responses), nil
	})
}

// search runs one search on the network and returns who answered with what.
func (s *Slskd) search(ctx context.Context, text string) ([]slskdResponse, error) {
	id := newUUID()
	s.postMu.Lock()
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var status int
		status, err = s.call(ctx, "POST", "searches", map[string]any{"id": id, "searchText": text, "searchTimeout": 8,
			"responseLimit": 50, "fileLimit": 2000, "filterResponses": true, "minimumResponseFileCount": 1}, nil)
		if status != 429 {
			break
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
	s.postMu.Unlock()
	if err != nil {
		return nil, err
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		s.call(dctx, "DELETE", "searches/"+id, nil, nil)
	}()
	deadline := time.Now().Add(45 * time.Second)
	for {
		var st struct {
			IsComplete bool   `json:"isComplete"`
			State      string `json:"state"`
		}
		if _, err := s.call(ctx, "GET", "searches/"+id, nil, &st); err != nil {
			return nil, err
		}
		if st.IsComplete || strings.Contains(st.State, "Completed") || time.Now().After(deadline) {
			break
		}
		select {
		case <-time.After(s.poll):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var responses []slskdResponse
	if _, err := s.call(ctx, "GET", "searches/"+id+"/responses", nil, &responses); err != nil {
		return nil, err
	}
	return responses, nil
}

// candidates picks, from each user's answer, the file that fits the track best. Users who
// can send right away, and quickly, come first (the matcher keeps that order for equal scores),
// and the wanted kind of file (lossless or not) before the other.
func (s *Slskd) candidates(t *Track, responses []slskdResponse) []Candidate {
	type pick struct {
		c    Candidate
		r    slskdResponse
		loss bool
	}
	var picks []pick
	for _, r := range responses {
		var names []string
		var files []slskdFile
		for _, f := range r.Files {
			if !f.IsLocked && audioExts[strings.ToLower(path.Ext(slskdPath(f.Filename)))] {
				names = append(names, slskdPath(f.Filename))
				files = append(files, f)
			}
		}
		i, stem := bestFile(t, names)
		if i < 0 {
			continue
		}
		f := files[i]
		q := fileQuality(names[i], f.Size, float64(f.Length))
		if f.BitRate > 0 && !q.Lossless {
			q.Kbps = f.BitRate
		}
		if f.BitDepth > 0 {
			q.BitDepth = f.BitDepth
		}
		dir := path.Dir(names[i])
		parts := strings.Split(dir, "/")
		c := Candidate{Source: "slskd", URL: "soulseek://" + r.Username + "/" + names[i], Title: stem,
			Artist: strings.Join(parts[max(0, len(parts)-3):], " "), Album: path.Base(dir), Duration: float64(f.Length),
			Ref: r.Username + "\x00" + f.Filename + "\x00" + fmt.Sprint(f.Size), Quality: q}
		picks = append(picks, pick{c, r, q.Lossless})
	}
	sort.SliceStable(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if a.loss != b.loss {
			return a.loss == s.lossless
		}
		if a.r.HasFreeUploadSlot != b.r.HasFreeUploadSlot {
			return a.r.HasFreeUploadSlot
		}
		if a.r.QueueLength != b.r.QueueLength {
			return a.r.QueueLength < b.r.QueueLength
		}
		return a.r.UploadSpeed > b.r.UploadSpeed
	})
	var out []Candidate
	for _, p := range picks[:min(len(picks), 40)] {
		out = append(out, p.c)
	}
	return out
}

// slskdPath turns a Soulseek path (Windows-style, "@@abcd\Music\Album\01 Song.flac") into a
// slash-separated one.
func slskdPath(name string) string { return strings.ReplaceAll(name, `\`, "/") }

type slskdTransfer struct {
	ID               string  `json:"id"`
	Filename         string  `json:"filename"`
	State            string  `json:"state"`
	Size             int64   `json:"size"`
	BytesTransferred int64   `json:"bytesTransferred"`
	AverageSpeed     float64 `json:"averageSpeed"`
	Exception        string  `json:"exception"`
}

// transfer finds the transfer of filename from user in slskd.
func (s *Slskd) transfer(ctx context.Context, user, filename string) (*slskdTransfer, error) {
	var res struct {
		Directories []struct {
			Files []slskdTransfer `json:"files"`
		} `json:"directories"`
	}
	status, err := s.call(ctx, "GET", "transfers/downloads/"+url.PathEscape(user), nil, &res)
	if status == 404 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, d := range res.Directories {
		for i := range d.Files {
			if d.Files[i].Filename == filename {
				return &d.Files[i], nil
			}
		}
	}
	return nil, nil
}

func (s *Slskd) Download(ctx context.Context, c Candidate, t *Track, workdir string) (string, error) {
	parts := strings.SplitN(c.Ref, "\x00", 3)
	if len(parts) != 3 {
		return "", failf("bad Soulseek reference")
	}
	user, filename := parts[0], parts[1]
	var size int64
	fmt.Sscan(parts[2], &size)
	if _, err := s.call(ctx, "POST", "transfers/downloads/"+url.PathEscape(user),
		[]map[string]any{{"filename": filename, "size": size}}, nil); err != nil {
		return "", err
	}
	task := taskFrom(ctx)
	task.park() // waiting in someone's upload queue needs no download slot
	got, err := s.await(ctx, user, filename, size, task)
	if rerr := task.resume(ctx); err == nil {
		err = rerr
	}
	if err != nil {
		return "", err
	}
	return linkInto(got, workdir)
}

// await follows the transfer until it's done and returns where slskd put the file.
func (s *Slskd) await(ctx context.Context, user, filename string, size int64, task *Task) (string, error) {
	start := time.Now()
	var id string
	cancel := func() {
		if id == "" {
			return
		}
		dctx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		s.call(dctx, "DELETE", "transfers/downloads/"+url.PathEscape(user)+"/"+id+"?remove=true", nil, nil)
	}
	started := false
	for {
		select {
		case <-time.After(s.poll):
		case <-ctx.Done():
			cancel()
			return "", ctx.Err()
		}
		tr, err := s.transfer(ctx, user, filename)
		if err != nil {
			if ctx.Err() != nil {
				cancel()
				return "", ctx.Err()
			}
			if isUnavailable(err) {
				continue
			}
			return "", err
		}
		if tr == nil {
			if time.Since(start) > time.Minute {
				return "", failf("slskd lost the transfer of %s", path.Base(slskdPath(filename)))
			}
			continue
		}
		id = tr.ID
		st := tr.State
		switch {
		case strings.Contains(st, "Succeeded"):
			return s.pickUp(filename, size)
		case strings.Contains(st, "Completed"):
			why := strings.TrimSpace(strings.TrimPrefix(st, "Completed,"))
			if tr.Exception != "" {
				why += ": " + tr.Exception
			}
			return "", failf("%s's Soulseek transfer ended (%s)", user, why)
		case strings.Contains(st, "InProgress") || tr.BytesTransferred > 0:
			started = true
			task.setStage("downloading from Soulseek")
			task.setProgress(tr.BytesTransferred, tr.Size)
			task.setNote(fmt.Sprintf("%s/s from %s", fmtBytes(int64(tr.AverageSpeed)), user))
		default:
			task.setStage("waiting in " + user + "'s Soulseek queue")
			if !started && time.Since(start) > s.queueWait {
				cancel()
				return "", failf("%s didn't start sending within %s (queue too long)", user, fmtWait(s.queueWait))
			}
		}
	}
}

// pickUp finds the finished file in slskd's downloads folder: in a folder named after the
// remote one, or wherever slskd's settings put it.
func (s *Slskd) pickUp(filename string, size int64) (string, error) {
	base := path.Base(slskdPath(filename))
	dir := path.Base(path.Dir(slskdPath(filename)))
	var found string
	for _, p := range []string{filepath.Join(s.downloads, dir, base), filepath.Join(s.downloads, base)} {
		if st, err := os.Stat(p); err == nil && (size == 0 || st.Size() == size) {
			found = p
			break
		}
	}
	if found == "" {
		var newest time.Time
		filepath.WalkDir(s.downloads, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != base {
				return nil
			}
			if info, err := d.Info(); err == nil && (size == 0 || info.Size() == size) && info.ModTime().After(newest) {
				found, newest = p, info.ModTime()
			}
			return nil
		})
	}
	if found == "" {
		return "", failf("slskd finished %s, but it's not in %s (set slskd's downloads folder in Sources → Soulseek)", base, s.downloads)
	}
	return found, nil
}
