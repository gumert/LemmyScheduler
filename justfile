set shell := ["bash", "-cu"]

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

# Smoke test: boot both services exactly as `tilt up` does and fail if any
# resource errors or crashes, or never becomes healthy. The Tiltfile installs
# frontend dependencies itself. Needs ports 8080 and 3000 free, so stop any
# running `tilt up` first.
tilt-check:
    tilt ci --port 0 --timeout 180s

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
