package core

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Soulseek chat: private messages and chat rooms.

type SlskLine struct {
	At   time.Time
	From string
	Text string
	Own  bool
}

type SlskConversation struct {
	Name   string
	Room   bool
	Lines  []SlskLine
	Unread int
	Users  []string // in a room
	Joined bool     // a room we're in
}

func (cv *SlskConversation) Key() string { return ChatKey(cv.Name, cv.Room) }

func ChatKey(name string, room bool) string {
	if room {
		return "#" + name
	}
	return "@" + name
}

type SlskRoomInfo struct {
	Name  string
	Users int
}

type slskChat struct {
	mu    sync.Mutex
	convs map[string]*SlskConversation
	order []string // conversation keys, oldest first
	rooms []SlskRoomInfo
	seq   int64 // changes, for the app to redraw
}

func newSlskChat() *slskChat { return &slskChat{convs: map[string]*SlskConversation{}} }

func (ch *slskChat) conv(name string, room bool) *SlskConversation {
	k := ChatKey(name, room)
	cv := ch.convs[k]
	if cv == nil {
		cv = &SlskConversation{Name: name, Room: room}
		ch.convs[k] = cv
		ch.order = append(ch.order, k)
	}
	return cv
}

func (cv *SlskConversation) add(l SlskLine) {
	cv.Lines = append(cv.Lines, l)
	if len(cv.Lines) > 1000 {
		cv.Lines = cv.Lines[len(cv.Lines)-1000:]
	}
	if !l.Own {
		cv.Unread++
	}
}

func (ch *slskChat) private(user, text string, at time.Time, own bool) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	from := user
	if own {
		from = ""
	}
	ch.conv(user, false).add(SlskLine{At: at, From: from, Text: text, Own: own})
	ch.seq++
}

// roomLine adds what someone said in a room (the server repeats what we say, too).
func (ch *slskChat) roomLine(room, user, text string, own bool) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.conv(room, true).add(SlskLine{At: time.Now(), From: user, Text: text, Own: own})
	ch.seq++
}

func (ch *slskChat) notice(room, text string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.conv(room, true).add(SlskLine{At: time.Now(), Text: text, Own: true})
	ch.seq++
}

func (ch *slskChat) joined(room string, users []string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	cv := ch.conv(room, true)
	cv.Users, cv.Joined = users, true
	sort.Strings(cv.Users)
	ch.seq++
}

func (ch *slskChat) left(room string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if cv := ch.convs[ChatKey(room, true)]; cv != nil {
		cv.Joined, cv.Users = false, nil
	}
	ch.seq++
}

func (ch *slskChat) userJoined(room, user string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if cv := ch.convs[ChatKey(room, true)]; cv != nil && !ContainsString(cv.Users, user) {
		cv.Users = append(cv.Users, user)
		sort.Strings(cv.Users)
		ch.seq++
	}
}

func (ch *slskChat) userLeft(room, user string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if cv := ch.convs[ChatKey(room, true)]; cv != nil {
		for i, u := range cv.Users {
			if u == user {
				cv.Users = append(cv.Users[:i], cv.Users[i+1:]...)
				ch.seq++
				break
			}
		}
	}
}

func (ch *slskChat) setRooms(names []string, counts []int) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.rooms = ch.rooms[:0]
	for i, n := range names {
		r := SlskRoomInfo{Name: n}
		if i < len(counts) {
			r.Users = counts[i]
		}
		ch.rooms = append(ch.rooms, r)
	}
	sort.Slice(ch.rooms, func(i, j int) bool { return ch.rooms[i].Users > ch.rooms[j].Users })
	ch.seq++
}

// Conversations returns copies of the conversations, in the order they started.
func (ch *slskChat) Conversations() ([]SlskConversation, int64) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	out := make([]SlskConversation, 0, len(ch.order))
	for _, k := range ch.order {
		cv := *ch.convs[k]
		cv.Lines = append([]SlskLine(nil), cv.Lines...)
		cv.Users = append([]string(nil), cv.Users...)
		out = append(out, cv)
	}
	return out, ch.seq
}

func (ch *slskChat) Rooms() []SlskRoomInfo {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return append([]SlskRoomInfo(nil), ch.rooms...)
}

func (ch *slskChat) markRead(key string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if cv := ch.convs[key]; cv != nil && cv.Unread > 0 {
		cv.Unread = 0
		ch.seq++
	}
}

// Unread counts unread private messages (rooms are too busy to count).
func (ch *slskChat) Unread() int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	n := 0
	for _, cv := range ch.convs {
		if !cv.Room {
			n += cv.Unread
		}
	}
	return n
}

func (ch *slskChat) close(key string) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	delete(ch.convs, key)
	for i, k := range ch.order {
		if k == key {
			ch.order = append(ch.order[:i], ch.order[i+1:]...)
			break
		}
	}
	ch.seq++
}

// --- the client's chat actions ----------------------------------------------------------------

func (c *slskClient) SendPrivate(user, text string) error {
	user, text = strings.TrimSpace(user), strings.TrimSpace(text)
	if user == "" || text == "" {
		return nil
	}
	if err := c.sendServer(ssMessageUser, (&slskMsg{}).str(user).str(text)); err != nil {
		return err
	}
	c.chat.private(user, text, time.Now(), true)
	return nil
}

func (c *slskClient) OpenPrivate(user string) {
	if user = strings.TrimSpace(user); user != "" {
		c.chat.mu.Lock()
		c.chat.conv(user, false)
		c.chat.seq++
		c.chat.mu.Unlock()
		c.sendServer(ssWatchUser, (&slskMsg{}).str(user))
	}
}

func (c *slskClient) JoinRoom(room string) error {
	if room = strings.TrimSpace(room); room == "" {
		return nil
	}
	return c.sendServer(ssJoinRoom, (&slskMsg{}).str(room).u32(0))
}

func (c *slskClient) LeaveRoom(room string) error {
	return c.sendServer(ssLeaveRoom, (&slskMsg{}).str(room))
}

func (c *slskClient) Say(room, text string) error {
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	return c.sendServer(ssSayChatroom, (&slskMsg{}).str(room).str(text))
}

// RefreshRooms asks the server for the full room list.
func (c *slskClient) RefreshRooms() error { return c.sendServer(ssRoomList, &slskMsg{}) }
