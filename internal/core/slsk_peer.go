package core

import (
	"context"
	"sync"
	"time"
)

// Peer messages, search requests from others, and the distributed search network.

func (c *slskClient) onPeer(sc *slskConn, code uint32, r *slskRead) {
	user := sc.user
	switch code {
	case pmFileSearchResponse:
		data, err := slskDecompress(r.b, 32<<20)
		if err != nil {
			return
		}
		r = &slskRead{b: data}
		resp := slskResponse{User: r.str()}
		token := r.u32()
		resp.Files = readSlskFiles(r, 20000)
		resp.FreeSlot, resp.Speed, resp.Queue = r.boolean(), int(r.u32()), int(r.u32())
		if r.err != nil && len(resp.Files) == 0 {
			return
		}
		resp.User = user // the connection's user, not what the message claims
		c.mu.Lock()
		s := c.searches[token]
		c.mu.Unlock()
		if s != nil && !c.isBanned(user) {
			s.add(resp)
		}
	case pmUserInfoResponse:
		c.mu.Lock()
		ch := c.infoWait[user]
		delete(c.infoWait, user)
		c.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	case pmFolderContentsResponse:
		data, err := slskDecompress(r.b, 32<<20)
		if err != nil {
			return
		}
		r = &slskRead{b: data}
		token := r.u32()
		r.str()
		n := int(r.u32())
		var files []slskFile
		for i := 0; i < n && i < 1000 && r.err == nil; i++ {
			dir := r.str()
			for _, f := range readSlskFiles(r, 20000) {
				f.Name = dir + `\` + f.Name
				files = append(files, f)
			}
		}
		c.mu.Lock()
		ch := c.folderWait[token]
		delete(c.folderWait, token)
		c.mu.Unlock()
		if ch != nil {
			ch <- files
		}
	case pmSharedFileListResponse:
		c.mu.Lock()
		ch := c.browseWait[user]
		delete(c.browseWait, user)
		c.mu.Unlock()
		if ch == nil {
			return // not asked for
		}
		data, err := slskDecompress(r.b, 512<<20)
		if err != nil {
			ch <- nil
			return
		}
		r = &slskRead{b: data}
		n := int(r.u32())
		var files []slskFile
		for i := 0; i < n && i < 200000 && r.err == nil; i++ {
			dir := r.str()
			for _, f := range readSlskFiles(r, 100000) {
				f.Name = dir + `\` + f.Name
				files = append(files, f)
			}
		}
		ch <- files
	case pmSharedFileListRequest:
		var body []byte
		if c.config().NoShare || c.isBanned(user) {
			body = slskCompress((&slskMsg{}).u32(0).u32(0).u32(0).b)
		} else {
			body = c.shares.fileList()
		}
		go sc.write(slskFrame(pmSharedFileListResponse, 4, body))
	case pmUserInfoRequest:
		cfg := c.config()
		slots, queued, free := c.ups.info()
		m := (&slskMsg{}).str(cfg.Description).boolean(false).u32(uint32(slots)).u32(uint32(queued)).boolean(free).u32(0)
		go sc.send(pmUserInfoResponse, m)
	case pmFolderContentsRequest:
		token, folder := r.u32(), r.str()
		if r.err != nil {
			return
		}
		m := (&slskMsg{}).u32(token).str(folder)
		if c.config().NoShare || c.isBanned(user) {
			m.u32(0)
		} else {
			c.shares.folderContents(m, folder)
		}
		go sc.write(slskFrame(pmFolderContentsResponse, 4, slskCompress(m.b)))
	case pmTransferRequest:
		dir, token, path := r.u32(), r.u32(), r.str()
		var size uint64
		if dir == 1 {
			size = r.fileSize()
		}
		if r.err != nil && path == "" {
			return
		}
		if dir == 1 { // they're ready to send us a file
			c.transferOffered(sc, token, path, int64(size))
		} else { // an old-style download request: queue it like QueueUpload
			reason := c.ups.enqueue(user, path)
			if reason == "" {
				reason = "Queued"
			}
			go sc.send(pmTransferResponse, (&slskMsg{}).u32(token).boolean(false).str(reason))
		}
	case pmTransferResponse:
		token, allowed := r.u32(), r.boolean()
		reason := ""
		if !allowed {
			reason = r.str()
		}
		c.ups.response(user, token, allowed, reason)
	case pmQueueUpload:
		path := r.str()
		if r.err != nil {
			return
		}
		if reason := c.ups.enqueue(user, path); reason != "" && reason != "Queued" {
			go sc.send(pmUploadDenied, (&slskMsg{}).str(path).str(reason))
		}
	case pmPlaceInQueueRequest:
		path := r.str()
		if place := c.ups.place(user, path); place > 0 {
			go sc.send(pmPlaceInQueueResponse, (&slskMsg{}).str(path).u32(uint32(place)))
		}
	case pmPlaceInQueueResponse:
		path, place := r.str(), r.u32()
		c.downloadEvent(user, path, slskDlEvent{kind: "place", place: int(place)})
	case pmUploadDenied:
		path, reason := r.str(), r.str()
		c.downloadEvent(user, path, slskDlEvent{kind: "denied", reason: reason})
	case pmUploadFailed:
		path := r.str()
		c.downloadEvent(user, path, slskDlEvent{kind: "failed"})
	}
}

// --- answering searches ---------------------------------------------------------------------

// searchRequest answers someone's search when we share matching files.
func (c *slskClient) searchRequest(user string, token uint32, text string) {
	c.searchReqs.Add(1)
	c.mu.Lock()
	me, excluded := c.user, c.excluded
	c.mu.Unlock()
	if user == "" || user == me || len(text) < 3 {
		return
	}
	cfg := c.config()
	if cfg.NoShare || ContainsFold(cfg.Banned, user) {
		return
	}
	files := c.shares.search(text, excluded, 150)
	if len(files) == 0 {
		return
	}
	select {
	case c.answerSem <- struct{}{}:
	default:
		return // too many answers on their way already
	}
	c.searchHits.Add(1)
	_, queued, free := c.ups.info()
	m := (&slskMsg{}).str(me).u32(token).u32(uint32(len(files)))
	for _, f := range files {
		writeSlskFile(m, f)
	}
	m.boolean(free).u32(uint32(c.ups.avgSpeed())).u32(uint32(queued)).u32(0).u32(0)
	body := slskCompress(m.b)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer func() { <-c.answerSem }()
		ctx, cancel := context.WithTimeout(c.ctx, 40*time.Second)
		defer cancel()
		if err := c.sendPeer(ctx, user, pmFileSearchResponse, &slskMsg{b: body}); err != nil {
			c.debugf("couldn't send %d search results to %s: %v", len(files), user, err)
		} else {
			c.debugf("sent %s %d results for %q", user, len(files), text)
		}
	}()
}

// --- distributed network ------------------------------------------------------------------------

// Searches reach most clients through the distributed network: a tree of clients passing
// them on. We join it as a leaf (no children of our own): the server suggests possible
// parents, we connect to them, and adopt the first that passes on a search.

type slskParentCand struct {
	user string
	addr slskAddr
}

type slskDist struct {
	mu         sync.Mutex
	me         string
	parent     *slskConn
	parentName string
	level      int
	root       string
	cands      map[string]*slskCandConn
}

type slskCandConn struct {
	sc       *slskConn
	level    int
	hasLevel bool
	root     string
}

func (d *slskDist) reset(me string) {
	d.mu.Lock()
	d.me, d.parent, d.parentName, d.level, d.root, d.cands = me, nil, "", 0, me, map[string]*slskCandConn{}
	d.mu.Unlock()
}

func (d *slskDist) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.parent != nil {
		d.parent.close()
	}
	for _, cc := range d.cands {
		if cc.sc != nil {
			cc.sc.close()
		}
	}
	d.parent, d.parentName, d.cands = nil, "", map[string]*slskCandConn{}
}

// Parent returns our parent in the distributed network and our branch level.
func (c *slskClient) Parent() (string, int) {
	c.dist.mu.Lock()
	defer c.dist.mu.Unlock()
	return c.dist.parentName, c.dist.level
}

func (c *slskClient) haveNoParent() {
	c.dist.mu.Lock()
	me := c.dist.me
	c.dist.parent, c.dist.parentName, c.dist.level, c.dist.root = nil, "", 0, me
	c.dist.mu.Unlock()
	c.sendServer(ssHaveNoParent, (&slskMsg{}).boolean(true))
	c.sendServer(ssBranchRoot, (&slskMsg{}).str(me))
	c.sendServer(ssBranchLevel, (&slskMsg{}).u32(0))
	c.sendServer(ssAcceptChildren, (&slskMsg{}).boolean(false))
}

func (c *slskClient) possibleParents(cands []slskParentCand) {
	c.dist.mu.Lock()
	defer c.dist.mu.Unlock()
	if c.dist.parent != nil || c.dist.cands == nil {
		return
	}
	for _, p := range cands {
		if p.user == c.dist.me || c.dist.cands[p.user] != nil || len(c.dist.cands) >= 10 {
			continue
		}
		cc := &slskCandConn{}
		c.dist.cands[p.user] = cc
		p := p
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.tryParent(p, cc)
		}()
	}
}

func (c *slskClient) tryParent(p slskParentCand, cc *slskCandConn) {
	addr := p.addr
	sc, err := c.connect(c.ctx, p.user, "D", &addr)
	c.dist.mu.Lock()
	if err != nil || c.dist.cands[p.user] != cc || c.dist.parent != nil {
		if c.dist.cands[p.user] == cc {
			delete(c.dist.cands, p.user)
		}
		c.dist.mu.Unlock()
		if sc != nil {
			sc.close()
		}
		return
	}
	cc.sc = sc
	c.dist.mu.Unlock()
	defer func() {
		sc.close()
		c.dist.mu.Lock()
		wasParent := c.dist.parent == sc
		if c.dist.cands[p.user] == cc {
			delete(c.dist.cands, p.user)
		}
		if wasParent {
			c.dist.parent, c.dist.parentName = nil, ""
		}
		c.dist.mu.Unlock()
		if wasParent && c.ctx.Err() == nil && c.online() {
			c.debugf("lost our distributed parent %s", p.user)
			c.haveNoParent()
		}
	}()
	deadline := time.Now().Add(30 * time.Second) // a candidate must send a search soon
	for {
		c.dist.mu.Lock()
		isParent := c.dist.parent == sc
		c.dist.mu.Unlock()
		if isParent {
			sc.c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		} else {
			sc.c.SetReadDeadline(deadline)
		}
		code, body, err := readSlskFrame(sc.r, 1, slskLimitDist)
		if err != nil {
			return
		}
		r := &slskRead{b: body}
		if code == 93 { // an embedded message, sent by older SoulseekQt versions
			if r.u8() != 3 {
				continue
			}
			code, r = 3, &slskRead{b: r.rest()}
		}
		switch code {
		case 4: // branch level
			level := int(r.i32())
			if r.err != nil || level < 0 {
				return
			}
			c.dist.mu.Lock()
			if c.dist.parent == sc {
				c.dist.level = level + 1
				c.dist.mu.Unlock()
				c.sendServer(ssBranchLevel, (&slskMsg{}).u32(uint32(level+1)))
				continue
			}
			cc.level, cc.hasLevel = level, true
			if level == 0 {
				cc.root = p.user
			}
			c.dist.mu.Unlock()
		case 5: // branch root
			root := r.str()
			if r.err != nil || root == "" || len(root) > 256 {
				return
			}
			c.dist.mu.Lock()
			if c.dist.parent == sc {
				c.dist.root = root
				c.dist.mu.Unlock()
				c.sendServer(ssBranchRoot, (&slskMsg{}).str(root))
				continue
			}
			cc.root = root
			c.dist.mu.Unlock()
		case 3: // search
			c.dist.mu.Lock()
			switch {
			case c.dist.parent == sc:
				c.dist.mu.Unlock()
			case c.dist.parent == nil && cc.hasLevel && cc.root != "":
				c.adoptParent(p.user, sc, cc) // unlocks
			default:
				c.dist.mu.Unlock()
				if c.dist.parent != nil {
					return // someone else is our parent
				}
				continue
			}
			c.distSearch(r)
		}
	}
}

// adoptParent makes a candidate our parent; c.dist.mu is held and released.
func (c *slskClient) adoptParent(user string, sc *slskConn, cc *slskCandConn) {
	d := &c.dist
	d.parent, d.parentName, d.level, d.root = sc, user, cc.level+1, cc.root
	level, root := d.level, d.root
	for name, other := range d.cands {
		if other != cc && other.sc != nil {
			other.sc.close()
		}
		if other != cc {
			delete(d.cands, name)
		}
	}
	d.mu.Unlock()
	c.debugf("distributed parent: %s (branch level %d, root %s)", user, level, root)
	c.sendServer(ssHaveNoParent, (&slskMsg{}).boolean(false))
	c.sendServer(ssBranchRoot, (&slskMsg{}).str(root))
	c.sendServer(ssBranchLevel, (&slskMsg{}).u32(uint32(level)))
}

// distSearch handles a search request from the distributed network.
func (c *slskClient) distSearch(r *slskRead) {
	r.u32() // always 49
	user, token, text := r.str(), r.u32(), r.str()
	if r.err == nil {
		c.searchRequest(user, token, text)
	}
}

// FolderContents asks user for the files of folder (and its subfolders), with full paths.
func (c *slskClient) FolderContents(ctx context.Context, user, folder string) ([]slskFile, error) {
	token := c.nextToken()
	ch := make(chan []slskFile, 1)
	c.mu.Lock()
	c.folderWait[token] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.folderWait, token)
		c.mu.Unlock()
	}()
	if err := c.sendPeer(ctx, user, pmFolderContentsRequest, (&slskMsg{}).u32(token).str(folder)); err != nil {
		return nil, err
	}
	select {
	case files := <-ch:
		return files, nil
	case <-time.After(60 * time.Second):
		return nil, failf("%s didn't send the folder's contents", user)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Browse asks user for the list of every file they share, with full paths.
func (c *slskClient) Browse(ctx context.Context, user string) ([]slskFile, error) {
	ch := make(chan []slskFile, 1)
	c.mu.Lock()
	if c.browseWait[user] != nil {
		c.mu.Unlock()
		return nil, failf("already browsing %s", user)
	}
	c.browseWait[user] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.browseWait[user] == ch {
			delete(c.browseWait, user)
		}
		c.mu.Unlock()
	}()
	if err := c.sendPeer(ctx, user, pmSharedFileListRequest, &slskMsg{}); err != nil {
		return nil, err
	}
	select {
	case files := <-ch:
		if files == nil {
			return nil, failf("%s sent an unreadable file list", user)
		}
		return files, nil
	case <-time.After(3 * time.Minute):
		return nil, failf("%s didn't send their file list", user)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// lastSpeed is how fast the last download from user came in (bytes/s; 0: none yet).
func (c *slskClient) lastSpeed(user string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.speeds[user]
}
