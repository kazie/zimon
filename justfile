# Shortcuts for zimon development. Run `just` to list them.
# Each recipe is plain commands you can also run yourself; for everyday work
# `go test ./...`, `go run .` and `go build` are all you need. The demo and
# release recipes use vhs and goreleaser from the dev shell (`direnv allow`).

default:
    @just --list

# Run the tests with the race detector
test:
    GORACE="atexit_sleep_ms=0 ${GORACE:-}" go test -race ./...

# Lint and vet
lint:
    go vet ./...
    golangci-lint run

# Coverage per package, counting code exercised by any package's tests
cover:
    go test -coverpkg=./internal/... -coverprofile=cover.out ./...
    go run ./tools/coversummary cover.out

# Generate the store's Go code from its SQL (sqlc from the dev shell)
generate:
    cd internal/store && sqlc generate

# Pin kiwix-serve for zimon serve to fetch: the Containerfile's release, from Kiwix (network, ~60 MB)
pin-kiwix:
    go run ./tools/kiwixpin

# Run the benchmarks
bench:
    go test -run '^$' -bench . -benchmem ./...

# Run every fuzz target for a while, e.g. `just fuzz 5m` (default $FUZZTIME or 30s)
fuzz time=env_var_or_default("FUZZTIME", "30s"):
    bash tools/fuzz.sh {{time}}

# Check dependencies and code for known vulnerabilities (govulncheck from the dev shell)
vuln:
    govulncheck ./...

# Format Go and Nix files
fmt:
    gofmt -w .
    nix fmt flake.nix

# Check every built-in collection item against the live Kiwix catalog
check-collections:
    go run ./tools/checkcollections

# Run the fake Kiwix server used by the demo tapes
fake addr="127.0.0.1:8899" throttle="6MB":
    go run ./tools/fakekiwix -addr {{addr}} -throttle {{throttle}}

# Render demo tapes into docs/img, e.g. `just demo search info`; only changed images are replaced
demo +names:
    bash tools/demo.sh {{names}}

# Render every demo tape; only changed images are replaced
demos:
    bash tools/demo.sh

# Retake the screenshots of zimon serve (docs/img/serve-*.png) on the demo library (chromium and node)
serve-shots:
    bash tools/serve-shots.sh

# Report which demo images are out of date, without touching docs/img (all tapes, or the ones named)
demo-check *names:
    bash tools/demo.sh --check {{names}}

# Check the goreleaser config and build a snapshot locally
release-check:
    goreleaser check
    goreleaser release --snapshot --clean --skip=publish
