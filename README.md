# spotify-downloader

Makes a local copy of your Spotify library: your playlists and Liked Songs. It never downloads
from Spotify; it finds each song elsewhere. By default it tries these sources in this order, and
`--sources` picks any of them in any order:

| Source | What it uses |
|---|---|
| **Bandcamp** | Only releases the artist offers as a free download. Paid ones show up in the failure report with a link to buy them. |
| **Internet Archive** | Only items published under a license (Creative Commons / public domain). Live concert tapes are excluded. |
| **YouTube Music** | Official "song" uploads, found by ISRC and by artist + title. |
| **SoundCloud** | Public tracks. 30-second preview streams are detected and rejected. |
| **YouTube** | Regular uploads. |
| **Torrent** *(optional)* | Searched through your own Jackett or Prowlarr, downloaded with aria2c, fetching only the file needed. Added at the end of the default order once set up; see **[TORRENTS.md](TORRENTS.md)**. |

Every result is checked before it's used:

- title, artist and length must match;
- versions like *live*, *remix*, *cover*, *karaoke*, *sped up* or *1 hour* are rejected unless your track is that version;
- the real length of the downloaded file is checked again.

Files are then converted, tagged (title, artists, album, track number, year, ISRC, cover art) and
saved as `Album Artist/Album/NN - Title.ext`.

At the end you get, in `~/Music/Spotify/reports/` (as `.md` and `.csv`):

1. **`artists`**: every artist you got at least one track from, with links: Spotify, Deezer, and where the music was found (Bandcamp page, YouTube Music channel, …)
2. **`downloaded`**: every downloaded track, with its source link and file path
3. **`failed`**: every track that was not downloaded and why, per source (e.g. "on Bandcamp but not a free download; you can buy it at …")

Also in `~/Music/Spotify/Playlists/`: one `.m3u8` file per playlist, which any music player opens.

## Requirements

- **The `spotify-downloader` program**: a single file with nothing to install. Build it once with `go build` (see the end of this file).
- **ffmpeg**. Most Linux systems have it; otherwise `sudo pacman -S ffmpeg` / `sudo apt install ffmpeg`.
- **yt-dlp** is downloaded automatically on first use (the official build, checksum-verified, refreshed weekly) into `~/.cache/spotify-downloader/`.
- **For YouTube**, a JavaScript runtime: `node` (which you have) or `deno`.
- **For torrents only**: the **`aria2`** package, which provides the `aria2c` command
  (`sudo pacman -S aria2` / `sudo apt install aria2`; there is no package called `aria2c`),
  plus Jackett or Prowlarr. [TORRENTS.md](TORRENTS.md) walks through it.

## Step 1: get your track list (no Premium needed)

Spotify only lets apps you register yourself read your library if you pay for Premium, so the
tool reads the track list from a file instead. Pick one:

- **Spotify's own data export (recommended, complete).**
  1. Go to <https://www.spotify.com/account/privacy/>.
  2. Under *Download your data*, tick **Account data** and request it.
  3. Spotify emails you a link, usually within a few days. Download `my_spotify_data.zip`.

  It contains all your playlists and Liked Songs. It doesn't say which playlists you *made* versus
  only *follow*, so check with `--list` and leave the followed ones out with `--exclude-playlist`.
  It also has no track lengths, so the tool looks them up on Deezer's free public API.
- **[Exportify](https://exportify.net)**: log in, click *Export All*, and you get a zip of CSVs
  including Liked Songs. It's a third-party app, so Spotify's limits on such apps may block it
  for your account; if it works, it's instant.
- **Any CSV** with track and artist columns (TuneMyMusic and similar tools export these).
- **A text file** with one `Artist - Title` per line.
- **A public playlist or album link.** It's instant, but Spotify only shows the first 100 tracks
  this way.

You can combine several: `--from a.zip --from b.csv`.

## Step 2: run it

```sh
# See what was found:
./spotify-downloader --from ~/Downloads/my_spotify_data.zip --list

# Test with a few tracks first, leaving out playlists you only follow:
./spotify-downloader --from ~/Downloads/my_spotify_data.zip --exclude-playlist "Discover Weekly" --limit 20

# Then the whole thing. The track list is remembered, so later runs need no --from:
./spotify-downloader --exclude-playlist "Discover Weekly"
```

While it runs, finished tracks scroll by (✓ downloaded, ✗ not, and why), and a panel at the
bottom shows each track in progress (with a progress bar while it downloads), the overall
progress, the download speed and how much disk space is left.

You can stop it with Ctrl-C at any time. It finishes the tracks in progress, writes the reports,
and continues where it left off on the next run. Tracks that failed because a site was unreachable
are retried automatically. Tracks with no match are retried only with `--retry-failed`.

### Options

| Option | Meaning |
|---|---|
| `--out DIR` | Music folder (default `~/Music/Spotify`). |
| `--format mp3\|opus\|m4a\|flac\|original` | Default `mp3` (VBR V0). YouTube audio is Opus at about 130–160 kbps, so `opus` or `original` avoid re-encoding it. Free Bandcamp downloads come in lossless. |
| `--playlist NAME` / `--exclude-playlist NAME` / `--no-liked` | Choose which playlists to download (repeatable). |
| `--sources LIST` | Which sources to use, in order: any of `bandcamp,archive,youtube_music,soundcloud,youtube,torrent`. Default: all but `torrent`, plus `torrent` at the end once torrents are set up. Examples: `--sources torrent`, `--sources torrent,youtube_music`. |
| `--workers N` | Tracks searched/converted in parallel (default 3; more makes YouTube bot checks likelier). Tracks waiting for a torrent don't count. |
| `--limit N` | Only process N tracks. |
| `--retry-failed` | Try "no match" tracks again. |
| `--cookies-from-browser firefox` | Let yt-dlp use your browser's YouTube login (see Troubleshooting). |
| `--template "{artist}/{title}"` | File layout. Fields: `{artist} {artists} {album_artist} {album} {title} {tracknum} {year}`. |
| `--report-only` | Just rewrite the reports and playlists. |
| `-v` | Show what's happening in detail. |

## Torrents

`torrent` is a source like the others. Set it up once:

1. Install the **aria2** package.
2. Install Jackett or Prowlarr and add the indexers you want.
3. Point the tool at it:

```sh
./spotify-downloader --torznab http://127.0.0.1:9117 --torznab-key YOUR_KEY --check-torrents   # Jackett
./spotify-downloader --torznab http://127.0.0.1:9696 --torznab-key YOUR_KEY --check-torrents   # Prowlarr
```

`--check-torrents` tests every step (aria2c, connection, API key, a sample search, a sample
torrent's file list) and says what to fix. The settings are remembered. From then on, torrents
are tried after the other sources, or wherever you put `torrent` in `--sources`.

**[TORRENTS.md](TORRENTS.md)** is the full guide: installing Jackett/Prowlarr (AUR, Docker),
adding indexers, FlareSolverr for Cloudflare-protected sites, choosing the order, how torrents
and files are picked, tuning, and troubleshooting every message.

BitTorrent uploads to other peers *while* downloading, and many public torrents of commercial
music are unauthorized copies. What you download this way, and whether it's legal where you live,
is up to you.

## Troubleshooting

- **"YouTube asked to sign in (bot check)"**: run again with `--cookies-from-browser firefox` (or
  `chrome`, `brave`, …) while logged in to YouTube in that browser, and/or use `--workers 2`.
- **"skipped, it kept failing to respond earlier in this run"**: a site was unreachable five times
  in a row, so it was skipped for the rest of the run. Those tracks are retried next time.
- **Wrong song downloaded**: the log (path printed at the end of each run) shows which result was
  picked and why. Delete the file and run again with a different `--sources` order.
- **Podcast episodes and audiobooks** in playlists are skipped and listed in `failed`.

## Building

You need Go 1.22 or newer. There are no third-party dependencies.

```sh
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o spotify-downloader .
# Other systems, e.g. Windows:
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o spotify-downloader.exe .
go test ./...      # tests (need ffmpeg)
```

The tool keeps its state in `<music folder>/.spotify-downloader/`:

- `library.json`: the imported track list plus the Deezer lookups;
- `state.jsonl`: per-track results;
- `logs/`: one log per run.
