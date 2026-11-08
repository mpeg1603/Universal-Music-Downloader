#!/bin/bash
# Builds the Linux packages of the window app: an AppImage and a tar.gz with its Qt libraries.
#   packaging/build-appimage.sh BINARY VERSION OUTPUT_DIR [TOOLS_DIR]
# Needs Qt 6 and its qmake; fetches linuxdeploy and its Qt plugin into TOOLS_DIR.
set -euo pipefail

binary=$(realpath "$1")
version=$2
mkdir -p "$3"
out=$(realpath "$3")
tools=$(realpath -m "${4:-${TMPDIR:-/tmp}/linuxdeploy-tools}")
here=$(dirname "$(realpath "$0")")
arch=$(uname -m)
name=universal-music-downloader-gui-$version-linux-$arch

mkdir -p "$tools"
for tool in linuxdeploy-$arch.AppImage linuxdeploy-plugin-qt-$arch.AppImage; do
  if [ ! -x "$tools/$tool" ]; then
    case $tool in
      linuxdeploy-plugin-qt*) repo=linuxdeploy-plugin-qt ;;
      *) repo=linuxdeploy ;;
    esac
    curl -fsSL -o "$tools/$tool" "https://github.com/linuxdeploy/$repo/releases/download/continuous/$tool"
    chmod +x "$tools/$tool"
  fi
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
appdir=$work/AppDir
install -Dm755 "$binary" "$appdir/usr/bin/universal-music-downloader-gui"
install -Dm644 "$here/universal-music-downloader.desktop" "$appdir/usr/share/applications/universal-music-downloader.desktop"
install -Dm644 "$here/icon.png" "$appdir/usr/share/icons/hicolor/256x256/apps/universal-music-downloader.png"

cd "$work"
export APPIMAGE_EXTRACT_AND_RUN=1
export PATH="$tools:$PATH"
export QMAKE=${QMAKE:-$(command -v qmake6 || command -v qmake)}
export EXTRA_QT_PLUGINS="waylandcompositor;"
export EXTRA_PLATFORM_PLUGINS="libqwayland-generic.so;"
export OUTPUT=$out/$name.AppImage
export LDAI_OUTPUT=$OUTPUT
"$tools/linuxdeploy-$arch.AppImage" --appdir "$appdir" --plugin qt --output appimage

# The same files as a plain folder, for those who'd rather not use an AppImage.
mkdir -p "$work/tar"
cp -a "$appdir" "$work/tar/$name"
tar -C "$work/tar" -czf "$out/$name.tar.gz" "$name"
ls -l "$out"
