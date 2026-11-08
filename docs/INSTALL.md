# Installing, updating and removing

Everything is on the [releases page](../../../releases). There are two apps, and they share
their settings and data, though they can't run at the same time:

- **The window** (`universal-music-downloader-gui`): the desktop app, for most people.
- **The terminal app** (`universal-music-downloader`): the same features from the keyboard. It
  also runs over SSH and on a machine with no desktop.

| You have | Window app | Terminal app |
|---|---|---|
| Windows 10 or 11 | `…-windows-amd64-setup.exe`, or `…-windows-amd64-portable.zip` | `…-windows-amd64.zip` |
| Arch and its relatives | `universal-music-downloader-…-x86_64.pkg.tar.zst`, or [build it](#arch-linux-and-its-relatives) | the same package has both |
| Any other 64-bit Linux | `…-linux-x86_64.AppImage`, or `…-linux-x86_64.tar.gz` | `…-linux-amd64.tar.gz` |
| Linux on ARM | build it from source | `…-linux-arm64.tar.gz` |
| macOS on Apple silicon | `…-macos-arm64.zip` | `…-darwin-arm64.tar.gz` |
| macOS on Intel | build it from source | `…-darwin-amd64.tar.gz` |

`SHA256SUMS` on the same page has every file's checksum.

ffmpeg and yt-dlp are fetched by the app if they're missing (see the
[README](../README.md#requirements)).

## Windows

**Installer**
- *Install:* run `…-setup.exe`. The app isn't code-signed, so Windows may warn you: choose
  **More info → Run anyway**.
- *Update:* run the new installer over the old version.
- *Remove:* Settings → Apps → Installed apps → Universal Music Downloader → Uninstall.

**Portable zip, or the terminal app**
- *Install:* unzip it anywhere and run the `.exe` inside.
- *Update:* quit the app and replace the folder with the new one.
- *Remove:* delete the folder.

## Arch Linux and its relatives

This is an ordinary pacman package, with no AUR involved. The window app appears in your app
launcher (rofi's `drun` mode, for example).

**Using the ready-made package**
```sh
sudo pacman -U universal-music-downloader-*-x86_64.pkg.tar.zst
```

**Or building it yourself**, which uses your own system's libraries:
```sh
git clone https://github.com/mpeg1603/Universal-Music-Downloader.git
cd Universal-Music-Downloader/packaging/arch
makepkg -si --nocheck
```

- *Update:* install the new package, or in that folder run `git pull && makepkg -si --nocheck`.
- *Remove:* `sudo pacman -R universal-music-downloader`

## Other Linux

**AppImage (window app)**
- *Install:* `chmod +x universal-music-downloader-gui-*.AppImage` and run it. On distributions without FUSE 2 add
  `--appimage-extract-and-run`.
- *Update:* replace the file with the new one.
- *Remove:* delete the file.

**tar.gz (either app)**
- *Install:* unpack it anywhere and run `universal-music-downloader` inside, or for the window
  app `usr/bin/universal-music-downloader-gui` through `AppRun`.
- *Update:* quit the app and replace the folder.
- *Remove:* delete the folder.

## macOS

The macOS builds are the least tested: they're made automatically and aren't notarized by
Apple.

**Window app**
- *Install:* unzip it and drag the app to Applications. The first time, right-click it and choose
  **Open**. If macOS refuses, use System Settings → Privacy & Security → **Open Anyway**.
- *Update:* quit the app and replace it with the new one.
- *Remove:* move it to the Trash.

**Terminal app**
- *Install:* unpack it and run `xattr -d com.apple.quarantine universal-music-downloader`, then
  `./universal-music-downloader`.
- *Update:* replace the file.
- *Remove:* delete it.

## From source

You need Go 1.25 or newer. For the window you also need a C++ compiler and Qt 6: `qt6-base` on
Arch, `qt6-base-dev` on Debian and Ubuntu, `qt` from Homebrew on macOS.

```sh
git clone https://github.com/mpeg1603/Universal-Music-Downloader.git
cd Universal-Music-Downloader
go build -o universal-music-downloader ./cmd/universal-music-downloader
go build -o universal-music-downloader-gui ./cmd/universal-music-downloader-gui
```

The first window build compiles the Qt bindings and takes a while.

- *Update:* `git pull`, then build again.
- *Remove:* delete the folder.

## Removing your settings and data too

Uninstalling leaves these alone, so a later install picks up where you left off. **Your music is
never in them.** To remove them as well, delete:

| | Settings (accounts and passwords) | State, logs, temporary files | Downloaded tools |
|---|---|---|---|
| Linux | `~/.config/universal-music-downloader` | `~/.local/share/universal-music-downloader` | `~/.cache/universal-music-downloader` |
| Windows | `%AppData%\universal-music-downloader` | `%UserProfile%\.local\share\universal-music-downloader` | `%LocalAppData%\universal-music-downloader` |
| macOS | `~/Library/Application Support/universal-music-downloader` | `~/.local/share/universal-music-downloader` | `~/Library/Caches/universal-music-downloader` |
