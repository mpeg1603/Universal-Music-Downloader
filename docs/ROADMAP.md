# Roadmap: the window version

The app gets a Qt window (Linux and Windows) next to the terminal app, over the same backend
(`core.Backend`). Qt through the [miqt](https://github.com/mappu/miqt) Go bindings: one
language, one program, no web view.

| Phase | Milestone | State |
|---|---|---|
| **0. Groundwork** | Backend change notifications (`Watch`); CI (tests, Linux/Windows builds, draft releases with checksums); GPL-3.0; the direct route on Windows; ffmpeg fetched when missing | done |
| **1. Window and queue** | Main window, tabs, the header with speeds, pause; the Queue (fast at 70,000 tracks) with filters, sorting, find, details, cancel/restart/do next; the Log | done |
| **2. Everyday tabs** | Search (fields and tags, artists/albums/tracks), Library (with upgrade), Sources (settings windows, routes, indexers and accounts), Settings, the first-run setup as a wizard | done |
| **3. Soulseek** | Search, transfers, browse, users, chat, interests | |
| **4. Windows and packages** | Windows build in CI with Qt's libraries, installer and portable zip, Linux AppImage and tar.gz, checksums; code signing (SignPath) when there's a release | |
| **5. Polish and docs** | Tray icon, notifications, light/dark, shortcuts, README and tutorials with screenshots | |
