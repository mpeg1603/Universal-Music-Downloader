# spotify-downloader

A terminal app that downloads the songs of your Spotify playlists from other sources, and
**skips the ones your music library already has in good enough quality**. It never downloads
from Spotify, and never writes to your library: new files go to their own downloads folder.

Everything is set up and controlled inside the app: playlists, library folders, quality
threshold, sources and their order, bandwidth, and every track (cancel, restart, do next).
Changes apply right away, while it runs.

## Sources

Tried top to bottom for each track; the order is yours to change (Sources tab).

| Source | What it uses | Quality |
|---|---|---|
| **Bandcamp** | Free and name-your-price downloads. When the artist asks for an email address, Bandcamp emails you the download link: you paste it, or the app reads it from your Gmail or IMAP mailbox (see below). Paid releases are listed in the report with a link to buy them. | FLAC when offered |
| **Internet Archive** | Items published under a license (Creative Commons, public domain); no live tapes. | FLAC / MP3 |
| **Yandex Music** *(optional)* | Your own account (token). Full tracks need Plus. Reached directly, not through your VPN. | FLAC (Plus) / MP3 320 |
| **VK** *(optional)* | Your own account (token). Reached directly, not through your VPN. | MP3 up to 320 |
| **YouTube Music** | Official "song" uploads, found by ISRC and by artist + title. | Opus ~130 |
| **SoundCloud** | Public tracks; 30-second previews are rejected. | MP3 128 / AAC |
| **YouTube** | Regular uploads. | Opus ~130 |
| **Torrent** *(optional)* | Your Jackett or Prowlarr, downloaded with aria2c (only the file needed). See **[TORRENTS.md](TORRENTS.md)**. | often FLAC |

Every result is checked before it's used: title, artist and length must match; *live*,
*remix*, *cover*, *sped up* and similar versions are rejected unless your track is that
version; the downloaded file's real length is checked again. Files are tagged (title, artists,
album, track number, year, ISRC, cover) and saved as `Album Artist/Album/NN - Title.ext`.

## Your library: what gets downloaded

Add your music folders in the **Library** tab. They are scanned (tags, codec, bitrate; read
with built-in readers, so even big libraries take seconds after the first scan) and **only
read: nothing in them is ever created, changed, moved or deleted.** The downloads folder can't
be inside a library folder (or the other way round); the app refuses that setting.

A song listed more than once (in one playlist or several, also under two Spotify IDs, e.g. the
album version and a re-release: same artist and length, same title or ISRC) is one track:
downloaded once, and listed once in each playlist.

For each track of your playlists:

- **Not in your library** → downloaded.
- **In your library at or above the quality threshold** → skipped ("◆ in library").
- **In your library below the threshold** → downloaded **only if a source has it in clearly
  better quality**. Sources that can't do better aren't even searched, results that are only
  as good are passed over, and the downloaded file is measured again: if it isn't better, it's
  dropped. A better file goes to the downloads folder; your copy stays where it is, and the
  report `downloaded.md` lists which library file it improves on so you can swap them yourself.

The threshold (Library tab, `t`) is one of: `lossless` (default: FLAC, ALAC, WAV…), `320`,
`256`, `192`, `128`, `any`. Lossy files are compared by MP3-equivalent bitrate (Opus 160 ≈ MP3
256, AAC 256 ≈ MP3 320). `r` on an "in library" track downloads it anyway.

## Bandwidth

Set a bandwidth in **Settings** (or press `[` / `]` anywhere): it's both the **target** and the
**cap** for all downloads together, torrents included. The app keeps adding parallel downloads
while the total stays below it (up to "Most downloads at once") and never goes above it. With
no bandwidth set, it runs "Most downloads at once" downloads in parallel. Searching runs
separately ("Searches at once"), ahead of the downloads, so they never sit idle.

## Requirements

- **ffmpeg** (`sudo pacman -S ffmpeg` / `sudo apt install ffmpeg`).
- **yt-dlp** is downloaded automatically (official build, checksum-verified, refreshed weekly)
  into `~/.cache/spotify-downloader/`. For YouTube it needs `node` or `deno`.
- **For torrents only:** the **aria2** package and Jackett or Prowlarr ([TORRENTS.md](TORRENTS.md)).

## Getting started

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o spotify-downloader .
./spotify-downloader
```

1. **Playlists** tab (`2`): `a` adds a playlist file. Any of:
   - CSV files from [Exportify](https://exportify.net), TuneMyMusic and similar tools (a file, a
     folder of them, or a zip);
   - Spotify's own data export (spotify.com → Account → Privacy → Download your data → *Account
     data*), as the zip; it includes Liked Songs;
   - a text file with one `Artist - Title` per line;
   - a public Spotify playlist or album link (only its first 100 tracks).
2. **Library** tab (`3`): `a` adds your music folders; `t` sets the quality threshold.
3. **Settings** tab (`5`): the downloads folder (default `~/Music/SpotifyDownloads`), format,
   bandwidth. **Save** applies them.
4. Watch the **Queue** tab (`1`).

## The app

| Tab | What's there | Keys |
|---|---|---|
| `1` Queue | every track: state, progress, speed, quality | `Enter` details (every source tried and why it didn't work), `c` cancel, `r` restart (search again from the first source; for a downloaded or in-library track, download again), `p` do next, `R` retry everything not found, `f` filter, `/` search, `o` sort |
| `2` Playlists | your playlist files, and the playlists in them | `a` add, `d` remove, `space` on/off, `+` `-` priority (higher goes first), `Enter` show its tracks, `R` read the files again |
| `3` Library | library folders, scan status, threshold | `a` add folder (suggestions show as you type: `↑` `↓` pick, `Tab` completes, `Enter` accepts, `Esc` closes), `d` remove, `s` rescan, `t` threshold |
| `4` Sources | the sources in order, their route, state and counts; torrent indexers | `space` on/off, `K` `J` move up/down, `Enter` settings (tokens, cookies, email, Jackett), `e` route, `p` pause/resume, `b` paste Bandcamp download links |
| `5` Settings | downloads folder, format, layout, threshold, bandwidth, parallel downloads and searches | `Tab`/arrows move, `Esc` leaves a field, **Save** applies |
| `6` Log | what happened | `v` detailed log, `End` follow |

Anywhere: `1`–`6` (or `Alt+1`–`6`, `F1`–`F6`) switch tabs, `[` `]` lower/raise the bandwidth,
`P` pauses/resumes everything, `q` quits. Quitting stops the tracks in progress; they continue
next time. Reports are rewritten every few minutes and on quit.

### Reports

In the downloads folder: `reports/` has `artists`, `downloaded`, `in_library` and `failed`
(as `.md` and `.csv`); `Playlists/` has one `.m3u8` per playlist, pointing at the downloaded
file or, for songs you already had, at your library copy.

## Bot checks and blocks

When a site pushes back (YouTube's "sign in to confirm you're not a bot", rate limits, VK
captchas, Bandcamp's browser check), that source is **paused** for a while (2 minutes, then
5, 15, 30, up to an hour) instead of being hammered; tracks that needed it wait ("⟳ retry
later") and continue when it's back. Other sources carry on meanwhile. The Sources tab shows
which source is paused, until when, and why; `p` resumes it early.

To keep YouTube from flagging you:

- **Use your browser's YouTube login:** Sources → YouTube (or YouTube Music) → `Enter` →
  *Cookies from browser* → your browser (where you're logged in to YouTube). yt-dlp reads the
  cookies from the browser each time; nothing is stored. A `cookies.txt` file works too.
- YouTube requests are spaced out (2 seconds by default, "Seconds between YouTube requests").
- If your VPN's address is flagged, you can give YouTube a different route (`e` in Sources).

The app doesn't try to get around bot checks; it backs off and uses your own logins.
Spotify is only contacted when you add a playlist *link*; CSV files and the data export need
no connection to Spotify.

## Routes: VPN or direct, per source

Each source has a route (`e` in the Sources tab):

- **system**: your system's routing (with a VPN on, through the VPN);
- **direct**: bound to your physical network interface (Wi-Fi/Ethernet), bypassing the VPN's
  tunnel. Detected automatically (the interface whose default route has a gateway), or set in
  Settings. Done per connection inside the app (`SO_BINDTODEVICE` on Linux, no root needed);
  your system's routes, DNS and VPN settings aren't touched;
- a **proxy**: `socks5://user:pass@host:port` or `http://host:port`.

Yandex Music and VK default to **direct**, since they need a Russian address.

## Yandex Music and VK

**Yandex Music:** Sources → Yandex Music → `Enter` → paste your OAuth token. To get it, open
<https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d>
in your browser, log in, and copy the `access_token=…` part of the address you end up on.
Full tracks need a Plus subscription (FLAC where Yandex has it); without Plus Yandex only gives
previews, which are rejected. `space` turns the source on.

**VK:** Sources → VK → `Enter` → *Log in to get a token…* (phone/email, password, and the
confirmation code if VK asks; the password isn't stored), or paste a Kate Mobile token. VK
only lets a few apps use its music API; this uses Kate Mobile's identity, as other VK music
tools do.

Tokens are kept in `config.json`, readable only by you.

## Bandcamp name-your-price with an email address

Some free (name-your-price) releases make you enter an email address; Bandcamp then emails the
download link. Sources → Bandcamp → `Enter` → *Your email address*, and choose where the links
come from (*Download links from*):

- **I paste them** (no mailbox access): the app sends your address to Bandcamp (once per
  release, remembered across restarts) and the tracks wait ("⟳ retry later"). When the emails
  arrive, copy the links (or whole emails, several at once) and paste them with `b` in the
  Sources tab. The waiting tracks continue right away.
- **Gmail: sign in with Google** (read only, no app password): see below.
- **IMAP mailbox**: any mailbox that allows IMAP with a password or app password. **Yandex
  Mail**: `imap.yandex.ru:993`, user = your login, an app password (id.yandex.ru → Security →
  App passwords → Mail), and in Yandex Mail's settings → *Email clients* turn on IMAP and *App
  passwords and OAuth tokens*. **Mail.ru**: `imap.mail.ru:993` with an app password. Only read:
  nothing is marked as read, moved or deleted.

Tracks waiting for such a link wait for it instead of being taken from a worse source. Without
an email address, such releases are listed in the report with a link.

SMTP is no help here: it only *sends* mail, and Bandcamp's link has to be *read* from your
inbox (that's IMAP, the Gmail API, or you).

### Gmail

Google only lets registered apps read Gmail, and only through its own sign-in page, so you
register this app for yourself once (free, about 5 minutes; no billing needed):

1. <https://console.cloud.google.com/> → create a project (any name).
2. *APIs & Services* → *Library* → **Gmail API** → *Enable*.
3. *Google Auth Platform* (*OAuth consent screen* in older consoles) → *Get started*: any app
   name, your address as support and contact email, audience **External**.
4. *Audience* → *Test users* → add your Gmail address.
5. *Clients* → *Create client* → type **Desktop app** → copy the **Client ID** and **Client
   secret**.
6. In the app: Sources → Bandcamp → `Enter` → *Download links from*: **Gmail** → paste the ID and
   secret → **Sign in with Google…**. Your browser opens Google's page; pick your account and
   allow read-only access ("Google hasn't verified this app" is expected: it's your own app →
   *Continue*). Back in the app it says "signed in as …".

The app asks only for `gmail.readonly` (it can't send, change or delete mail), reads only
Bandcamp's emails, and keeps the sign-in in `config.json`. While the app is in *Testing*, Google
ends the sign-in after 7 days (the Sources tab then says to sign in again); on *Audience* →
*Publish app* (*In production*), it lasts. To remove the access:
<https://myaccount.google.com/permissions>.

## Without the app

```sh
./spotify-downloader --headless   # work through the queue with the saved settings, print progress, exit
./spotify-downloader --check-torrents
./spotify-downloader --add ~/playlists --library ~/Music --out ~/Music/SpotifyDownloads --bandwidth 5
```

`--add`, `--library`, `--out` and `--bandwidth` change the saved settings (handy for a first
setup or scripts). `--config FILE` and `--data DIR` use another settings file and state folder.

## Files

- `~/.config/spotify-downloader/config.json`: settings (private: holds tokens and passwords).
- `~/.local/share/spotify-downloader/`: `state.jsonl` (per-track results), `library.json`
  (imported playlists and Deezer lookups), `library-index.json` (library scan),
  `bandcamp-emails.json` (releases Bandcamp was asked to email, and the links received),
  `cache/` (torrent files and searches), `logs/` (one per run, last 20 kept).

## Troubleshooting

- **A source shows a problem in the Sources tab** (needs a token, yt-dlp missing, Jackett
  unreachable): fix it in its settings (`Enter`); it's set up again when you save.
- **"paused until 18:05 (…bot check…)"**: see *Bot checks* above.
- **"Bandcamp is showing its bot check page to this connection"**: Bandcamp currently serves a
  browser challenge to this network; the source pauses and tries again later. Try another route
  for it (`e`), or put it lower in the order.
- **Wrong song downloaded**: `Enter` on the track shows which result was picked; delete the
  file, move the source down, and `r` the track.
- **A track in your library isn't recognized**: the match needs the artist and title tags (or,
  without tags, an `Artist/Album/NN - Title` or `Artist - Title` file name) and a similar
  length. `Enter` shows what was found.

## Building and tests

You need Go 1.22 or newer.

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o spotify-downloader .
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o spotify-downloader.exe .
go test ./...      # needs ffmpeg; the torrent tests also use aria2c if installed
```
