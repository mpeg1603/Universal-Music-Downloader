package core

import "context"

// Backend is everything a frontend uses: the terminal app (internal/tui), the headless mode,
// and any other frontend. Frontends never reach into the backend's internals, so either side
// can change without the other as long as this stays. Methods are safe to call from any
// goroutine; the views they return are copies.
type Backend interface {
	// Running.
	Start() error
	Stop()
	Context() context.Context // ends when the backend stops
	Idle() bool               // nothing left to do right now
	SummaryLine() string

	// Settings (saved and applied right away; UpdateConfig refuses invalid ones).
	Config() Config
	UpdateConfig(change func(*Config)) error

	// The queue.
	Overview() Overview
	Jobs() []JobView
	Queue(q QueueQuery) []JobView
	QueueCounts() map[string]int
	JobView(key string) (JobView, bool)
	Cancel(key string)
	Restart(key string)
	Prioritize(key string)
	RetryFailed(includeCancelled bool) int
	SetPaused(paused bool)
	Paused() bool

	// Playlists and search.
	Inputs() []InputStatus
	Playlists() []PlaylistStatus
	Reimport()
	SearchMusic(ctx context.Context, text string) ([]MusicResult, error)
	AddMusic(ctx context.Context, r MusicResult, progress func(string)) (playlist string, tracks int, err error)
	Additions() []AdditionStatus
	RemoveAddition(playlist string) bool

	// Your library.
	LibraryStatus() LibScanStatus
	Rescan()
	UpgradeCounts() map[Threshold]int // library files below each quality level
	UpgradeLibrary(th Threshold) (playlist string, tracks int, err error)

	// Sources.
	SourceStatus() []SourceStatus
	PauseSource(name string, paused bool)
	TorrentFeeds() []FeedStatus
	BandcampMailStatus() (pending int, problem string)
	AddBandcampLinks(text string) (found, added int, errs []string)

	// Soulseek.
	SoulseekStatus() SoulseekView
	SoulseekTransfers() []SlskTransferView
	SoulseekSearch(ctx context.Context, text string) ([]SlskResult, error)
	SoulseekDownload(user, file string, size int64)
	SoulseekDownloadFolder(user, folder string) error
	SoulseekAbortUpload(user, path string)
	SoulseekBan(user string) error
	SoulseekReconnect()
	CheckSoulseekPort(ctx context.Context) (open bool, port int, err error)
	SoulseekChats() ([]SlskConversation, int64)
	SoulseekUnread() int // unread private messages
	SoulseekRooms() []SlskRoomInfo
	SoulseekOpenChat(user string)
	SoulseekSay(key, text string) error
	SoulseekMarkRead(key string)
	SoulseekJoinRoom(room string) error
	SoulseekCloseChat(key string)
	SoulseekRefreshRooms()

	// The log; OnLog and OnFinish are for frontends that print as things happen (set them
	// before Start).
	Log() ([]LogLine, int64)
	OnLog(func(LogLine))
	OnFinish(func(JobView)) // a track reached a final state
}

var _ Backend = (*Manager)(nil)

func (m *Manager) Context() context.Context { return m.ctx }

func (m *Manager) BandcampMailStatus() (int, string) { return m.bcMail.Status() }

// Log returns the log's recent lines and a number that changes when lines are added.
func (m *Manager) Log() ([]LogLine, int64) { return AppLog.Snapshot() }

func (m *Manager) OnLog(f func(LogLine)) { AppLog.OnLine = f }

func (m *Manager) OnFinish(f func(JobView)) { m.onFinish = f }

// SetVerbose shows (or hides) the detailed log.
func SetVerbose(v bool) { Verbose = v }
