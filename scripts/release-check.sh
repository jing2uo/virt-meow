#!/bin/sh
# Builds virt-mm from what is committed (git archive HEAD, not the
# worktree), runs it under test/dev-serve.sh (from the same archive) and
# looks at the machine list, a machine's page and its VNC console (and its
# keyboard grab, by real keys) with
# page-shot: it fails on a JS error, a file the page asks for and doesn't
# get, or a view that didn't draw. A file left out of git (noVNC's pako
# once was) shows here and nowhere else.
#
#   scripts/release-check.sh
#
# RELEASE_CHECK_DROP='path …' removes paths from the archive first, to see
# the check fail. RELEASE_CHECK_REV checks another commit or tree than HEAD
# (RELEASE_CHECK_REV=$(git write-tree): what is staged).
set -eu
cd "$(dirname "$0")/.."
work=$(mktemp -d "${TMPDIR:-/tmp}/virt-mm-release.XXXXXX")
dev=
cleanup() {
	# dev-serve stops its serve and removes its container and files on TERM.
	if [ -n "$dev" ]; then
		kill -TERM "$dev" 2>/dev/null || true
		wait "$dev" 2>/dev/null || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

git archive --format=tar "${RELEASE_CHECK_REV:-HEAD}" | tar -x -C "$work" --one-top-level=src
for p in ${RELEASE_CHECK_DROP:-}; do rm -rf "$work/src/$p"; done
port=$(python3 -I -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
"$work/src/test/dev-serve.sh" "$port" >"$work/dev.log" 2>&1 &
dev=$!
i=0
until curl -fs "http://127.0.0.1:$port/" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 300 ] || ! kill -0 "$dev" 2>/dev/null; then
		cat "$work/dev.log" >&2
		echo "release-check: virt-mm serve from the archive did not come up" >&2
		exit 1
	fi
	sleep 0.5
done

# Each view: its hash, and an expression that is "ok" once it drew.
fail=0
check() {
	name=$1 hash=$2 expr=$3
	out=$(SHOT_SIZE=1280x800 SHOT_DO="${DO:-}" SHOT_KEYS="${KEYS:-}" "$work/src/scripts/page-shot.sh" "http://127.0.0.1:$port/$hash" "$work/$name.png" 8000 "$expr" 2>&1) || true
	# rpc.js asks for Wails' runtime to know whether it is in the window;
	# under serve that is a 404 by design.
	bad=$(printf '%s\n' "$out" | grep -E '^(exception|console\.error|http [45][0-9][0-9]|load failed|log\.error):' | grep -v '/wails/runtime.js' || true)
	if [ -n "$bad" ] || ! printf '%s\n' "$out" | grep -q '^value: "ok"$'; then
		echo "release-check: $name ($hash):" >&2
		printf '%s\n' "$out" | sed 's/^/  /' >&2
		fail=1
	else
		echo "release-check: $name ok"
	fi
}
check list "" 'document.querySelector("table.machines tr.open") ? "ok" : document.querySelector("#main").textContent.slice(0, 200)'
check machine "#machine/web01" 'document.querySelector("#m-power") && document.querySelector("#m-body .card") ? "ok" : document.querySelector("#main").textContent.slice(0, 200)'
check console "#console/web01/vnc" 'document.querySelector("#screen canvas") && /^(已连接|Connected)$/.test(document.querySelector("#con-status").textContent) ? "ok" : document.querySelector("#con-status")?.textContent'
# The keyboard grab, with real keys: on (the browser's Keyboard Lock, in
# full screen), not let go by Ctrl+Alt+Tab, let go by Ctrl+Alt alone.
grabbed='document.querySelector("#con-grab")?.getAttribute("aria-pressed") === "true" && document.fullscreenElement'
DO='document.querySelector("#con-grab").click()' KEYS='+Control +Alt Tab -Alt -Control' \
	check grab "#console/web01/vnc" "$grabbed ? \"ok\" : \"not grabbed\""
DO="(async () => { document.querySelector('#con-grab').click(); await new Promise((r) => setTimeout(r, 1000)); window.was = !!($grabbed); })()" \
	KEYS='+Control +Alt -Alt -Control' \
	check ungrab "#console/web01/vnc" "window.was && !($grabbed) ? \"ok\" : \"grabbed before: \" + window.was"
exit $fail
