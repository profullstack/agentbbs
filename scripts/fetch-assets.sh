#!/usr/bin/env bash
# Build doom-ascii and fetch the default (legally clean) game content into
# ./assets — Freedoom by default (PRD §9.1). Run on the host before enabling
# the arcade's DOOM entries.
#
#   scripts/fetch-assets.sh [--shareware]
#
# --shareware additionally fetches the freely redistributable doom1.wad
# shareware episode.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ASSETS="$ROOT/assets"
BUILD="$ROOT/.build"
FREEDOOM_VERSION="${FREEDOOM_VERSION:-0.13.0}"

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
if [ "${1:-}" = "--shareware" ] && [ ! -f "$ASSETS/wads/doom1.wad" ]; then
  echo ">> fetching Doom shareware episode"
  curl -fsSL -o "$ASSETS/wads/doom1.wad" \
    "https://distro.ibiblio.org/slitaz/sources/packages/d/doom1.wad"
  echo ">> installed doom1.wad (shareware)"
fi

echo ">> done. WADs:"
ls -l "$ASSETS/wads"
