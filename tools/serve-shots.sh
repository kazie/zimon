#!/usr/bin/env bash
# Checks the admin page and takes the README's screenshots of zimon serve
# (docs/img/serve-*.png), in headless Chromium, on a demo library from the
# fake Kiwix (docs/tapes/demo-env.sh) with a little job history: one add that
# worked, one that failed. See tools/serve-shots.mjs for what it checks.
# Needs chromium and node. The build stamps no version, so the footer says
# "zimon dev" and the images only change when the pages do.
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
# Chromium's helpers may still be writing its profile for a moment after it exits.
trap 'kill $serve $browser $DEMO_FAKE_PID 2>/dev/null; wait $serve $browser 2>/dev/null
      rm -rf "$DEMO_DIR" "$bin" 2>/dev/null || { sleep 1; rm -rf "$DEMO_DIR" "$bin"; }' EXIT
for _ in $(seq 100); do
    curl -sf -o /dev/null http://127.0.0.1:18780/ && curl -sf -o /dev/null http://127.0.0.1:18790/json/version && break
    sleep 0.1
done

# Job history for the Overview and the Jobs view.
post() { curl -sf -o /dev/null -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$2" "http://127.0.0.1:18780/api/v1/$1"; }
post add '{"keys":["devdocs_en_python"]}'
# Nothing listens on port 9 (discard): the job fails, with the same message every run.
post add '{"sources":[{"from":"http://127.0.0.1:9/gone.zim","name":"gone_en_all"}]}'
busy() { curl -sf -H "Authorization: Bearer $token" http://127.0.0.1:18780/api/v1/jobs | grep -qE '"state":"(queued|running)"'; }
for _ in $(seq 300); do busy || break; sleep 0.2; done
if busy; then echo "the demo jobs didn't finish in a minute" >&2; exit 1; fi

cd "$repo"
node tools/serve-shots.mjs 18790 http://127.0.0.1:18780/ http://127.0.0.1:18781 "$token"
