#!/usr/bin/env bash
# Build doom-ascii and fetch the default (legally clean) game content into
# ./assets — Freedoom by default (PRD §9.1). Run on the host before enabling
# the arcade's DOOM entries.
#
#   scripts/fetch-assets.sh [--shareware] [--arcade]
#
# --shareware additionally fetches the freely redistributable doom1.wad
#   shareware episode.
# --arcade installs the 80s arcade classics (Space Invaders, Pac-Man, Tetris,
#   Moon Patrol) the arcade plugin launches via the same sandboxed-PTY path as
#   DOOM. Needs apt + sudo (Debian/Ubuntu); the menu lists whatever installs.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$ROOT/assets"
BUILD="$ROOT/.build"
FREEDOOM_VERSION="${FREEDOOM_VERSION:-0.13.0}"

want_shareware=0
want_arcade=0
for arg in "$@"; do
  case "$arg" in
    --shareware) want_shareware=1 ;;
    --arcade) want_arcade=1 ;;
    *) echo "!! unknown flag: $arg (use --shareware and/or --arcade)" >&2; exit 2 ;;
  esac
done

mkdir -p "$ASSETS/bin" "$ASSETS/wads" "$BUILD"

# --- doom-ascii -------------------------------------------------------------
if [ ! -x "$ASSETS/bin/doom_ascii" ]; then
  echo ">> building doom-ascii"
  if [ ! -d "$BUILD/doom-ascii" ]; then
    git clone --depth 1 https://github.com/wojciech-graj/doom-ascii "$BUILD/doom-ascii"
  fi
  make -C "$BUILD/doom-ascii" -j"$(nproc)"
  BIN="$(find "$BUILD/doom-ascii" -maxdepth 3 -type f \( -name 'doom-ascii' -o -name 'doom_ascii' \) -executable | head -1)"
  if [ -z "$BIN" ]; then
    echo "!! doom-ascii build produced no binary" >&2
    exit 1
  fi
  cp "$BIN" "$ASSETS/bin/doom_ascii"
  echo ">> installed $ASSETS/bin/doom_ascii"
else
  echo ">> doom-ascii already built"
fi

# --- Freedoom ---------------------------------------------------------------
if [ ! -f "$ASSETS/wads/freedoom1.wad" ]; then
  echo ">> fetching Freedoom $FREEDOOM_VERSION"
  ZIP="$BUILD/freedoom.zip"
  curl -fsSL -o "$ZIP" \
    "https://github.com/freedoom/freedoom/releases/download/v$FREEDOOM_VERSION/freedoom-$FREEDOOM_VERSION.zip"
  unzip -o -j "$ZIP" '*/freedoom1.wad' '*/freedoom2.wad' -d "$ASSETS/wads"
  echo ">> installed freedoom1.wad freedoom2.wad"
else
  echo ">> Freedoom already present"
fi

# --- Doom shareware (optional) ----------------------------------------------
if [ "$want_shareware" = 1 ] && [ ! -f "$ASSETS/wads/doom1.wad" ]; then
  echo ">> fetching Doom shareware episode"
  curl -fsSL -o "$ASSETS/wads/doom1.wad" \
    "https://distro.ibiblio.org/slitaz/sources/packages/d/doom1.wad"
  echo ">> installed doom1.wad (shareware)"
fi

# --- Arcade classics (optional) ---------------------------------------------
# Tiny, well-packaged ncurses C programs from the distro (Debian/Ubuntu
# universe). They land in /usr/games, which the arcade plugin probes alongside
# assets/bin and PATH. The arcade menu lists whichever of these is present.
ARCADE_PKGS="ninvaders pacman4console moon-buggy tint"
if [ "$want_arcade" = 1 ]; then
  if command -v apt-get >/dev/null 2>&1; then
    SUDO=""
    [ "$(id -u)" -ne 0 ] && SUDO="sudo"
    echo ">> installing arcade classics: $ARCADE_PKGS"
    $SUDO apt-get update -y
    # Install individually so one missing package doesn't abort the rest.
    for pkg in $ARCADE_PKGS; do
      $SUDO apt-get install -y "$pkg" || echo "!! $pkg not available; skipping"
    done
  else
    echo "!! --arcade needs apt-get (Debian/Ubuntu)." >&2
    echo "   On other distros, install equivalents of: $ARCADE_PKGS" >&2
  fi
fi

echo ">> done. WADs:"
ls -l "$ASSETS/wads"
echo ">> arcade classics on host:"
for bin in ninvaders pacman4console moon-buggy tint vitetris; do
  p="$(command -v "$bin" 2>/dev/null || true)"
  [ -z "$p" ] && [ -x "/usr/games/$bin" ] && p="/usr/games/$bin"
  [ -n "$p" ] && echo "   $bin -> $p"
done
