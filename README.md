# Universal Music Downloader

An app that downloads the songs of your playlists, and the tracks, albums and
discographies you search for, from many sources (Bandcamp, Soulseek, torrents, YouTube…), and
**skips what your music library already has in good enough quality**. It never downloads from
Spotify, and doesn't write to your library: new files go to their own downloads folder. (The
one exception is **Sort & merge**, which tidies your folders when, and only when, you apply a
plan you've looked over.)

It's self-contained: the Soulseek client, the BitTorrent client and the torrent site search
(Jackett's public site definitions) are built in, and the programs it uses (ffmpeg, yt-dlp)
are downloaded by the app when they aren't installed.

It comes in two versions over the same engine: **a window** ([what it looks like](docs/gui-guide.md)),
plain and explained for everyone, and **a terminal app** for people who'd rather use the keyboard.
Everything either can do, the other can do too.

Everything is set up and controlled inside the app: playlists, library folders, quality
threshold, sources and their order, bandwidth, and every track (cancel, restart, do next).
Changes apply right away, while it runs. The first start walks you through it (below).

## Sources

Tried top to bottom for each track; the order is yours to change (Sources tab).

| Source | What it uses | Quality |
|---|---|---|
| **Bandcamp** | Free and name-your-price downloads. When the artist asks for an email address, Bandcamp emails you the download link: you paste it, or the app reads it from your Gmail or IMAP mailbox (see below). Paid releases are listed in the report with a link to buy them. | FLAC when offered |
| **Internet Archive** | Items published under a license (Creative Commons, public domain); no live tapes. | FLAC / MP3 |
| **Yandex Music** *(optional)* | Your own account (token). Full tracks need Plus. Reached directly, not through your VPN. | FLAC (Plus) / MP3 320 |
| **VK** *(optional)* | Your own account (token). Reached directly, not through your VPN. | MP3 up to 320 |
| **Soulseek** *(optional)* | A Soulseek client built into the app, with your Soulseek account: people share their music folders, and you share yours. See **Soulseek** below. | often FLAC |
| **Audius** | An open platform where artists upload their own music (much independent electronic music); free API, no account. Tracks only for paying fans are left out. | MP3 320 |
| **YouTube Music** | Official "song" uploads, found by ISRC and by artist + title. | Opus ~130 |
| **SoundCloud** | Public tracks; 30-second previews are rejected. | MP3 128 / AAC |
| **YouTube** | Regular uploads. | Opus ~130 |
| **Torrent** *(optional)* | Public torrent sites, searched with Jackett's site definitions (built in; your own Jackett or Prowlarr can be added), downloaded by the built-in BitTorrent client (only the files needed, no seeding afterwards). See **[TORRENTS.md](TORRENTS.md)**. | often FLAC |

Each source is searched with a few queries, the next one only when the ones before found
nothing that fits: artist + title (with the version, e.g. "… The Midnight Remix", when the title
names one), album artist + title, title + the other credited artists, title + album. Torrents
are searched by album artist + album (just the album for a compilation), artist + title, then
the album artist and the artist alone (discographies).

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

**Upgrading your library** (Library tab, `u`): pick a quality (e.g. *below lossless*, *below
320*); every library file below it, playlist or not, is queued to be **downloaded again in
better quality** (never just converted: a source must have a clearly better copy, and the
downloaded file is measured again). The new files go to the downloads folder, your copies stay
where they are, and `downloaded.md` says which library file each one improves on. The upgrade
is a playlist of its own (`Upgrade: below lossless`), so the Search tab can turn it off or
remove it, and the Queue's filter *library upgrades* shows its tracks.

## Bandwidth

Set a bandwidth in **Settings** (or press `[` / `]` anywhere): it's both the **target** and the
**cap** for all downloads together, torrents included. The app keeps adding parallel downloads
while the total stays below it (up to "Most downloads at once") and never goes above it. With
no bandwidth set, it runs "Most downloads at once" downloads in parallel. Searching runs
separately ("Searches at once"), ahead of the downloads, so they never sit idle.

**Testing a connection.** Sources → *Test* checks that a source answers, and *Test all* every
source that's on (`t` in the terminal app). Sources → Torrent → *Choose sites* tests each torrent
site the same way (`t`, and `T` for all that are on); a site that works again is used again at once.

**Queue jumping.** Soulseek and torrents have limits of their own (a few Soulseek downloads at
once, a few torrents at once). While bandwidth is to spare, a track that would wait for such a
busy source moves on to the next source in the order instead of holding a place in line; once
the bandwidth is used up, tracks wait for the sources they'd get first again. A waiting
torrent that hasn't received anything for 3 minutes lets its tracks move on the same way.
Settings → *Skip busy sources while bandwidth is to spare* turns it off. Whatever that says, a
source whose line would take more than 20 seconds (Soulseek's paced searches, say) never ties
tracks up: at most the few that will get their turn soon wait for it, the others go on to the
next sources and come back later if those don't have the song.

The header shows the speeds: the app's own downloads, torrents, and Soulseek (down and up,
with the number of transfers each way).

## Requirements

- **ffmpeg**: the installed one is used; without one, the app downloads ffmpeg and ffprobe once
  (the FFmpeg project's recommended static builds, [FFmpeg-Builds](https://github.com/FFmpeg-Builds/FFmpeg-Builds),
  LGPL variant, about 135 MB, checksum-verified) into
  `~/.cache/universal-music-downloader/bin/ffmpeg/`.
- **yt-dlp** is downloaded automatically (official build, checksum-verified, refreshed weekly)
  into `~/.cache/universal-music-downloader/`. For YouTube it needs `node` or `deno`.
- Nothing else: Soulseek, BitTorrent and the torrent site search are built in. Optional:
  [FlareSolverr](TORRENTS.md#flaresolverr-sites-behind-cloudflare) for torrent sites behind
  Cloudflare, your own Jackett or Prowlarr.

## The window

![The Downloads page](docs/img/downloads.png)

`universal-music-downloader-gui` is the same app with a desktop interface. The sidebar has the
things you'd come for: **Dashboard**, **Downloads**, **Find music**, **Playlists**, **My library**,
**Sort & merge**, then **Sources** (where music comes from, and in what order), **Soulseek**,
**Settings**, **Activity** and **Help**. Every page says what it's for, and the small **?** next to its title explains it.
Light, dark, or following your desktop (Settings → Appearance). [docs/gui-guide.md](docs/gui-guide.md)
walks through it.

The downloads on the [releases page](../../releases)
include everything it needs, Qt included: an AppImage or tar.gz on Linux, an installer or a
portable zip on Windows. The window and the terminal app share settings and state, but can't
run at the same time.

## Getting started

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o universal-music-downloader ./cmd/universal-music-downloader
./universal-music-downloader
```

The first start opens the **setup**: a window per step, each of which can be skipped, and all of
it can be changed later (Settings and Sources; Settings → *Run the setup again*):

- where downloads go and their format; your library folders and what quality is good enough;
  playlists to start with; the bandwidth and how many downloads run at once;
- every download method, one by one, set up or left off: Bandcamp (and its email address),
  Internet Archive, Audius, YouTube / YouTube Music / SoundCloud (and your browser's login),
  Soulseek (account, sharing, port; or import from Nicotine+), torrents (built-in indexers,
  FlareSolverr, Jackett/Prowlarr), VK, Yandex Music.

After that:

1. **Search** tab (`2`): type what you want in **Find** (`/`), or `a` adds a playlist file. Files
   can be:
   - CSV files from [Exportify](https://exportify.net), TuneMyMusic and similar tools (a file, a
     folder of them, or a zip);
   - Spotify's own data export (spotify.com → Account → Privacy → Download your data → *Account
     data*), as the zip; it includes Liked Songs;
   - a plain-text list of tracks, albums and artists (see *Finding music* below);
   - a public Spotify playlist or album link (only its first 100 tracks).
2. **Library** tab (`3`): `a` adds your music folders; `t` sets the quality threshold.
3. **Settings** tab (`6`): the downloads folder (default `~/Music/Universal Music Downloader`),
   format, bandwidth. **Save** applies them.
4. Watch the **Queue** tab (`1`).

## Finding music: by name, and plain-text lists

In the Search tab, **Find** takes what you'd say: `Artist - Title`, `Title - Artist`,
`Artist - Album`, or just `Artist`. Tags narrow it down: `artist:Deftones album:White Pony`,
`track:change year:2000`, `year:1995-1999` (a tag's value runs to the next tag). Below Find are
**Artist**, **Album**, **Track** and **Year** fields that do the same (`Tab` moves between
them); fill in any of them. Matching is fuzzy: small typos, missing words, *the*, accents and
punctuation don't stop a match, and exact matches come first.

It's looked up in Deezer's catalog (free, no account). The results come in three sections,
**Artists**, **Albums** (with year and track count) and **Tracks** (with album, year and
length), best first. `Enter` on one adds it to the queue, ahead of everything else:

- a **track**: that song;
- an **album**: all its tracks;
- an **artist**: the whole discography: albums, EPs and singles, each song once (the album's
  version rather than the single's or a reissue's; compilations and other artists' songs on
  splits are left out). You're asked first.

Each addition becomes a playlist of its own (`Search: Artist - Album`) in the Playlists list
(once every song is downloaded, in your library or not found, it moves to *Finished playlists*),
so it can be turned off, given a priority, or removed again (`d`; files already downloaded
stay).

**Plain-text lists** (`.txt`, added with `a`) take one of those per line: `Artist - Title`,
`Title - Artist`, `album: Artist - Album`, `Artist` (the discography), or tags
(`artist:… album:… year:…`); `#` starts a comment.
Each line is looked up the same way (the answers are kept for a month, so a list isn't looked
up again on every start); a line nothing fits is read as `Artist - Title`. Without a
connection to Deezer the lines are read as `Artist - Title`.

## The app

| Tab | What's there | Keys |
|---|---|---|
| `1` Queue | every track: state, progress, speed, quality | `Enter` details (every source tried and why it didn't work), `c` cancel, `r` restart (search again from the first source; for a downloaded or in-library track, download again), `p` do next, `R` retry everything not found, `f` filter, `o` sort, `O` reverse the order, `/` find in the queue, `Esc` show everything |
| `2` Search | find music by name; your playlist files, and the playlists in them | `/` find, `Enter` add a result (or show a playlist's tracks), `Tab` next list, `a` add a file, `d` remove, `space` on/off, `+` `-` priority (higher goes first), `R` read the files again |
| `3` Library | library folders, scan status, threshold | `a` add folder (suggestions show as you type: `↑` `↓` pick, `Tab` completes, `Enter` accepts, `Esc` closes), `d` remove, `s` rescan, `t` threshold, `u` upgrade the library |
| `4` Sources | the sources in order, their route, state and counts; torrent indexers | `space` on/off, `K` `J` move up/down, `Enter` settings (route, tokens, cookies, email, indexers, the Soulseek account), `e` route, `p` pause/resume, `b` paste Bandcamp download links, `t` test it |
| `5` Soulseek | a full Soulseek client in six pages: Search, Transfers, Browse, Users, Chat, Interests (see *Soulseek*) | `<` `>` switch pages, `/` search, `u` the user menu, `m` message, `b` browse their files, `i` user info, `n` message someone, `j` join a room, `A` away/online, `o` check your port, `R` reconnect |
| `6` Settings | downloads folder, format, layout, threshold, bandwidth, parallel downloads and searches | `Tab`/arrows move, `Esc` leaves a field, **Save** applies |
| `7` Log | what happened | `v` detailed log, `End` follow |
| `8` Dashboard | speeds, how far the songs have come, disk use | |
| `9` Sort | tidy your library and downloads, duplicates aside (see *Sort & merge*) | `s` make a plan, `a` apply, `d` drop it, `D` moves/duplicates, `f` kind, `/` filter, `x` stop, `u` undo a plan, `C` carry on with one |

Anywhere: `1`–`9` (or `Alt+1`–`9`, `F1`–`F9`) switch tabs, `[` `]` lower/raise the bandwidth,
`P` pauses/resumes everything, `q` quits.

**Queue filters and sorting.** `f` lists what can be shown, with how many tracks each:
everything, to do, in progress, downloading now (without the searches), searching now, queued, moved to the front, to be retried, downloaded,
downloaded lossless or lossy, downloaded to replace a worse library copy, in your library,
in your library below the threshold, not found, cancelled, added from the Search tab, and
downloaded from each source, library upgrades. `o` sorts by playlist order, state, artist, title, album, length,
quality, source, progress, priority or when it finished; `O` reverses the order. `/` narrows
it to tracks whose artist, title or album contains what you type; `Esc` shows everything again.

**Pause** (`P`) stops everything right away: searches and downloads in progress are stopped and
go back to the front of the queue (they continue from the source they were at), torrents are
paused and keep what they have, and nothing new starts. Tracks being converted
finish. `P` again resumes.

Quitting (`q`, closing the terminal, or `kill`) stops the tracks in progress; they continue
next time. Reports are rewritten every few minutes and on quit.

### Sort & merge

Tidies your library and downloads into one library, and moves duplicate songs into a folder of
their own, sorted the same way, with a report of which file replaced which:

```
Music/Artist/Album/01 - Song.flac
Music/Artist/Singles/Song.mp3
Music/Various Artists/Album/01 - Artist - Song.mp3
Duplicates/Artist/Album/01 - Song.mp3      (a worse copy of something kept)
Duplicates/duplicates_report.txt
```

It works in three steps, and only the last changes anything:

1. **Make a plan.** It reads your files: their tags, then their file and folder names, then (for
   the few that need it, and if you allow it) MusicBrainz and Deezer. Nothing changes.
   MusicBrainz takes one question a second, so with a big library the lookups are the slow part
   the first time: the page says how many are left and about how long that takes, every answer
   is remembered, and you can skip the rest (those files are then filed from their tags and
   names alone). Songs already in an `Artist/Singles` folder aren't asked about again.
2. **Look it over.** Every file's old and new place, every duplicate and why.
3. **Apply it.** Files move, or are copied if you'd rather. Downloads pause meanwhile.

Two files are the same recording if they're byte-for-byte identical, or if artist and title match
and their lengths differ by at most a few seconds: a 7-minute extended cut and the 3-minute
original are both kept, and so are live versions and remixes. The best copy stays: lossless beats
lossy, then higher quality wins.

**Nothing is deleted or re-encoded, and nothing is overwritten.** A file that changed since the
plan was made is skipped. Each finished move goes straight to a journal, so applying can be
stopped and continued, and a plan can be undone, even after a restart. Files you downloaded are
followed to their new place, so they aren't downloaded again. Playlists (`.m3u`) pointing at the
old places stop matching. Plans are kept in the data folder under `sort-plans/`.

### Reports

In the downloads folder: `reports/` has `artists`, `downloaded`, `in_library` and `failed`
(as `.md` and `.csv`); `Playlists/` has one `.m3u8` per playlist, pointing at the downloaded
file or, for songs you already had, at your library copy.

## Bot checks and blocks

When a site pushes back (YouTube's "sign in to confirm you're not a bot", rate limits, VK
captchas, Bandcamp's browser check), that source is **paused** for a while (2 minutes, then
5, 15, 30, up to an hour) instead of being hammered; tracks that needed it wait ("⟳ retry
later") and continue when it's back, spread out over a couple of minutes. Other sources carry
on meanwhile. The Sources tab shows which source is paused, until when, and why; `p` resumes it
early. A track only gives up after several rounds in which a source actually *failed*;
waiting for a paused source doesn't count.

Bandcamp rate-limits (HTTP 429) quickly: its requests are spaced a second apart, and after a
429 twice as far each time (up to 20 s), coming back down as answers come in. When its
requests would wait more than 20 s for their turn, tracks skip it for now (like a paused
source) instead of holding up the other searches, and come back to it later.

To keep YouTube from flagging you:

- **Use your browser's YouTube login:** Sources → YouTube (or YouTube Music) → `Enter` →
  *Cookies from browser* → your browser (where you're logged in to YouTube). They're read from
  the browser every 10 minutes (YouTube's, Google's and SoundCloud's cookies only), so cookies
  the browser rotates are picked up, and each yt-dlp run gets a temporary copy. A `cookies.txt`
  file works too, but YouTube invalidates exported cookies of a session that's still in use;
  export them from a private window that you then close. A browser, when set, is used instead
  of the file. Your browser and your cookies file are never written to (yt-dlp rewrites the
  cookie file it's given, and YouTube's answers drop expired login cookies from it: given your
  own file, that used to log you out). The Sources tab says when the cookies hold no YouTube
  login.
- YouTube requests are spaced out (2 seconds by default, "Seconds between YouTube requests").
- If your VPN's address is flagged, you can give YouTube a different route (`e` in Sources).

The app doesn't try to get around bot checks; it backs off and uses your own logins.
Spotify is only contacted when you add a playlist *link*; CSV files and the data export need
no connection to Spotify.

## Routes: VPN or direct, per source

Each source has a route (`e` in the Sources tab, or the top of its settings): pick the type,
and for a proxy fill in its address, port and (if it needs one) its login.

- **system**: your system's routing (with a VPN on, through the VPN);
- **direct**: bound to your physical network interface (Wi-Fi/Ethernet), bypassing the VPN's
  tunnel. Detected automatically (the interface whose default route has a gateway), or set in
  Settings. Done per connection inside the app (`SO_BINDTODEVICE` on Linux, `IP_UNICAST_IF` on
  Windows; no administrator rights needed);
  your system's routes, DNS and VPN settings aren't touched;
- a **SOCKS5** or **HTTP proxy**: address (host name or IP), port, and optionally a user and
  password. (The settings file keeps it as `socks5://user:pass@host:port`.)

Yandex Music and VK default to **direct**, since they need a Russian address.

## Soulseek

Soulseek is a file-sharing network for music (people share their music folders, often in
FLAC). The client is built into the app; nothing else needs to be installed. It needs your
Soulseek account: Sources (`4`) → Soulseek → `Enter` → **Username** and **Password** (a name
nobody uses yet makes a new account on first login; there's no way to reset a forgotten
password, so keep it). The same window holds the route, port, limits, sharing and chat
rooms (the setup at first start asks for all of it too). If you use Nicotine+, **Import from Nicotine+** copies its account,
port, shared folders and upload limits (Nicotine+'s files are only read). Then turn the
Soulseek source on in the Sources tab (`space`). Don't run another Soulseek client with the
same account at the same time: each logs the other out (the app then stays offline until you
press `R` in the Soulseek tab, instead of fighting over the login).

**Being a good citizen.** The network's rules ask clients not to flood it, and the server
bans those that do for 30 minutes ("…too many operations at once… Do not quickly repeat a
search…"). It publishes no limits; one search every 5 seconds got this app banned after about
170 searches. So now:

- **Searches are budgeted**: one every 10 seconds at most (configurable, never less than 8), at
  most **120 an hour** and a quarter of that in any 10 minutes (configurable, 20 to 200), at
  most two at a time. When the budget is used up, tracks move on to other sources (queue
  jumping) instead of waiting.
- **The same search isn't sent twice within an hour**: its answers are kept, and a search
  that's under way is shared by everything asking for the same thing.
- **Albums are searched once**: a track's first search is its album (`Artist Album`), since
  people share album folders; the album's other tracks are then found in the same answers
  without a search of their own. The song itself is searched only when the album search had
  nothing.
- **A ban is waited out in full**: when the server says you're banned, the app logs out and
  doesn't connect again until the ban is over plus 5 minutes, also across restarts (`R` doesn't
  shorten it). A login the server doesn't answer (that's how bans look from outside) is tried
  again after 10 minutes at least; other reconnects start at 30 seconds and grow to 30
  minutes. A new login after a settings change waits 30 seconds after the last one.
- Downloads: **at most 2 files at once** (configurable 1–4) and only one from any user; a user
  who hasn't started sending within 5 minutes (configurable) is passed over instead of asked
  again; places in queues are asked for only every few minutes.

Users who can send right away, and quickly, are tried first. Soulseek downloads count toward
the bandwidth setting.

**Sharing.** Many users don't send files to people who share nothing, and the network's
rules expect clients to share. By default your library folders and the downloads folder are
shared, read only (Settings → **Shared folders** to choose others; `Name=/path` names one).
Others see a folder by its name (`Music\Artist\Album\01 Song.flac`). The app answers their
searches (through the distributed search network, which it joins as a leaf), lets them browse
your files, and uploads what they ask for: **2 upload slots** by default, privileged users
(those who donated to Soulseek) first, then round robin. Settings: upload slots, an upload
speed limit (KB/s), a queue limit per user (files and MB), banned users. The shared folders
are read again after each library scan; new downloads are shared as they arrive. Turn
**Share files** off to share nothing.

**Getting through: port forwarding.** Others reach you on the listening port (TCP 2234; the
next free one if it's taken). With it closed, Soulseek still works, but users whose own port is
closed can't exchange files or search results with you. With **UPnP** on (the default, as in
Nicotine+), the app asks your router to forward it while it runs; otherwise forward it by hand.
With a VPN, set Soulseek's route to **direct** (Sources → Soulseek → `Enter` → Route), so the
server and other users see your home address, where the port is forwarded; the app then also
binds its listening socket to your network card, so answers to incoming connections leave
through your router rather than the VPN. `o` in the Soulseek tab asks the Soulseek site's port
checker whether others can reach you.

On a **MikroTik** router (RouterOS 7), in its Terminal, with your computer's address in place
of `192.168.1.50`:

```
/ip dhcp-server lease make-static [find address=192.168.1.50]
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=2234 \
    action=dst-nat to-addresses=192.168.1.50 to-ports=2234 comment="Soulseek"
```

The first line keeps the computer's address from changing, the second forwards the port. The
default firewall lets forwarded connections in. WinBox/WebFig steps, UPnP on MikroTik, other
routers, your computer's firewall and what to check when it stays closed (carrier-grade NAT,
for one): **[docs/soulseek-port-forwarding.md](docs/soulseek-port-forwarding.md)**.

**Fast seeders get more.** When a user sends a track quickly (from 250 KB/s) and in good
quality (lossless, or 256 kbps and up), the app browses their shares once and checks them
against the rest of the queue: every other track of yours they have (the same album, or
anything else they share) is downloaded from them while they're there, ahead of other users.
Nothing more is asked of them than of anyone: their queue, the per-user limit and the 2-at-once
cap still apply.

**Wishlist and chat.** Tracks that no source had, Soulseek included, go on the Soulseek
wishlist: the server lets each client search for one wishlist item every 12 minutes (2 for
privileged users), and the app does, in turn; when something fits, the track is tried on
Soulseek again. The Soulseek tab shows private messages (the tab title counts unread ones) and
chat rooms; rooms you join are joined again next time.

**The Soulseek tab** works as a full client, in six pages (`<` `>` switch between them):

- **Search**: the files others share, users who can send right away first, with size, quality
  and length. `Enter` downloads a file, `d` the whole folder it's in (its subfolders too). These
  go to the downloads folder under `Soulseek/<folder>/` as they are (not renamed or tagged),
  wait their turn with the other Soulseek downloads, and show in Transfers.
- **Transfers**: your downloads, and your **uploads** (what others download from you, with
  speed and progress). `x` stops an upload, `C` clears finished ones.
- **Browse**: a user's shared folders and files (`b` on any user): `Enter` downloads a file, `d`
  a folder.
- **Users**: your buddies, ignored and banned users (`a` adds a buddy, `x` takes one off), and
  user info (`i`: their description, country, shared files and folders, upload slots, queue and
  speed, whether they're online).
- **Chat**: private messages (the tab title counts unread ones) and chat rooms (`j` joins one,
  from the server's list or by name; `l` leaves). Rooms you join are joined again next time.
- **Interests**: the things you like and dislike (`a`, `h`, `x`), the recommendations the server
  makes from them, users with similar interests, and what other users like.

On any user, `u` opens the user menu: message, browse, user info, their interests, add to
buddies, ignore (hide their messages), ban (no downloads from you, no file list). `A` sets you
away (and back).

The client speaks the protocol as documented by Nicotine+ (doc/SLSKPROTOCOL.md) and logs in
as an experimental client (major version 177), as new clients are asked to.

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
./universal-music-downloader --headless   # work through the queue with the saved settings, print progress, exit
./universal-music-downloader --check-torrents   # search the torrent indexers once, look into the top result
./universal-music-downloader --add ~/playlists --library ~/Music --out ~/Music/Downloads --bandwidth 5
```

`--add`, `--library`, `--out` and `--bandwidth` change the saved settings (handy for a first
setup or scripts). `--config FILE` and `--data DIR` use another settings file and state folder.

## Files

- `~/.config/universal-music-downloader/config.json`: settings (private: holds tokens and passwords).
- `~/.local/share/universal-music-downloader/`: `state.jsonl` (per-track results), `library.json`
  (imported playlists and Deezer lookups), `library-index.json` (library scan),
  `bandcamp-emails.json` (releases Bandcamp was asked to email, and the links received),
  `cache/` (Bandcamp album zips, torrent files, searches, the indexer definitions), `logs/` (one
  per run). These are kept in bounds: every hour the app removes what's old or over the limit,
  least recently used first (album zips: 5 GB, 3 days; logs: 100 MB, 2 weeks). To clear them by
  hand: Settings → *Temporary files* (`Clear temporary files` in the terminal app's Settings).
- Moving from the old name (*spotify-downloader*): on the first start the app moves
  `~/.config/spotify-downloader`, `~/.local/share/spotify-downloader` and
  `~/.cache/spotify-downloader` to the new names (leaving links behind), so settings, state and
  caches carry over.

## Troubleshooting

- **A source shows a problem in the Sources tab** (needs a token, yt-dlp missing, Jackett
  unreachable, no indexers chosen): fix it in its settings (`Enter`); it's set up again when you save.
- **"paused until 18:05 (…bot check…)"**: see *Bot checks* above.
- **"Bandcamp is showing its bot check page to this connection"**: Bandcamp currently serves a
  browser challenge to this network; the source pauses and tries again later. Try another route
  for it (`e`), or put it lower in the order.
- **Wrong song downloaded**: `Enter` on the track shows which result was picked; delete the
  file, move the source down, and `r` the track.
- **A track in your library isn't recognized**: the match needs the artist and title tags (or,
  without tags, an `Artist/Album/NN - Title` or `Artist - Title` file name) and a similar
  length. `Enter` shows what was found.
- **Soulseek: few results or downloads never start**: your port is probably closed and the
  other user's too; see *Getting through* (`o` checks it).

## Building and tests

You need Go 1.24 or newer.

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o universal-music-downloader ./cmd/universal-music-downloader
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o universal-music-downloader.exe ./cmd/universal-music-downloader
go test ./...      # some tests need ffmpeg installed; no network needed
```

**The window.** `universal-music-downloader-gui` needs Qt 6 to build (`qt6-base` on Arch,
`qt6-base-dev` on Debian/Ubuntu) and a C++ compiler; the first build compiles the Qt bindings
and takes a while:

```sh
go build -o universal-music-downloader-gui ./cmd/universal-music-downloader-gui
```

`packaging/` has what releases are built from (`.github/workflows/release.yml`): the AppImage
and tar.gz script and the Windows installer.

To see the window without a screen, build with `-tags guishots` and run it with
`QT_QPA_PLATFORM=offscreen UMD_SHOTS=<folder>`: it walks through every page and dialog in both
themes, saves a picture of each and checks that the controls work.

The code is in three parts:

- `internal/core`: the backend: playlists, library, sources, Soulseek, the queue, settings.
  Frontends use it only through `core.Backend` (`internal/core/api.go`): methods and the
  plain views they return.
- `internal/tui`: the terminal app, one file per tab; `internal/gui`: the Qt window.
- `cmd/universal-music-downloader`: the program: options, the headless mode, and starting the app;
  `cmd/universal-music-downloader-gui`: the window's.

## License

Universal Music Downloader is free software under the **GNU General Public License v3.0**
([LICENSE](LICENSE)): anyone may use, study, share and change it; whoever distributes it, or a
changed version, must do so under the same license, with the source code.
