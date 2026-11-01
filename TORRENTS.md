# Torrent setup guide

`torrent` is a download source like the others: you can use it alone (`--sources torrent`), put
it first, or leave it at the end of the list, where it goes automatically once it's set up.

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

```sh
./spotify-downloader --torznab http://127.0.0.1:9117 --torznab-key YOUR_API_KEY --check-torrents   # Jackett
./spotify-downloader --torznab http://127.0.0.1:9696 --torznab-key YOUR_API_KEY --check-torrents   # Prowlarr
```

If every line shows ✓, you're done. The settings are saved, so later runs need no `--torznab`
flags. Continue with [step 8](#step-8-choose-where-torrents-go-in-the-order).

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

```sh
./spotify-downloader --torznab http://127.0.0.1:9117 --torznab-key PASTE_KEY_HERE --check-torrents
```

Use `http://127.0.0.1:9696` for Prowlarr.

The check goes through every step and says what to fix when something's wrong:

```
Checking the torrent setup (TORRENTS.md explains each step):
  ✓ aria2 version 1.37.0 (/usr/bin/aria2c)
  ✓ Jackett (all indexers) answers (http://127.0.0.1:9117/api/v2.0/indexers/all/results/torznab/api)
  ✓ searching for "Daft Punk Random Access Memories": 37 results, 31 with seeders
      1. Daft Punk - Random Access Memories (2013) [FLAC]  (seeders: 52)
      …
  ✓ the top result's torrent file lists 15 files (13 audio)

All good.
```

Things to know about this step:

- **What it searches for:** an album from your library, or `--torrent-query "Artist Album"` if you
  give one.
- **The settings are saved** in `~/.config/spotify-downloader/torrents.json`, readable only by you
  because it holds the key. Later runs don't need the flags again. `--torznab none` forgets them.
- **Instead of saving them**, you can set `TORZNAB_URL` and `TORZNAB_KEY` in your environment.

### Other ways to point it at your indexers

| You pass | What happens |
|---|---|
| `--torznab http://127.0.0.1:9117` | Jackett, all configured indexers at once |
| `--torznab http://127.0.0.1:9696` | Prowlarr: every enabled torrent indexer, found automatically |
| `--torznab "http://127.0.0.1:9117/api/v2.0/indexers/NAME/results/torznab/"` | one specific Jackett indexer (copy it with **Copy Torznab Feed** on the dashboard) |
| `--torznab URL1 --torznab URL2` | several feeds; results are merged |

A feed URL that already contains `apikey=…` keeps its own key.

## Step 8: choose where torrents go in the order

Sources are tried left to right for each track; the first one with a good match wins.

| Command | Effect |
|---|---|
| `./spotify-downloader` | default order, torrents last: Bandcamp, Internet Archive, YouTube Music, SoundCloud, YouTube, Torrent |
| `./spotify-downloader --sources torrent` | torrents only |
| `./spotify-downloader --sources torrent,youtube_music` | torrents first, YouTube Music for the rest |
| `./spotify-downloader --sources bandcamp,torrent,youtube_music` | free Bandcamp downloads first, then torrents, then YouTube Music |
| `./spotify-downloader --sources bandcamp,archive,youtube_music,soundcloud,youtube` | no torrents, even though they're set up |

Torrents usually give the best quality, especially with `--format flac` or `--format original`,
which prefer lossless torrents. They're slower per track than YouTube.

To retry tracks that weren't found earlier, now that torrents are available, add `--retry-failed`.

---

## How it picks a torrent and a file

For each track:

1. **Search** for `Album Artist + Album` (albums are what most music torrents contain), and if
   that finds nothing usable, for `Artist + Title` (singles, discographies, compilations). Each
   search is done once per run and reused for the album's other tracks.
2. **Rank** the results by how well their name fits the artist and album, preferring the format
   you asked for (FLAC for `--format flac`/`original`, otherwise MP3) and more seeders. Torrents
   with 0 seeders are dropped.
3. **Look inside** the 3 best: fetch their `.torrent` file and pick the file whose name fits the
   track best (track number counts too). Files whose names say *live*, *remix*, *instrumental*
   etc. are skipped unless your track is that version.
4. **Download only that file** with aria2c into a temporary folder. There's no seeding afterwards
   (`--seed-time=0`), and the leftover pieces of neighbouring files are deleted.
5. **Check, convert, tag:** like every other source, the file's real length must match the
   track, then it's converted to your format, tagged and filed. In the reports the source shows
   as *Torrent: «torrent name»*, linking to its page on the indexer.

Results that only have a **magnet link** need aria2c to fetch the torrent's file list from other
peers first, which can take up to 2 minutes.

## Tuning

| Setting | Effect |
|---|---|
| `--torrent-timeout 1h` | give a single torrent download more time (default 30 minutes) |
| `--workers N` | tracks processed in parallel. At most 2 aria2c downloads run at once regardless, to go easy on your connection and the swarms. |
| `--format flac` / `original` | prefer lossless torrents |
| Firewall | aria2c listens on TCP/UDP ports 6881–6999. Opening them (or forwarding them on your router) lets more peers connect, so it's faster. It works without that too. |

A download that receives no data for 5 minutes is stopped as "no seeders".

## Troubleshooting

Run `./spotify-downloader --check-torrents` first. It tells you which step is broken. Run with
`-v` (or read the log, whose path is printed at the end of each run) for details.

| Message | What to do |
|---|---|
| `aria2c not found` | Install the **aria2** package (step 1). |
| `can't reach …` / `connection failed` | Jackett/Prowlarr isn't running or is on another address: `systemctl status jackett` (or `prowlarr`), or `docker compose ps`. |
| `error 100: Invalid API Key` / `rejected the API key` | Copy the key again (step 6). Prowlarr and Jackett keys are different. |
| `HTTP 404` / `doesn't look like a Torznab feed` | Give just the address (`http://127.0.0.1:9117`) instead of a hand-written path. |
| `Prowlarr … has no enabled torrent indexers` | Add/enable indexers in Prowlarr (step 4). |
| `search … found nothing` | No indexers, or none carry music: add music indexers and use **Test**. Try `--torrent-query "Artist Album"` with a well-known album. |
| `couldn't get the torrent file` | The indexer refused the download: it's behind Cloudflare (step 5) or your login on that site expired (re-enter it in Jackett/Prowlarr). |
| `N torrents found; the 3 best-matching ones don't contain this track` | Normal for rarer music: the album isn't on your indexers in a form that contains this track. More indexers help. |
| `no peers answered for the magnet link` | The torrent is dead or your network blocks BitTorrent. Other results are tried for the next track. |
| `aria2c couldn't download it (no seeder sent data for 5 minutes)` | Dead torrent. Rerun later with `--retry-failed`. |
| `didn't finish within 30m` | Slow swarm: raise `--torrent-timeout`. |
| `Torrent: skipped, it kept failing to respond earlier in this run` | Jackett/Prowlarr stopped answering five times in a row, so torrents were paused for the run. Check that it's still running; those tracks are retried next run. |
| `Skipping torrents: …` at the start | Torrents are set up but the check failed, so the run continued without them. Run `--check-torrents`. |

To stop using torrents: `--torznab none` forgets the settings. To skip them for one run only, pass
`--sources` without `torrent`.
