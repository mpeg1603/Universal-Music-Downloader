package core

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// File transfers. Downloading: we ask the user to queue the file (QueueUpload); when it's our
// turn they send a TransferRequest, we accept, and they open a file connection, send the
// token, we answer with the offset and the file follows. Uploading is the same with the
// roles swapped.

// --- downloads ------------------------------------------------------------------------------

type slskDlEvent struct {
	kind   string // offered, place, denied, failed
	place  int
	reason string
}

type slskDownload struct {
	user, path string
	size       int64
	token      uint32
	offered    bool
	started    time.Time
	events     chan slskDlEvent
	conn       chan *slskConn

	mu          sync.Mutex
	state       string
	done, total int64
	speed       float64
}

func (d *slskDownload) set(state string) {
	d.mu.Lock()
	d.state = state
	d.mu.Unlock()
}

func dlKey(user, path string) string { return user + "\x00" + path }

func (c *slskClient) downloadEvent(user, path string, ev slskDlEvent) {
	c.mu.Lock()
	d := c.downloads[dlKey(user, path)]
	c.mu.Unlock()
	if d == nil {
		return
	}
	select {
	case d.events <- ev:
	default:
	}
}

// transferOffered answers a user who's ready to send us a file: yes if we asked for it.
func (c *slskClient) transferOffered(sc *slskConn, token uint32, path string, size int64) {
	c.mu.Lock()
	d := c.downloads[dlKey(sc.user, path)]
	if d != nil && !d.offered {
		d.offered, d.token = true, token
		if size > 0 {
			d.size = size
		}
	} else {
		d = nil
	}
	c.mu.Unlock()
	if d == nil {
		// Not (or no longer) wanted.
		go sc.send(pmTransferResponse, (&slskMsg{}).u32(token).boolean(false).str("Cancelled"))
		return
	}
	go sc.send(pmTransferResponse, (&slskMsg{}).u32(token).boolean(true))
	select {
	case d.events <- slskDlEvent{kind: "offered"}:
	default:
	}
}

// fileConn takes a file connection a user opened to send us a file.
func (c *slskClient) fileConn(sc *slskConn) {
	sc.c.SetReadDeadline(time.Now().Add(30 * time.Second))
	var b [4]byte
	if _, err := io.ReadFull(sc.r, b[:]); err != nil {
		sc.close()
		return
	}
	sc.c.SetReadDeadline(time.Time{})
	token := binary.LittleEndian.Uint32(b[:])
	c.mu.Lock()
	var d *slskDownload
	for _, x := range c.downloads {
		if x.user == sc.user && x.offered && x.token == token {
			d = x
		}
	}
	c.mu.Unlock()
	if d == nil {
		sc.close()
		return
	}
	select {
	case d.conn <- sc:
	default:
		sc.close()
	}
}

// lockUser makes downloads from one user go one at a time.
func (c *slskClient) lockUser(ctx context.Context, user string) (func(), error) {
	for {
		c.mu.Lock()
		ch := c.userLocks[user]
		if ch == nil {
			ch = make(chan struct{})
			c.userLocks[user] = ch
			c.mu.Unlock()
			return func() {
				c.mu.Lock()
				delete(c.userLocks, user)
				c.mu.Unlock()
				close(ch)
			}, nil
		}
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// busyUsers are the users we're downloading from (or waiting for) now.
func (c *slskClient) busyUsers() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.userLocks))
	for u := range c.userLocks {
		out[u] = true
	}
	return out
}

// Download fetches path (size bytes) from user into dest. It waits for one of the few
// Soulseek download slots, then in the user's queue for at most the configured time.
func (c *slskClient) Download(ctx context.Context, user, path string, size int64, dest string, task *Task) error {
	task.park() // waiting here needs none of the app's download slots
	task.setStage("waiting for a Soulseek download slot")
	// One file at a time from a user: wait for that before taking one of the few slots.
	unlock, err := c.lockUser(ctx, user)
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.dlGate.acquire(ctx, 0); err != nil {
		return err
	}
	defer c.dlGate.release()
	if !c.waitOnline(ctx, 45*time.Second) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return unavailablef("%s", c.offlineReason())
	}
	d := &slskDownload{user: user, path: path, size: size, events: make(chan slskDlEvent, 16), conn: make(chan *slskConn, 1),
		state: "asking to queue", total: size}
	key := dlKey(user, path)
	c.mu.Lock()
	if c.downloads[key] != nil {
		c.mu.Unlock()
		return failf("already downloading this file")
	}
	c.downloads[key] = d
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.downloads, key)
		c.mu.Unlock()
	}()
	name := path[strings.LastIndexAny(path, `\/`)+1:]
	task.setStage("asking " + user + " for the file")
	if err := c.sendPeer(ctx, user, pmQueueUpload, (&slskMsg{}).str(path)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return failf("%s: %v", user, err)
	}
	wait := c.config().QueueTimeout()
	giveUp := time.NewTimer(wait)
	defer giveUp.Stop()
	askPlace := time.NewTimer(15 * time.Second)
	defer askPlace.Stop()
	var connWait <-chan time.Time
	var partial int64 // bytes we have from a transfer that broke off
	retried := false
	// again asks once more after their side failed (as Nicotine+ does); a transfer that broke
	// off goes on from where it stopped.
	again := func(why string) error {
		if retried {
			return failf("%s", why)
		}
		retried = true
		c.mu.Lock()
		d.offered = false
		c.mu.Unlock()
		connWait = nil
		giveUp.Reset(wait)
		d.set("queued again")
		task.setStage("asking " + user + " again")
		c.debugf("%s; asking %s again", why, user)
		return c.sendPeer(ctx, user, pmQueueUpload, (&slskMsg{}).str(path))
	}
	d.set("queued")
	task.setStage("waiting in " + user + "'s Soulseek queue")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-giveUp.C:
			c.mu.Lock()
			offered := d.offered
			c.mu.Unlock()
			if offered {
				continue
			}
			return failf("%s didn't start sending within %s (queue too long)", user, FmtWait(wait))
		case <-askPlace.C:
			c.sendPeer(ctx, user, pmPlaceInQueueRequest, (&slskMsg{}).str(path))
			askPlace.Reset(3 * time.Minute)
		case <-connWait:
			if err := again(fmt.Sprintf("%s couldn't connect to send %s", user, name)); err != nil {
				return err
			}
		case ev := <-d.events:
			switch ev.kind {
			case "offered":
				d.set("connecting")
				task.setStage("connecting to " + user)
				connWait = time.After(90 * time.Second)
			case "place":
				task.setNote(fmt.Sprintf("number %d in %s's queue", ev.place, user))
				d.set(fmt.Sprintf("queued (#%d)", ev.place))
			case "denied":
				return failf("%s refused: %s", user, strings.TrimSuffix(ev.reason, "."))
			case "failed":
				if err := again(fmt.Sprintf("%s's upload of %s failed", user, name)); err != nil {
					return err
				}
			}
		case fc := <-d.conn:
			got, err := c.receive(ctx, fc, d, dest, partial, task)
			if err == nil || ctx.Err() != nil {
				return err
			}
			if got > partial {
				partial = got
			}
			if err := again(fmt.Sprintf("%s: %v", user, err)); err != nil {
				return err
			}
		}
	}
}

// receive reads the file from a file connection, going on from have bytes (a transfer that
// broke off); it returns how much of the file is there.
func (c *slskClient) receive(ctx context.Context, fc *slskConn, d *slskDownload, dest string, have int64, task *Task) (int64, error) {
	defer fc.close()
	stop := context.AfterFunc(ctx, fc.close)
	defer stop()
	if have > d.size { // the file changed on their side
		have = 0
	}
	var off [8]byte
	binary.LittleEndian.PutUint64(off[:], uint64(have))
	if err := fc.write(off[:]); err != nil {
		return have, errors.New("the file connection closed")
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return have, err
	}
	defer f.Close()
	if err := f.Truncate(have); err != nil {
		return 0, err
	}
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		return 0, err
	}
	d.set("downloading")
	d.started = time.Now()
	task.setStage("downloading from Soulseek")
	task.setNote("from " + d.user)
	r := bw.Reader(ctx, fc.r)
	buf := make([]byte, 64<<10)
	done := have
	for done < d.size {
		fc.c.SetReadDeadline(time.Now().Add(90 * time.Second))
		n, err := r.Read(buf[:min(int64(len(buf)), d.size-done)])
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			task.setProgress(done, d.size)
			d.mu.Lock()
			d.done = done
			if el := time.Since(d.started).Seconds(); el > 0 {
				d.speed = float64(done-have) / el
			}
			d.mu.Unlock()
		}
		if err != nil {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			if done < d.size {
				return done, fmt.Errorf("the transfer stopped at %s of %s", FmtBytes(done), FmtBytes(d.size))
			}
		}
	}
	d.set("done")
	return done, f.Close()
}

// --- uploads --------------------------------------------------------------------------------------

type slskUpload struct {
	User, Path string // the virtual path the user asked for
	real       string
	Size       int64
	Queued     time.Time
	State      string // queued, requesting, connecting, sending, done, failed: …
	Sent       int64
	Speed      float64
	Privileged bool
	token      uint32
	resp       chan slskUpResp
	cancel     context.CancelFunc
	finished   time.Time
}

type slskUpResp struct {
	allowed bool
	reason  string
}

type slskUploads struct {
	c          *slskClient
	mu         sync.Mutex
	queue      []*slskUpload // waiting, in arrival order
	active     map[uint32]*slskUpload
	recent     []*slskUpload // finished, newest last
	lastServed map[string]time.Time
	speeds     []float64 // of recent uploads (bytes/s), for the average we report
	kick       chan struct{}
}

func newSlskUploads(c *slskClient) *slskUploads {
	return &slskUploads{c: c, active: map[uint32]*slskUpload{}, lastServed: map[string]time.Time{}, kick: make(chan struct{}, 1)}
}

func (u *slskUploads) wake() {
	select {
	case u.kick <- struct{}{}:
	default:
	}
}

// enqueue queues a file a user asked for; it returns why not, if not ("Queued" if it is already).
func (u *slskUploads) enqueue(user, vpath string) string {
	cfg := u.c.config()
	if cfg.NoShare {
		return "File not shared."
	}
	if ContainsFold(cfg.Banned, user) {
		return "Banned"
	}
	real, size, ok := u.c.shares.lookup(vpath)
	if !ok {
		return "File not shared."
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	files, bytes := 0, int64(0)
	for _, x := range u.queue {
		if x.User == user {
			if x.Path == vpath {
				return "Queued"
			}
			files++
			bytes += x.Size
		}
	}
	for _, x := range u.active {
		if x.User == user && x.Path == vpath {
			return "Queued"
		}
	}
	if files >= cfg.FilesPerUser() {
		return "Too many files"
	}
	if bytes+size > int64(cfg.MBPerUser())<<20 {
		return "Too many megabytes"
	}
	if len(u.queue) >= 5000 {
		return "Too many files"
	}
	u.queue = append(u.queue, &slskUpload{User: user, Path: vpath, real: real, Size: size, Queued: time.Now(), State: "queued",
		Privileged: u.c.isPrivileged(user)})
	u.c.debugf("%s queued %s", user, vpath)
	u.wake()
	return ""
}

// place is a queued file's place in our queue (0: not queued).
func (u *slskUploads) place(user, vpath string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	order := u.orderLocked()
	for i, x := range order {
		if x.User == user && x.Path == vpath {
			return i + 1
		}
	}
	return 0
}

// orderLocked is the queue in the order it will be served: privileged users first, then
// round robin by who was served longest ago (Nicotine+'s default).
func (u *slskUploads) orderLocked() []*slskUpload {
	out := append([]*slskUpload(nil), u.queue...)
	firstOf := map[string]int{}
	for _, x := range out {
		if _, ok := firstOf[x.User]; !ok {
			firstOf[x.User] = len(firstOf)
		}
	}
	nth := map[*slskUpload]int{}
	count := map[string]int{}
	for _, x := range out {
		nth[x] = count[x.User]
		count[x.User]++
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Privileged != b.Privileged {
			return a.Privileged
		}
		if nth[a] != nth[b] {
			return nth[a] < nth[b]
		}
		la, lb := u.lastServed[a.User], u.lastServed[b.User]
		if !la.Equal(lb) {
			return la.Before(lb)
		}
		return firstOf[a.User] < firstOf[b.User]
	})
	return out
}

// info returns the upload slots, the queue length and whether a slot is free.
func (u *slskUploads) info() (slots, queued int, free bool) {
	cfg := u.c.config()
	if cfg.NoShare {
		return 0, 0, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	slots = cfg.Slots()
	return slots, len(u.queue), len(u.active) < slots
}

func (u *slskUploads) avgSpeed() float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.speeds) == 0 {
		return 0
	}
	var sum float64
	for _, s := range u.speeds {
		sum += s
	}
	return sum / float64(len(u.speeds))
}

func (u *slskUploads) response(user string, token uint32, allowed bool, reason string) {
	u.mu.Lock()
	x := u.active[token]
	u.mu.Unlock()
	if x == nil || x.User != user {
		return
	}
	select {
	case x.resp <- slskUpResp{allowed, reason}:
	default:
	}
}

// loop starts queued uploads when slots are free.
func (u *slskUploads) loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		u.startNext(ctx)
		select {
		case <-ctx.Done():
			u.mu.Lock()
			for _, x := range u.active {
				if x.cancel != nil {
					x.cancel()
				}
			}
			u.mu.Unlock()
			return
		case <-t.C:
		case <-u.kick:
		}
	}
}

func (u *slskUploads) startNext(ctx context.Context) {
	if !u.c.online() {
		return
	}
	slots := u.c.config().Slots()
	u.mu.Lock()
	defer u.mu.Unlock()
	for len(u.active) < slots && len(u.queue) > 0 {
		busy := map[string]bool{}
		for _, x := range u.active {
			busy[x.User] = true
		}
		var next *slskUpload
		for _, x := range u.orderLocked() {
			if !busy[x.User] {
				next = x
				break
			}
		}
		if next == nil {
			return
		}
		for i, x := range u.queue {
			if x == next {
				u.queue = append(u.queue[:i], u.queue[i+1:]...)
				break
			}
		}
		next.token = u.c.nextToken()
		next.State, next.resp = "requesting", make(chan slskUpResp, 1)
		uctx, cancel := context.WithCancel(ctx)
		next.cancel = cancel
		u.active[next.token] = next
		u.c.wg.Add(1)
		go func(x *slskUpload) {
			defer u.c.wg.Done()
			defer cancel()
			err := u.c.upload(uctx, x)
			u.mu.Lock()
			delete(u.active, x.token)
			u.lastServed[x.User] = time.Now()
			x.finished = time.Now()
			switch {
			case err == nil:
				x.State = "done"
			case uctx.Err() != nil && ctx.Err() == nil:
				x.State = "cancelled"
			default:
				x.State = "failed: " + err.Error()
			}
			u.recent = append(u.recent, x)
			if len(u.recent) > 100 {
				u.recent = u.recent[len(u.recent)-100:]
			}
			u.mu.Unlock()
			if err != nil {
				u.c.debugf("upload of %s to %s: %v", x.Path, x.User, err)
			} else {
				u.c.logf("uploaded %s to %s", path.Base(strings.ReplaceAll(x.Path, `\`, "/")), x.User)
			}
			u.wake()
		}(next)
	}
}

func (u *slskUploads) setState(x *slskUpload, state string) {
	u.mu.Lock()
	x.State = state
	u.mu.Unlock()
}

// upload sends a queued file to its user.
func (c *slskClient) upload(ctx context.Context, x *slskUpload) error {
	u := c.ups
	if err := c.sendPeer(ctx, x.User, pmTransferRequest, (&slskMsg{}).u32(1).u32(x.token).str(x.Path).u64(uint64(x.Size))); err != nil {
		return err
	}
	select {
	case r := <-x.resp:
		if !r.allowed {
			return fmt.Errorf("they declined (%s)", FirstNonEmpty(r.reason, "no reason"))
		}
	case <-time.After(60 * time.Second):
		return errors.New("they didn't answer")
	case <-ctx.Done():
		return ctx.Err()
	}
	u.setState(x, "connecting")
	fc, err := c.connect(ctx, x.User, "F", nil)
	if err != nil {
		return err
	}
	defer fc.close()
	stop := context.AfterFunc(ctx, fc.close)
	defer stop()
	var tb [4]byte
	binary.LittleEndian.PutUint32(tb[:], x.token)
	if err := fc.write(tb[:]); err != nil {
		return err
	}
	fc.c.SetReadDeadline(time.Now().Add(60 * time.Second))
	var ob [8]byte
	if _, err := io.ReadFull(fc.r, ob[:]); err != nil {
		return errors.New("they didn't say where to start")
	}
	offset := int64(binary.LittleEndian.Uint64(ob[:]))
	if offset < 0 || offset > x.Size {
		offset = 0
	}
	f, err := os.Open(x.real)
	if err != nil {
		c.sendPeer(ctx, x.User, pmUploadFailed, (&slskMsg{}).str(x.Path))
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	u.setState(x, "sending")
	start := time.Now()
	buf := make([]byte, 32<<10)
	sent := offset
	for sent < x.Size {
		n, rerr := f.Read(buf[:min(int64(len(buf)), x.Size-sent)])
		if n > 0 {
			if err := c.upLimit.wait(ctx, n); err != nil {
				return err
			}
			fc.c.SetWriteDeadline(time.Now().Add(90 * time.Second))
			if _, err := fc.c.Write(buf[:n]); err != nil {
				if ctx.Err() == nil {
					c.sendPeer(c.ctx, x.User, pmUploadFailed, (&slskMsg{}).str(x.Path))
				}
				return errors.New("the connection closed")
			}
			sent += int64(n)
			u.mu.Lock()
			x.Sent = sent
			if el := time.Since(start).Seconds(); el > 0 {
				x.Speed = float64(sent-offset) / el
			}
			u.mu.Unlock()
		}
		if rerr != nil && sent < x.Size {
			c.sendPeer(c.ctx, x.User, pmUploadFailed, (&slskMsg{}).str(x.Path))
			return fmt.Errorf("reading the file: %v", rerr)
		}
	}
	// The downloader closes the connection when it has everything.
	fc.c.SetReadDeadline(time.Now().Add(60 * time.Second))
	io.Copy(io.Discard, fc.r)
	if el := time.Since(start).Seconds(); el > 0 && sent > offset {
		speed := float64(sent-offset) / el
		u.mu.Lock()
		u.speeds = append(u.speeds, speed)
		if len(u.speeds) > 20 {
			u.speeds = u.speeds[1:]
		}
		u.mu.Unlock()
		c.sendServer(ssSendUploadSpeed, (&slskMsg{}).u32(uint32(speed)))
	}
	return nil
}

// Abort cancels an upload in progress or drops a queued one.
func (u *slskUploads) Abort(user, vpath string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, x := range u.queue {
		if x.User == user && x.Path == vpath {
			u.queue = append(u.queue[:i], u.queue[i+1:]...)
			x.State, x.finished = "cancelled", time.Now()
			u.recent = append(u.recent, x)
			return
		}
	}
	for _, x := range u.active {
		if x.User == user && x.Path == vpath && x.cancel != nil {
			x.cancel()
		}
	}
}

// dropUser removes everything a (now banned) user queued.
func (u *slskUploads) dropUser(user string) {
	u.mu.Lock()
	var keep []*slskUpload
	for _, x := range u.queue {
		if !strings.EqualFold(x.User, user) {
			keep = append(keep, x)
		}
	}
	u.queue = keep
	for _, x := range u.active {
		if strings.EqualFold(x.User, user) && x.cancel != nil {
			x.cancel()
		}
	}
	u.mu.Unlock()
}

// --- the transfer list ------------------------------------------------------------------------

type SlskTransferView struct {
	Up          bool
	User, File  string
	State       string
	Done, Total int64
	Speed       float64
	Privileged  bool
}

// Transfers lists downloads in progress, then active, queued and recent uploads.
func (c *slskClient) Transfers() []SlskTransferView {
	var out []SlskTransferView
	c.mu.Lock()
	for _, d := range c.downloads {
		d.mu.Lock()
		out = append(out, SlskTransferView{User: d.user, File: d.path, State: d.state, Done: d.done, Total: d.total, Speed: d.speed})
		d.mu.Unlock()
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	u := c.ups
	u.mu.Lock()
	var act []*slskUpload
	for _, x := range u.active {
		act = append(act, x)
	}
	sort.Slice(act, func(i, j int) bool { return act[i].Queued.Before(act[j].Queued) })
	add := func(x *slskUpload) {
		out = append(out, SlskTransferView{Up: true, User: x.User, File: x.Path, State: x.State, Done: x.Sent, Total: x.Size,
			Speed: x.Speed, Privileged: x.Privileged})
	}
	for _, x := range act {
		add(x)
	}
	for _, x := range u.orderLocked() {
		add(x)
	}
	for i := len(u.recent) - 1; i >= 0; i-- {
		add(u.recent[i])
	}
	u.mu.Unlock()
	return out
}

// UploadCounts returns the uploads sending now and waiting.
func (c *slskClient) UploadCounts() (active, queued int, speed float64) {
	u := c.ups
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, x := range u.active {
		if x.State == "sending" {
			speed += x.Speed
		}
	}
	return len(u.active), len(u.queue), speed
}

// --- upload speed limit ---------------------------------------------------------------------------

// slskThrottle is a token bucket for all uploads together.
type slskThrottle struct {
	mu     sync.Mutex
	rate   float64 // bytes/s; 0: unlimited
	tokens float64
	last   time.Time
}

func (t *slskThrottle) setRate(bps float64) {
	t.mu.Lock()
	t.rate = max(0, bps)
	t.mu.Unlock()
}

func (t *slskThrottle) wait(ctx context.Context, n int) error {
	t.mu.Lock()
	if t.rate <= 0 {
		t.mu.Unlock()
		return nil
	}
	now := time.Now()
	burst := max(t.rate/4, 32<<10)
	if t.last.IsZero() {
		t.last, t.tokens = now, burst
	}
	t.tokens = min(t.tokens+t.rate*now.Sub(t.last).Seconds(), burst)
	t.last = now
	t.tokens -= float64(n)
	var d time.Duration
	if t.tokens < 0 {
		d = time.Duration(-t.tokens / t.rate * float64(time.Second))
	}
	t.mu.Unlock()
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
