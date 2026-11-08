package core

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

// Torrent downloads go through the built-in client (torrent_engine.go), one for the whole
// run. Tracks that need files from the same torrent share one download: the first
// request starts it after a short pause that lets the album's other tracks join in (a little
// longer for tracks known to be on their way), and later requests are added to the running
// download. Each track gets its file as soon as that file is complete, without waiting for
// the rest.

// jumpStall: a track that waits this long for a torrent that sends nothing goes on to later
// sources, when queue jumping allows (see Manager.mayJump).
const jumpStall = 3 * time.Minute

// stallTimeout is how long a torrent may receive nothing before it's given up on.
const stallTimeout = 3 * time.Minute

// globalSpeed is the overall torrent download speed (bytes/s).
func (m *torrentManager) globalSpeed() float64 { return m.eng.globalSpeed() }

// setGlobalLimit caps the overall torrent download speed (0: no cap).
func (m *torrentManager) setGlobalLimit(bps float64) bool {
	m.mu.Lock()
	m.limit = bps
	m.mu.Unlock()
	return m.eng.setLimit(bps)
}

type torrentFetcher interface {
	// fetch downloads file index of torrentFile and puts it into dest, returning its path.
	fetch(ctx context.Context, torrentFile string, index int, dest string, task *Task) (string, error)
	close()
}

type torrentManager struct {
	eng       *btEngine
	dir       string        // one folder per download in here
	timeout   time.Duration // per file, counted while its torrent is actually downloading
	gather    time.Duration // after a request, wait this long for more files of the same torrent…
	maxGather time.Duration // …but no longer than this in total
	along     time.Duration // and up to this long for the files of tracks on their way (expect)

	mu      sync.Mutex
	jobs    map[string]*torrentJob // by the torrent's hash: two sites can offer the same one
	hashes  map[string]string      // .torrent path → the torrent's hash
	polling bool
	closed  bool
	started int     // downloads started, for the log and tests
	added   int     // times files were added to a running download (which restarts it), for tests
	limit   float64 // overall speed limit (bytes/s), applied when the client starts
	paused  bool    // everything paused (by you); downloads started meanwhile start paused
	// jump says whether a track may stop waiting for a torrent that sends nothing (queue
	// jumping; see Manager.mayJump). nil: wait until the time limit.
	jump func() bool
}

type torrentJob struct {
	key   string // its hash, in torrentManager.jobs
	id    string
	data  []byte
	names []string

	// The current download of this torrent, if any.
	gid      string
	dir      string
	selected map[int]bool   // files it downloads
	complete map[int]string // its finished files
	pending  map[int]bool   // requested files not yet handed to the client
	waiters  []*fileWaiter
	expected map[int]time.Time // files of tracks on their way, and until when to wait for them

	timer       *time.Timer
	gatherUntil time.Time
	rpcErrors   int
	failed      error // why the last download failed, reported to new requests for a while
	failedAt    time.Time
}

type fileWaiter struct {
	since       time.Time // when it was asked for
	index       int
	dest        string
	task        *Task
	activeSince time.Time
	done        chan struct{}
	path        string
	err         error
}

func newTorrentManager(eng *btEngine, dir string, timeout time.Duration) *torrentManager {
	return &torrentManager{eng: eng, dir: dir, timeout: timeout, gather: 2 * time.Second, maxGather: 8 * time.Second,
		along: 20 * time.Second, jobs: map[string]*torrentJob{}, hashes: map[string]string{}}
}

func (m *torrentManager) fetch(ctx context.Context, torrentFile string, index int, dest string, task *Task) (string, error) {
	task.setWaitStage("starting torrent")
	fresh := !m.eng.alive()
	if err := m.eng.ensure(ctx); err != nil {
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
	w := &fileWaiter{since: time.Now(), index: index, dest: dest, task: task, done: make(chan struct{})}
	job.waiters = append(job.waiters, w)
	if !job.selected[index] && !job.pending[index] {
		job.pending[index] = true
		m.schedule(job)
	}
	if job.gid == "" && job.expecting(time.Now()) > 0 {
		task.setWaitStage("waiting for the torrent's other tracks")
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
	if job := m.jobs[m.hashes[torrentFile]]; job != nil {
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
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, failf("bad torrent file: %v", err)
	}
	key := mi.HashInfoBytes().HexString()
	m.hashes[torrentFile] = key
	if job := m.jobs[key]; job != nil { // the same torrent, from another site
		return job, nil
	}
	sum := sha1.Sum(data)
	job := &torrentJob{key: key, id: hex.EncodeToString(sum[:8]), data: data, names: names,
		selected: map[int]bool{}, complete: map[int]string{}, pending: map[int]bool{}, expected: map[int]time.Time{}}
	m.jobs[key] = job
	return job, nil
}

// schedule hands the pending files to the client after a short pause, so that requests for the
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
	job := m.jobs[m.hashes[torrentFile]]
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
	// Add the files to the running download, which keeps what it already has.
	all := map[int]bool{}
	for i := range job.selected {
		all[i] = true
	}
	for i := range job.pending {
		all[i] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.eng.setFiles(ctx, job.gid, all); err != nil {
		// Most likely it just finished; the files then go into a new download (see endDownload).
		debugf("torrent %s: adding files to download %s: %v", job.id, job.gid, err)
		return
	}
	debugf("torrent %s: now downloading files %s", job.id, indexList(all))
	m.added++
	job.selected = all
	job.pending = map[int]bool{}
}

// setPaused pauses (or resumes) every torrent download. Their time limits don't run while
// they're paused.
func (m *torrentManager) setPaused(p bool) {
	m.mu.Lock()
	m.paused = p
	running := false
	for _, job := range m.jobs {
		running = running || job.gid != ""
	}
	m.mu.Unlock()
	if !running || !m.eng.alive() {
		return
	}
	m.eng.pauseAll(context.Background(), p)
}

func (m *torrentManager) startDownload(job *torrentJob) {
	job.dir = filepath.Join(m.dir, fmt.Sprintf("%s.%d", job.id, m.started+1))
	files := indexList(job.pending)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	gid, err := m.eng.add(ctx, job.data, job.dir, job.pending, m.paused)
	if err != nil {
		debugf("torrent %s: the client refused it: %v", job.id, err)
		m.failWaiters(job, func(w *fileWaiter) bool { return job.pending[w.index] }, failf("the torrent client refused the torrent: %v", err))
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
				delete(m.jobs, job.key) // nothing to do (e.g. expected tracks that went elsewhere)
			}
		}
		m.mu.Unlock()
	}
}

func (m *torrentManager) poll(job *torrentJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	st, err := m.eng.status(ctx, job.gid)
	cancel()
	if err != nil {
		job.rpcErrors++
		debugf("torrent %s: status of %s: %v", job.id, job.gid, err)
		if !m.eng.alive() || job.rpcErrors >= 10 {
			m.endDownload(job, unavailablef("the torrent client stopped"))
		}
		return
	}
	job.rpcErrors = 0
	now := time.Now()
	files := map[int]torrentFileStatus{}
	for _, f := range st.Files {
		files[f.Index-1] = f
		if job.selected[f.Index-1] && (f.Length > 0 && f.CompletedLength >= f.Length || st.Status == "complete") {
			job.complete[f.Index-1] = f.Path
		}
	}
	note := fmt.Sprintf("torrent %s/s · %d peers", FmtBytes(st.DownloadSpeed), st.Connections)
	if st.NumSeeders > 0 {
		note += fmt.Sprintf(" (%d seeding)", st.NumSeeders)
	}
	for _, w := range append([]*fileWaiter(nil), job.waiters...) {
		if !job.selected[w.index] {
			w.task.setWaitStage("joining the torrent")
			continue
		}
		f := files[w.index]
		switch {
		case m.paused:
			w.task.setWaitStage("paused")
			w.activeSince = time.Time{} // the time limit starts again on resume
		case st.Status == "waiting" || st.Status == "paused":
			w.task.setWaitStage("queued behind other torrents")
		case st.CompletedLength == 0:
			w.task.setWaitStage("looking for peers")
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
		} else if st.CompletedLength == 0 && !m.paused && now.Sub(w.since) > jumpStall && m.jump != nil && m.jump() {
			m.resolve(job, w, "", busyf("no peer sent anything for %s; bandwidth is to spare, so later sources go first", FmtWait(jumpStall)))
		} else if !m.paused && !w.activeSince.IsZero() && now.Sub(w.activeSince) > m.timeout {
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
			m.endDownload(job, nil) // all its files are done
		} else if len(job.waiters) == 0 && len(job.pending) == 0 { // every track waiting for it gave up
			debugf("torrent %s: download %s no longer needed", job.id, job.gid)
			m.eng.forget(job.gid)
			os.RemoveAll(job.dir)
			delete(m.jobs, job.key)
		}
	}
}

func downloadError(st *torrentStatus) error {
	switch {
	case st.Status == "removed":
		return failf("the torrent download was cancelled")
	case st.ErrorMessage != "":
		return failf("torrent: %s", st.ErrorMessage)
	case st.CompletedLength == 0:
		return failf("no peer sent any data for %s (dead torrent?)", FmtWait(stallTimeout))
	}
	return failf("the torrent download stalled (no data for %s)", FmtWait(stallTimeout))
}

// endDownload wraps up the current download: waiters still without their file get err (or a
// generic one), and files requested meanwhile go into a new download.
func (m *torrentManager) endDownload(job *torrentJob, err error) {
	if err == nil {
		err = failf("the torrent finished without this file")
	}
	m.failWaiters(job, func(w *fileWaiter) bool { return job.selected[w.index] }, err)
	m.eng.forget(job.gid)
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
		delete(m.jobs, job.key)
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
	m.eng.close()
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

// indexList lists 0-based file indices as 1-based numbers, for the log.
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
