# Shared setup for the vhs demo tapes. Each tape sources it while hidden:
#
#   source docs/tapes/demo-env.sh [name[:flavour] ...]
#
# It starts fakekiwix on a free port, points a throwaway zimon config at it,
# optionally pre-installs some ZIMs, and sets a clean prompt. Nothing touches
# your real config or library, or the live Kiwix service.

DEMO_DIR=$(mktemp -d -t zimon-demo.XXXXXX)
export HOME="$DEMO_DIR/home"   # so paths print as ~/zim/...
export ZIMON_CONFIG="$HOME/.config/zimon/config.toml"
# Plans and budgets show this much free space, whatever disk renders the demo.
export ZIMON_DEMO_FREE_SPACE=32.5GB
mkdir -p "$HOME/.config/zimon"

fakekiwix -addr 127.0.0.1:0 -addr-file "$DEMO_DIR/addr" -quiet &
DEMO_FAKE_PID=$!
trap 'kill $DEMO_FAKE_PID 2>/dev/null; rm -rf "$DEMO_DIR"' EXIT
for _ in $(seq 50); do [ -s "$DEMO_DIR/addr" ] && break; sleep 0.1; done
DEMO_FAKE="http://$(cat "$DEMO_DIR/addr")"

printf '%s\n' \
    "library_dir = \"$HOME/zim\"" \
    "state_dir = \"$HOME/.local/share/zimon\"" \
    'languages = ["eng"]' \
    "catalog_url = \"$DEMO_FAKE/catalog/v2\"" > "$ZIMON_CONFIG"

zimon refresh >/dev/null 2>&1
[ $# -gt 0 ] && zimon add "$@" >/dev/null 2>&1

# Downloads in the recording run at a speed where progress bars are visible.
curl -s -X POST "$DEMO_FAKE/_fake/throttle?rate=${DEMO_THROTTLE:-6MB}"

# bump <name[:flavour]>: publish a newer release on the fake server.
bump() {
    local name=${1%%:*} flavour=
    [[ $1 == *:* ]] && flavour=${1#*:}
    curl -s -X POST "$DEMO_FAKE/_fake/bump?name=$name&flavour=$flavour" >/dev/null
}

cd "$HOME" || exit
PS1=$'\e[1;36m❯\e[0m '
unset PROMPT_COMMAND
clear
