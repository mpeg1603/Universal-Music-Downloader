package core

// SourceHelp says what each source is, for frontends.
var SourceHelp = map[string]string{
	"bandcamp": "Free and name-your-price downloads (lossless when offered). For name-your-price releases whose artist asks for an email address, enter your email in the settings: you paste the link Bandcamp sends (b, or Paste Bandcamp links), or it's read from Gmail or an IMAP mailbox.",
	"archive":  "Internet Archive items published under a license (Creative Commons, public domain); live concert tapes are left out.",
	"yandex": "Yandex Music with your account (Plus needed for full tracks; FLAC where available). Get a token: open " + YandexTokenURL +
		" in a browser, log in, and copy the access_token=… part of the address you end up on. Route \"direct\" keeps it off your VPN.",
	"vk":            "VK music with your account's token (MP3 up to 320 kbps). Log in from its settings (Enter) to get a token, or paste one. Route \"direct\" keeps it off your VPN.",
	"youtube_music": "Official \"song\" uploads, found by ISRC and by artist + title (Opus ~130 kbps). If YouTube asks to sign in (bot check), set \"Cookies from browser\" in its settings.",
	"soundcloud":    "Public SoundCloud tracks; 30-second previews are detected and rejected.",
	"youtube":       "Regular YouTube uploads (Opus ~130 kbps). Shares the bot-check pause and cookies with YouTube Music.",
	"torrent":       "Torrents, searched on built-in indexers (Jackett's public site definitions; its settings choose which) and, if you like, your own Jackett or Prowlarr, and downloaded by the built-in client (only the file needed; no seeding afterwards). Indexers that keep failing are left out for a while instead of slowing every search. Sites behind Cloudflare's browser check need FlareSolverr.",
	"audius":        "Audius, an open platform where artists upload their own music (much independent electronic music). Full tracks as MP3 320; no account needed. Tracks for paying fans only are left out.",
	"soulseek":      "Soulseek, a file-sharing network where people share their music folders, often in FLAC. The client is built in: its settings hold your account (a new name and password make a new account; or import it from Nicotine+), sharing and limits. Searches are paced and only a few files are requested at once, as the network asks. The Soulseek tab searches the network by hand, shows transfers and chat; tracks no source had go on its wishlist.",
}

// SetSourceEnabled turns a source on or off.
func (m *Manager) SetSourceEnabled(name string, on bool) error {
	return m.UpdateConfig(func(c *Config) { c.SetSource(name, on) })
}

// MoveSource moves a source up (delta -1) or down (+1) in the order they're tried in.
func (m *Manager) MoveSource(name string, delta int) error {
	return m.UpdateConfig(func(c *Config) {
		for i, s := range c.Sources {
			if j := i + delta; s.Name == name && j >= 0 && j < len(c.Sources) {
				c.Sources[i], c.Sources[j] = c.Sources[j], c.Sources[i]
				return
			}
		}
	})
}
