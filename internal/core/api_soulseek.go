package core

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What frontends use of Soulseek: the connection's state, transfers, chat, and searching the
// network by hand to download files or whole folders (they go to the downloads folder under
// "Soulseek/", as they are: not renamed or tagged).

// SoulseekView is the Soulseek tab's summary.
type SoulseekView struct {
	On         bool // the source is on with an account: the client runs
	Configured bool // an account is set
	Username   string
	SlskState
	NoShare       bool
	ShareRoots    []string
	SharedFiles   int
	SharedFolders int
	ShareScanning bool
	ShareScanned  time.Time
	ShareProblem  string
	SearchReqs    int64 // searches from others we got
	SearchHits    int64 // and answered
	Parent        string
	BranchLevel   int
	DlUsed        int
	DlLimit       int
	DlWaiting     int
	SearchEvery   time.Duration
	SearchBacklog time.Duration
	UpActive      int
	UpQueued      int
	UpSpeed       float64
	UpSlots       int
	UpLimitKBps   int
	WishTracks    int
	WishEvery     time.Duration
	WishNext      time.Time
	Unread        int
}

func (m *Manager) SoulseekStatus() SoulseekView {
	cfg := m.Config()
	v := SoulseekView{Configured: cfg.Soulseek.Configured(), Username: cfg.Soulseek.Username, NoShare: cfg.Soulseek.NoShare,
		SearchEvery: cfg.Soulseek.SearchInterval(), UpSlots: cfg.Soulseek.Slots(), UpLimitKBps: cfg.Soulseek.UploadKBps}
	c := m.slskClient()
	if c == nil {
		return v
	}
	v.On = true
	v.SlskState = c.State()
	sh := m.shares.Status()
	for _, r := range sh.Roots {
		v.ShareRoots = append(v.ShareRoots, r.Name)
	}
	v.SharedFiles, v.SharedFolders, v.ShareScanning, v.ShareScanned, v.ShareProblem = sh.Files, sh.Folders, sh.Scanning, sh.Scanned, sh.Problem
	v.SearchReqs, v.SearchHits = c.searchReqs.Load(), c.searchHits.Load()
	v.Parent, v.BranchLevel = c.Parent()
	v.DlUsed, v.DlLimit, v.DlWaiting = c.dlGate.Stats()
	v.SearchBacklog = c.searchBacklog()
	v.UpActive, v.UpQueued, v.UpSpeed = c.UploadCounts()
	v.WishEvery, v.WishNext = c.WishlistStatus()
	v.WishTracks = m.WishlistSize()
	v.Unread = c.chat.Unread()
	return v
}

// SoulseekTransfers lists downloads (automatic and by hand) and uploads.
func (m *Manager) SoulseekTransfers() []SlskTransferView {
	c := m.slskClient()
	var out []SlskTransferView
	active := map[string]bool{}
	if c != nil {
		out = c.Transfers()
		for _, x := range out {
			if !x.Up {
				active[dlKey(x.User, x.File)] = true
			}
		}
	}
	m.manualMu.Lock()
	var manual []SlskTransferView
	for i := len(m.manual) - 1; i >= 0; i-- {
		x := m.manual[i]
		if !active[dlKey(x.User, x.File)] {
			manual = append(manual, *x)
		}
	}
	m.manualMu.Unlock()
	// Downloads in progress first, then those waiting or finished, then uploads.
	var downs, ups []SlskTransferView
	for _, x := range out {
		if x.Up {
			ups = append(ups, x)
		} else {
			downs = append(downs, x)
		}
	}
	return append(append(downs, manual...), ups...)
}

func (m *Manager) SoulseekChats() ([]SlskConversation, int64) {
	if c := m.slskClient(); c != nil {
		return c.chat.Conversations()
	}
	return nil, 0
}

func (m *Manager) SoulseekUnread() int {
	if c := m.slskClient(); c != nil {
		return c.chat.Unread()
	}
	return 0
}

func (m *Manager) SoulseekRooms() []SlskRoomInfo {
	if c := m.slskClient(); c != nil {
		return c.chat.Rooms()
	}
	return nil
}

func (m *Manager) soulseekOn() (*slskClient, error) {
	if c := m.slskClient(); c != nil {
		return c, nil
	}
	return nil, fmt.Errorf("Soulseek is off: set your account in Sources → Soulseek and turn the source on")
}

func (m *Manager) SoulseekOpenChat(user string) {
	if c := m.slskClient(); c != nil {
		c.OpenPrivate(user)
	}
}

// SoulseekSay sends text to a conversation: "@user" or "#room".
func (m *Manager) SoulseekSay(key, text string) error {
	c, err := m.soulseekOn()
	if err != nil {
		return err
	}
	name := key[1:]
	if strings.HasPrefix(key, "#") {
		return c.Say(name, text)
	}
	return c.SendPrivate(name, text)
}

func (m *Manager) SoulseekMarkRead(key string) {
	if c := m.slskClient(); c != nil {
		c.chat.markRead(key)
	}
}

// SoulseekJoinRoom joins a room, now and whenever logging in.
func (m *Manager) SoulseekJoinRoom(room string) error {
	c, err := m.soulseekOn()
	if err != nil {
		return err
	}
	if err := c.JoinRoom(room); err != nil {
		return err
	}
	return m.UpdateConfig(func(cfg *Config) {
		if !ContainsString(cfg.Soulseek.Rooms, room) {
			cfg.Soulseek.Rooms = append(cfg.Soulseek.Rooms, room)
		}
	})
}

// SoulseekCloseChat closes a conversation; a room is left (and not joined again).
func (m *Manager) SoulseekCloseChat(key string) {
	c := m.slskClient()
	if c == nil {
		return
	}
	if strings.HasPrefix(key, "#") {
		room := key[1:]
		c.LeaveRoom(room)
		m.UpdateConfig(func(cfg *Config) {
			var keep []string
			for _, r := range cfg.Soulseek.Rooms {
				if r != room {
					keep = append(keep, r)
				}
			}
			cfg.Soulseek.Rooms = keep
		})
	}
	c.chat.close(key)
}

func (m *Manager) SoulseekRefreshRooms() {
	if c := m.slskClient(); c != nil {
		c.RefreshRooms()
	}
}

func (m *Manager) SoulseekAbortUpload(user, path string) {
	if c := m.slskClient(); c != nil {
		c.ups.Abort(user, path)
	}
}

func (m *Manager) SoulseekReconnect() {
	if c := m.slskClient(); c != nil {
		c.Reconnect()
	}
}

// --- searching and downloading by hand --------------------------------------------------------

// SlskResult is a file found by a Soulseek search.
type SlskResult struct {
	User       string
	Path       string // as the user shares it (backslashes)
	Size       int64
	Bitrate    int // kbps; 0 for lossless files
	Duration   int // seconds
	SampleRate int
	BitDepth   int
	FreeSlot   bool
	Queue      int
	Speed      int // the user's upload speed, bytes/s
}

// Folder is the folder the file is in.
func (r SlskResult) Folder() string {
	if i := strings.LastIndexByte(r.Path, '\\'); i >= 0 {
		return r.Path[:i]
	}
	return ""
}

// SoulseekSearch searches the network (paced like every Soulseek search); users who can send
// right away and quickly come first.
func (m *Manager) SoulseekSearch(ctx context.Context, text string) ([]SlskResult, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, err
	}
	resps, err := c.Search(ctx, text)
	if err != nil {
		return nil, err
	}
	var out []SlskResult
	for _, r := range resps {
		for _, f := range r.Files {
			out = append(out, SlskResult{User: r.User, Path: f.Name, Size: f.Size, Bitrate: f.attr(slskAttrBitrate),
				Duration: f.attr(slskAttrDuration), SampleRate: f.attr(slskAttrSampleRate), BitDepth: f.attr(slskAttrBitDepth),
				FreeSlot: r.FreeSlot, Queue: r.Queue, Speed: r.Speed})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.FreeSlot != b.FreeSlot {
			return a.FreeSlot
		}
		if a.Queue != b.Queue {
			return a.Queue < b.Queue
		}
		if a.Speed != b.Speed {
			return a.Speed > b.Speed
		}
		return a.Path < b.Path
	})
	return out[:min(len(out), 1000)], nil
}

// SoulseekDownload downloads a file by hand into the downloads folder's "Soulseek" folder. It
// waits its turn like every Soulseek download.
func (m *Manager) SoulseekDownload(user, file string, size int64) {
	x := &SlskTransferView{User: user, File: file, Total: size, State: "waiting"}
	m.manualMu.Lock()
	for _, o := range m.manual {
		if o.User != user || o.File != file {
			continue
		}
		done, have := strings.CutPrefix(o.State, "done: ")
		if o.State == "waiting" || (have && fileExists(done)) {
			m.manualMu.Unlock()
			return // already on its way, or here
		}
	}
	m.manual = append(m.manual, x)
	if len(m.manual) > 300 {
		m.manual = m.manual[len(m.manual)-300:]
	}
	m.manualMu.Unlock()
	set := func(state string) {
		m.manualMu.Lock()
		x.State = state
		m.manualMu.Unlock()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		c, err := m.soulseekOn()
		if err != nil {
			set("failed: " + err.Error())
			return
		}
		slash := strings.ReplaceAll(file, `\`, "/")
		tmpDir := filepath.Join(m.dataDir, "tmp", "soulseek")
		os.MkdirAll(tmpDir, 0o755)
		tmp, err := os.CreateTemp(tmpDir, "*"+path.Ext(slash))
		if err != nil {
			set("failed: " + err.Error())
			return
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		err = c.Download(m.ctx, user, file, size, tmp.Name(), nil)
		if err != nil {
			if m.ctx.Err() != nil {
				set("stopped")
			} else {
				set("failed: " + err.Error())
			}
			return
		}
		rel := "Soulseek/" + sanitize(path.Base(path.Dir(slash))) + "/" + sanitize(path.Base(slash))
		dest, err := m.place(tmp.Name(), rel, "soulseek:"+user+"\x00"+file, m.Config())
		if err != nil {
			set("failed: " + err.Error())
			return
		}
		m.manualMu.Lock()
		x.State, x.Done = "done: "+dest, size
		m.manualMu.Unlock()
		Logf("Soulseek: downloaded %s from %s", dest, user)
		m.shares.add(dest)
	}()
}

// SoulseekDownloadFolder downloads every file of a user's folder (and its subfolders).
func (m *Manager) SoulseekDownloadFolder(user, folder string) error {
	c, err := m.soulseekOn()
	if err != nil {
		return err
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
		defer cancel()
		files, err := c.FolderContents(ctx, user, folder)
		if err != nil {
			Logf("Soulseek: couldn't get %s's folder %s: %v", user, folder, err)
			return
		}
		Logf("Soulseek: downloading %d files of %s's folder %s", len(files), user, folder)
		for _, f := range files {
			m.SoulseekDownload(user, f.Name, f.Size)
		}
	}()
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// manualDownloads are what you downloaded by hand from the Soulseek tab.
type manualDownloads struct {
	manualMu sync.Mutex
	manual   []*SlskTransferView
}

// --- users, buddies, interests ------------------------------------------------------------

// SoulseekBrowse lists every file a user shares.
func (m *Manager) SoulseekBrowse(ctx context.Context, user string) ([]SlskResult, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, err
	}
	files, err := c.Browse(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]SlskResult, 0, len(files))
	for _, f := range files {
		out = append(out, SlskResult{User: user, Path: f.Name, Size: f.Size, Bitrate: f.attr(slskAttrBitrate),
			Duration: f.attr(slskAttrDuration), SampleRate: f.attr(slskAttrSampleRate), BitDepth: f.attr(slskAttrBitDepth)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path) })
	return out, nil
}

// SoulseekUserInfo asks a user for their details, and the server for their status and stats.
func (m *Manager) SoulseekUserInfo(ctx context.Context, user string) (SlskUser, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return SlskUser{Name: user, Status: -1}, err
	}
	return c.UserInfo(ctx, user)
}

// SoulseekBuddies are the users you follow, with what's known about them.
func (m *Manager) SoulseekBuddies() []SlskUser {
	names := m.Config().Soulseek.Buddies
	if c := m.slskClient(); c != nil {
		return c.Users(names)
	}
	out := make([]SlskUser, 0, len(names))
	for _, n := range names {
		out = append(out, SlskUser{Name: n, Status: -1})
	}
	return out
}

// SoulseekSetUser adds a user to (on) or takes them off (off) a list: "buddies", "banned" or
// "ignored".
func (m *Manager) SoulseekSetUser(list, user string, on bool) error {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil
	}
	err := m.UpdateConfig(func(cfg *Config) {
		var l *[]string
		switch list {
		case "buddies":
			l = &cfg.Soulseek.Buddies
		case "banned":
			l = &cfg.Soulseek.Banned
		case "ignored":
			l = &cfg.Soulseek.Ignored
		default:
			return
		}
		var keep []string
		for _, x := range *l {
			if !strings.EqualFold(x, user) {
				keep = append(keep, x)
			}
		}
		if on {
			keep = append(keep, user)
		}
		*l = keep
	})
	if c := m.slskClient(); c != nil && err == nil && list == "banned" && on {
		c.ups.dropUser(user)
	}
	return err
}

func (m *Manager) SoulseekSetAway(away bool) {
	if c := m.slskClient(); c != nil {
		c.SetAway(away)
	}
}

// SoulseekSetInterest adds (or with remove, takes away) something you like (or with hate,
// dislike); the server works out recommendations and similar users from these.
func (m *Manager) SoulseekSetInterest(item string, hate, remove bool) error {
	item = strings.TrimSpace(item)
	if item == "" {
		return nil
	}
	return m.UpdateConfig(func(cfg *Config) {
		l := &cfg.Soulseek.Likes
		if hate {
			l = &cfg.Soulseek.Hates
		}
		var keep []string
		for _, x := range *l {
			if !strings.EqualFold(x, item) {
				keep = append(keep, x)
			}
		}
		if !remove {
			keep = append(keep, item)
		}
		*l = keep
	})
}

func (m *Manager) SoulseekRecommendations(ctx context.Context, global bool) ([]SlskRecommendation, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, err
	}
	return c.Recommendations(ctx, global)
}

func (m *Manager) SoulseekItemRecommendations(ctx context.Context, item string) ([]SlskRecommendation, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, err
	}
	return c.ItemRecommendations(ctx, item)
}

// SoulseekSimilarUsers lists users with interests like yours (or who like item).
func (m *Manager) SoulseekSimilarUsers(ctx context.Context, item string) ([]SlskUserScore, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, err
	}
	return c.SimilarUsers(ctx, item)
}

func (m *Manager) SoulseekUserInterests(ctx context.Context, user string) ([]string, []string, error) {
	c, err := m.soulseekOn()
	if err != nil {
		return nil, nil, err
	}
	return c.UserInterests(ctx, user)
}

// SoulseekClearFinished takes finished, failed and cancelled transfers off the lists.
func (m *Manager) SoulseekClearFinished() {
	m.manualMu.Lock()
	var keep []*SlskTransferView
	for _, x := range m.manual {
		if x.State == "waiting" {
			keep = append(keep, x)
		}
	}
	m.manual = keep
	m.manualMu.Unlock()
	if c := m.slskClient(); c != nil {
		c.ups.mu.Lock()
		c.ups.recent = nil
		c.ups.mu.Unlock()
	}
}
