package core

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A Soulseek client built into the app: it logs in with your account, searches, downloads,
// shares your music (uploads, search answers, browsing), joins the distributed search
// network, chats, keeps a wishlist and gives privileged users their place in the upload
// queue. The protocol is documented in Nicotine+'s doc/SLSKPROTOCOL.md; the behaviour here
// follows Nicotine+'s defaults where the protocol leaves a choice.
//
// Being a good citizen of the network matters (the server bans abusive clients): searches
// are paced and few at a time, only a handful of files are requested at once and at most one
// per user, nobody is asked twice for a file they refused, and the server connection isn't
// retried in a tight loop.

const (
	SlskDefaultServer = "server.slsknet.org:2242"
	SlskDefaultPort   = 2234
	// Major version 177 is the one set aside for new clients (doc: "Major Versions"); the
	// established clients' numbers must not be impersonated.
	slskMajor = 177
	slskMinor = 1

	slskLimitServer = 64 << 20
	slskLimitPeer   = 64 << 20
	slskLimitDist   = 64 << 10
)

// Server message codes used here.
const (
	ssLogin             = 1
	ssSetWaitPort       = 2
	ssGetPeerAddress    = 3
	ssWatchUser         = 5
	ssGetUserStatus     = 7
	ssSayChatroom       = 13
	ssJoinRoom          = 14
	ssLeaveRoom         = 15
	ssUserJoinedRoom    = 16
	ssUserLeftRoom      = 17
	ssConnectToPeer     = 18
	ssMessageUser       = 22
	ssMessageAcked      = 23
	ssFileSearch        = 26
	ssSetStatus         = 28
	ssServerPing        = 32
	ssSharedFoldersFile = 35
	ssGetUserStats      = 36
	ssRelogged          = 41
	ssRoomList          = 64
	ssAdminMessage      = 66
	ssPrivilegedUsers   = 69
	ssHaveNoParent      = 71
	ssParentMinSpeed    = 83
	ssParentSpeedRatio  = 84
	ssAddToPrivileged   = 91
	ssCheckPrivileges   = 92
	ssEmbeddedMessage   = 93
	ssAcceptChildren    = 100
	ssPossibleParents   = 102
	ssWishlistSearch    = 103
	ssWishlistInterval  = 104
	ssSendUploadSpeed   = 121
	ssBranchLevel       = 126
	ssBranchRoot        = 127
	ssResetDistributed  = 130
	ssExcludedPhrases   = 160
	ssCantConnectToPeer = 1001
	ssCantCreateRoom    = 1003
)

// Peer message codes.
const (
	pmSharedFileListRequest  = 4
	pmSharedFileListResponse = 5
	pmFileSearchResponse     = 9
	pmUserInfoRequest        = 15
	pmUserInfoResponse       = 16
	pmFolderContentsRequest  = 36
	pmFolderContentsResponse = 37
	pmTransferRequest        = 40
	pmTransferResponse       = 41
	pmQueueUpload            = 43
	pmPlaceInQueueResponse   = 44
	pmUploadFailed           = 46
	pmUploadDenied           = 50
	pmPlaceInQueueRequest    = 51
)

// slskConn is a connection to the server or to a peer.
type slskConn struct {
	c    net.Conn
	r    *bufio.Reader
	user string // the peer's name
	typ  string // P, F or D
	wmu  sync.Mutex
	once sync.Once
	done chan struct{}
}

func newSlskConn(c net.Conn) *slskConn {
	if tc, ok := c.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}
	return &slskConn{c: c, r: bufio.NewReaderSize(c, 64<<10), done: make(chan struct{})}
}

func (sc *slskConn) write(p []byte) error {
	sc.wmu.Lock()
	defer sc.wmu.Unlock()
	sc.c.SetWriteDeadline(time.Now().Add(60 * time.Second))
	_, err := sc.c.Write(p)
	if err != nil {
		sc.close()
	}
	return err
}

func (sc *slskConn) send(code uint32, m *slskMsg) error { return sc.write(slskFrame(code, 4, m.b)) }

func (sc *slskConn) close() {
	sc.once.Do(func() {
		close(sc.done)
		sc.c.Close()
	})
}

func (sc *slskConn) closed() bool {
	select {
	case <-sc.done:
		return true
	default:
		return false
	}
}

type slskAddr struct {
	ip   net.IP
	port int
	at   time.Time
}

func (a slskAddr) String() string { return net.JoinHostPort(a.ip.String(), strconv.Itoa(a.port)) }
func (a slskAddr) ok() bool       { return a.port > 0 && a.ip != nil && !a.ip.IsUnspecified() }

// slskConnReq waits for a peer to answer our indirect connection request (PierceFirewall).
type slskConnReq struct {
	user, typ string
	ch        chan *slskConn
	cant      chan struct{}
	cantOnce  sync.Once
}

type slskDial struct {
	done chan struct{}
	sc   *slskConn
	err  error
}

// SlskState is the connection to the Soulseek server, as a frontend shows it.
type SlskState struct {
	Online     bool
	Connecting bool
	Problem    string // why it's offline
	Permanent  bool   // offline until you reconnect (bad password, logged in elsewhere)
	User       string
	Server     string
	Since      time.Time
	PublicIP   string
	Port       int
	PortNote   string // the router port mapping
	Privileges time.Duration
	Supporter  bool
	MOTD       string
	Away       bool
}

type slskClient struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	shares    *slskShares
	chat      *slskChat
	dlGate    *gate
	upLimit   *slskThrottle
	reconnect chan struct{}

	mu         sync.Mutex
	cfg        SoulseekConfig
	route      string
	state      SlskState
	srv        *slskConn
	user       string
	ln         net.Listener
	upnp       *upnpMapping
	token      uint32
	privileged map[string]bool
	addrs      map[string]slskAddr
	addrWait   map[string][]chan slskAddr
	pending    map[uint32]*slskConnReq
	peers      map[string]*slskConn
	dialing    map[string]*slskDial
	searches   map[uint32]*slskSearch
	folderWait map[uint32]chan []slskFile // FolderContents waiting for answers
	browseWait map[string]chan []slskFile // Browse waiting for users' file lists
	infoWait   map[string]chan *slskRead  // UserInfo waiting for users' answers
	serverWait map[string][]chan *slskRead
	users      map[string]*SlskUser
	speeds     map[string]float64 // the last download speed from each user (bytes/s)
	downloads  map[string]*slskDownload
	userLocks  map[string]chan struct{}
	ups        *slskUploads
	excluded   []string
	wishEvery  time.Duration
	wishFn     func() (string, func([]slskResponse))
	wishNext   time.Time
	dist       slskDist

	budget     *slskSearchBudget // how often searches may go out
	answers    *slskSearchCache  // searches made in the last hour, and their answers
	notBefore  time.Time         // the first login waits until then (a new client after a settings change)
	banFile    string            // where a ban's end is kept across restarts ("": not kept)
	bannedTill time.Time         // the server banned us until then: don't connect before
	searchMin  time.Duration     // a search collects answers for at least this long
	searchMax  time.Duration     // and at most this long
	searchSem  chan struct{}
	answerSem  chan struct{}
	handshakes chan struct{}

	searchReqs, searchHits, searchesSent atomic.Int64
}

func newSlskClient(cfg SoulseekConfig, route string, shares *slskShares) *slskClient {
	ctx, cancel := context.WithCancel(context.Background())
	c := &slskClient{ctx: ctx, cancel: cancel, cfg: cfg, route: route, shares: shares, chat: newSlskChat(),
		dlGate: newGate(cfg.MaxDownloads()), upLimit: &slskThrottle{}, reconnect: make(chan struct{}, 1),
		token: rand.Uint32N(1 << 30), privileged: map[string]bool{}, addrs: map[string]slskAddr{},
		addrWait: map[string][]chan slskAddr{}, pending: map[uint32]*slskConnReq{}, peers: map[string]*slskConn{},
		dialing: map[string]*slskDial{}, searches: map[uint32]*slskSearch{}, folderWait: map[uint32]chan []slskFile{}, browseWait: map[string]chan []slskFile{}, speeds: map[string]float64{}, downloads: map[string]*slskDownload{},
		userLocks: map[string]chan struct{}{}, wishEvery: 12 * time.Minute,
		budget: newSlskSearchBudget(cfg.SearchInterval(), cfg.SearchesPerHour()), answers: &slskSearchCache{}, searchMin: 4 * time.Second, searchMax: 12 * time.Second, searchSem: make(chan struct{}, 2),
		answerSem: make(chan struct{}, 12), handshakes: make(chan struct{}, 64)}
	c.ups = newSlskUploads(c)
	c.upLimit.setRate(float64(cfg.UploadKBps) * 1024)
	c.state.Server = cfg.ServerAddr()
	return c
}

func (c *slskClient) logf(format string, args ...any) { Logf("Soulseek: "+format, args...) }
func (c *slskClient) debugf(format string, args ...any) {
	debugf("Soulseek: "+format, args...)
}

// Start listens for peers and logs in, in the background.
func (c *slskClient) Start() {
	c.listen()
	c.wg.Add(3)
	go func() { defer c.wg.Done(); c.run() }()
	go func() { defer c.wg.Done(); c.ups.loop(c.ctx) }()
	go func() { defer c.wg.Done(); c.wishlistLoop() }()
}

// Close logs out and stops every transfer.
func (c *slskClient) Close() {
	c.cancel()
	c.mu.Lock()
	if c.srv != nil {
		c.srv.close()
	}
	if c.ln != nil {
		c.ln.Close()
	}
	for _, p := range c.peers {
		p.close()
	}
	c.dist.closeAll()
	m := c.upnp
	c.upnp = nil
	c.mu.Unlock()
	if m != nil {
		m.remove()
	}
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// apply takes settings that don't need a new login (the manager makes a new client for the others).
func (c *slskClient) apply(cfg SoulseekConfig) {
	c.mu.Lock()
	old := c.cfg
	c.cfg = cfg
	c.mu.Unlock()
	c.dlGate.setLimit(cfg.MaxDownloads())
	c.upLimit.setRate(float64(cfg.UploadKBps) * 1024)
	c.budget.set(cfg.SearchInterval(), cfg.SearchesPerHour())
	if fmt.Sprint(old.Rooms) != fmt.Sprint(cfg.Rooms) {
		for _, r := range cfg.Rooms {
			if !ContainsString(old.Rooms, r) {
				c.JoinRoom(r)
			}
		}
	}
	if old.NoShare != cfg.NoShare {
		c.sendShareCounts()
	}
	diff := func(a, b []string) []string { // in b, not in a
		var out []string
		for _, x := range b {
			if !ContainsString(a, x) {
				out = append(out, x)
			}
		}
		return out
	}
	for _, x := range diff(old.Likes, cfg.Likes) {
		c.Interest(x, false, false)
	}
	for _, x := range diff(cfg.Likes, old.Likes) {
		c.Interest(x, false, true)
	}
	for _, x := range diff(old.Hates, cfg.Hates) {
		c.Interest(x, true, false)
	}
	for _, x := range diff(cfg.Hates, old.Hates) {
		c.Interest(x, true, true)
	}
	for _, b := range diff(old.Buddies, cfg.Buddies) {
		c.Watch(b)
	}
	c.ups.wake()
}

func (c *slskClient) config() SoulseekConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

func (c *slskClient) State() SlskState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *slskClient) online() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.srv != nil
}

func (c *slskClient) nextToken() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token++
	return c.token
}

func (c *slskClient) isPrivileged(user string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.privileged[user]
}

func (c *slskClient) isBanned(user string) bool {
	return ContainsFold(c.config().Banned, user)
}

// isIgnored: their private and room messages aren't shown (they may still download from you).
func (c *slskClient) isIgnored(user string) bool {
	return ContainsFold(c.config().Ignored, user)
}

func ContainsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

// Reconnect logs in again after a permanent problem (bad password, logged in elsewhere).
func (c *slskClient) Reconnect() {
	select {
	case c.reconnect <- struct{}{}:
	default:
	}
}

func (c *slskClient) dial(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return router.Dial(ctx, c.route, "tcp", addr)
}

// --- listening --------------------------------------------------------------------------------

// listen opens the port peers connect to (the first free one from the setting on), and asks
// the router to forward it when UPnP is on.
func (c *slskClient) listen() {
	cfg := c.config()
	base := cfg.ListenPort()
	var ln net.Listener
	var err error
	// With the "direct" route, connections from peers come in through your router, not your
	// VPN: the listening socket is bound to the physical interface so the answers go back the
	// same way (otherwise they'd leave through the VPN's tunnel and never arrive).
	var lc net.ListenConfig
	if NormalizeRoute(c.route) == RouteDirect {
		if iface, err := router.DirectInterface(); err == nil {
			d := &net.Dialer{}
			bindToInterface(d, iface)
			lc.Control = d.Control
		}
	}
	for p := base; p < base+10 && p < 65536 && (p == base || base > 0); p++ {
		if ln, err = lc.Listen(c.ctx, "tcp4", fmt.Sprintf(":%d", p)); err == nil {
			break
		}
	}
	if ln == nil {
		c.logf("can't listen for peers on ports %d-%d (%v); only peers that accept connections can be reached", base, base+9, err)
		return
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if port != base {
		c.logf("port %d is in use (another Soulseek client?); listening on %d", base, port)
	}
	c.mu.Lock()
	c.ln, c.state.Port = ln, port
	c.mu.Unlock()
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.acceptLoop(ln) }()
	if !cfg.NoUPnP {
		c.wg.Add(1)
		go func() { defer c.wg.Done(); c.mapPort(port) }()
	}
}

func (c *slskClient) listenPort() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Port
}

func (c *slskClient) mapPort(port int) {
	for c.ctx.Err() == nil {
		m, err := upnpMap(c.ctx, port, "Soulseek (universal-music-downloader)", 2*time.Hour)
		c.mu.Lock()
		if err != nil {
			c.state.PortNote = "router port mapping (UPnP) failed: " + err.Error()
		} else {
			c.upnp = m
			c.state.PortNote = fmt.Sprintf("port %d forwarded by your router (UPnP, external IP %s)", port, m.externalIP)
		}
		c.mu.Unlock()
		if err != nil {
			c.debugf("UPnP: %v", err)
		}
		select {
		case <-time.After(time.Hour):
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *slskClient) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if c.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		select {
		case c.handshakes <- struct{}{}:
		default:
			conn.Close() // too many half-open connections
			continue
		}
		go c.incoming(conn, func() { <-c.handshakes })
	}
}

// incoming reads the first message of a connection a peer made to us; done is called when
// that's over.
func (c *slskClient) incoming(conn net.Conn, done func()) {
	sc := newSlskConn(conn)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	code, body, err := readSlskFrame(sc.r, 1, 4096)
	done()
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	r := &slskRead{b: body}
	switch code {
	case 1: // PeerInit
		user, typ := r.str(), r.str()
		r.u32()
		if r.err != nil || user == "" || len(user) > 256 {
			conn.Close()
			return
		}
		sc.user, sc.typ = user, typ
		c.serve(sc)
	case 0: // PierceFireWall: the answer to our indirect connection request
		token := r.u32()
		c.mu.Lock()
		req := c.pending[token]
		if req != nil {
			sc.user, sc.typ = req.user, req.typ
			select {
			case req.ch <- sc:
				c.mu.Unlock()
				return
			default:
			}
		}
		c.mu.Unlock()
		conn.Close()
	default:
		conn.Close()
	}
}

// serve handles a connection that's set up (the peer and its type known).
func (c *slskClient) serve(sc *slskConn) {
	switch sc.typ {
	case "P":
		c.mu.Lock()
		c.peers[sc.user] = sc
		c.mu.Unlock()
		c.readPeer(sc)
	case "F":
		c.fileConn(sc)
	case "D":
		// We don't take distributed children (AcceptChildren false): nothing to send them.
		sc.close()
	default:
		sc.close()
	}
}

// --- the server ---------------------------------------------------------------------------------

func (c *slskClient) setState(f func(*SlskState)) {
	c.mu.Lock()
	f(&c.state)
	c.mu.Unlock()
}

// The server asks clients not to connect and disconnect quickly: the first retry comes after
// 30 seconds, then twice as long each time, up to 30 minutes; a login the server doesn't
// answer (a ban, most likely) waits 10 minutes at least, and a ban it announced is waited
// out in full.
func (c *slskClient) run() {
	const firstWait = 30 * time.Second
	backoff := firstWait
	c.mu.Lock()
	c.bannedTill = later(c.bannedTill, loadBan(c.banFile))
	c.mu.Unlock()
	if d := time.Until(c.notBefore); d > 0 {
		c.setState(func(s *SlskState) {
			s.Connecting, s.Problem = false, "logging in again at "+c.notBefore.Format("15:04:05")+" (the server asks clients not to reconnect quickly)"
		})
		select {
		case <-time.After(d):
		case <-c.ctx.Done():
			return
		}
	}
	for c.ctx.Err() == nil {
		if !c.waitBan() {
			return
		}
		start := time.Now()
		err, permanent := c.session()
		if c.ctx.Err() != nil {
			return
		}
		if time.Since(start) > 10*time.Minute {
			backoff = firstWait
		}
		msg := "disconnected"
		if err != nil {
			msg = err.Error()
		}
		if errors.Is(err, errSlskNoLoginAnswer) {
			backoff = max(backoff, 10*time.Minute)
		}
		var wait <-chan time.Time
		if until := c.banEnd(); !until.IsZero() {
			permanent = false
			msg = fmt.Sprintf("banned by the Soulseek server until %s (too many searches or other requests); not connecting before then", until.Format("15:04"))
			c.logf("%s", msg)
			wait = time.After(0) // waitBan does the waiting
		} else if !permanent {
			wait = time.After(backoff)
			c.logf("%s; connecting again in %s", msg, FmtWait(backoff))
			msg += "; connecting again at " + time.Now().Add(backoff).Format("15:04:05")
			backoff = min(backoff*2, 30*time.Minute)
		} else {
			c.logf("%s", msg)
		}
		c.setState(func(s *SlskState) {
			s.Online, s.Connecting, s.Problem, s.Permanent = false, false, msg, permanent
		})
		select {
		case <-wait:
		case <-c.reconnect:
			if !errors.Is(err, errSlskNoLoginAnswer) {
				backoff = firstWait
			}
		case <-c.ctx.Done():
			return
		}
	}
}

var errSlskNoLoginAnswer = errors.New("the Soulseek server didn't answer the login (it does that for banned accounts and addresses)")

// banEnd is when the server's ban ends; zero when there's none.
func (c *slskClient) banEnd() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Until(c.bannedTill) <= 0 {
		return time.Time{}
	}
	return c.bannedTill
}

// waitBan waits for a ban to end (reconnecting by hand doesn't shorten it). false: closing.
func (c *slskClient) waitBan() bool {
	until := c.banEnd()
	if until.IsZero() {
		return true
	}
	c.setState(func(s *SlskState) {
		s.Online, s.Connecting, s.Permanent = false, false, false
		s.Problem = fmt.Sprintf("banned by the Soulseek server until %s (too many searches or other requests); not connecting before then", until.Format("15:04"))
	})
	select {
	case <-time.After(time.Until(until)):
		return true
	case <-c.ctx.Done():
		return false
	}
}

// serverNotice reads what the server says for a ban: everything stops, and the client
// stays away until the ban is over, plus a few minutes.
func (c *slskClient) serverNotice(from, text string) {
	d, ok := slskBanLength(text)
	if !ok || !(strings.EqualFold(from, "server") || from == "") {
		return
	}
	until := time.Now().Add(d + 5*time.Minute)
	c.mu.Lock()
	c.bannedTill = later(c.bannedTill, until)
	until = c.bannedTill
	srv := c.srv
	c.mu.Unlock()
	saveBan(c.banFile, until)
	c.logf("the server banned this account for %s; not connecting again before %s", FmtWait(d), until.Format("15:04"))
	if srv != nil {
		srv.close()
	}
}

// session logs in and handles the server's messages until the connection ends. permanent:
// don't try again by yourself.
func (c *slskClient) session() (err error, permanent bool) {
	cfg := c.config()
	if cfg.Username == "" || cfg.Password == "" {
		return errors.New("no Soulseek username and password set (Settings → Soulseek)"), true
	}
	c.setState(func(s *SlskState) { s.Connecting, s.Problem, s.Permanent = true, "", false })
	conn, err := c.dial(c.ctx, cfg.ServerAddr(), 30*time.Second)
	if err != nil {
		return fmt.Errorf("can't reach the Soulseek server %s (%s)", cfg.ServerAddr(), describeNetErr(err)), false
	}
	sc := newSlskConn(conn)
	defer sc.close()
	sum := md5.Sum([]byte(cfg.Username + cfg.Password))
	login := (&slskMsg{}).str(cfg.Username).str(cfg.Password).u32(slskMajor).str(hex.EncodeToString(sum[:])).u32(slskMinor)
	port := c.listenPort()
	if sc.send(ssLogin, login) != nil || sc.send(ssSetWaitPort, (&slskMsg{}).u32(uint32(port))) != nil {
		return errors.New("the Soulseek server closed the connection"), false
	}
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	code, body, err := readSlskFrame(sc.r, 4, slskLimitServer)
	if err != nil || code != ssLogin {
		// A banned account or address gets no answer at all.
		return errSlskNoLoginAnswer, false
	}
	conn.SetReadDeadline(time.Time{})
	r := &slskRead{b: body}
	if !r.boolean() {
		reason := r.str()
		detail := ""
		if r.more() {
			detail = r.str()
		}
		switch reason {
		case "INVALIDPASS":
			return fmt.Errorf("wrong password for Soulseek user %q", cfg.Username), true
		case "INVALIDUSERNAME":
			return fmt.Errorf("the Soulseek server refused the username %q: %s", cfg.Username, FirstNonEmpty(detail, "invalid")), true
		case "EMPTYPASSWORD":
			return errors.New("the Soulseek password is empty"), true
		case "INVALIDVERSION":
			return errors.New("the Soulseek server refused this client's version"), true
		}
		return fmt.Errorf("the Soulseek server refused the login: %s %s", reason, detail), false
	}
	motd := r.str()
	ip := r.ip()
	r.str()
	supporter := r.boolean()

	c.mu.Lock()
	c.srv, c.user = sc, cfg.Username
	c.state = SlskState{Online: true, User: cfg.Username, Server: cfg.ServerAddr(), Since: time.Now(), PublicIP: ip.String(),
		Port: c.state.Port, PortNote: c.state.PortNote, Supporter: supporter, MOTD: strings.TrimSpace(motd)}
	c.dist.reset(cfg.Username)
	c.mu.Unlock()
	c.logf("logged in as %s (as seen by the server, your address is %s, port %d)", cfg.Username, ip, port)
	defer func() {
		c.mu.Lock()
		if c.srv == sc {
			c.srv = nil
		}
		for u, ws := range c.addrWait {
			for _, w := range ws {
				close(w)
			}
			delete(c.addrWait, u)
		}
		c.mu.Unlock()
		c.dist.closeAll()
	}()

	sc.send(ssCheckPrivileges, &slskMsg{})
	sc.send(ssSetStatus, (&slskMsg{}).i32(2))
	c.sendShareCounts()
	c.haveNoParent()
	for _, room := range cfg.Rooms {
		c.JoinRoom(room)
	}
	c.sendInterests()
	for _, b := range cfg.Buddies {
		c.Watch(b)
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep the connection alive (at most once a minute, the server says)
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				sc.send(ssServerPing, &slskMsg{})
			case <-stop:
				return
			}
		}
	}()
	go func() {
		select {
		case <-c.ctx.Done():
			sc.close()
		case <-stop:
		}
	}()
	for {
		code, body, err := readSlskFrame(sc.r, 4, slskLimitServer)
		if err != nil {
			if c.ctx.Err() != nil {
				return nil, true
			}
			return errors.New("lost the connection to the Soulseek server"), false
		}
		if relogged := c.onServer(code, &slskRead{b: body}); relogged {
			return errors.New("logged out: someone logged in with your Soulseek account elsewhere (another Soulseek client?); reconnect from the Soulseek tab (R)"), true
		}
	}
}

func (c *slskClient) sendServer(code uint32, m *slskMsg) error {
	c.mu.Lock()
	sc := c.srv
	c.mu.Unlock()
	if sc == nil {
		return unavailablef("not connected to the Soulseek server")
	}
	return sc.send(code, m)
}

func (c *slskClient) sendShareCounts() {
	folders, files := 0, 0
	if !c.config().NoShare {
		folders, files = c.shares.counts()
	}
	c.sendServer(ssSharedFoldersFile, (&slskMsg{}).u32(uint32(folders)).u32(uint32(files)))
}

// onServer handles a message from the server; it returns true when we were logged out
// because the account logged in elsewhere.
func (c *slskClient) onServer(code uint32, r *slskRead) bool {
	switch code {
	case ssGetPeerAddress:
		user, ip, port := r.str(), r.ip(), int(r.u32())
		if r.err != nil {
			return false
		}
		a := slskAddr{ip: ip, port: port, at: time.Now()}
		c.mu.Lock()
		c.addrs[user] = a
		ws := c.addrWait[user]
		delete(c.addrWait, user)
		c.mu.Unlock()
		for _, w := range ws {
			w <- a
			close(w)
		}
	case ssConnectToPeer:
		user, typ, ip, port, token := r.str(), r.str(), r.ip(), int(r.u32()), r.u32()
		privileged := r.boolean()
		if r.err != nil && user == "" {
			return false
		}
		if privileged {
			c.mu.Lock()
			c.privileged[user] = true
			c.mu.Unlock()
		}
		go c.pierce(user, typ, slskAddr{ip: ip, port: port}, token)
	case ssCantConnectToPeer:
		token := r.u32()
		c.mu.Lock()
		if req := c.pending[token]; req != nil {
			req.cantOnce.Do(func() { close(req.cant) })
		}
		c.mu.Unlock()
	case ssGetUserStatus:
		user, status, privileged := r.str(), r.u32(), r.boolean()
		if r.err == nil {
			c.mu.Lock()
			if privileged {
				c.privileged[user] = true
			} else {
				delete(c.privileged, user)
			}
			c.mu.Unlock()
			c.userStatus(user, int(status), privileged)
		}
	case ssWatchUser:
		c.onWatchUser(r)
	case ssGetUserStats:
		c.onUserStats(r)
	case ssRecommendations, ssGlobalRecommends, ssSimilarUsers:
		c.deliver(code, "", r.b)
	case ssItemRecommends, ssItemSimilarUsers, ssUserInterests:
		key := (&slskRead{b: r.b}).str()
		c.deliver(code, key, r.b)
	case ssPrivilegedUsers:
		n := int(r.u32())
		list := map[string]bool{}
		for i := 0; i < n && r.err == nil; i++ {
			list[r.str()] = true
		}
		c.mu.Lock()
		c.privileged = list
		c.mu.Unlock()
		c.debugf("%d privileged users", len(list))
	case ssAddToPrivileged:
		user := r.str()
		c.mu.Lock()
		c.privileged[user] = true
		c.mu.Unlock()
	case ssCheckPrivileges:
		secs := r.u32()
		c.setState(func(s *SlskState) { s.Privileges = time.Duration(secs) * time.Second })
	case ssWishlistInterval:
		if secs := r.u32(); secs > 0 {
			c.mu.Lock()
			c.wishEvery = time.Duration(secs) * time.Second
			c.mu.Unlock()
		}
	case ssExcludedPhrases:
		n := int(r.u32())
		var list []string
		for i := 0; i < n && r.err == nil; i++ {
			if p := strings.ToLower(strings.TrimSpace(r.str())); p != "" {
				list = append(list, p)
			}
		}
		c.mu.Lock()
		c.excluded = list
		c.mu.Unlock()
	case ssFileSearch: // someone searches our files (user or room search)
		user, token, text := r.str(), r.u32(), r.str()
		if r.err == nil {
			c.searchRequest(user, token, text)
		}
	case ssEmbeddedMessage: // we're a branch root: a search from the server
		dcode := r.u8()
		if dcode == 3 {
			c.distSearch(&slskRead{b: r.rest()})
		}
	case ssPossibleParents:
		n := int(r.u32())
		var cands []slskParentCand
		for i := 0; i < n && i < 10 && r.err == nil; i++ {
			cands = append(cands, slskParentCand{r.str(), slskAddr{ip: r.ip(), port: int(r.u32())}})
		}
		c.possibleParents(cands)
	case ssResetDistributed:
		c.dist.closeAll()
		c.haveNoParent()
	case ssParentMinSpeed, ssParentSpeedRatio:
		// We don't take children, so these don't matter.
	case ssRelogged:
		return true
	case ssMessageUser:
		id, ts, user, text := r.u32(), r.u32(), r.str(), r.str()
		if r.err == nil {
			c.sendServer(ssMessageAcked, (&slskMsg{}).u32(id))
			if !c.isBanned(user) && !c.isIgnored(user) {
				c.chat.private(user, text, time.Unix(int64(ts), 0), false)
				c.logf("message from %s: %s", user, FitWidth(text, 120))
			}
			c.serverNotice(user, text)
		}
	case ssSayChatroom:
		room, user, text := r.str(), r.str(), r.str()
		if r.err == nil && !c.isBanned(user) && !c.isIgnored(user) {
			c.mu.Lock()
			own := user == c.user
			c.mu.Unlock()
			c.chat.roomLine(room, user, text, own)
		}
	case ssJoinRoom:
		room := r.str()
		n := int(r.u32())
		var users []string
		for i := 0; i < n && i < 100000 && r.err == nil; i++ {
			users = append(users, r.str())
		}
		c.chat.joined(room, users)
	case ssLeaveRoom:
		c.chat.left(r.str())
	case ssUserJoinedRoom:
		room, user := r.str(), r.str()
		c.chat.userJoined(room, user)
	case ssUserLeftRoom:
		room, user := r.str(), r.str()
		c.chat.userLeft(room, user)
	case ssRoomList:
		n := int(r.u32())
		names := make([]string, 0, min(n, 10000))
		for i := 0; i < n && r.err == nil; i++ {
			names = append(names, r.str())
		}
		m := int(r.u32())
		counts := make([]int, 0, min(m, 10000))
		for i := 0; i < m && r.err == nil; i++ {
			counts = append(counts, int(r.u32()))
		}
		c.chat.setRooms(names, counts)
	case ssAdminMessage:
		text := r.str()
		c.chat.private("(server)", text, time.Now(), false)
		c.logf("message from the server admins: %s", text)
		c.serverNotice("", text)
	case ssCantCreateRoom:
		room := r.str()
		c.chat.notice(room, "the server refused to create this room")
	}
	return false
}

// --- peer connections -----------------------------------------------------------------------

// peerAddress asks the server where user listens.
func (c *slskClient) peerAddress(ctx context.Context, user string) (slskAddr, error) {
	c.mu.Lock()
	if a, ok := c.addrs[user]; ok && time.Since(a.at) < 2*time.Minute {
		c.mu.Unlock()
		return a, nil
	}
	ch := make(chan slskAddr, 1)
	first := len(c.addrWait[user]) == 0
	c.addrWait[user] = append(c.addrWait[user], ch)
	c.mu.Unlock()
	if first {
		if err := c.sendServer(ssGetPeerAddress, (&slskMsg{}).str(user)); err != nil {
			return slskAddr{}, err
		}
	}
	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	select {
	case a, ok := <-ch:
		if !ok {
			return slskAddr{}, unavailablef("lost the connection to the Soulseek server")
		}
		return a, nil
	case <-t.C:
		return slskAddr{}, failf("the server didn't say where %s is", user)
	case <-ctx.Done():
		return slskAddr{}, ctx.Err()
	}
}

func peerInit(user, typ string) []byte {
	return slskFrame(1, 1, (&slskMsg{}).str(user).str(typ).u32(0).b)
}

// connect opens a connection of type typ to user: directly, and at the same time through the
// server (the peer connects to us), whichever works first. addr, when known, saves asking
// the server for it.
func (c *slskClient) connect(ctx context.Context, user, typ string, addr *slskAddr) (*slskConn, error) {
	c.mu.Lock()
	me := c.user
	c.token++
	token := c.token
	req := &slskConnReq{user: user, typ: typ, ch: make(chan *slskConn, 1), cant: make(chan struct{})}
	c.pending[token] = req
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, token)
		c.mu.Unlock()
		select {
		case sc := <-req.ch: // arrived too late
			sc.close()
		default:
		}
	}()
	if err := c.sendServer(ssConnectToPeer, (&slskMsg{}).u32(token).str(user).str(typ)); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	direct := make(chan *slskConn, 1)
	go func() {
		var a slskAddr
		if addr != nil {
			a = *addr
		} else {
			var err error
			if a, err = c.peerAddress(ctx, user); err != nil {
				direct <- nil
				return
			}
		}
		if !a.ok() {
			direct <- nil
			return
		}
		conn, err := c.dial(ctx, a.String(), 10*time.Second)
		if err != nil {
			direct <- nil
			return
		}
		sc := newSlskConn(conn)
		sc.user, sc.typ = user, typ
		if sc.write(peerInit(me, typ)) != nil {
			direct <- nil
			return
		}
		direct <- sc
	}()
	directFailed, cant := false, false
	cantCh := req.cant
	take := func(sc *slskConn) (*slskConn, error) {
		go func() { // the other way may still come through
			if !directFailed {
				if d := <-direct; d != nil {
					d.close()
				}
			}
		}()
		return sc, nil
	}
	for {
		select {
		case sc := <-direct:
			if sc != nil {
				directFailed = true // read already
				return sc, nil
			}
			directFailed = true
			if cant {
				return nil, failf("can't connect to %s (both behind closed ports?)", user)
			}
		case sc := <-req.ch:
			return take(sc)
		case <-cantCh:
			cant = true
			cantCh = nil
			if directFailed {
				return nil, failf("can't connect to %s (both behind closed ports?)", user)
			}
		case <-ctx.Done():
			if err := ctx.Err(); errors.Is(err, context.Canceled) {
				return nil, err
			}
			return nil, failf("can't connect to %s (no answer)", user)
		}
	}
}

// pierce answers a peer's indirect connection request: we connect to them.
func (c *slskClient) pierce(user, typ string, a slskAddr, token uint32) {
	if !a.ok() || (typ != "P" && typ != "F" && typ != "D") {
		return
	}
	select {
	case c.handshakes <- struct{}{}:
	default:
		return
	}
	conn, err := c.dial(c.ctx, a.String(), 15*time.Second)
	<-c.handshakes
	if err != nil {
		c.sendServer(ssCantConnectToPeer, (&slskMsg{}).u32(token).str(user))
		return
	}
	sc := newSlskConn(conn)
	sc.user, sc.typ = user, typ
	if sc.write(slskFrame(0, 1, (&slskMsg{}).u32(token).b)) != nil {
		return
	}
	if typ == "D" {
		sc.close() // we don't take children
		return
	}
	c.serve(sc)
}

// peer returns a message connection to user, opening one if needed.
func (c *slskClient) peer(ctx context.Context, user string) (*slskConn, error) {
	for {
		c.mu.Lock()
		if sc := c.peers[user]; sc != nil && !sc.closed() {
			c.mu.Unlock()
			return sc, nil
		}
		if d := c.dialing[user]; d != nil {
			c.mu.Unlock()
			select {
			case <-d.done:
				if d.err != nil {
					return nil, d.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		d := &slskDial{done: make(chan struct{})}
		c.dialing[user] = d
		c.mu.Unlock()
		sc, err := c.connect(ctx, user, "P", nil)
		c.mu.Lock()
		delete(c.dialing, user)
		if err == nil {
			c.peers[user] = sc
		}
		d.sc, d.err = sc, err
		close(d.done)
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		c.wg.Add(1)
		go func() { defer c.wg.Done(); c.readPeer(sc) }()
		return sc, nil
	}
}

// sendPeer sends a peer message to user, connecting first if needed.
func (c *slskClient) sendPeer(ctx context.Context, user string, code uint32, m *slskMsg) error {
	frame := slskFrame(code, 4, m.b)
	for attempt := 0; attempt < 2; attempt++ {
		sc, err := c.peer(ctx, user)
		if err != nil {
			return err
		}
		if err = sc.write(frame); err == nil {
			return nil
		}
		c.mu.Lock()
		if c.peers[user] == sc {
			delete(c.peers, user)
		}
		c.mu.Unlock()
	}
	return failf("lost the connection to %s", user)
}

// readPeer handles a peer's messages until the connection closes or stays idle.
func (c *slskClient) readPeer(sc *slskConn) {
	defer func() {
		sc.close()
		c.mu.Lock()
		if c.peers[sc.user] == sc {
			delete(c.peers, sc.user)
		}
		c.mu.Unlock()
	}()
	for {
		sc.c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		code, body, err := readSlskFrame(sc.r, 4, slskLimitPeer)
		if err != nil {
			return
		}
		c.onPeer(sc, code, &slskRead{b: body})
	}
}

// --- searching ----------------------------------------------------------------------------------

type slskResponse struct {
	User     string
	Files    []slskFile
	FreeSlot bool
	Speed    int // bytes/s
	Queue    int
}

type slskSearch struct {
	mu    sync.Mutex
	resps []slskResponse
	files int
	last  time.Time
}

func (s *slskSearch) add(r slskResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.resps) >= 300 || s.files >= 5000 {
		return
	}
	s.resps = append(s.resps, r)
	s.files += len(r.Files)
	s.last = time.Now()
}

func (s *slskSearch) snapshot() ([]slskResponse, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]slskResponse(nil), s.resps...), s.last
}

// searchBacklog is how long a search started now would wait for its turn.
func (c *slskClient) searchBacklog() time.Duration { return c.budget.backlog() }

// slskQuery makes search text the network handles well: plain words, no punctuation
// (SoulseekQt answers nothing when some characters are in it), no words the server would
// read as "exclude this".
func slskQuery(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune("!\"#$%&'()*+,./:;<=>?@[\\]^_`{|}~–—‐’“”…", r) {
			return ' '
		}
		return r
	}, s)
	var words []string
	for _, w := range strings.Fields(s) {
		w = strings.TrimLeft(w, "-'")
		if w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// Search sends a search to the network and collects the answers that arrive in the next
// few seconds. Searches are budgeted (slskSearchBudget), at most two run at once, and the
// same search within an hour gets the answers of the first (also while it's under way).
func (c *slskClient) Search(ctx context.Context, text string) ([]slskResponse, error) {
	text = slskQuery(text)
	if len(text) < 3 {
		return nil, nil
	}
	for {
		s, mine := c.answers.get(text)
		if !mine {
			select {
			case <-s.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if s.err != nil { // that one was given up on: search again
				continue
			}
			c.debugf("answers to %q kept from %s ago", text, FmtWait(time.Since(s.at)))
			return s.resps, nil
		}
		resps, err := c.search(ctx, text)
		c.answers.finish(s, resps, err)
		return resps, err
	}
}

func (c *slskClient) search(ctx context.Context, text string) ([]slskResponse, error) {
	if until := c.banEnd(); !until.IsZero() {
		return nil, unavailablef("banned by the Soulseek server until %s", until.Format("15:04"))
	}
	if !c.waitOnline(ctx, 45*time.Second) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailablef("%s", c.offlineReason())
	}
	select {
	case c.searchSem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.searchSem }()
	if err := c.budget.wait(ctx); err != nil {
		return nil, err
	}
	if !c.online() {
		return nil, unavailablef("%s", c.offlineReason())
	}
	token := c.nextToken()
	s := &slskSearch{}
	c.mu.Lock()
	c.searches[token] = s
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.searches, token)
		c.mu.Unlock()
	}()
	if err := c.sendServer(ssFileSearch, (&slskMsg{}).u32(token).str(text)); err != nil {
		return nil, err
	}
	c.searchesSent.Add(1)
	c.debugf("searching for %q", text)
	return c.collect(ctx, s, c.searchMin, c.searchMax)
}

// collect waits for a search's answers: at least minWait, then until none came for 2.5
// seconds, at most maxWait.
func (c *slskClient) collect(ctx context.Context, s *slskSearch, minWait, maxWait time.Duration) ([]slskResponse, error) {
	start := time.Now()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
		resps, last := s.snapshot()
		el := time.Since(start)
		if el >= maxWait || (el >= minWait && (len(resps) == 0 && el >= minWait+2*time.Second || len(resps) > 0 && time.Since(last) > 2500*time.Millisecond)) {
			return resps, nil
		}
	}
}

// waitOnline waits for a login under way (or about to start) to finish.
func (c *slskClient) waitOnline(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		c.mu.Lock()
		on, st := c.srv != nil, c.state
		c.mu.Unlock()
		if on {
			return true
		}
		if !(st.Connecting || st.Problem == "" && !st.Permanent) || time.Now().After(deadline) {
			return false
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
}

func (c *slskClient) offlineReason() string {
	st := c.State()
	if st.Connecting {
		return "connecting to the Soulseek server"
	}
	return "offline: " + FirstNonEmpty(st.Problem, "not connected to the Soulseek server")
}

// --- wishlist -----------------------------------------------------------------------------

// setWishlist sets where wishlist searches come from: next returns the text to search for
// (empty: nothing now) and what to do with the answers.
func (c *slskClient) setWishlist(next func() (string, func([]slskResponse))) {
	c.mu.Lock()
	c.wishFn = next
	c.mu.Unlock()
}

// WishlistStatus returns the server's wishlist interval and when the next one is due.
func (c *slskClient) WishlistStatus() (time.Duration, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wishEvery, c.wishNext
}

// wishlistLoop sends one wishlist search per interval the server sets (12 minutes, 2 for
// privileged users), as the official clients do.
func (c *slskClient) wishlistLoop() {
	for {
		c.mu.Lock()
		every := c.wishEvery
		c.wishNext = time.Now().Add(every)
		c.mu.Unlock()
		select {
		case <-time.After(every):
		case <-c.ctx.Done():
			return
		}
		c.mu.Lock()
		fn := c.wishFn
		c.mu.Unlock()
		if fn == nil || !c.online() {
			continue
		}
		text, done := fn()
		if text = slskQuery(text); len(text) < 3 {
			continue
		}
		token := c.nextToken()
		s := &slskSearch{}
		c.mu.Lock()
		c.searches[token] = s
		c.mu.Unlock()
		if c.sendServer(ssWishlistSearch, (&slskMsg{}).u32(token).str(text)) == nil {
			c.debugf("wishlist search for %q", text)
			resps, _ := c.collect(c.ctx, s, c.searchMin+2*time.Second, c.searchMax+3*time.Second)
			if done != nil && c.ctx.Err() == nil {
				done(resps)
			}
		}
		c.mu.Lock()
		delete(c.searches, token)
		c.mu.Unlock()
	}
}
