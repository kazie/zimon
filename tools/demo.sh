#!/usr/bin/env bash
# Renders demo tapes and updates docs/img only with images whose picture
# changed, so a re-render that changes nothing leaves nothing to commit.
# `just demo`, `just demos` and `just demo-check` use it; vhs comes from the
# dev shell.
#
#   bash tools/demo.sh search info    # these tapes
#   bash tools/demo.sh                # every tape in docs/tapes
#   bash tools/demo.sh --check list   # only report; exit 1 if an image changed
#   bash tools/demo.sh --force browse # replace every image of the tape
#
# A "# live: why" comment before an Output or Screenshot marks a picture of
# something in motion (a progress bar, downloads behind the screen): it is
# never compared, and is updated with the rest of its tape, or when missing.
set -euo pipefail
cd "$(dirname "$0")/.."
check=0 force=0
case ${1:-} in
--check) check=1; shift ;;
--force) force=1; shift ;;
esac

mkdir -p .demo/bin
go build -o .demo/bin/zimon .
go build -o .demo/bin/fakekiwix ./tools/fakekiwix
go build -o .demo/bin/demodiff ./tools/demodiff
export PATH="$PWD/.demo/bin:$PATH"

if [ $# -eq 0 ]; then
    for t in docs/tapes/*.tape; do set -- "$@" "$(basename "$t" .tape)"; done
fi

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
outdated=0

for name in "$@"; do
    tape=docs/tapes/$name.tape
    [ -f "$tape" ] || { echo "no such tape: $tape" >&2; exit 2; }
    echo "==> $name"
    # The tape writes docs/img/... relative to where vhs runs: a scratch copy
    # of the layout it expects, so docs/img itself changes only below.
    out=$work/$name
    mkdir -p "$out/docs/img"
    ln -s "$PWD/docs/tapes" "$out/docs/tapes"
    (cd "$out" && vhs -q "docs/tapes/$name.tape")

    # The tape's images, one per line as "path compare" or "path live".
    awk '/^# live:/ { live = 1; next }
        /^#/ || NF == 0 { next }
        $1 == "Output" || $1 == "Screenshot" { gsub(/"/, "", $2); print $2, (live ? "live" : "compare") }
        { live = 0 }' "$tape" >"$out/images"

    changed=$force
    while read -r img kind; do
        if [ "$kind" = live ]; then
            if [ -f "$img" ]; then
                echo "   live       $img (not compared)"
            else
                echo "   new        $img"
                changed=1
            fi
            continue
        fi
        status=0
        why=$(demodiff "$img" "$out/$img") || status=$?
        case $status in
        0) echo "   unchanged  $img" ;;
        1) echo "   changed    $img: $why"; changed=1 ;;
        *) exit "$status" ;;
        esac
    done <"$out/images"
    # A tape's images come from one render, so they are kept together.
    if [ "$changed" = 1 ]; then
        outdated=1
        if [ "$check" = 0 ]; then
            while read -r img _; do cp "$out/$img" "$img"; done <"$out/images"
        fi
    fi
done
if [ "$check" = 1 ] && [ "$outdated" = 1 ]; then
    echo "Some images are out of date: just demo NAME updates them." >&2
    exit 1
fi
