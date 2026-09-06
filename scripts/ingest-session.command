#!/bin/sh
# One-shot ingest session: make sure the dedicated debug Chrome is up with the
# game page open, then run `mjcap ingest` in this terminal. Open replays +
# MAKA, Ctrl-C to finish. Double-clickable from Finder/Dock (.command opens in
# Terminal) and equally usable from a hotkey runner (Shortcuts.app, Raycast).
#
# Opening the game URL here is the user's own browser launch; mjcap itself
# still only observes and never navigates via CDP.
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE="$HOME/.local/share/mjcap/chrome-profile"
GAME_URL="https://game.mahjongsoul.com/"

has_game_tab() {
  curl -s --max-time 2 http://127.0.0.1:9222/json/list 2>/dev/null | grep -q '"type": *"page".*mahjongsoul\|mahjongsoul' 
}
endpoint_up() {
  curl -s --max-time 2 http://127.0.0.1:9222/json/version >/dev/null 2>&1
}

if ! endpoint_up || ! has_game_tab; then
  echo "opening dedicated Chrome with the game page..."
  "$CHROME" \
    --remote-debugging-address=127.0.0.1 \
    --remote-debugging-port=9222 \
    --user-data-dir="$PROFILE" \
    "$GAME_URL" >/dev/null 2>&1 &
  for _ in $(seq 1 150); do
    endpoint_up && has_game_tab && break
    sleep 0.2
  done
fi

cd "$ROOT"
if [ ! -x bin/mjcap ]; then
  echo "bin/mjcap missing; run make build first" >&2
  exit 1
fi
exec ./bin/mjcap ingest "$@"
