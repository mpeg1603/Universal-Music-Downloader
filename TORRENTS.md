# Torrent setup guide

`Torrent` is a download source like the others. In the app's **Sources** tab (key `4`) you turn
it on, put it anywhere in the order (`K`/`J`), and enter your Jackett or Prowlarr address and
API key (`Enter` on the Torrent row).

It needs two things next to `spotify-downloader`:

1. **aria2**, a small command-line download program that does the actual BitTorrent transfer.
2. **Jackett _or_ Prowlarr**, a program that searches torrent sites ("indexers") for you and
   answers through a standard search API called **Torznab**. You choose which indexers it uses.

```
spotify-downloader ──search "Artist Album"──▶ Jackett / Prowlarr ──▶ the indexers you added
        │                                             │
        │◀────────────── results + .torrent files ────┘
        │
        └── aria2c: downloads only the one file it needs, stops seeding as soon as it's done
```

> **Before you start.**
> - BitTorrent uploads pieces to other people while it downloads, and your IP address is
>   visible to everyone in the swarm.
> - Many public torrents of commercial music are unauthorized copies; downloading or sharing
>   them is illegal in many countries.
> - What you download is your responsibility. Free and licensed sources (Bandcamp free
>   downloads, Internet Archive) are tried first by default for that reason.

---

## Quick start (if Jackett or Prowlarr is already running)

1. In the app: **Sources** tab → select **Torrent** → `Enter` → enter `http://127.0.0.1:9117`
   (Jackett) or `http://127.0.0.1:9696` (Prowlarr) and the API key → **Save**; `space` turns
   the source on.
2. Check the whole chain from a terminal:

```sh
./spotify-downloader --check-torrents
```

If every line shows ✓, you're done. Continue with [step 8](#step-8-choose-where-torrents-go-in-the-order).

---

## Step 1: install aria2

The package is called **`aria2`**; the command it installs is **`aria2c`**. There is no package
named `aria2c`.

| System | Command |
|---|---|
| Arch, CachyOS, Manjaro | `sudo pacman -S aria2` |
| Debian, Ubuntu, Mint | `sudo apt install aria2` |
| Fedora | `sudo dnf install aria2` |
| macOS (Homebrew) | `brew install aria2` |

Check it with `aria2c --version`.

## Step 2: pick Jackett or Prowlarr

Both work the same way for this tool. You only need one.

| | Jackett | Prowlarr |
|---|---|---|
| Web page | <http://127.0.0.1:9117> | <http://127.0.0.1:9696> |
| How this tool uses it | one feed that searches all your indexers at once | it asks Prowlarr for its list of indexers and searches each enabled torrent indexer |
| Good for | the simplest setup | people who also use Sonarr/Radarr/Lidarr, which Prowlarr can feed too |

If unsure, take **Jackett**.

## Step 3: install it

### Arch / CachyOS (AUR)

They aren't in the official repositories. Install them from the AUR with your AUR helper
(`paru` or `yay`):

```sh
paru -S jackett-bin          # or: paru -S prowlarr-bin
sudo systemctl enable --now jackett      # or: prowlarr
systemctl status jackett                 # should say "active (running)"
```

### Any Linux with Docker

Save this as `docker-compose.yml` in a new folder and run `docker compose up -d` there. It
includes FlareSolverr, which [step 5](#step-5-flaresolverr-only-if-an-indexer-needs-it) may need.

```yaml
services:
  jackett:                                  # or prowlarr: image lscr.io/linuxserver/prowlarr:latest, port 9696
    image: lscr.io/linuxserver/jackett:latest
    environment: [PUID=1000, PGID=1000, TZ=Etc/UTC]
    volumes: [./jackett:/config]
    ports: ["127.0.0.1:9117:9117"]
    restart: unless-stopped
  flaresolverr:
    image: ghcr.io/flaresolverr/flaresolverr:latest
    ports: ["127.0.0.1:8191:8191"]
    restart: unless-stopped
```

### Other systems

See the official instructions: [Jackett](https://github.com/Jackett/Jackett#installation) or
[Prowlarr](https://wiki.servarr.com/prowlarr/installation).

When it's running, open its web page (addresses in step 2).

## Step 4: add indexers

Indexers are the torrent sites Jackett/Prowlarr search for you. Without any, every search comes
back empty.

**Jackett**
1. Click **+ Add indexer**.
2. Use the filters at the top to show indexers with music (category **Audio**).
3. Click the **+** next to the ones you want:
   - **public** ones work right away;
   - **semi-private/private** ones need your account details on that site.
4. Back on the dashboard, click **Test All**. Each indexer should get a green check.

**Prowlarr**
1. Go to **Indexers → Add Indexer**.
2. Filter by *Protocol: torrent* and look for indexers whose categories include **Audio**.
3. For each one, fill in what it asks for and click **Test**, then **Save**.
4. Make sure each indexer is **enabled**; the tool skips disabled ones.

Tips:
- Add several indexers. Different sites carry different music, and more seeders means faster
  downloads.
- Indexers that specialise in music (including lossless) give far better results for this tool
  than general ones.

## Step 5: FlareSolverr (only if an indexer needs it)

Some sites sit behind Cloudflare's "checking your browser" page. Jackett/Prowlarr then report
errors mentioning *Cloudflare* or *challenge* when you test that indexer. FlareSolverr solves
those pages for them.

1. Install it: `paru -S flaresolverr-bin` and `sudo systemctl enable --now flaresolverr`, or use
   the Docker service from step 3. It listens on <http://127.0.0.1:8191>.
2. Connect it:
   - **Jackett:** at the bottom of the dashboard, under *Jackett Configuration*, set **FlareSolverr
     API URL** to `http://127.0.0.1:8191` (in the Docker setup: `http://flaresolverr:8191`), then
     **Apply server settings**.
   - **Prowlarr:**
     1. Go to **Settings → Indexers → +** and add a **FlareSolverr** proxy with host
        `http://127.0.0.1:8191/` and a tag such as `flaresolverr`.
     2. Give the same tag to the indexers that need it.
3. Test those indexers again.

## Step 6: find the API key

| | Where the key is |
|---|---|
| Jackett | top right of the dashboard, **API Key** (copy button next to it) |
| Prowlarr | **Settings → General → Security → API Key** |

## Step 7: connect spotify-downloader and check

In the app's **Sources** tab, select **Torrent**, press `Enter` and fill in the address
(`http://127.0.0.1:9117` for Jackett, `http://127.0.0.1:9696` for Prowlarr) and the API key.
Then, from a terminal:

```sh
./spotify-downloader --check-torrents
```

The check goes through every step and says what to fix when something's wrong:

```
Checking the torrent setup (TORRENTS.md explains each step):
  ✓ aria2 version 1.37.0 (/usr/bin/aria2c)
  ✓ Jackett: RuTracker.org answers (http://127.0.0.1:9117/api/v2.0/indexers/rutracker/results/torznab/api)
  ✓ searching for "Daft Punk Random Access Memories": 37 results, 31 with seeders
      1. Daft Punk - Random Access Memories (2013) [FLAC]  (seeders: 52)
      …
  ✓ the top result's torrent file lists 15 files (13 audio)

All good.
```

Things to know about this step:

- **What it searches for:** an album from your playlists, or `--torrent-query "Artist Album"` if you
  give one.
- **The settings are saved** in `~/.config/spotify-downloader/config.json`, readable only by you
  because it holds the key.

### Other ways to point it at your indexers

| Address field | What happens |
|---|---|
| `http://127.0.0.1:9117` | Jackett: every configured indexer that carries music, each searched on its own (in parallel), so an indexer that fails shows its error instead of quietly returning nothing |
| `http://127.0.0.1:9696` | Prowlarr: every enabled torrent indexer, found automatically |
| `http://127.0.0.1:9117/api/v2.0/indexers/NAME/results/torznab/` | one specific Jackett indexer (copy it with **Copy Torznab Feed** on the dashboard) |
| `URL1, URL2` | several feeds; results are merged |

A feed URL that already contains `apikey=…` keeps its own key.

## Step 8: choose where torrents go in the order

Sources are tried top to bottom (Sources tab) for each track; the first one with a good match
wins. Move Torrent with `K` (up) and `J` (down); `space` turns a source on or off. For example,
Torrent right after Bandcamp gets lossless files first and leaves YouTube for the rest.

Torrents usually give the best quality, especially with the format set to `flac` or `original`
(Settings), which prefer lossless torrents. They're slower per track than YouTube.

To retry tracks that weren't found earlier, now that torrents are available, press `R` in the
Queue tab.

---

## How it picks a torrent and a file

For each track:

1. **Search**, from most to least specific, until something fits:
   1. `Album Artist + Album`, with edition details left out (*Turn On The Bright Lights: The
      Tenth Anniversary Edition (Remastered)* is searched as *Turn On The Bright Lights*), since
      indexers only find torrents whose names contain every word you search for;
   2. `Artist + Title`, for singles;
   3. the artist alone, for discographies and albums named differently on the indexer.

   Punctuation that trips up search engines (`: ( ) [ ] /` …) is left out. Each search is made
   once and reused for the album's other tracks; results are also kept on disk for 12 hours,
   so restarting the app doesn't search everything again.

   All your indexers are asked at once, but a search **doesn't wait for slow ones**: each gets
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
4. **Download** with aria2c, only the files needed. One aria2c runs for the whole run, and all
   tracks that need files from the same torrent share one download: the album's tracks are
   collected for a moment and then fetched together, and tracks that come later are added to
   the running download. Each track carries on as soon as *its* file is complete. There's no
   seeding afterwards (`--seed-time=0`), and leftover pieces of neighbouring files are deleted.
5. **Check, convert, tag:** like every other source, the file's real length must match the
   track, then it's converted to your format, tagged and filed. In the reports the source shows
   as *Torrent: «torrent name»*, linking to its page on the indexer.

While a track waits for its torrent it doesn't take a download slot, so other downloads go on
meanwhile, and several torrents download at once (up to 5; more wait in line). The bandwidth
cap (Settings) covers torrents too: aria2c gets what the other downloads leave over.

Results that only have a **magnet link** need aria2c to fetch the torrent's file list from other
peers first, which can take up to 50 seconds; results with a `.torrent` file are looked into
first. Fetched `.torrent` files are kept between runs.

## Tuning

| Setting | Effect |
|---|---|
| Give up on a torrent download after (Torrent settings) | give a track's torrent file more time (default 30 minutes, counted while its torrent is downloading, not while it waits in line) |
| Give up on an indexer search after (Torrent settings) | default 20 seconds; lower it if some indexers are always slow |
| Format `flac` / `original` (Settings) | prefer lossless torrents |
| Firewall | aria2c listens on TCP/UDP ports 6881–6999. Opening them (or forwarding them on your router) lets more peers connect, so it's faster. It works without that too. |

A torrent that receives no data for 3 minutes is given up on as dead.

## Troubleshooting

Run `./spotify-downloader --check-torrents` first. It tells you which step is broken. The
Sources tab shows each indexer's last error; the Log tab (`v` for details) and the log file
(in `~/.local/share/spotify-downloader/logs/`) have more.

| Message | What to do |
|---|---|
| `aria2c not found` | Install the **aria2** package (step 1). |
| `can't reach …` / `connection failed` | Jackett/Prowlarr isn't running or is on another address: `systemctl status jackett` (or `prowlarr`), or `docker compose ps`. |
| `error 100: Invalid API Key` / `rejected the API key` | Copy the key again (step 6). Prowlarr and Jackett keys are different. |
| `HTTP 404` / `doesn't look like a Torznab feed` | Give just the address (`http://127.0.0.1:9117`) instead of a hand-written path. |
| `Prowlarr … has no enabled torrent indexers` | Add/enable indexers in Prowlarr (step 4). |
| `search … found nothing` | No indexers, or none carry music: add music indexers and use **Test**. Try `--check-torrents --torrent-query "Artist Album"` with a well-known album. |
| Every indexer fails within a second with `The response ended prematurely` | Jackett can't get out: check **Proxy** in Jackett's settings (a proxy that's down or refuses Jackett breaks every indexer at once). |
| `couldn't get the torrent file` | The indexer refused the download: it's behind Cloudflare (step 5) or your login on that site expired (re-enter it in Jackett/Prowlarr). |
| `N torrents found; the M best-matching ones don't contain this track` | Normal for rarer music: the album isn't on your indexers in a form that contains this track. More indexers help. The log lists which torrents were looked into. |
| `Jackett: NAME: error 900: …` / `answered HTTP 400` | That indexer failed inside Jackett (login expired, Cloudflare, site down, proxy). Fix it in Jackett (**Test** shows the same error). After three failures it's left out for a while; the Sources tab shows when it's tried again. |
| `no peers answered for the magnet link` | The torrent is dead or your network blocks BitTorrent. Other results are tried for the next track. |
| `no peer sent any data for 3 minutes (dead torrent?)` / `the torrent download stalled` | Dead or nearly dead torrent; the next-best one is tried right away. Retry later with `r` (one track) or `R` (all) in the Queue tab. |
| `didn't finish within 30m` | Slow swarm: raise "Give up on a torrent download after". |
| `every torrent indexer is paused after repeated errors` | All indexers failed three times in a row; tracks wait and are retried when the first one comes back. |
| `Torrent: …` problem in the Sources tab | The source couldn't be set up (Jackett/Prowlarr unreachable, wrong key, no aria2c). Fix it and save the Torrent settings again. |

To stop using torrents, turn the Torrent source off (`space` in the Sources tab).
