# Torrents

`Torrent` is a download source like the others. In the app's **Sources** tab (key `4`) you turn
it on (`space`), put it anywhere in the order (`K`/`J`), and choose its indexers (`Enter` on the
Torrent row). Nothing needs to be installed:

1. **Indexers**, the torrent sites that are searched, are built in. The app reads **Jackett's
   site definitions** (the same YAML files Jackett itself runs, fetched from Jackett's GitHub
   repository and checked weekly for changes), and uses the **public** ones: sites that need no
   account.
2. The **BitTorrent client** is built in too: it downloads only the files needed and stops as
   soon as they're complete (no seeding afterwards).

Your own **Jackett** or **Prowlarr** can be added on top (for private trackers you have an
account on, say), and **FlareSolverr** for sites behind Cloudflare's browser check; both are
optional.

```
Universal Music Downloader ──search "Artist Album"──▶ built-in indexers (Jackett's public definitions)
        │                                         └─▶ your Jackett / Prowlarr (optional)
        │◀────────────── results + .torrent files / magnet links
        │
        └── built-in client: downloads only the files it needs, stops when they're done
```

> **Before you start.**
> - BitTorrent uploads pieces to other people while it downloads, and your IP address is
>   visible to everyone in the swarm. A route (Sources → `e`) doesn't change the BitTorrent
>   client's connections; the indexer searches follow it.
> - Many public torrents of commercial music are unauthorized copies; downloading or sharing
>   them is illegal in many countries.
> - What you download is your responsibility. Free and licensed sources (Bandcamp free
>   downloads, Internet Archive) are tried first by default for that reason.

---

## Setting it up

1. **Sources** tab → **Torrent** → `Enter`.
2. **Built-in indexers** is on by default, with a dozen general public sites (1337x, The Pirate
   Bay, LimeTorrents, TorrentDownload(s), KickassTorrents, TheRARBG, uIndex, TorrentGalaxy,
   MagnetCat, ExtraTorrent, NoNaMe Club, RuTracker.RU). **Choose indexers…** lists every public
   definition (about 80, a few dozen with music categories), plus RuTracker.org:
   - `space` turns one on or off; `/` finds one by name;
   - `Enter` on one that needs an account (RuTracker.org) asks for it;
   - `a` shows all of them or only those with music;
   - `u` fetches the newest definitions from GitHub now (otherwise weekly);
   - the list shows each one's language, whether it has music, how its searches went and its
     last problem.
3. **Save**, and `space` on the Torrent row turns the source on (the setup at first start asks
   the same).
4. Check it from a terminal (optional):

```sh
./universal-music-downloader --check-torrents
```

It searches every indexer you chose once, for an album from your playlists (or
`--torrent-query "Artist Album"`), and looks into the top result:

```
Checking the torrent setup (TORRENTS.md explains each step):
  ✓ built-in indexer 1337x
  ✓ built-in indexer The Pirate Bay
  …
  ✓ searching for "Daft Punk Random Access Memories": 37 results, 31 with seeders
      1. Daft Punk - Random Access Memories (2013) [FLAC]  (seeders: 52)
      …
  ✓ the top result's torrent file lists 15 files (13 audio)

All good.
```

The definitions are kept in `~/.local/share/universal-music-downloader/cache/indexers/`.

## RuTracker.org (needs your account)

RuTracker.org, the big Russian tracker with a lot of lossless music, needs an account (free
registration on the site). Jackett runs it from its own code rather than a definition, so the
app comes with a definition of its own for it, which searches only its music forums. In
**Choose indexers…**, select **RuTracker.org** → `Enter` → your username and password → **Save
and turn on**. They're kept in `config.json`, readable only by you.

The app logs in once and keeps the session; it logs in again only when RuTracker sends a request
back to its login page. A failed login isn't tried again for 30 minutes (RuTracker asks for a
captcha after a few failures): log in once in your browser then, check the password, and save
again. RuTracker sits behind Cloudflare's check for some addresses (VPNs especially): then it
needs FlareSolverr (below). Torrent files are downloaded with your login.

(RuTracker.RU, a different, public site, needs no account and is on by default.)

## FlareSolverr (sites behind Cloudflare)

Some sites sit behind Cloudflare's "checking your browser" page; their searches then fail with
an error that mentions *Cloudflare*. [FlareSolverr](https://github.com/FlareSolverr/FlareSolverr)
solves those pages (it runs a real browser), and is the one part that isn't built in. If you run
it, put its address (`http://127.0.0.1:8191`) in **FlareSolverr address**. The app then sends the
blocked request again with FlareSolverr's cookies (searches, logins and torrent files alike). Without it, such
sites are left out after three failures and the others carry on. Install it with Docker
(`docker run -d -p 127.0.0.1:8191:8191 ghcr.io/flaresolverr/flaresolverr:latest`) or from its
releases page.

## Your own Jackett or Prowlarr (optional)

For private trackers, or indexers you already run: fill in **Jackett / Prowlarr address** and
**API key** in the same window. Their results are merged with the built-in indexers'; turn
**Built-in indexers** off to use only yours.

| Address field | What happens |
|---|---|
| `http://127.0.0.1:9117` | Jackett: every configured indexer that carries music, each searched on its own (in parallel), so an indexer that fails shows its error instead of quietly returning nothing |
| `http://127.0.0.1:9696` | Prowlarr: every enabled torrent indexer, found automatically |
| `http://127.0.0.1:9117/api/v2.0/indexers/NAME/results/torznab/` | one specific Jackett indexer (copy it with **Copy Torznab Feed** on the dashboard) |
| `URL1, URL2` | several feeds; results are merged |

A feed URL that already contains `apikey=…` keeps its own key. The key is at the top right of
Jackett's dashboard, or in Prowlarr under **Settings → General → Security**. Installing them:
[Jackett](https://github.com/Jackett/Jackett#installation),
[Prowlarr](https://wiki.servarr.com/prowlarr/installation).

## Where torrents go in the order

Sources are tried top to bottom (Sources tab) for each track; the first one with a good match
wins. Move Torrent with `K` (up) and `J` (down). For example, Torrent right after Bandcamp gets
lossless files first and leaves YouTube for the rest. Torrents usually give the best quality,
especially with the format set to `flac` or `original` (Settings), which prefer lossless
torrents; they're slower per track than YouTube. While bandwidth is to spare, tracks waiting
for a busy torrent slot move on to the next source instead (README, *Queue jumping*).

To retry tracks that weren't found earlier, now that torrents are available, press `R` in the
Queue tab.

---

## How it picks a torrent and a file

For each track:

1. **Search**, from most to least specific, until something fits:
   1. `Album Artist + Album`, with edition details left out (*Turn On The Bright Lights: The
      Tenth Anniversary Edition (Remastered)* is searched as *Turn On The Bright Lights*), since
      indexers only find torrents whose names contain every word you search for; for a
      compilation (album artist *Various Artists*), just the album;
   2. `Artist + Title`, for singles;
   3. the album artist alone, when it's someone else than the track's artist (a remix on the
      remixer's album, a guest on someone's record): their discographies;
   4. the artist alone, for discographies and albums named differently on the indexer.

   Punctuation that trips up search engines (`: ( ) [ ] /` …) is left out. Each search is made
   once and reused for the album's other tracks; results are also kept on disk for 12 hours,
   so restarting the app doesn't search everything again.

   Built-in indexers whose site has a separate page per result are followed to it for the
   download link. All your indexers are asked at once, but a search **doesn't wait for slow ones**: each gets
   20 seconds at most (Sources → Torrent → settings), and once one indexer has answered the
   others get only a few more seconds. An indexer that fails three times in a row is **left
   out** for 5 minutes, then 10, 20, 40, 80, so a broken one can't slow every search down. The
   Sources tab shows each indexer's state, its last error and how fast it answers.
2. **Rank** the results by how well their name fits the artist and album, preferring: the
   format you asked for (lossless for `--format flac`/`original`, otherwise lossy), one file per
   track over one-file-per-CD images (*image+.cue*, which can't be split here), CD-resolution
   over 24-bit/vinyl rips (smaller, and usually the master streaming services use), the edition your track list
   names (*Anniversary*, *Deluxe*…), discographies, and more seeders. Dropped: torrents with 0
   seeders, audiobooks, and torrents that fit neither the artist nor the album.
3. **Look inside** the best ones (5, or 10 for the artist-only search): fetch their `.torrent`
   files and pick the file whose name fits the track best. A track number, a folder named after
   the album, and shortened names (*05 - Say Hello.flac* for *Say Hello to the Angels*) count.
   Files whose names say *live*, *remix*, *demo* etc. are skipped unless your track is that
   version.
4. **Download** with the built-in client, only the files needed. One client runs for the whole run, and all
   tracks that need files from the same torrent share one download: the album's tracks are
   collected for a moment and then fetched together, and tracks that come later are added to
   the running download. Each track carries on as soon as *its* file is complete. There's no
   seeding afterwards, and leftover pieces of neighbouring files are deleted.

   Before a torrent's download starts, its file list is checked for **your other tracks still
   to download**: the rest of the album, or the artist's other songs in a discography. Each one
   found moves to the front of the queue and skips the torrent search (it still tries the
   sources above Torrent first), and the download waits up to 20 seconds for them, so their
   files go into it from the start. Files are matched the way a search matches them (artist,
   title, track number). With the format set to `flac` or `original`, a lossy file is only
   handed to tracks of the same album; the others may find a lossless copy with their own
   search. In a very big torrent only some of your tracks are checked, the same album's
   first; the rest find it with their own search. If a file handed over this way fails, that
   track searches the indexers as usual.
5. **Check, convert, tag:** like every other source, the file's real length must match the
   track, then it's converted to your format, tagged and filed. In the reports the source shows
   as *Torrent: «torrent name»*, linking to its page on the indexer.

While a track waits for its torrent it doesn't take a download slot, so other downloads go on
meanwhile, and several torrents download at once (up to 5; more wait in line). The bandwidth
cap (Settings) covers torrents too: they get what the other downloads leave over.

Results that only have a **magnet link** need the client to fetch the torrent's file list from other
peers first, which can take up to 50 seconds; results with a `.torrent` file are looked into
first. Fetched `.torrent` files are kept between runs. A torrent whose file list couldn't be
had (no peers answered its magnet link, its link was broken) is left alone for 12 hours, also
across restarts: before, the same dead torrent was asked for again for every track it might
hold.

**Pause** (`P`) pauses the torrents; they keep what they have, and their time limit
starts again when you resume.

## Tuning

| Setting | Effect |
|---|---|
| Give up on a torrent download after (Torrent settings) | give a track's torrent file more time (default 30 minutes, counted while its torrent is downloading, not while it waits in line) |
| Give up on an indexer search after (Torrent settings) | default 20 seconds; lower it if some indexers are always slow |
| Format `flac` / `original` (Settings) | prefer lossless torrents |
| Firewall | The client listens on a free port it picks at start (and uses UPnP where the router allows it). It works without incoming connections. |

A torrent that receives no data for 3 minutes is given up on as dead.

## Troubleshooting

Run `./universal-music-downloader --check-torrents` first. It tells you which step is broken. The
Sources tab shows each indexer's last error; the Log tab (`v` for details) and the log file
(in `~/.local/share/universal-music-downloader/logs/`) have more.

| Message | What to do |
|---|---|
| `couldn't fetch the indexer definitions from GitHub` | GitHub wasn't reachable on the first start; the definitions are fetched when you save the Torrent settings again (or `u` in *Choose indexers…*). |
| `no indexers chosen` | **Choose indexers…** and turn some on, or set Jackett/Prowlarr. |
| `… Cloudflare …` on an indexer | Set up FlareSolverr (above), or turn that indexer off. |
| `can't reach …` / `connection failed` | A site is down, or Jackett/Prowlarr isn't running or is on another address: `systemctl status jackett` (or `prowlarr`), or `docker compose ps`. |
| `error 100: Invalid API Key` / `rejected the API key` | Copy the Jackett/Prowlarr key again. Prowlarr and Jackett keys are different. |
| `HTTP 404` / `doesn't look like a Torznab feed` | Give just the address (`http://127.0.0.1:9117`) instead of a hand-written path. |
| `Prowlarr … has no enabled torrent indexers` | Add/enable indexers in Prowlarr. |
| `search … found nothing` | Your indexers don't carry it: choose more (music ones especially). Try `--check-torrents --torrent-query "Artist Album"` with a well-known album. |
| Every indexer fails within a second with `The response ended prematurely` | Jackett can't get out: check **Proxy** in Jackett's settings (a proxy that's down or refuses Jackett breaks every indexer at once). |
| `couldn't get the torrent file` | The site refused the download: it's behind Cloudflare (FlareSolverr above), or (Jackett/Prowlarr) your login on that site expired. |
| `N torrents found; the M best-matching ones don't contain this track` | Normal for rarer music: the album isn't on your indexers in a form that contains this track. More indexers help. The log lists which torrents were looked into. |
| `Jackett: NAME: error 900: …` / `answered HTTP 400` | That indexer failed inside Jackett (login expired, Cloudflare, site down, proxy). Fix it in Jackett (**Test** shows the same error). After three failures it's left out for a while; the Sources tab shows when it's tried again. |
| `no peers answered for the magnet link` | The torrent is dead or your network blocks BitTorrent. Other results are tried for the next track; this one is left alone for 12 hours (`… (tried 2h ago)`). |
| `no peer sent any data for 3 minutes (dead torrent?)` / `the torrent download stalled` | Dead or nearly dead torrent; the next-best one is tried right away. Retry later with `r` (one track) or `R` (all) in the Queue tab. |
| `didn't finish within 30m` | Slow swarm: raise "Give up on a torrent download after". |
| `every torrent indexer is paused after repeated errors` | All indexers failed three times in a row; tracks wait and are retried when the first one comes back. |
| `Torrent: …` problem in the Sources tab | The source couldn't be set up (no indexers, Jackett/Prowlarr unreachable, wrong key). Fix it and save the Torrent settings again. |

To stop using torrents, turn the Torrent source off (`space` in the Sources tab).
