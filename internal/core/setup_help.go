package core

// The setup's longer explanations ("What's this?").

const soulseekMore = `Soulseek is a peer-to-peer network, running since 2002 and made for music: people share
folders from their own collections, and you download from them directly, user to user. It's
strong on whole albums, rare and older releases, and lossless (FLAC) copies.

Why an account: the Soulseek server keeps track of who's online and passes searches around, so
you log in with a username and password. A name nobody has yet makes a new account on the first
login. There's no email and no way to reset a lost password, so keep it.

Why you share: Soulseek runs on give and take. Many users don't send files to people who share
nothing, and the network's rules expect it. The app shares your library folders and downloads,
read only, with 2 upload slots by default.

The rules: the server bans clients that flood it (many searches, connecting and disconnecting
quickly). The app keeps to a search budget (one search every 10 seconds at most, 120 an hour),
downloads at most 2 files at once, and waits out a ban instead of trying again.

The port: other users connect to you on a listening port (2234). If your router doesn't let
them in, Soulseek still works, but some users can't reach you. UPnP (on by default) asks your
router to open it; otherwise forward it by hand: docs/soulseek-port-forwarding.md, MikroTik
included. With a VPN, set Soulseek's route to "direct" in the Sources tab.

Nicotine+ is a popular Soulseek program: "Import from Nicotine+" copies its account and
settings. Don't run both with the same account at the same time: each logs the other out.`

const torrentMore = `Torrent sites list torrents (a .torrent file or a magnet link describes a set of files);
the files themselves come from other people's computers over BitTorrent. Music torrents are
usually whole albums or discographies, often lossless. The app downloads only the files it
needs and stops when they're complete (no seeding afterwards).

Indexers are the sites that get searched. They're built in: the app reads Jackett's site
definitions (the files Jackett uses to know how to search each site), uses the public ones and
updates them from GitHub weekly. RuTracker.org, a big Russian tracker with a lot of lossless
music, needs your account there: Sources → Torrent → Choose indexers → RuTracker.org → Enter.

Jackett and Prowlarr are separate programs people run to search many torrent sites at once
for apps like this one; they answer in a standard format (Torznab). You don't need either,
since the app searches the sites itself. If you already run one (with private trackers you
have accounts on, say), give its address and API key and its results are added.

FlareSolverr: some sites put Cloudflare's "checking your browser" page in front, which only a
real browser gets past. FlareSolverr is a separate program that opens such pages in a browser
and hands the cookies to apps like this one (Jackett uses it the same way); its address is
usually http://127.0.0.1:8191. Without it, those sites are skipped and the others carry on.

BitTorrent shows your IP address to the others in a swarm, and many public torrents of
commercial music are unauthorized copies: what you download is your responsibility.`
