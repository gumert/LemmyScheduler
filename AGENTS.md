# Repository guidance

## Project shape

LemmyScheduler is a locally hosted application for preparing and publishing content to Lemmy on a schedule. The repository currently contains a small Go backend, a Next.js frontend, Nix development tooling, and Tilt orchestration.

## Working agreements

- Keep pull requests small and focused; avoid mixing product behavior with infrastructure changes.
- Use the reproducible Nix development shell for repository tooling. Do not require globally installed Go, Node.js, pnpm, or Tilt.
- The Nix shell (`flake.nix` and `flake.lock`) is the single source of truth for Go, Node.js, and pnpm versions. Do not add `packageManager` or `engines` fields; keep the `go` directive in `backend/go.mod` at or below the Nix-provided Go version.
- Never commit directly to `main`. Work on a branch, keep history linear, and rebase rather than creating merge commits.
- Keep secrets, tokens, and machine-specific paths out of source control.
- Store timestamps in UTC and make timezone behavior explicit when scheduling features are introduced.
- Prefer standard-library Go code until an external dependency has a clear benefit.
- Add or update tests with behavioral changes.
- Run formatting, tests, and builds before opening a pull request.
- Treat generated code as disposable: document its source and avoid hand-editing generated files.

## Current scope

The initial skeleton intentionally does not implement Lemmy authentication, Lemmy API calls, persistence, scheduling, retries, or production deployment. Preserve that boundary unless a change explicitly expands the scope.

## Validation

From the repository root:

```sh
nix develop
just check
```

To run the local services together:

```sh
tilt up
```
