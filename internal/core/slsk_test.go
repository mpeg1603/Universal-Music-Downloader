package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A stand-in for the Soulseek network: a server and one remote user, on localhost. Nothing
// here reaches the real network.

type fakeSlskServer struct {
	t     *testing.T
	ln    net.Listener
	mu    sync.Mutex
	got   map[uint32][][]byte
	conns map[string]*slskConn
	addrs map[string]slskAddr
	// onSearch is called for our client's searches.
	onSearch func(token uint32, text string)
}

func newFakeSlskServer(t *testing.T) *fakeSlskServer {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSlskServer{t: t, ln: ln, got: map[uint32][][]byte{}, conns: map[string]*slskConn{}, addrs: map[string]slskAddr{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(newSlskConn(c))
		}
	}()
	return s
}

func (s *fakeSlskServer) addr() string { return s.ln.Addr().String() }

func (s *fakeSlskServer) serve(sc *slskConn) {
	defer sc.close()
	user := ""
	for {
		code, body, err := readSlskFrame(sc.r, 4, 1<<20)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.got[code] = append(s.got[code], body)
		s.mu.Unlock()
		r := &slskRead{b: body}
		switch code {
		case ssLogin:
			user = r.str()
			pass := r.str()
			if major := r.u32(); major != slskMajor {
				s.t.Errorf("major version %d", major)
			}
			if pass == "wrong" {
				sc.send(ssLogin, (&slskMsg{}).boolean(false).str("INVALIDPASS"))
				continue
			}
			s.mu.Lock()
			s.conns[user] = sc
			s.mu.Unlock()
			m := (&slskMsg{}).boolean(true).str("welcome")
			putIP(m, net.IPv4(127, 0, 0, 1))
			sc.send(ssLogin, m.str("hash").boolean(false))
			sc.send(ssWishlistInterval, (&slskMsg{}).u32(720))
			sc.send(ssPrivilegedUsers, (&slskMsg{}).u32(1).str("vip"))
		case ssSetWaitPort:
			port := int(r.u32())
			s.mu.Lock()
			s.addrs[user] = slskAddr{ip: net.IPv4(127, 0, 0, 1), port: port}
			s.mu.Unlock()
		case ssGetPeerAddress:
			who := r.str()
			s.mu.Lock()
			a := s.addrs[who]
			s.mu.Unlock()
			m := (&slskMsg{}).str(who)
			putIP(m, firstIP(a.ip))
			sc.send(ssGetPeerAddress, m.u32(uint32(a.port)).u32(0).u32(0))
		case ssFileSearch:
			token, text := r.u32(), r.str()
			if s.onSearch != nil {
				go s.onSearch(token, text)
			}
		}
	}
}

func firstIP(ip net.IP) net.IP {
	if ip == nil {
		return net.IPv4zero
	}
	return ip
}

func (s *fakeSlskServer) send(user string, code uint32, m *slskMsg) {
	s.mu.Lock()
	sc := s.conns[user]
	s.mu.Unlock()
	if sc == nil {
		s.t.Fatalf("%s isn't logged in", user)
	}
	sc.send(code, m)
}

// waitFor waits for the n-th message with code (1-based) and returns it.
func (s *fakeSlskServer) waitFor(code uint32, n int) []byte {
	s.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := s.got[code]
		s.mu.Unlock()
		if len(got) >= n {
			return got[n-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.t.Fatalf("the server got no message %d (#%d)", code, n)
	return nil
}

// fakeSlskPeer is another user: it shares files, answers searches, sends files when asked,
// and downloads ours.
type fakeSlskPeer struct {
	t     *testing.T
	name  string
	ln    net.Listener
	files map[string][]byte

	mu       sync.Mutex
	got      map[uint32][][]byte
	received map[uint32][]byte // files we uploaded to it, by token
	pconn    *slskConn         // its message connection to our client
	clientAt string            // our client's listening address
	wantFile map[uint32]int64  // tokens of files it agreed to receive, and their sizes
	breakAt  int               // >0: the first file sent breaks off after this many bytes
	offsets  []uint64          // the offsets our client asked for
	nextTok  uint32
	tokPath  map[uint32]string // the file each offered transfer is for
}

func newFakeSlskPeer(t *testing.T, name string) *fakeSlskPeer {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeSlskPeer{t: t, name: name, ln: ln, files: map[string][]byte{}, got: map[uint32][][]byte{},
		received: map[uint32][]byte{}, wantFile: map[uint32]int64{}, nextTok: 777, tokPath: map[uint32]string{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.incoming(newSlskConn(c))
		}
	}()
	return p
}

func (p *fakeSlskPeer) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *fakeSlskPeer) incoming(sc *slskConn) {
	code, body, err := readSlskFrame(sc.r, 1, 4096)
	if err != nil {
		sc.close()
		return
	}
	r := &slskRead{b: body}
	if code != 1 {
		p.t.Errorf("peer got init code %d", code)
		sc.close()
		return
	}
	sc.user, sc.typ = r.str(), r.str()
	switch sc.typ {
	case "P":
		p.readP(sc)
	case "F": // our client uploads to us: token, then our offset, then the file
		defer sc.close()
		var tb [4]byte
		io.ReadFull(sc.r, tb[:])
		token := binary.LittleEndian.Uint32(tb[:])
		p.mu.Lock()
		size, ok := p.wantFile[token]
		p.mu.Unlock()
		if !ok {
			p.t.Errorf("unexpected file token %d", token)
			return
		}
		sc.write(make([]byte, 8))
		data := make([]byte, size)
		io.ReadFull(sc.r, data)
		p.mu.Lock()
		p.received[token] = data
		p.mu.Unlock()
	case "D": // we're a distributed parent for our client
		defer sc.close()
		sc.write(slskFrame(4, 1, (&slskMsg{}).i32(0).b))
		sc.write(slskFrame(5, 1, (&slskMsg{}).str(p.name).b))
		sc.write(slskFrame(3, 1, (&slskMsg{}).u32(49).str(p.name).u32(56).str("album song").b))
		io.Copy(io.Discard, sc.r)
	}
}

func (p *fakeSlskPeer) readP(sc *slskConn) {
	p.mu.Lock()
	p.pconn = sc
	p.mu.Unlock()
	for {
		code, body, err := readSlskFrame(sc.r, 4, 64<<20)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.got[code] = append(p.got[code], body)
		p.mu.Unlock()
		r := &slskRead{b: body}
		switch code {
		case pmQueueUpload:
			path := r.str()
			data, ok := p.files[path]
			if !ok {
				sc.send(pmUploadDenied, (&slskMsg{}).str(path).str("File not shared."))
				continue
			}
			p.mu.Lock()
			tok := p.nextTok
			p.nextTok++
			p.tokPath[tok] = path
			p.mu.Unlock()
			sc.send(pmTransferRequest, (&slskMsg{}).u32(1).u32(tok).str(path).u64(uint64(len(data))))
		case pmTransferResponse:
			token, allowed := r.u32(), r.boolean()
			p.mu.Lock()
			_, ours := p.tokPath[token]
			p.mu.Unlock()
			if !ours || !allowed {
				p.t.Errorf("transfer response %d %v", token, allowed)
				continue
			}
			go p.sendFile(token)
		case pmTransferRequest: // our client is ready to upload to us
			dir, token := r.u32(), r.u32()
			r.str() // the file
			size := r.u64()
			if dir != 1 {
				p.t.Errorf("direction %d", dir)
			}
			p.mu.Lock()
			p.wantFile[token] = int64(size)
			p.mu.Unlock()
			sc.send(pmTransferResponse, (&slskMsg{}).u32(token).boolean(true))
		case pmFolderContentsRequest:
			token, folder := r.u32(), r.str()
			m := (&slskMsg{}).u32(token).str(folder).u32(1).str(folder)
			var names []string
			for path := range p.files {
				if strings.HasPrefix(path, folder+`\`) {
					names = append(names, path)
				}
			}
			m.u32(uint32(len(names)))
			for _, path := range names {
				writeSlskFile(m, slskFile{Name: path[len(folder)+1:], Size: int64(len(p.files[path]))})
			}
			sc.write(slskFrame(pmFolderContentsResponse, 4, slskCompress(m.b)))
		case pmPlaceInQueueRequest:
			sc.send(pmPlaceInQueueResponse, (&slskMsg{}).str(r.str()).u32(1))
		}
	}
}

// sendFile opens a file connection to our client and sends the file the transfer is for.
func (p *fakeSlskPeer) sendFile(token uint32) {
	p.mu.Lock()
	data := p.files[p.tokPath[token]]
	p.mu.Unlock()
	conn, err := net.Dial("tcp4", p.clientAt)
	if err != nil {
		p.t.Errorf("dial client: %v", err)
		return
	}
	sc := newSlskConn(conn)
	defer sc.close()
	sc.write(peerInit(p.name, "F"))
	var tb [4]byte
	binary.LittleEndian.PutUint32(tb[:], token)
	sc.write(tb[:])
	var ob [8]byte
	if _, err := io.ReadFull(sc.r, ob[:]); err != nil {
		p.t.Errorf("no offset: %v", err)
		return
	}
	off := binary.LittleEndian.Uint64(ob[:])
	p.mu.Lock()
	p.offsets = append(p.offsets, off)
	cut := p.breakAt
	p.breakAt = 0
	p.mu.Unlock()
	if cut > 0 { // the connection drops; our client asks again
		sc.write(data[off:cut])
		return
	}
	sc.write(data[off:])
	io.Copy(io.Discard, sc.r) // the downloader closes
}

// dialClient opens a message connection to our client.
func (p *fakeSlskPeer) dialClient() *slskConn {
	conn, err := net.Dial("tcp4", p.clientAt)
	if err != nil {
		p.t.Fatal(err)
	}
	sc := newSlskConn(conn)
	sc.user, sc.typ = "client", "P"
	sc.write(peerInit(p.name, "P"))
	go p.readP(sc)
	return sc
}

func (p *fakeSlskPeer) answerSearch(token uint32) {
	sc := p.dialClient()
	m := (&slskMsg{}).str(p.name).u32(token).u32(uint32(len(p.files)))
	for path, data := range p.files {
		writeSlskFile(m, slskFile{Name: path, Size: int64(len(data)), Attrs: []slskAttr{{slskAttrDuration, 4}, {slskAttrSampleRate, 44100}, {slskAttrBitDepth, 16}}})
	}
	m.boolean(true).u32(1 << 20).u32(0).u32(0).u32(0)
	sc.write(slskFrame(pmFileSearchResponse, 4, slskCompress(m.b)))
}

func (p *fakeSlskPeer) waitFor(code uint32, n int) []byte {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := p.got[code]
		p.mu.Unlock()
		if len(got) >= n {
			return got[n-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.t.Fatalf("the peer got no message %d (#%d)", code, n)
	return nil
}

func testSlskClient(t *testing.T, srv *fakeSlskServer, shares *slskShares, mutate func(*SoulseekConfig)) *slskClient {
	cfg := SoulseekConfig{Username: "me", Password: "pw", Server: srv.addr(), Port: -1, NoUPnP: true}
	if mutate != nil {
		mutate(&cfg)
	}
	if shares == nil {
		shares = newSlskShares()
	}
	c := newSlskClient(cfg, RouteSystem, shares)
	c.searchMin, c.searchMax = 300*time.Millisecond, 3*time.Second
	c.Start()
	t.Cleanup(c.Close)
	deadline := time.Now().Add(10 * time.Second)
	for !c.online() && time.Now().Before(deadline) {
		if st := c.State(); st.Permanent {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c
}

func TestSoulseekDownload(t *testing.T) {
	tools := NeedFFmpeg(t)
	flac, err := os.ReadFile(sine(t, filepath.Join(t.TempDir(), "x.flac"), 4))
	if err != nil {
		t.Fatal(err)
	}
	srv := newFakeSlskServer(t)
	peer := newFakeSlskPeer(t, "remote")
	peer.files[`Music\Daft Punk\Random Access Memories\08 - Get Lucky.flac`] = flac
	var searches []string
	var mu sync.Mutex
	srv.onSearch = func(token uint32, text string) {
		mu.Lock()
		searches = append(searches, text)
		mu.Unlock()
		peer.answerSearch(token)
	}
	c := testSlskClient(t, srv, nil, nil)
	if !c.online() {
		t.Fatalf("not online: %+v", c.State())
	}
	srv.mu.Lock()
	srv.addrs["remote"] = slskAddr{ip: net.IPv4(127, 0, 0, 1), port: peer.port()}
	peer.clientAt = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.listenPort()))
	srv.mu.Unlock()
	if !c.isPrivileged("vip") || c.isPrivileged("remote") {
		t.Error("privileged users")
	}

	src := newSoulseekSource(c, "flac")
	tr := GetLucky()
	tr.Key, tr.DurationMs = "a", 4000
	m := TestManager(t, tools, []Source{src}, TestLibrary(tr), func(c *Config) { c.Format = "flac" })
	out := RunManager(t, m)
	v := out["a"]
	if v.State != JobDone || !strings.HasPrefix(v.Rec.SourceURL, "soulseek://remote/Music/Daft Punk/") || !v.Rec.Quality.Lossless {
		t.Fatalf("%v %s %+v", v.State, v.Reason, v.Rec)
	}
	mu.Lock()
	if len(searches) != 1 || searches[0] != "Daft Punk Get Lucky" {
		t.Errorf("searches %q", searches)
	}
	mu.Unlock()
	if q := peer.waitFor(pmQueueUpload, 1); !bytes.Contains(q, []byte("08 - Get Lucky.flac")) {
		t.Errorf("queued %q", q)
	}

	// A transfer that breaks off goes on from where it stopped.
	peer.mu.Lock()
	peer.breakAt = len(flac) / 3
	peer.mu.Unlock()
	dest := filepath.Join(t.TempDir(), "again.flac")
	var path string
	for k := range peer.files {
		path = k
	}
	if err := c.Download(context.Background(), "remote", path, int64(len(flac)), dest, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, flac) {
		t.Errorf("resumed file differs (%d bytes, want %d)", len(got), len(flac))
	}
	peer.mu.Lock()
	if n := len(peer.offsets); n < 2 || peer.offsets[n-2] != 0 || peer.offsets[n-1] != uint64(len(flac)/3) {
		t.Errorf("offsets %v", peer.offsets)
	}
	peer.mu.Unlock()

	// A file the user doesn't have is refused; the download says so.
	err = c.Download(context.Background(), "remote", `Music\nope.flac`, 10, filepath.Join(t.TempDir(), "x"), nil)
	if err == nil || !strings.Contains(err.Error(), "File not shared") {
		t.Errorf("refused download: %v", err)
	}
}

func TestSoulseekSharing(t *testing.T) {
	dir := t.TempDir()
	song := bytes.Repeat([]byte("soulseek"), 20000)
	os.MkdirAll(filepath.Join(dir, "Artist", "Album"), 0o755)
	os.WriteFile(filepath.Join(dir, "Artist", "Album", "01 Song.mp3"), song, 0o644)
	os.WriteFile(filepath.Join(dir, "Artist", "Album", ".hidden.mp3"), song, 0o644)
	shares := newSlskShares()
	shares.scan(context.Background(), shareRoots([]string{dir}), func(string) (Quality, float64, bool) {
		return Quality{Codec: "mp3", Kbps: 320}, 200, true
	})
	if f, n := shares.counts(); f != 1 || n != 1 {
		t.Fatalf("%d folders, %d files", f, n)
	}
	root := filepath.Base(dir)
	vpath := root + `\Artist\Album\01 Song.mp3`

	srv := newFakeSlskServer(t)
	peer := newFakeSlskPeer(t, "remote")
	c := testSlskClient(t, srv, shares, func(c *SoulseekConfig) { c.Banned = []string{"evil"} })
	if !c.online() {
		t.Fatalf("not online: %+v", c.State())
	}
	srv.mu.Lock()
	srv.addrs["remote"] = slskAddr{ip: net.IPv4(127, 0, 0, 1), port: peer.port()}
	srv.mu.Unlock()
	peer.clientAt = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.listenPort()))
	c.sendShareCounts()
	if counts := &(slskRead{b: srv.waitFor(ssSharedFoldersFile, 2)}); counts.u32() != 1 || counts.u32() != 1 {
		t.Error("share counts")
	}

	// Someone searches (a user search, straight from the server): we answer.
	srv.send("me", ssFileSearch, (&slskMsg{}).str("remote").u32(42).str("artist SONG"))
	resp := peer.waitFor(pmFileSearchResponse, 1)
	data, err := slskDecompress(resp, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r := &slskRead{b: data}
	if user, token := r.str(), r.u32(); user != "me" || token != 42 {
		t.Errorf("answer from %s for %d", user, token)
	}
	files := readSlskFiles(r, 100)
	if len(files) != 1 || files[0].Name != vpath || files[0].Size != int64(len(song)) || files[0].attr(slskAttrBitrate) != 320 {
		t.Errorf("%+v", files)
	}
	// Searches that don't match, or from banned users, get no answer.
	srv.send("me", ssFileSearch, (&slskMsg{}).str("remote").u32(43).str("artist -song"))
	srv.send("me", ssFileSearch, (&slskMsg{}).str("evil").u32(44).str("artist song"))

	// Browsing and user info.
	pc := peer.dialClient()
	pc.send(pmSharedFileListRequest, &slskMsg{})
	list, err := slskDecompress(peer.waitFor(pmSharedFileListResponse, 1), 1<<20)
	if err != nil || !bytes.Contains(list, []byte(root+`\Artist\Album`)) || !bytes.Contains(list, []byte("01 Song.mp3")) || bytes.Contains(list, []byte(".hidden")) {
		t.Errorf("file list %q %v", list, err)
	}
	pc.send(pmFolderContentsRequest, (&slskMsg{}).u32(9).str(root+`\Artist`))
	fc, _ := slskDecompress(peer.waitFor(pmFolderContentsResponse, 1), 1<<20)
	if !bytes.Contains(fc, []byte("01 Song.mp3")) {
		t.Errorf("folder contents %q", fc)
	}
	pc.send(pmUserInfoRequest, &slskMsg{})
	info := &slskRead{b: peer.waitFor(pmUserInfoResponse, 1)}
	if info.str(); info.boolean() || info.u32() != 2 {
		t.Error("user info")
	}

	// They download from us.
	pc.send(pmQueueUpload, (&slskMsg{}).str(vpath))
	pc.send(pmQueueUpload, (&slskMsg{}).str(root+`\nope.mp3`))
	denied := &slskRead{b: peer.waitFor(pmUploadDenied, 1)}
	if p, why := denied.str(), denied.str(); !strings.HasSuffix(p, "nope.mp3") || why != "File not shared." {
		t.Errorf("denied %q %q", p, why)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		peer.mu.Lock()
		var got []byte
		for _, d := range peer.received {
			got = d
		}
		peer.mu.Unlock()
		if got != nil {
			if !bytes.Equal(got, song) {
				t.Errorf("uploaded %d bytes, want %d", len(got), len(song))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing uploaded: %+v", c.Transfers())
		}
		time.Sleep(20 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		xs := c.Transfers()
		if len(xs) == 1 && xs[0].Up && xs[0].State == "done" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%+v", xs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	srv.waitFor(ssSendUploadSpeed, 1)

	// The distributed network: the server suggests the peer as our parent, which passes on
	// a search we answer.
	m := (&slskMsg{}).u32(1).str("remote")
	putIP(m, net.IPv4(127, 0, 0, 1))
	srv.send("me", ssPossibleParents, m.u32(uint32(peer.port())))
	resp = peer.waitFor(pmFileSearchResponse, 2)
	data, _ = slskDecompress(resp, 1<<20)
	if r := (&slskRead{b: data}); r.str() != "me" || r.u32() != 56 {
		t.Error("distributed search answer")
	}
	if parent, level := c.Parent(); parent != "remote" || level != 1 {
		t.Errorf("parent %q level %d", parent, level)
	}
	if hnp := srv.waitFor(ssHaveNoParent, 2); len(hnp) != 1 || hnp[0] != 0 {
		t.Errorf("have-no-parent %v", hnp)
	}
	peer.mu.Lock()
	n := len(peer.got[pmFileSearchResponse])
	peer.mu.Unlock()
	if n != 2 {
		t.Errorf("%d search answers, want 2 (none for the excluded word or the banned user)", n)
	}

	// Chat.
	srv.send("me", ssMessageUser, (&slskMsg{}).u32(5).u32(uint32(time.Now().Unix())).str("remote").str("hi there").boolean(true))
	if ack := (&slskRead{b: srv.waitFor(ssMessageAcked, 1)}); ack.u32() != 5 {
		t.Error("ack")
	}
	if c.chat.Unread() != 1 {
		t.Errorf("unread %d", c.chat.Unread())
	}
	c.SendPrivate("remote", "hello")
	pm := &slskRead{b: srv.waitFor(ssMessageUser, 1)}
	if to, text := pm.str(), pm.str(); to != "remote" || text != "hello" {
		t.Errorf("sent %q to %q", text, to)
	}
	c.JoinRoom("room")
	srv.waitFor(ssJoinRoom, 1)
	srv.send("me", ssJoinRoom, (&slskMsg{}).str("room").u32(2).str("me").str("remote"))
	srv.send("me", ssSayChatroom, (&slskMsg{}).str("room").str("remote").str("hello room"))
	deadline = time.Now().Add(5 * time.Second)
	for {
		convs, _ := c.chat.Conversations()
		if len(convs) == 2 && convs[1].Room && len(convs[1].Users) == 2 && len(convs[1].Lines) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%+v", convs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Logged in elsewhere: offline until you reconnect.
	srv.send("me", ssRelogged, &slskMsg{})
	deadline = time.Now().Add(5 * time.Second)
	for st := c.State(); !st.Permanent || st.Online; st = c.State() {
		if time.Now().After(deadline) {
			t.Fatalf("%+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSoulseekWrongPassword(t *testing.T) {
	srv := newFakeSlskServer(t)
	c := testSlskClient(t, srv, nil, func(c *SoulseekConfig) { c.Password = "wrong" })
	st := c.State()
	if st.Online || !st.Permanent || !strings.Contains(st.Problem, "wrong password") {
		t.Errorf("%+v", st)
	}
	if _, err := c.Search(context.Background(), "anything at all"); !isUnavailable(err) {
		t.Errorf("search while offline: %v", err)
	}
}

func TestSlskShareSearch(t *testing.T) {
	s := newSlskShares()
	for _, p := range []string{`Music\Daft Punk\Random Access Memories\08 - Get Lucky.flac`, `Music\Daft Punk\Discovery\01 One More Time.mp3`,
		`Music\Other\Lucky Star.mp3`} {
		s.addLocked(p, "/x/"+p, 100, nil)
	}
	names := func(fs []slskFile) string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Name[strings.LastIndexByte(f.Name, '\\')+1:])
		}
		return strings.Join(out, "|")
	}
	cases := map[string]string{
		"daft punk lucky":        "08 - Get Lucky.flac",
		"LUCKY":                  "08 - Get Lucky.flac|Lucky Star.mp3",
		"lucky -star":            "08 - Get Lucky.flac",
		"daft *ery":              "01 One More Time.mp3",
		"*ery":                   "",
		"punk nothing":           "",
		"random-access memories": "08 - Get Lucky.flac",
	}
	for q, want := range cases {
		if got := names(s.search(q, nil, 10)); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
	if got := names(s.search("lucky", []string{"lucky star"}, 10)); got != "08 - Get Lucky.flac" {
		t.Errorf("excluded phrase: %q", got)
	}
	if real, _, ok := s.lookup(`music\daft punk\discovery\01 one more time.mp3`); ok || real != "" {
		t.Error("lookup of a missing file")
	}
}

func TestSlskQuery(t *testing.T) {
	for in, want := range map[string]string{
		"AC/DC - Back in Black":  "AC DC Back in Black",
		"Sigur Rós (Hoppípolla)": "Sigur Rós Hoppípolla",
		"-ban this":              "ban this",
		"Don't Stop Me Now":      "Don t Stop Me Now",
	} {
		if got := slskQuery(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestUploadOrder(t *testing.T) {
	c := &slskClient{}
	u := newSlskUploads(c)
	add := func(user, path string, priv bool) {
		u.queue = append(u.queue, &slskUpload{User: user, Path: path, Privileged: priv})
	}
	add("a", "1", false)
	add("a", "2", false)
	add("b", "1", false)
	add("vip", "1", true)
	u.lastServed["b"] = time.Now()
	var got []string
	for _, x := range u.orderLocked() {
		got = append(got, x.User+x.Path)
	}
	if strings.Join(got, " ") != "vip1 a1 b1 a2" {
		t.Errorf("order %v", got)
	}
}

func TestImportNicotine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	os.MkdirAll(filepath.Join(dir, "nicotine"), 0o755)
	os.WriteFile(filepath.Join(dir, "nicotine", "config"), []byte(`[server]
server = ('server.slsknet.org', 2242)
login = someone
passw = se cret
portrange = (2240, 2240)
upnp = False
banlist = ['bad guy', "worse"]
autojoin = ['nicotine']
[transfers]
shared = [('Music', '/data/Music'), ('Mixes', '/data/dj sets')]
uploadslots = 4
useupslots = True
use_upload_speed_limit = primary
uploadlimit = 4000
queuelimit = 500
filelimit = 50
[userinfo]
descr = 'Hello\nthere'
`), 0o600)
	c, err := ImportNicotine(SoulseekConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := SoulseekConfig{Username: "someone", Password: "se cret", Port: 2240, NoUPnP: true, Shares: []string{"/data/Music", "Mixes=/data/dj sets"},
		UploadSlots: 4, UploadKBps: 4000, QueueFiles: 50, QueueMB: 500, Banned: []string{"bad guy", "worse"}, Rooms: []string{"nicotine"},
		Description: "Hello\nthere"}
	if got, w := fmt.Sprintf("%+v", c), fmt.Sprintf("%+v", want); got != w {
		t.Errorf("got  %s\nwant %s", got, w)
	}
	roots := (Config{Soulseek: c}).ShareRoots()
	if len(roots) != 2 || roots[0].Name != "Mixes" || roots[1].Name != "Music" {
		t.Errorf("%+v", roots)
	}
}

func TestSoulseekManualDownloads(t *testing.T) {
	srv := newFakeSlskServer(t)
	peer := newFakeSlskPeer(t, "remote")
	song, cover := bytes.Repeat([]byte("flac"), 30000), []byte("jpeg data")
	peer.files[`Music\Daft Punk\RAM\08 - Get Lucky.flac`] = song
	peer.files[`Music\Daft Punk\RAM\cover.jpg`] = cover
	srv.onSearch = func(token uint32, text string) { peer.answerSearch(token) }
	c := testSlskClient(t, srv, nil, nil)
	srv.mu.Lock()
	srv.addrs["remote"] = slskAddr{ip: net.IPv4(127, 0, 0, 1), port: peer.port()}
	srv.mu.Unlock()
	peer.clientAt = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.listenPort()))
	m := TestManager(t, &Tools{}, nil, TestLibrary(GetLucky()), nil)
	m.slsk = c
	res, err := m.SoulseekSearch(context.Background(), "daft punk lucky")
	if err != nil || len(res) != 2 || res[0].User != "remote" || !res[0].FreeSlot {
		t.Fatalf("%+v %v", res, err)
	}
	var flac SlskResult
	for _, r := range res {
		if strings.HasSuffix(r.Path, ".flac") {
			flac = r
		}
	}
	m.SoulseekDownload(flac.User, flac.Path, flac.Size)
	dest := filepath.Join(m.Config().OutputDir, "Soulseek", "RAM", "08 - Get Lucky.flac")
	WaitFor(t, func() bool { got, _ := os.ReadFile(dest); return bytes.Equal(got, song) })
	WaitFor(t, func() bool {
		for _, x := range m.SoulseekTransfers() {
			if x.File == flac.Path && strings.HasPrefix(x.State, "done: ") {
				return true
			}
		}
		return false
	})
	if err := m.SoulseekDownloadFolder("remote", `Music\Daft Punk\RAM`); err != nil {
		t.Fatal(err)
	}
	coverDest := filepath.Join(m.Config().OutputDir, "Soulseek", "RAM", "cover.jpg")
	WaitFor(t, func() bool { got, _ := os.ReadFile(coverDest); return bytes.Equal(got, cover) })
}
