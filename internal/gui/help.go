package gui

import (
	"fmt"
	"strings"

	qt "github.com/mappu/miqt/qt6"
)

// Explanations, in plain words: the "?" next to a page's title shows its topic, and the Help
// page has them all.

type helpTopic struct {
	title string
	body  string // simple HTML
}

var helpOrder = []string{"start", "downloads", "find", "playlists", "library", "sources", "quality", "soulseek", "connection", "keys", "problems"}

var helpTopics = map[string]helpTopic{
	"start": {"How it works", `
<p>Tell the app what you want, and it finds the best copy it can get and saves it to a folder on your computer.</p>
<ol>
<li><b>Say what you want.</b> Search for a song, album or artist in <b>Find music</b>, or bring in whole playlists in <b>Playlists</b>.</li>
<li><b>The app checks what you already have.</b> Anything in <b>My library</b> at a quality you're happy with is skipped.</li>
<li><b>It looks for the rest.</b> For each song it goes through your <b>Sources</b> from top to bottom and takes the first good copy.</li>
<li><b>Files arrive in your downloads folder</b>, named and tagged, ready for your music player. Watch them in <b>Downloads</b>.</li>
</ol>
<p>You can close the app at any time: it remembers where it stopped and carries on next time.</p>`},
	"downloads": {"Downloads", `
<p>Every song the app is working on or has finished.</p>
<ul>
<li><b>Waiting</b>: its turn hasn't come yet.</li>
<li><b>Searching</b>: the app is asking your sources for it.</li>
<li><b>Downloading</b>: on its way, with speed and progress.</li>
<li><b>Will retry</b>: a source was busy or having trouble; the app tries again by itself.</li>
<li><b>Downloaded</b>: the file is in your downloads folder.</li>
<li><b>In your library</b>: you already have it, so nothing was downloaded.</li>
<li><b>Not found</b>: no source had it. Click the song to see what each source said; <b>Try not-found again</b> gives them all another go later.</li>
</ul>
<p>Click a song for its details and what you can do with it. <b>Do next</b> moves it to the front of the line.</p>`},
	"find": {"Find music", `
<p>Type a song, an album or an artist, in any way that comes to mind, and pick from the results.</p>
<ul>
<li>A <b>song</b> adds just that song.</li>
<li>An <b>album</b> adds all its tracks.</li>
<li>An <b>artist</b> adds everything they've released, each song once.</li>
</ul>
<p>What you add goes to the front of the line in Downloads. The results come from a large music catalog (Deezer) so names and artwork details are right; the music itself comes from your Sources.</p>
<p><b>More options</b> lets you search by artist, album, track or year separately, when plain words find too much.</p>`},
	"playlists": {"Playlists", `
<p>The app can work through whole playlists.</p>
<p><b>From Spotify:</b> open <a href="https://exportify.net">exportify.net</a>, log in, press <i>Export all</i>, and add the file you get here. Spotify's own "download your data" file works too. A public playlist or album link also works, up to its first 100 tracks.</p>
<p><b>From anywhere else:</b> CSV files from tools like TuneMyMusic, or a plain text file with one song, album or artist per line.</p>
<p>Turn a playlist off and its songs leave the line without being forgotten. <b>Priority</b> decides which playlist goes first.</p>`},
	"library": {"My library", `
<p>Tell the app where your music already is, and it won't download what you already have.</p>
<p>Your library folders are <b>only ever read</b>. The app never creates, changes, moves or deletes anything in them. New files always go to your downloads folder.</p>
<p><b>Good enough quality</b> is the line you draw: a song you have at that quality or better is skipped. A song you have in worse quality is downloaded only if a source has a clearly better copy, and your old file stays where it is.</p>
<p><b>Upgrade my library</b> goes looking for better copies of the files below a quality you choose.</p>`},
	"sources": {"Sources", `
<p>Sources are the services the app downloads from. For each song it tries them <b>from top to bottom</b> and uses the first one with a good copy, so put your favorites first. Lossless sources near the top get you better quality; those with the largest catalogs near the bottom catch what the others miss.</p>
<p>Some work right away, some need an account or a sign-in: <b>Set up</b> on each one tells you what it needs.</p>
<p>If a source has trouble, the app pauses it for a while on its own and carries on with the others.</p>`},
	"quality": {"Quality, in short", `
<p><b>Lossless</b> (FLAC, ALAC, WAV) is an exact copy of the recording. Files are big, about 30 MB a song.</p>
<p><b>MP3 320</b> and similar are compressed, but most people can't hear the difference. About 10 MB a song.</p>
<p><b>MP3 128</b> and YouTube's audio are noticeably lower quality, but they exist for almost anything.</p>
<p>The app never makes a file sound better than its source. With <b>Format: keep original</b> (the default), files are saved exactly as they came, which is the best choice for nearly everyone.</p>`},
	"soulseek": {"Soulseek", `
<p>Soulseek is a long-running community where people share their music collections with each other directly, often in lossless quality, including a lot you won't find anywhere else.</p>
<p><b>You need a free account</b>: choose any new name and password in <b>Sources → Soulseek → Set up</b> and it's created for you.</p>
<p><b>It's give and take.</b> Sharing your music is what keeps the network alive, and many people won't send files to someone who shares nothing. You choose exactly which folders are shared.</p>
<p>The app is a good citizen by design: it spaces out its searches and asks for only a few files at a time, because the network bans clients that flood it.</p>
<p>The Soulseek page lets you search by hand, see transfers, browse what other people share and chat. Songs no source had are searched for again slowly, on a wishlist.</p>`},
	"connection": {"How sources connect", `
<p>Each source can connect in its own way (under <b>Set up → Connection</b>). Most people never need this.</p>
<ul>
<li><b>Like everything else</b> uses your computer's normal connection, including a VPN if you have one on.</li>
<li><b>Direct</b> goes around your VPN for that one source. Useful for services that dislike VPNs, or for Soulseek so other people can reach you.</li>
<li><b>Proxy</b> sends just that source through a proxy you have.</li>
</ul>`},
	"keys": {"Keyboard shortcuts", `
<table cellpadding="3">
<tr><td><b>Ctrl+1 … Ctrl+8</b></td><td>go to a page</td></tr>
<tr><td><b>Ctrl+F</b></td><td>find music</td></tr>
<tr><td><b>Shift+P</b></td><td>pause or resume everything</td></tr>
<tr><td><b>F1</b></td><td>this help</td></tr>
<tr><td><b>Ctrl+Q</b></td><td>quit</td></tr>
<tr><td colspan="2"><br><b>In Downloads, on a selected song</b></td></tr>
<tr><td><b>Enter</b></td><td>show its details</td></tr>
<tr><td><b>N</b></td><td>do it next</td></tr>
<tr><td><b>R</b></td><td>try it again</td></tr>
<tr><td><b>C</b></td><td>cancel it</td></tr>
<tr><td><b>Shift+R</b></td><td>try everything not found again</td></tr>
<tr><td><b>/</b></td><td>filter the list by name</td></tr>
<tr><td><b>Esc</b></td><td>show everything again</td></tr>
</table>`},
	"problems": {"Troubleshooting", `
<p><b>"Not found" on everything.</b> Check <b>Sources</b>: at least one has to be on, and marked as working. Then look in <b>Activity</b> for what went wrong.</p>
<p><b>YouTube asks to sign in.</b> YouTube sometimes takes automated downloads for bots. In <b>Sources → YouTube → Set up</b>, choose the browser you're signed in to YouTube with.</p>
<p><b>Soulseek says "logged in elsewhere".</b> Another Soulseek program is using your account. Close it, then press Reconnect on the Soulseek page.</p>
<p><b>Slow Soulseek downloads, or few people answering.</b> Your "port" may be closed to others. The Soulseek page has a <b>Check my connection</b> button, and the project's documentation has a step-by-step guide to opening it.</p>
<p><b>Torrents find nothing.</b> Many torrent sites sit behind browser checks; a helper program called FlareSolverr gets past those. <b>Sources → Torrents → Set up</b> explains it.</p>`},
}

func (a *app) buildHelpPage() *qt.QWidget {
	p := a.newPage("Help", "How the app works, in plain words.", "")
	outer := hbox()
	outer.SetSpacing(16)
	list := qt.NewQListWidget2()
	list.SetFixedWidth(220)
	list.SetFocusPolicy(qt.StrongFocus)
	list.SetHorizontalScrollBarPolicy(qt.ScrollBarAlwaysOff)
	list.SetSpacing(1)
	for _, id := range helpOrder {
		list.AddItem(helpTopics[id].title)
	}
	body := richText("", "")
	body.SetAlignment(qt.AlignTop | qt.AlignLeft)
	body.SetTextInteractionFlags(qt.TextBrowserInteraction)
	scroll, inner, _ := scrolling()
	inner.AddWidget(body.QWidget)
	inner.AddStretch()
	show := func(row int) {
		if row < 0 || row >= len(helpOrder) {
			return
		}
		t := helpTopics[helpOrder[row]]
		body.SetText(fmt.Sprintf("<h2>%s</h2>%s", t.title, helpHTML(t.body)))
	}
	list.OnCurrentRowChanged(show)
	a.onTheme(func() { show(list.CurrentRow()) })
	list.SetCurrentRow(0)
	outer.AddWidget(list.QWidget)
	outer.AddWidget2(scroll.QWidget, 1)
	p.box.AddLayout2(outer.QLayout, 1)
	return p.w
}

// helpHTML colors a topic's links for the theme.
func helpHTML(s string) string {
	return strings.ReplaceAll(s, "<a ", "<a style='color:"+th.Accent+"' ")
}

// showHelp explains one thing, without leaving the page.
func (a *app) showHelp(topic string) {
	t, ok := helpTopics[topic]
	if !ok {
		return
	}
	d := qt.NewQDialog(a.win.QWidget)
	d.SetWindowTitle(t.title)
	d.Resize(560, 460)
	box := qt.NewQVBoxLayout(d.QWidget)
	box.SetContentsMargins(20, 18, 20, 14)
	box.SetSpacing(10)
	title := qt.NewQLabel3(t.title)
	setProp(title.QWidget, "role", "h1")
	box.AddWidget(title.QWidget)
	scroll, inner, _ := scrolling()
	body := richText(helpHTML(t.body), "")
	body.SetAlignment(qt.AlignTop | qt.AlignLeft)
	inner.AddWidget(body.QWidget)
	inner.AddStretch()
	box.AddWidget2(scroll.QWidget, 1)
	row := hbox()
	row.AddStretch()
	ok2 := button("Got it", "primary", func() { d.Accept() })
	ok2.SetDefault(true)
	row.AddWidget(ok2.QWidget)
	box.AddLayout(row.QLayout)
	d.Exec()
}

// Friendly words for things the backend names technically.

// sourceInfo is how the window introduces a source.
type sourceInfo struct {
	blurb   string // what it is, in one line
	quality string // what you get
	needs   string // what it takes to use
	icon    string
}

var sourceInfos = map[string]sourceInfo{
	"bandcamp":      {"Music sold directly by artists; the free and name-your-price releases are downloaded.", "Often lossless", "Works right away", "music"},
	"archive":       {"A public library of freely licensed recordings.", "Often lossless", "Works right away", "album"},
	"audius":        {"Independent artists' own uploads, a lot of electronic music.", "MP3 320", "Works right away", "music"},
	"youtube_music": {"The official songs on YouTube Music. Has almost everything.", "Good (compressed)", "Works right away", "play"},
	"soundcloud":    {"Tracks and mixes people post publicly.", "Standard", "Works right away", "signal"},
	"youtube":       {"Ordinary YouTube videos; the last resort for rare recordings.", "Good (compressed)", "Works right away", "play"},
	"soulseek":      {"A community sharing their collections directly with each other.", "Often lossless", "Needs a free account", "people"},
	"torrent":       {"Albums and discographies from torrent sites. Only the files needed are fetched.", "Often lossless", "Works right away", "download"},
	"vk":            {"The music library of the VK social network.", "MP3 up to 320", "Needs your VK account", "music"},
	"yandex":        {"The Yandex Music streaming service.", "FLAC with Plus", "Needs your Yandex account", "music"},
}

// routeWords says how a source connects.
var routeWords = map[string]string{
	"system": "Like everything else on this computer",
	"direct": "Direct, around any VPN",
	"socks5": "Through a SOCKS5 proxy",
	"http":   "Through an HTTP proxy",
}

// thresholdWords describes the quality levels.
var thresholdWords = map[string]string{
	"lossless": "Lossless only (FLAC, ALAC, WAV)",
	"320":      "High (MP3 320 or as good)",
	"256":      "Good (MP3 256 or as good)",
	"192":      "Fair (MP3 192 or as good)",
	"128":      "Basic (MP3 128 or as good)",
	"any":      "Any copy will do",
}

// formatWords are the formats files can be saved in, in core.Formats' order.
var formatWords = []string{"Keep original (recommended)", "FLAC (lossless)", "MP3", "Opus", "M4A (AAC)"}

// filterWords are the Downloads page's filters, by core's names.
var filterWords = map[string]string{
	"all": "All", "todo": "Still to do", "active": "In progress", "downloading": "Downloading now", "searching": "Searching now",
	"queued": "Waiting", "prioritized": "Moved to the front", "waiting": "Will retry later", "done": "Downloaded",
	"lossless": "Downloaded in lossless", "lossy": "Downloaded in lossy quality", "upgrades": "Better than my library copy",
	"library": "In my library", "below": "In my library, below my quality", "failed": "Not found", "cancelled": "Cancelled",
	"search": "Added from Find music", "upgrading": "Library upgrades",
}

// sortWords are the Downloads page's orders.
var sortWords = map[string]string{
	"playlist": "Playlist order", "state": "Status", "artist": "Artist", "title": "Title", "album": "Album", "length": "Length",
	"quality": "Quality", "source": "Source", "progress": "Progress", "priority": "Priority", "updated": "Recently finished",
}
