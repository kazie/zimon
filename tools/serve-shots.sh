#!/usr/bin/env bash
# Takes the README's screenshots of zimon serve (docs/img/serve-*.png): the
# start page, the admin page and its Catalog tab, in headless Chromium, on a
# demo library from the fake Kiwix (docs/tapes/demo-env.sh). Needs chromium
# and node. The build stamps no version, so the footer says "zimon dev" and
# the images only change when the pages do.
set -euo pipefail
cd "$(dirname "$0")/.."
repo=$PWD
bin=$(mktemp -d -t zimon-shots.XXXXXX)
go build -buildvcs=false -o "$bin/zimon" .
go build -o "$bin/fakekiwix" ./tools/fakekiwix
export PATH="$bin:$PATH"

DEMO_THROTTLE=0
# shellcheck source=../docs/tapes/demo-env.sh
source docs/tapes/demo-env.sh >/dev/null
zimon add @anime-manga @japan @linux --level light >/dev/null 2>&1
token=$(zimon token add shots 2>/dev/null | grep -o 'zimon_[A-Za-z0-9_-]*')

zimon serve --port 18780 >"$DEMO_DIR/serve.log" 2>&1 &
serve=$!
chromium --headless=new --disable-gpu --no-sandbox --hide-scrollbars --user-data-dir="$DEMO_DIR/chromium" \
    --remote-debugging-port=18790 about:blank >/dev/null 2>&1 &
browser=$!
trap 'kill $serve $browser $DEMO_FAKE_PID 2>/dev/null; rm -rf "$DEMO_DIR" "$bin"' EXIT
for _ in $(seq 100); do
    curl -sf -o /dev/null http://127.0.0.1:18780/ && curl -sf -o /dev/null http://127.0.0.1:18790/json/version && break
    sleep 0.1
done

cd "$repo"
node tools/serve-shots.mjs 18790 http://127.0.0.1:18780/ http://127.0.0.1:18781 "$token"
