package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/anacrolix/torrent"
	bnc "github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/types"
	"golang.org/x/time/rate"
)

// The built-in BitTorrent client (github.com/anacrolix/torrent), for torrentManager: it fetches
// the files tracks ask for (only those), uploads while it downloads, doesn't seed afterwards,
// and gives up on a torrent that sends nothing for stallTimeout.

// torrentStatus is a download's state, as torrentManager reads it.
type torrentStatus struct {
	Status          string // active, paused, error, complete
	CompletedLength int64
	DownloadSpeed   int64
	Connections     int
	NumSeeders      int
	ErrorMessage    string
	Files           []torrentFileStatus
}

type torrentFileStatus struct {
	Index           int // from 1
	Path            string
	Length          int64
	CompletedLength int64 // verified data only
}

type btEngine struct {
	mu      sync.Mutex
	cl      *torrent.Client
	down    *rate.Limiter
	ts      map[string]*btTorrent
	seq     int
	paused  bool
	closed  bool
	speed   float64 // overall, smoothed
	lastAll int64
	lastAt  time.Time
	// metaUsers counts the callers waiting for each magnet's details: the client has one
	// torrent per hash, so they share it, and the last one to finish lets go of it.
	metaUsers map[metainfo.Hash]int
}

// btCompletion is the library's in-memory record of finished pieces, kept from taking the whole
// app down. A piece still being checked when its torrent ends goes on to ask for its file's
// pieces; closing the record empties it, its range lookup then returns nothing at all, and the
// library panics, in a goroutine of its own where nothing can catch it. So closing does nothing
// here (each download has its own record, gone along with it), and range lookups go through
// Get, which always has an answer.
type btCompletion struct{ storage.PieceCompletion }

func (btCompletion) Close() error { return nil }

// Persistent: nothing to flush a piece to disk for after each one.
func (btCompletion) Persistent() bool { return false }

type btTorrent struct {
	t        *torrent.Torrent
	st       storage.ClientImplCloser
	dir      string
	files    map[int]bool
	all      []*torrent.File // the torrent's files, asked for once
	read     int64           // useful bytes read when last looked
	readAt   time.Time
	progress time.Time // when data last came in
	speed    float64
	err      string
}

func newBTEngine() *btEngine {
	return &btEngine{ts: map[string]*btTorrent{}, metaUsers: map[metainfo.Hash]int{}}
}

// ensure starts the client.
func (e *btEngine) ensure(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return unavailablef("torrents are shut down")
	}
	if e.cl != nil {
		return nil
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenPort = 0 // any free one
	cfg.Seed = false
	cfg.NoUpload = false // give back while downloading
	cfg.DataDir = os.TempDir()
	e.down = rate.NewLimiter(rate.Inf, 1<<20)
	cfg.DownloadRateLimiter = e.down
	cfg.Slogger = slog.New(slog.NewTextHandler(io.Discard, nil))
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return unavailablef("can't start the torrent client: %v", err)
	}
	e.cl = cl
	return nil
}

func (e *btEngine) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cl != nil && !e.closed
}

func (e *btEngine) mi(data []byte) (*metainfo.MetaInfo, error) {
	return metainfo.Load(bytes.NewReader(data))
}

// add starts downloading files (by index) of a .torrent into dir.
func (e *btEngine) add(ctx context.Context, data []byte, dir string, files map[int]bool, paused bool) (string, error) {
	mi, err := e.mi(data)
	if err != nil {
		return "", err
	}
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return "", err
	}
	st := storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: btCompletion{storage.NewMapPieceCompletion()}})
	spec.Trackers = capTrackers(spec.Trackers)
	spec.Storage = st
	e.mu.Lock()
	cl := e.cl
	e.mu.Unlock()
	if cl == nil {
		return "", failf("the torrent client isn't running")
	}
	t, isNew, err := cl.AddTorrentSpec(spec)
	if err != nil {
		st.Close()
		return "", err
	}
	if !isNew { // its magnet's details are still being fetched: it can't become a download so
		st.Close() // it would save to the wrong place. Whoever waits for the details gets an error.
		t.Drop()
		st = storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: dir, PieceCompletion: btCompletion{storage.NewMapPieceCompletion()}})
		spec.Storage = st
		if t, _, err = cl.AddTorrentSpec(spec); err != nil {
			st.Close()
			return "", err
		}
	}
	select {
	case <-t.GotInfo():
	case <-ctx.Done():
		t.Drop()
		return "", ctx.Err()
	}
	e.mu.Lock()
	e.seq++
	id := strconv.Itoa(e.seq)
	bt := &btTorrent{t: t, st: st, dir: dir, files: files, readAt: time.Now(), progress: time.Now()}
	e.ts[id] = bt
	if paused || e.paused {
		t.DisallowDataDownload()
	}
	e.mu.Unlock()
	e.select_(bt)
	return id, nil
}

func (e *btEngine) select_(bt *btTorrent) {
	for i, f := range bt.files_() {
		if bt.files[i] {
			f.SetPriority(types.PiecePriorityNormal)
		} else {
			f.SetPriority(types.PiecePriorityNone)
		}
	}
}

// files_ lists the torrent's files. It's a long list for a discography and is read every second.
func (bt *btTorrent) files_() []*torrent.File {
	if bt.all == nil {
		bt.all = bt.t.Files()
	}
	return bt.all
}

// setFiles changes which files a download fetches; what it has stays.
func (e *btEngine) setFiles(ctx context.Context, id string, files map[int]bool) error {
	e.mu.Lock()
	bt := e.ts[id]
	e.mu.Unlock()
	if bt == nil {
		return failf("no such torrent download")
	}
	bt.files = files
	e.select_(bt)
	return nil
}

func (e *btEngine) status(ctx context.Context, id string) (*torrentStatus, error) {
	e.mu.Lock()
	bt := e.ts[id]
	paused := e.paused
	e.mu.Unlock()
	if bt == nil {
		return nil, failf("no such torrent download")
	}
	stats := bt.t.Stats()
	now := time.Now()
	read := stats.BytesReadUsefulData.Int64()
	if dt := now.Sub(bt.readAt).Seconds(); dt >= 0.5 {
		inst := float64(read-bt.read) / dt
		bt.speed = 0.6*bt.speed + 0.4*inst
		if read > bt.read {
			bt.progress = now
		}
		bt.read, bt.readAt = read, now
	}
	st := &torrentStatus{Status: "active", DownloadSpeed: int64(bt.speed), Connections: stats.ActivePeers, NumSeeders: stats.ConnectedSeeders}
	all := true
	list := bt.files_()
	for i := range bt.files { // only those: looking at each file of a big torrent every second adds up
		if i < 0 || i >= len(list) {
			continue
		}
		f := list[i]
		done := f.BytesCompleted()
		st.Files = append(st.Files, torrentFileStatus{Index: i + 1, Path: filepath.Join(bt.dir, f.Path()), Length: f.Length(), CompletedLength: done})
		st.CompletedLength += done
		all = all && done >= f.Length()
	}
	switch {
	case all:
		st.Status = "complete"
	case paused:
		st.Status = "paused"
		bt.progress = now // the stall clock doesn't run while paused
	case now.Sub(bt.progress) > stallTimeout:
		st.Status = "error"
	}
	return st, nil
}

// pauseAll pauses (or resumes) every download.
func (e *btEngine) pauseAll(ctx context.Context, p bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paused = p
	for _, bt := range e.ts {
		if p {
			bt.t.DisallowDataDownload()
		} else {
			bt.t.AllowDataDownload()
			bt.progress = time.Now()
		}
	}
	return nil
}

// forget stops a download and lets go of it (its folder is the caller's to delete).
func (e *btEngine) forget(id string) {
	e.mu.Lock()
	bt := e.ts[id]
	delete(e.ts, id)
	e.mu.Unlock()
	if bt != nil {
		bt.t.Drop()
		bt.st.Close()
	}
}

// globalSpeed is the overall download speed (bytes/s).
func (e *btEngine) globalSpeed() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cl == nil {
		return 0
	}
	cs := e.cl.Stats()
	all := cs.BytesReadUsefulData.Int64()
	now := time.Now()
	if !e.lastAt.IsZero() {
		if dt := now.Sub(e.lastAt).Seconds(); dt > 0 {
			e.speed = 0.5*e.speed + 0.5*float64(all-e.lastAll)/dt
		}
	}
	e.lastAll, e.lastAt = all, now
	return e.speed
}

// setLimit caps the overall download speed (bytes/s; 0: none).
func (e *btEngine) setLimit(bps float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.down == nil {
		return false
	}
	if bps <= 0 {
		e.down.SetLimit(rate.Inf)
	} else {
		e.down.SetLimit(rate.Limit(bps))
		e.down.SetBurst(int(math.Max(bps/4, 256<<10)))
	}
	return true
}

func (e *btEngine) close() {
	e.mu.Lock()
	e.closed = true
	cl := e.cl
	ts := e.ts
	e.ts = map[string]*btTorrent{}
	e.mu.Unlock()
	for _, bt := range ts {
		bt.t.Drop()
		bt.st.Close()
	}
	if cl != nil {
		cl.Close()
	}
}

// maxTrackers: past a handful, each more tracker adds nothing but work: the torrent library
// spent over half the app's CPU keeping track of the announces of magnets with dozens of them.
const maxTrackers = 6

// capTrackers keeps the first few trackers of a torrent, the announce list's order being
// roughly best first.
func capTrackers(tiers [][]string) [][]string {
	n := 0
	var out [][]string
	for _, tier := range tiers {
		var keep []string
		for _, tr := range tier {
			if n < maxTrackers && tr != "" {
				keep, n = append(keep, tr), n+1
			}
		}
		if len(keep) > 0 {
			out = append(out, keep)
		}
	}
	return out
}

// trimMagnet is a magnet link with at most maxTrackers trackers.
func trimMagnet(magnet string) string {
	u, err := url.Parse(magnet)
	if err != nil {
		return magnet
	}
	q := u.Query()
	if len(q["tr"]) <= maxTrackers {
		return magnet
	}
	q["tr"] = q["tr"][:maxTrackers]
	u.RawQuery = q.Encode()
	return u.String()
}

// publicTrackers are Jackett's (MagnetUtil.cs), for magnet links made from a hash alone.
var publicTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.demonii.com:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://vito-tracker.space:6969/announce",
	"udp://vito-tracker.duckdns.org:6969/announce",
	"udp://tracker.theoks.net:6969/announce",
	"udp://tracker.srv00.com:6969/announce",
	"udp://tracker.qu.ax:6969/announce",
	"udp://tracker.corpscorp.online:80/announce",
}

// downloading says whether t is one of the running downloads.
func (e *btEngine) downloading(t *torrent.Torrent) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, bt := range e.ts {
		if bt.t == t {
			return true
		}
	}
	return false
}

// metadata gets the .torrent of a magnet link from its peers.
func (e *btEngine) metadata(ctx context.Context, magnet string) ([]byte, error) {
	if err := e.ensure(ctx); err != nil {
		return nil, err
	}
	e.mu.Lock()
	cl := e.cl
	e.mu.Unlock()
	spec, err := torrent.TorrentSpecFromMagnetUri(magnet)
	if err != nil {
		return nil, fmt.Errorf("bad magnet link: %w", err)
	}
	e.mu.Lock()
	e.metaUsers[spec.InfoHash]++
	e.mu.Unlock()
	t, err := cl.AddMagnet(trimMagnet(magnet))
	if err != nil {
		e.mu.Lock()
		e.metaUsers[spec.InfoHash]--
		e.mu.Unlock()
		return nil, fmt.Errorf("bad magnet link: %w", err)
	}
	defer func() {
		e.mu.Lock()
		e.metaUsers[spec.InfoHash]--
		last := e.metaUsers[spec.InfoHash] <= 0
		if last {
			delete(e.metaUsers, spec.InfoHash)
		}
		e.mu.Unlock()
		if last && !e.downloading(t) {
			t.Drop()
		}
	}()
	if !e.downloading(t) { // a download of the same torrent must keep going
		t.DisallowDataDownload()
	}
	if len(spec.Trackers) == 0 || len(spec.Trackers[0]) < 3 { // sites often give just the hash
		t.AddTrackers([][]string{publicTrackers[:5]})
	}
	select {
	case <-t.GotInfo():
	case <-t.Closed():
		return nil, unavailablef("torrents are shut down")
	case <-ctx.Done():
		return nil, failf("no peer sent the torrent's details in time")
	}
	mi := t.Metainfo()
	return bnc.Marshal(mi)
}
