# Roadmap: the window version

The app gets a Qt window (Linux and Windows) next to the terminal app, over the same backend
(`core.Backend`). Qt through the [miqt](https://github.com/mappu/miqt) Go bindings: one
language, one program, no web view.

| Phase | Milestone | State |
|---|---|---|
| **0. Groundwork** | Backend change notifications (`Watch`); CI (tests, Linux/Windows builds, draft releases with checksums); GPL-3.0; the direct route on Windows; ffmpeg fetched when missing | done |
| **1. Window and queue** | Main window, tabs, the header with speeds, pause; the Queue (fast at 70,000 tracks) with filters, sorting, find, details, cancel/restart/do next; the Log | done |
| **2. Everyday pages** | Search (fields and tags, artists/albums/tracks), Library (with upgrade), Sources (settings windows, routes, indexers and accounts), Settings, the first-run setup as a wizard | done |
| **3. Soulseek** | Search, transfers, browse, users, chat, interests | done |
| **4. Windows and packages** | Windows build in CI with Qt's libraries, installer and portable zip, Linux AppImage and tar.gz, checksums | written, first run is the first release; macOS builds and an Arch package added; code signing (SignPath) is put off for now |
| **5. Polish and docs** | Tray icon, notifications, light/dark, shortcuts, README and tutorials with screenshots | done |

The window was redesigned after phase 2: a desktop app with a sidebar and its own look, in plain
words, rather than the terminal app's tabs in a window. Both do the same things.

Since then: a Dashboard, and Sort & merge (planning, applying and undoing a tidy-up of your folders),
in both the window and the terminal app.
