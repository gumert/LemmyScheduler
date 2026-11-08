set shell := ["bash", "-cu"]

check: format-check vet test build lint typecheck frontend-build tilt-check

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
