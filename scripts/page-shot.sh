#!/bin/sh
# Screenshots a page of virt-mm serve in headless Chrome, after it has run
# for a while in real time, and prints its JS errors and a value read from
# it: a way to look at the pages without a window.
#
#   scripts/page-shot.sh <url> <out.png> [wait ms (8000)] [JS expression]
#
# SHOT_SIZE=760x900 takes it at another size than 1280x800; SHOT_SCHEME=dark
# shows the page as in a dark desktop (prefers-color-scheme); SHOT_LANG=en-US
# as in a browser in that language; SHOT_DO='js'
# runs js after the wait (a click), and the shot follows SHOT_DO_WAIT ms
# (1500) later; SHOT_KEYS='Tab Tab Enter' presses keys before it (S-Tab
# for Shift+Tab; +Control/-Control press and let go of a modifier).
#
# Needs node and Chrome (the com.google.Chrome Flatpak, or google-chrome /
# chromium on PATH). Starts its own Chrome with a scratch profile and stops
# only that.
set -eu
url=$1 out=$(realpath -m "$2") wait=${3:-8000} expr=${4:-document.title}
dir=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-shot.XXXXXX")
port=$(python3 -I -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
set -- --headless=new --disable-gpu --no-first-run --user-data-dir="$dir/profile" --remote-debugging-port="$port" about:blank
if command -v google-chrome >/dev/null; then
	google-chrome "$@" >"$dir/chrome.log" 2>&1 &
elif command -v chromium >/dev/null; then
	chromium "$@" >"$dir/chrome.log" 2>&1 &
else
	flatpak run --filesystem="$dir" com.google.Chrome "$@" >"$dir/chrome.log" 2>&1 &
fi
chrome=$!
stop() {
	for p in $(pgrep -f "remote-debugging-port=$port" || true); do
		case "$(tr '\0' ' ' </proc/"$p"/cmdline 2>/dev/null)" in *page-shot*|*/bin/sh*) ;; *) kill "$p" 2>/dev/null || true ;; esac
	done
	kill "$chrome" 2>/dev/null || true
	sleep 1
	rm -rf "$dir"
}
trap stop EXIT
i=0
until curl -s "http://127.0.0.1:$port/json/version" >/dev/null; do
	i=$((i + 1)); [ "$i" -gt 60 ] && { cat "$dir/chrome.log"; exit 1; }; sleep 0.5
done
node "$(dirname "$0")/page-shot.mjs" "$port" "$url" "$out" "$wait" "$expr"
