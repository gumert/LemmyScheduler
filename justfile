set shell := ["bash", "-cu"]

# Postgres settings for the db-* recipes. The Tiltfile passes the same values to
# the running stack explicitly. tilt-check overrides POSTGRES_DATA_DIR with a
# throwaway directory.
export POSTGRES_DATA_DIR := env('POSTGRES_DATA_DIR', 'target/postgres')
export POSTGRES_PORT := env('POSTGRES_PORT', '5433')

check: proto-lint generate-check format-check vet test build lint typecheck frontend-build tilt-check

# Regenerate the committed Go and TypeScript API code from proto/.
generate:
    buf generate

proto-lint:
    buf lint

# Regenerate into a temp dir and compare with the committed output. Never
# writes into the working tree. Uses diff -r (not git diff) so untracked and
# stale files are caught too.
[script("bash")]
generate-check:
    set -euo pipefail
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' EXIT
    buf generate --output "$tmp"
    status=0
    for dir in backend/internal/gen frontend/src/gen; do
        mkdir -p "$tmp/$dir"
        diff -r "$tmp/$dir" "$dir" || status=1
    done
    if [ "$status" -ne 0 ]; then
        echo "Generated API code is out of date (run 'just generate' and commit the result)." >&2
        exit 1
    fi

# Smoke test: boot all services exactly as `tilt up` does and fail if any
# resource errors or crashes, or never becomes healthy. The Tiltfile installs
# frontend dependencies itself. Needs ports 8080, 3000 and 5433 free, so stop any
# running `tilt up` first. Postgres uses a throwaway data directory, so this
# never reads, writes, or corrupts a developer's target/postgres.
[script("bash")]
tilt-check:
    set -euo pipefail
    POSTGRES_DATA_DIR="$(mktemp -d)"
    export POSTGRES_DATA_DIR
    trap 'rm -rf "$POSTGRES_DATA_DIR"' EXIT
    tilt ci --port 0 --timeout 180s

# Local Postgres. db-init (one-time initdb) and db-serve (the server) are split
# so Tilt can run them as `cmd` and `serve_cmd`. db-create makes the application
# database. Every db-* recipe runs db-guard first.
[private]
[script("bash")]
db-guard:
    set -euo pipefail
    case "$POSTGRES_DATA_DIR" in
        ""|/|//)
            echo "POSTGRES_DATA_DIR must name a directory other than / (got '$POSTGRES_DATA_DIR')." >&2
            exit 1
            ;;
    esac
    if [ "$(id -u)" -eq 0 ]; then
        echo "Refusing to run Postgres as root; run as a normal user." >&2
        exit 1
    fi

# Initialises the data directory once. A no-op if it already exists.
[script("bash")]
db-init: db-guard
    set -euo pipefail
    if [ ! -f "$POSTGRES_DATA_DIR/PG_VERSION" ]; then
        initdb --username=lemmyscheduler --auth=trust --encoding=UTF8 --pgdata="$POSTGRES_DATA_DIR"
    fi

# Serves Postgres on loopback only. exec replaces this shell so Tilt's SIGTERM
# reaches postgres. The unix socket is disabled because /run/postgresql is not
# writable under Nix.
[script("bash")]
db-serve: db-guard
    set -euo pipefail
    exec postgres -D "$POSTGRES_DATA_DIR" -p "$POSTGRES_PORT" -c listen_addresses=127.0.0.1 -c unix_socket_directories=''

# Creates the lemmyscheduler database if it is missing. Postgres has no
# CREATE DATABASE IF NOT EXISTS, so check first.
[script("bash")]
db-create: db-guard
    set -euo pipefail
    psql_admin() { psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -p "$POSTGRES_PORT" -U lemmyscheduler -d postgres "$@"; }
    exists="$(psql_admin -tAc "SELECT 1 FROM pg_database WHERE datname = 'lemmyscheduler'")"
    if [ "$exists" != "1" ]; then
        psql_admin -c "CREATE DATABASE lemmyscheduler"
    fi

format:
    gofmt -w backend

format-check:
    unformatted="$(gofmt -l backend)"; test -z "$unformatted" || { echo "gofmt needed (run 'just format'):"; echo "$unformatted"; exit 1; }

vet:
    go -C backend vet ./...

test:
    go -C backend test ./...

build:
    go -C backend build -o /dev/null ./...

frontend-install:
    pnpm --dir frontend install --frozen-lockfile

lint: frontend-install
    pnpm --dir frontend lint

typecheck: frontend-install
    pnpm --dir frontend typecheck

frontend-build: frontend-install
    pnpm --dir frontend build

# Needs network access and changes as new advisories are published, so it is
# not part of `check`; CI runs it as its own step.
audit: frontend-install
    pnpm --dir frontend audit --prod --audit-level high

# One-time setup per clone: install the git hooks and make git rebase instead
# of creating merge commits.
install-hooks:
    pre-commit install
    git config --local pull.rebase true
    git config --local merge.ff only
