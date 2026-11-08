package core

import (
	"context"
	"sort"
	"strings"
	"time"
)

// The rest of what a full Soulseek client does, as Nicotine+ does it: users' details and
// online status, buddies, ignoring people, being away, and interests (likes and hates, and
// the recommendations and similar users the server works out from them).

// More server message codes.
const (
	ssAddThingILike      = 51
	ssRemoveThingILike   = 52
	ssRecommendations    = 54
	ssGlobalRecommends   = 56
	ssUserInterests      = 57
	ssSimilarUsers       = 110
	ssItemRecommends     = 111
	ssItemSimilarUsers   = 112
	ssAddThingIHate      = 117
	ssRemoveThingIHate   = 118
	slskStatusOffline    = 0
	slskStatusAway       = 1
	slskStatusOnline     = 2
	slskServerAnswerWait = 20 * time.Second
)

// SlskUser is what's known about a user.
type SlskUser struct {
	Name       string
	Status     int // 0 offline, 1 away, 2 online; -1 unknown
	Privileged bool
	Speed      int // average upload speed, bytes/s
	Files      int
	Folders    int
	Country    string
	// From the user themselves (UserInfo).
	Description string
	HasPicture  bool
	Slots       int
	Queue       int
	FreeSlot    bool
}

func (u SlskUser) StatusText() string {
	switch u.Status {
	case slskStatusOffline:
		return "offline"
	case slskStatusAway:
		return "away"
	case slskStatusOnline:
		return "online"
	}
	return "?"
}

// SlskRecommendation is an interest with how many people share it (negative: dislike).
type SlskRecommendation struct {
	Item  string
	Score int
}

// SlskUserScore is a user found through interests.
type SlskUserScore struct {
	Name  string
	Score int
}

// --- waiting for the server's answers ---------------------------------------------------------

// awaitServer sends a request and waits for the server's answer with code (and, when key is
// set, whose first string is key).
func (c *slskClient) awaitServer(ctx context.Context, code uint32, key string, req *slskMsg) (*slskRead, error) {
	wk := waitKey(code, key)
	ch := make(chan *slskRead, 1)
	c.mu.Lock()
	if c.serverWait == nil {
		c.serverWait = map[string][]chan *slskRead{}
	}
	c.serverWait[wk] = append(c.serverWait[wk], ch)
	c.mu.Unlock()
	if err := c.sendServer(code, req); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r, nil
	case <-time.After(slskServerAnswerWait):
		return nil, failf("the Soulseek server didn't answer")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func waitKey(code uint32, key string) string {
	return strings.ToLower(FirstNonEmpty(key, "-")) + "\x00" + string(rune(code))
}

// deliver hands a server message to whoever waits for it.
func (c *slskClient) deliver(code uint32, key string, body []byte) {
	wk := waitKey(code, key)
	c.mu.Lock()
	ws := c.serverWait[wk]
	delete(c.serverWait, wk)
	c.mu.Unlock()
	for _, ch := range ws {
		ch <- &slskRead{b: body}
	}
}

// --- users ------------------------------------------------------------------------------------

func (c *slskClient) userLocked(name string) *SlskUser {
	if c.users == nil {
		c.users = map[string]*SlskUser{}
	}
	u := c.users[name]
	if u == nil {
		u = &SlskUser{Name: name, Status: -1}
		c.users[name] = u
	}
	return u
}

// userStatus records a GetUserStatus or WatchUser answer.
func (c *slskClient) userStatus(name string, status int, privileged bool) {
	c.mu.Lock()
	u := c.userLocked(name)
	u.Status = status
	u.Privileged = privileged
	c.mu.Unlock()
}

// onWatchUser reads a WatchUser answer: whether the user exists, their status and stats.
func (c *slskClient) onWatchUser(r *slskRead) {
	name := r.str()
	exists := r.boolean()
	c.mu.Lock()
	u := c.userLocked(name)
	if !exists {
		u.Status = slskStatusOffline
	} else {
		u.Status = int(r.u32())
		u.Speed, _, _, u.Files, u.Folders = int(r.u32()), r.u32(), r.u32(), int(r.u32()), int(r.u32())
		if u.Status != slskStatusOffline && r.more() {
			u.Country = r.str()
		}
	}
	c.mu.Unlock()
	c.deliver(ssWatchUser, name, r.b)
}

func (c *slskClient) onUserStats(r *slskRead) {
	name := r.str()
	c.mu.Lock()
	u := c.userLocked(name)
	u.Speed, _, _, u.Files, u.Folders = int(r.u32()), r.u32(), r.u32(), int(r.u32()), int(r.u32())
	c.mu.Unlock()
}

// Watch asks the server to keep us told about a user's status.
func (c *slskClient) Watch(user string) { c.sendServer(ssWatchUser, (&slskMsg{}).str(user)) }

// UserInfo asks a user for their details (description, slots, queue) and the server for their
// status and stats.
func (c *slskClient) UserInfo(ctx context.Context, user string) (SlskUser, error) {
	c.awaitServer(ctx, ssWatchUser, user, (&slskMsg{}).str(user)) // status, stats, country
	ch := make(chan *slskRead, 1)
	c.mu.Lock()
	if c.infoWait == nil {
		c.infoWait = map[string]chan *slskRead{}
	}
	c.infoWait[user] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.infoWait[user] == ch {
			delete(c.infoWait, user)
		}
		c.mu.Unlock()
	}()
	snapshot := func() SlskUser {
		c.mu.Lock()
		defer c.mu.Unlock()
		return *c.userLocked(user)
	}
	if err := c.sendPeer(ctx, user, pmUserInfoRequest, &slskMsg{}); err != nil {
		u := snapshot()
		return u, err
	}
	select {
	case r := <-ch:
		desc := r.str()
		hasPic := r.boolean()
		if hasPic {
			r.bytes()
		}
		slots, queue, free := int(r.u32()), int(r.u32()), r.boolean()
		c.mu.Lock()
		u := c.userLocked(user)
		u.Description, u.HasPicture, u.Slots, u.Queue, u.FreeSlot = desc, hasPic, slots, queue, free
		c.mu.Unlock()
		return snapshot(), nil
	case <-time.After(60 * time.Second):
		return snapshot(), failf("%s didn't send their details", user)
	case <-ctx.Done():
		return snapshot(), ctx.Err()
	}
}

// Users returns what's known about these users.
func (c *slskClient) Users(names []string) []SlskUser {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]SlskUser, 0, len(names))
	for _, n := range names {
		out = append(out, *c.userLocked(n))
	}
	return out
}

// SetAway sets our status: away (true) or online.
func (c *slskClient) SetAway(away bool) {
	st := slskStatusOnline
	if away {
		st = slskStatusAway
	}
	c.sendServer(ssSetStatus, (&slskMsg{}).i32(int32(st)))
	c.setState(func(s *SlskState) { s.Away = away })
}

// --- interests ----------------------------------------------------------------------------------

// sendInterests tells the server our likes and hates (it keeps none between logins).
func (c *slskClient) sendInterests() {
	cfg := c.config()
	for _, x := range cfg.Likes {
		c.sendServer(ssAddThingILike, (&slskMsg{}).str(x))
	}
	for _, x := range cfg.Hates {
		c.sendServer(ssAddThingIHate, (&slskMsg{}).str(x))
	}
}

// Interest adds (or removes) a like or hate on the server.
func (c *slskClient) Interest(item string, hate, remove bool) {
	code := map[[2]bool]uint32{{false, false}: ssAddThingILike, {false, true}: ssRemoveThingILike,
		{true, false}: ssAddThingIHate, {true, true}: ssRemoveThingIHate}[[2]bool{hate, remove}]
	c.sendServer(code, (&slskMsg{}).str(item))
}

func readRecommendations(r *slskRead) []SlskRecommendation {
	var out []SlskRecommendation
	for pass := 0; pass < 2 && r.more(); pass++ { // recommendations, then unrecommendations
		n := int(r.u32())
		for i := 0; i < n && r.err == nil; i++ {
			out = append(out, SlskRecommendation{Item: r.str(), Score: int(r.i32())})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Recommendations asks the server what people with our interests like (global: what everyone
// likes).
func (c *slskClient) Recommendations(ctx context.Context, global bool) ([]SlskRecommendation, error) {
	code := uint32(ssRecommendations)
	if global {
		code = ssGlobalRecommends
	}
	r, err := c.awaitServer(ctx, code, "", &slskMsg{})
	if err != nil {
		return nil, err
	}
	return readRecommendations(r), nil
}

// ItemRecommendations asks what people who like item also like.
func (c *slskClient) ItemRecommendations(ctx context.Context, item string) ([]SlskRecommendation, error) {
	r, err := c.awaitServer(ctx, ssItemRecommends, item, (&slskMsg{}).str(item))
	if err != nil {
		return nil, err
	}
	r.str()
	n := int(r.u32())
	var out []SlskRecommendation
	for i := 0; i < n && r.err == nil; i++ {
		out = append(out, SlskRecommendation{Item: r.str(), Score: int(r.i32())})
	}
	return out, nil
}

// SimilarUsers asks for users with interests like ours (item: like that one item).
func (c *slskClient) SimilarUsers(ctx context.Context, item string) ([]SlskUserScore, error) {
	if item == "" {
		r, err := c.awaitServer(ctx, ssSimilarUsers, "", &slskMsg{})
		if err != nil {
			return nil, err
		}
		n := int(r.u32())
		var out []SlskUserScore
		for i := 0; i < n && r.err == nil; i++ {
			out = append(out, SlskUserScore{Name: r.str(), Score: int(r.u32())})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		return out, nil
	}
	r, err := c.awaitServer(ctx, ssItemSimilarUsers, item, (&slskMsg{}).str(item))
	if err != nil {
		return nil, err
	}
	r.str()
	n := int(r.u32())
	var out []SlskUserScore
	for i := 0; i < n && r.err == nil; i++ {
		out = append(out, SlskUserScore{Name: r.str()})
	}
	return out, nil
}

// UserInterests asks what a user likes and hates.
func (c *slskClient) UserInterests(ctx context.Context, user string) (likes, hates []string, err error) {
	r, err := c.awaitServer(ctx, ssUserInterests, user, (&slskMsg{}).str(user))
	if err != nil {
		return nil, nil, err
	}
	r.str()
	for pass := 0; pass < 2; pass++ {
		n := int(r.u32())
		for i := 0; i < n && r.err == nil; i++ {
			if pass == 0 {
				likes = append(likes, r.str())
			} else {
				hates = append(hates, r.str())
			}
		}
	}
	return likes, hates, nil
}
