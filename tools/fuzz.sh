#!/usr/bin/env bash
# Runs every fuzz target in the module for TIME each (default 30s) and fails
# if any of them crashes. `just fuzz` and the weekly CI workflow use it.
#
#   bash tools/fuzz.sh 1m
set -euo pipefail
time=${1:-30s}
cd "$(dirname "$0")/.."

# Only packages with fuzz tests get built to list their targets: each prints
# its names, then "ok <package>". A package that does not build stops us here.
# go list gives the test files go test would use, build tags included.
dirs=()
while IFS= read -r dir; do dirs+=("$dir"); done < <(
    go list -f '{{range .TestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.Dir}}/{{.}}{{"\n"}}{{end}}' ./... |
        while IFS= read -r file; do if grep -q '^func Fuzz' "$file"; then dirname "$file"; fi; done | sort -u)
targets=""
if [ ${#dirs[@]} -gt 0 ]; then
    listing=$(go test -list '^Fuzz' "${dirs[@]}")
    targets=$(awk '/^Fuzz/ { names[n++] = $1 } /^ok / { for (i = 0; i < n; i++) print $2, names[i]; n = 0 }' <<<"$listing")
fi
if [ -z "$targets" ]; then
    echo "No fuzz targets."
    if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::warning::No fuzz targets found; nothing was fuzzed"; fi
    exit 0
fi

failed=()
while read -r pkg target; do
    echo "==> $pkg $target"
    go test -run '^$' -fuzz "^${target}\$" -fuzztime "$time" "$pkg" </dev/null || failed+=("$pkg $target")
done <<<"$targets"
if [ ${#failed[@]} -gt 0 ]; then
    printf 'fuzzing failed: %s\n' "${failed[@]}" >&2
    exit 1
fi
