package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Torrent downloads go through one aria2c process for the whole run, driven over its JSON-RPC
// interface. Tracks that need files from the same torrent share one download: the first
// request starts it after a short pause that lets the album's other tracks join in (a little
// longer for tracks known to be on their way), and later requests are added to the running
// download. Each track gets its file as soon as that file is complete, without waiting for
// the rest.

// --- aria2c over JSON-RPC ----------------------------------------------------------------------

type aria2 struct {
	bin    string
	client *http.Client

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
	url    string
	secret string
}

func newAria2(bin string) *aria2 {
	return &aria2{bin: bin, client: &http.Client{Timeout: 30 * time.Second}}
}

// stallTimeout is how long a torrent may receive nothing before aria2c gives up on it.
const stallTimeout = 3 * time.Minute

func aria2DaemonArgs(port int, secret string) []string {
	return []string{
		"--enable-rpc=true", "--rpc-listen-all=false", "--rpc-listen-port=" + strconv.Itoa(port),
		"--rpc-secret=" + secret, "--rpc-max-request-size=64M", "--rpc-save-upload-metadata=false",
		"--stop-with-process=" + strconv.Itoa(os.Getpid()),
		"--max-concurrent-downloads=5",
		// Download only, no seeding afterwards; give up on torrents that send nothing.
		"--seed-time=0", "--bt-stop-timeout=" + strconv.Itoa(int(stallTimeout.Seconds())),
		"--bt-remove-unselected-file=true",
		"--file-allocation=none", "--disk-cache=0", "--allow-overwrite=true", "--auto-file-renaming=false",
		// Find peers faster: shorter tracker timeouts, keep looking for peers while slow,
		// and a DHT bootstrap node for torrents that allow DHT.
		"--bt-tracker-connect-timeout=15", "--bt-tracker-timeout=30", "--bt-request-peer-speed-limit=10M",
		"--dht-entry-point=dht.transmissionbt.com:6881",
		"--summary-interval=0", "--show-console-readout=false", "--console-log-level=warn", "--enable-color=false",
	}
}

// ensure starts aria2c on first use (and again if it died).
func (a *aria2) ensure(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil {
		select {
		case <-a.exited:
			debugf("aria2c exited unexpectedly; starting it again")
			a.cmd = nil
		default:
			return nil
		}
	}
	if a.bin == "" {
		return failf("torrent downloads need aria2c; install the aria2 package")
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ { // the port can be taken between picking and binding it
		if err = a.startLocked(ctx); err == nil || ctx.Err() != nil {
			return err
		}
		debugf("starting aria2c: %v", err)
	}
	return unavailablef("couldn't start aria2c: %v", err)
}

func (a *aria2) startLocked(ctx context.Context) error {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	key := make([]byte, 16)
	rand.Read(key)
	secret := hex.EncodeToString(key)

	cmd := exec.Command(a.bin, aria2DaemonArgs(port, secret)...)
	ownProcessGroup(cmd)
	out := &lineLogger{prefix: "aria2c: "}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	url := fmt.Sprintf("http://127.0.0.1:%d/jsonrpc", port)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		select {
		case <-exited:
			return errors.New("aria2c exited right after starting (details in the log)")
		case <-ctx.Done():
			cmd.Process.Kill()
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		_, err := a.rawCall(pctx, url, secret, "aria2.getVersion")
		cancel()
		if err == nil {
			a.cmd, a.exited, a.url, a.secret = cmd, exited, url, secret
			debugf("aria2c is listening on port %d", port)
			return nil
		}
	}
	cmd.Process.Kill()
	return errors.New("aria2c didn't answer on its RPC port")
}

func (a *aria2) alive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil {
		return false
	}
	select {
	case <-a.exited:
		return false
	default:
		return true
	}
}

func (a *aria2) close() {
	a.mu.Lock()
	cmd, exited, url, secret := a.cmd, a.exited, a.url, a.secret
	a.cmd = nil
	a.mu.Unlock()
	if cmd == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	a.rawCall(ctx, url, secret, "aria2.forceShutdown")
	cancel()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-exited
	}
}

func (a *aria2) call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	a.mu.Lock()
	url, secret := a.url, a.secret
	a.mu.Unlock()
	if url == "" {
		return nil, errors.New("aria2c isn't running")
	}
	return a.rawCall(ctx, url, secret, method, params...)
}

func (a *aria2) rawCall(ctx context.Context, url, secret, method string, params ...any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "method": method,
		"params": append([]any{"token:" + secret}, params...)})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("unreadable answer from aria2c to %s (HTTP %d)", method, resp.StatusCode)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("aria2c: %s", r.Error.Message)
	}
	return r.Result, nil
}

type ariaFile struct {
	Index           int    `json:"index,string"` // from 1
	Path            string `json:"path"`
	Length          int64  `json:"length,string"`
	CompletedLength int64  `json:"completedLength,string"` // whole verified pieces only
	Selected        string `json:"selected"`
}

type ariaStatus struct {
	Status          string     `json:"status"` // active, waiting, paused, error, complete, removed
	TotalLength     int64      `json:"totalLength,string"`
	CompletedLength int64      `json:"completedLength,string"`
	DownloadSpeed   int64      `json:"downloadSpeed,string"`
	Connections     int        `json:"connections,string"`
	NumSeeders      int        `json:"numSeeders,string"`
	ErrorCode       string     `json:"errorCode"`
	ErrorMessage    string     `json:"errorMessage"`
	Files           []ariaFile `json:"files"`
}

func (a *aria2) addTorrent(ctx context.Context, torrent []byte, opts map[string]string) (string, error) {
	res, err := a.call(ctx, "aria2.addTorrent", base64.StdEncoding.EncodeToString(torrent), []string{}, opts)
	if err != nil {
		return "", err
	}
	var gid string
	return gid, json.Unmarshal(res, &gid)
}

func (a *aria2) tellStatus(ctx context.Context, gid string) (*ariaStatus, error) {
	res, err := a.call(ctx, "aria2.tellStatus", gid, []string{"status", "totalLength", "completedLength",
		"downloadSpeed", "connections", "numSeeders", "errorCode", "errorMessage", "files"})
	if err != nil {
		return nil, err
	}
	var st ariaStatus
	if err := json.Unmarshal(res, &st); err != nil {
		return nil, fmt.Errorf("unreadable status from aria2c: %w", err)
	}
	return &st, nil
}

func (a *aria2) changeOption(ctx context.Context, gid string, opts map[string]string) error {
	_, err := a.call(ctx, "aria2.changeOption", gid, opts)
	return err
}

// globalSpeed is aria2c's overall download speed (0 when it isn't running).
func (m *torrentManager) globalSpeed() float64 {
	if !m.aria.alive() {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := m.aria.call(ctx, "aria2.getGlobalStat")
	if err != nil {
		return 0
	}
	var st struct {
		DownloadSpeed int64 `json:"downloadSpeed,string"`
	}
	json.Unmarshal(res, &st)
	return float64(st.DownloadSpeed)
}

// setGlobalLimit caps aria2c's overall download speed (0: no cap).
func (m *torrentManager) setGlobalLimit(bps float64) bool {
	m.mu.Lock()
	m.limit = bps
	m.mu.Unlock()
	if !m.aria.alive() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := m.aria.call(ctx, "aria2.changeGlobalOption", map[string]string{"max-overall-download-limit": strconv.FormatInt(int64(bps), 10)})
	return err == nil
}

// forget stops a download if it's still running and drops it from aria2c's memory.
func (a *aria2) forget(gid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.call(ctx, "aria2.forceRemove", gid)
	a.call(ctx, "aria2.removeDownloadResult", gid)
}

// lineLogger writes a child process's output to the debug log, line by line.
type lineLogger struct {
	prefix string
	mu     sync.Mutex
	buf    []byte
}

func (l *lineLogger) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(l.buf[:i])); line != "" {
			debugf("%s%s", l.prefix, line)
		}
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

// --- downloads shared per torrent -----------------------------------------------------------------

type torrentFetcher interface {
	// fetch downloads file index of torrentFile and puts it into dest, returning its path.
	fetch(ctx context.Context, torrentFile string, index int, dest string, task *Task) (string, error)
	close()
}

type torrentManager struct {
	aria      *aria2
	dir       string        // one folder per download in here
	timeout   time.Duration // per file, counted while its torrent is actually downloading
	gather    time.Duration // after a request, wait this long for more files of the same torrent…
	maxGather time.Duration // …but no longer than this in total
	along     time.Duration // and up to this long for the files of tracks on their way (expect)

	mu      sync.Mutex
	jobs    map[string]*torrentJob // by .torrent path
	polling bool
	closed  bool
	started int     // aria2c downloads started, for the log and tests
	added   int     // times files were added to a running download (which restarts it), for tests
	limit   float64 // overall speed limit (bytes/s), applied when aria2c (re)starts
}

type torrentJob struct {
	file  string
	id    string
	data  []byte
	names []string

	// The current aria2c download of this torrent, if any.
	gid      string
	dir      string
	selected map[int]bool   // files it downloads
	complete map[int]string // its finished files
	pending  map[int]bool   // requested files not yet handed to aria2c
	waiters  []*fileWaiter
	expected map[int]time.Time // files of tracks on their way, and until when to wait for them

	timer       *time.Timer
	gatherUntil time.Time
	rpcErrors   int
	failed      error // why the last download failed, reported to new requests for a while
	failedAt    time.Time
}

type fileWaiter struct {
	index       int
	dest        string
	task        *Task
	activeSince time.Time
	done        chan struct{}
	path        string
	err         error
}

func newTorrentManager(a *aria2, dir string, timeout time.Duration) *torrentManager {
	return &torrentManager{aria: a, dir: dir, timeout: timeout, gather: 2 * time.Second, maxGather: 8 * time.Second,
		along: 20 * time.Second, jobs: map[string]*torrentJob{}}
}

func (m *torrentManager) fetch(ctx context.Context, torrentFile string, index int, dest string, task *Task) (string, error) {
	task.setStage("starting torrent")
	fresh := !m.aria.alive()
	if err := m.aria.ensure(ctx); err != nil {
		return "", err
	}
	m.mu.Lock()
	limit := m.limit
	m.mu.Unlock()
	if fresh && limit > 0 {
		m.setGlobalLimit(limit)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ctx.Err()
	}
	job, err := m.job(torrentFile)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}
	if index < 0 || index >= len(job.names) {
		m.mu.Unlock()
		return "", failf("bad torrent file reference")
	}
	if _, ok := job.expected[index]; ok { // arrived
		delete(job.expected, index)
		if job.timer != nil {
			m.schedule(job)
		}
	}
	if p, ok := job.complete[index]; ok { // another track already got this file
		m.mu.Unlock()
		return linkInto(p, dest)
	}
	if job.gid == "" && job.failed != nil && time.Since(job.failedAt) < 10*time.Minute {
		err := job.failed
		m.mu.Unlock()
		return "", err
	}
	w := &fileWaiter{index: index, dest: dest, task: task, done: make(chan struct{})}
	job.waiters = append(job.waiters, w)
	if !job.selected[index] && !job.pending[index] {
		job.pending[index] = true
		m.schedule(job)
	}
	if job.gid == "" && job.expecting(time.Now()) > 0 {
		task.setStage("waiting for the torrent's other tracks")
	}
	if !m.polling {
		m.polling = true
		go m.pollLoop()
	}
	m.mu.Unlock()

	select {
	case <-w.done:
		return w.path, w.err
	case <-ctx.Done():
		m.mu.Lock()
		m.dropWaiter(job, w)
		m.mu.Unlock()
		return "", ctx.Err()
	}
}

func (m *torrentManager) job(torrentFile string) (*torrentJob, error) {
	if job := m.jobs[torrentFile]; job != nil {
		return job, nil
	}
	data, err := os.ReadFile(torrentFile)
	if err != nil {
		return nil, err
	}
	names, err := torrentFiles(data)
	if err != nil {
		return nil, failf("bad torrent file: %v", err)
	}
	sum := sha1.Sum(data)
	job := &torrentJob{file: torrentFile, id: hex.EncodeToString(sum[:8]), data: data, names: names,
		selected: map[int]bool{}, complete: map[int]string{}, pending: map[int]bool{}, expected: map[int]time.Time{}}
	m.jobs[torrentFile] = job
	return job, nil
}

// schedule hands the pending files to aria2c after a short pause, so that requests for the
// other files of an album arriving meanwhile go in the same download. Files of tracks known
// to be on their way (expect) are waited for a little longer: adding a file to a running
// download restarts it, and peers take a while to come back.
func (m *torrentManager) schedule(job *torrentJob) {
	now := time.Now()
	if job.timer == nil {
		job.gatherUntil = now.Add(m.maxGather)
	}
	wait := max(0, min(m.gather, job.gatherUntil.Sub(now)))
	for i, until := range job.expected {
		if !until.After(now) {
			delete(job.expected, i) // didn't come in time; it joins the download when it does
			continue
		}
		wait = max(wait, until.Sub(now))
	}
	if job.timer == nil {
		job.timer = time.AfterFunc(wait, func() { m.apply(job) })
		return
	}
	job.timer.Reset(wait)
}

// expecting counts the files of tracks still on their way.
func (job *torrentJob) expecting(now time.Time) int {
	n := 0
	for _, until := range job.expected {
		if until.After(now) {
			n++
		}
	}
	return n
}

// expect says that a track is on its way for file index of torrentFile (it was found in
// there while another track's download was starting), so that the download waits a little
// for it.
func (m *torrentManager) expect(torrentFile string, index int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	job, err := m.job(torrentFile)
	if err != nil || index < 0 || index >= len(job.names) || job.selected[index] || job.pending[index] {
		return
	}
	if _, ok := job.complete[index]; ok {
		return
	}
	job.expected[index] = time.Now().Add(m.along)
	if job.timer != nil {
		m.schedule(job)
	}
}

// unexpect takes that back: the track was found elsewhere, or cancelled.
func (m *torrentManager) unexpect(torrentFile string, index int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[torrentFile]
	if job == nil {
		return
	}
	if _, ok := job.expected[index]; !ok {
		return
	}
	delete(job.expected, index)
	if job.timer != nil {
		m.schedule(job)
	}
}

func (m *torrentManager) apply(job *torrentJob) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.timer = nil
	if m.closed || len(job.pending) == 0 {
		return
	}
	if job.gid == "" {
		m.startDownload(job)
		return
	}
	// Add the files to the running download. aria2c restarts it, keeping what it already has.
	all := map[int]bool{}
	for i := range job.selected {
		all[i] = true
	}
	for i := range job.pending {
		all[i] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.aria.changeOption(ctx, job.gid, map[string]string{"select-file": indexList(all)}); err != nil {
		// Most likely it just finished; the files then go into a new download (see endDownload).
		debugf("torrent %s: adding files to download %s: %v", job.id, job.gid, err)
		return
	}
	debugf("torrent %s: now downloading files %s", job.id, indexList(all))
	m.added++
	job.selected = all
	job.pending = map[int]bool{}
}

func (m *torrentManager) startDownload(job *torrentJob) {
	job.dir = filepath.Join(m.dir, fmt.Sprintf("%s.%d", job.id, m.started+1))
	files := indexList(job.pending)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gid, err := m.aria.addTorrent(ctx, job.data, map[string]string{"dir": job.dir, "select-file": files})
	if err != nil {
		debugf("torrent %s: aria2c refused it: %v", job.id, err)
		m.failWaiters(job, func(w *fileWaiter) bool { return job.pending[w.index] }, failf("aria2c refused the torrent: %v", err))
		job.pending, job.dir = map[int]bool{}, ""
		return
	}
	debugf("torrent %s: download %s started with files %s", job.id, gid, files)
	m.started++
	job.gid, job.selected, job.complete, job.pending, job.rpcErrors = gid, job.pending, map[int]string{}, map[int]bool{}, 0
	job.failed = nil
}

func (m *torrentManager) pollLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		for _, job := range m.jobs {
			switch {
			case job.gid != "":
				m.poll(job)
			case len(job.waiters) == 0 && len(job.pending) == 0 && job.timer == nil && job.expecting(now) == 0 &&
				(job.failed == nil || now.Sub(job.failedAt) > 10*time.Minute):
				delete(m.jobs, job.file) // nothing to do (e.g. expected tracks that went elsewhere)
			}
		}
		m.mu.Unlock()
	}
}

func (m *torrentManager) poll(job *torrentJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := m.aria.tellStatus(ctx, job.gid)
	cancel()
	if err != nil {
		job.rpcErrors++
		debugf("torrent %s: status of %s: %v", job.id, job.gid, err)
		if !m.aria.alive() || job.rpcErrors >= 10 {
			m.endDownload(job, unavailablef("aria2c stopped responding"))
		}
		return
	}
	job.rpcErrors = 0
	now := time.Now()
	files := map[int]ariaFile{}
	for _, f := range st.Files {
		files[f.Index-1] = f
		if job.selected[f.Index-1] && (f.Length > 0 && f.CompletedLength >= f.Length || st.Status == "complete") {
			job.complete[f.Index-1] = f.Path
		}
	}
	note := fmt.Sprintf("torrent %s/s · %d peers", fmtBytes(st.DownloadSpeed), st.Connections)
	if st.NumSeeders > 0 {
		note += fmt.Sprintf(" (%d seeding)", st.NumSeeders)
	}
	for _, w := range append([]*fileWaiter(nil), job.waiters...) {
		if !job.selected[w.index] {
			w.task.setStage("joining the torrent")
			continue
		}
		f := files[w.index]
		switch {
		case st.Status == "waiting" || st.Status == "paused":
			w.task.setStage("queued behind other torrents")
		case st.CompletedLength == 0:
			w.task.setStage("looking for peers")
			w.task.setNote(note)
		default:
			w.task.setStage("downloading")
			w.task.setProgress(f.CompletedLength, f.Length)
			w.task.share(job.id, strconv.Itoa(w.index), float64(st.DownloadSpeed))
			w.task.setNote(note)
		}
		if st.Status == "active" && w.activeSince.IsZero() {
			w.activeSince = now
		}
		if p, ok := job.complete[w.index]; ok {
			m.resolve(job, w, p, nil)
		} else if !w.activeSince.IsZero() && now.Sub(w.activeSince) > m.timeout {
			m.resolve(job, w, "", failf("torrent download didn't finish within %s (raise --torrent-timeout?)", m.timeout))
		}
	}
	switch st.Status {
	case "complete":
		m.endDownload(job, nil)
	case "error", "removed":
		m.endDownload(job, downloadError(st))
	default:
		if len(job.waiters) == 0 && len(job.pending) == 0 && len(job.complete) == len(job.selected) {
			m.endDownload(job, nil) // all its files are done; aria2c just hasn't said so yet
		} else if len(job.waiters) == 0 && len(job.pending) == 0 { // every track waiting for it gave up
			debugf("torrent %s: download %s no longer needed", job.id, job.gid)
			m.aria.forget(job.gid)
			os.RemoveAll(job.dir)
			delete(m.jobs, job.file)
		}
	}
}

func downloadError(st *ariaStatus) error {
	switch {
	case st.Status == "removed":
		return failf("the torrent download was cancelled")
	case st.ErrorMessage != "":
		return failf("aria2c: %s", st.ErrorMessage)
	case st.CompletedLength == 0:
		return failf("no peer sent any data for %s (dead torrent?)", fmtWait(stallTimeout))
	}
	return failf("the torrent download stalled (no data for %s)", fmtWait(stallTimeout))
}

// endDownload wraps up the current download: waiters still without their file get err (or a
// generic one), and files requested meanwhile go into a new download.
func (m *torrentManager) endDownload(job *torrentJob, err error) {
	if err == nil {
		err = failf("aria2c finished the torrent without this file")
	}
	m.failWaiters(job, func(w *fileWaiter) bool { return job.selected[w.index] }, err)
	m.aria.forget(job.gid)
	os.RemoveAll(job.dir)
	failed := err != nil && len(job.complete) < len(job.selected)
	debugf("torrent %s: download %s ended (%d of %d files)", job.id, job.gid, len(job.complete), len(job.selected))
	job.gid, job.dir, job.selected, job.complete = "", "", map[int]bool{}, map[int]string{}
	if failed {
		job.failed, job.failedAt = err, time.Now()
		m.failWaiters(job, func(w *fileWaiter) bool { return true }, err)
		job.pending = map[int]bool{}
	}
	if len(job.pending) > 0 {
		if job.timer != nil {
			job.timer.Stop()
			job.timer = nil
		}
		m.startDownload(job)
	}
	if job.gid == "" && len(job.waiters) == 0 && job.failed == nil {
		delete(m.jobs, job.file)
	}
}

func (m *torrentManager) failWaiters(job *torrentJob, which func(*fileWaiter) bool, err error) {
	for _, w := range append([]*fileWaiter(nil), job.waiters...) {
		if which(w) {
			m.resolve(job, w, "", err)
		}
	}
}

// resolve hands a waiter its file (linked into its own folder) or an error.
func (m *torrentManager) resolve(job *torrentJob, w *fileWaiter, path string, err error) {
	if err == nil {
		w.path, w.err = linkInto(path, w.dest)
	} else {
		w.err = err
	}
	m.removeWaiter(job, w)
	close(w.done)
}

func (m *torrentManager) removeWaiter(job *torrentJob, w *fileWaiter) {
	for i, x := range job.waiters {
		if x == w {
			job.waiters = append(job.waiters[:i], job.waiters[i+1:]...)
			return
		}
	}
}

// dropWaiter forgets a request whose track was cancelled.
func (m *torrentManager) dropWaiter(job *torrentJob, w *fileWaiter) {
	m.removeWaiter(job, w)
	for _, x := range job.waiters {
		if x.index == w.index {
			return
		}
	}
	delete(job.pending, w.index)
}

func (m *torrentManager) close() {
	m.mu.Lock()
	m.closed = true
	for _, job := range m.jobs {
		if job.timer != nil {
			job.timer.Stop()
		}
	}
	m.mu.Unlock()
	m.aria.close()
}

// linkInto puts a hard link to (or else a copy of) src into dir. The download folder is
// deleted as soon as its torrent is done, so tracks get their own copy.
func linkInto(src, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return dst, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return dst, err
}

// indexList formats 0-based file indices the way aria2c's select-file wants them (1-based).
func indexList(set map[int]bool) string {
	var idx []int
	for i := range set {
		idx = append(idx, i+1)
	}
	sort.Ints(idx)
	parts := make([]string, len(idx))
	for i, n := range idx {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
