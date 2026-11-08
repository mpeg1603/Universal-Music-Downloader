package core

// The first-run setup: the steps every frontend walks you through, with what each says. A
// frontend shows them in this order, lets you fill in or skip each, and calls FinishSetup.

// SetupStep is one step of the setup.
type SetupStep struct {
	ID     string // what it sets up (a source's name for the download methods)
	Title  string
	Help   string
	Source bool // a download method: set it up or leave it off
}

var SetupSteps = []SetupStep{
	{ID: "welcome", Title: "Welcome", Help: "This app downloads the songs of your playlists, and music you search for, from many " +
		"sources, and skips what your music library already has in good enough quality. Each step can be skipped; " +
		"everything can be changed later (Settings and Sources)."},
	{ID: "output", Title: "Where downloads go", Help: "New files go to this folder (never into your library). The format: " +
		"\"original\" keeps what the source sends (no re-encoding)."},
	{ID: "library", Title: "Your music library", Help: "Folders with the music you already have. They're only read: songs " +
		"you have in good enough quality aren't downloaded again. The quality you call good enough decides what gets " +
		"replaced by a better download."},
	{ID: "playlists", Title: "Playlists", Help: "Add playlist files now (CSV from Exportify and the like, Spotify's data " +
		"export, text lists of tracks, albums and artists, public Spotify links), or skip: the Search tab finds music " +
		"by name too."},
	{ID: "bandwidth", Title: "Bandwidth", Help: "A cap for all downloads together (0: none), and how many run at once. " +
		"While bandwidth is to spare, tracks don't wait for busy sources."},
	{ID: "bandcamp", Source: true, Title: "Bandcamp", Help: "Free and name-your-price downloads, often lossless. An email " +
		"address lets you get the releases whose artists ask for one (Bandcamp emails you the link)."},
	{ID: "archive", Source: true, Title: "Internet Archive", Help: "Freely licensed recordings (Creative Commons, public domain)."},
	{ID: "audius", Source: true, Title: "Audius", Help: "Independent artists' own uploads, MP3 320; no account needed."},
	{ID: "youtube", Source: true, Title: "YouTube, YouTube Music, SoundCloud", Help: "Lossy (Opus ~130, MP3 128), but they " +
		"have almost everything. If YouTube asks to sign in (bot check), your browser's YouTube login helps."},
	{ID: "soulseek", Source: true, Title: "Soulseek", Help: "A file-sharing network where people share their music folders, " +
		"often in FLAC. Needs a Soulseek account (a new name and password make one), or import it from Nicotine+. " +
		"You share your library in return (many users don't send files to people who share nothing). Opening the " +
		"listening port in your router helps: see the README (\"Soulseek port forwarding\")."},
	{ID: "torrent", Source: true, Title: "Torrents", Help: "Albums and discographies from public torrent sites (built-in, " +
		"from Jackett's site definitions), downloaded by the built-in client; only the files needed. Sites behind " +
		"Cloudflare's browser check need FlareSolverr. Your own Jackett or Prowlarr can be added too."},
	{ID: "vk", Source: true, Title: "VK", Help: "VK's music with your account's token (MP3 up to 320)."},
	{ID: "yandex", Source: true, Title: "Yandex Music", Help: "Yandex Music with your account's token (FLAC with Plus)."},
	{ID: "done", Title: "All set", Help: "Settings are saved. Sources can be turned on or off and reordered in the Sources " +
		"tab; this setup can be run again from Settings."},
}

// NeedsSetup says whether the first-run setup hasn't been through yet.
func (m *Manager) NeedsSetup() bool { return !m.Config().SetupDone }

// FinishSetup saves what the setup collected.
func (m *Manager) FinishSetup(change func(*Config)) error {
	return m.UpdateConfig(func(c *Config) {
		change(c)
		c.SetupDone = true
	})
}
