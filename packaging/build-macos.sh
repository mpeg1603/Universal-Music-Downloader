#!/bin/bash
# Builds the macOS package of the window app: an .app with its Qt libraries, in a zip.
#   packaging/build-macos.sh BINARY VERSION OUTPUT_DIR
# Needs Qt 6 (macdeployqt). The app is signed ad hoc only, so macOS asks before opening it.
set -euo pipefail

binary=$1
version=$2
mkdir -p "$3"
out=$(cd "$3" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
arch=$(uname -m)
[ "$arch" = x86_64 ] && arch=amd64
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/Universal Music Downloader.app"

mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$binary" "$app/Contents/MacOS/universal-music-downloader-gui"

icons="$work/icon.iconset"
mkdir "$icons"
for size in 16 32 128 256; do
  sips -z "$size" "$size" "$here/icon.png" --out "$icons/icon_${size}x${size}.png" >/dev/null
done
iconutil -c icns "$icons" -o "$app/Contents/Resources/icon.icns"

cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Universal Music Downloader</string>
  <key>CFBundleIdentifier</key><string>io.github.mpeg1603.universal-music-downloader</string>
  <key>CFBundleExecutable</key><string>universal-music-downloader-gui</string>
  <key>CFBundleIconFile</key><string>icon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${version#v}</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

macdeployqt "$app"
codesign --force --deep --sign - "$app"
ditto -c -k --keepParent "$app" "$out/universal-music-downloader-gui-$version-macos-$arch.zip"
ls -l "$out"
