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

## Guiding principles

These apply to every change, and to every contributor, human or AI agent.

1. **Make the repo easy for humans to reason about.**
   - Prefer small, explicit, boring solutions over clever or implicit ones.
   - Keep one source of truth per concern (for example, the Nix shell for tool versions) and link to it rather than duplicating it.
   - Document the "why" for non-obvious decisions, such as in [docs/pre-commit-runbook.md](docs/pre-commit-runbook.md).
2. **Protect developers of all species from themselves.**
   - Assume humans and agents will make mistakes, so enforce rules with tooling instead of memory or goodwill.
   - Use layered guardrails: local pre-commit hooks for fast feedback, CI as the full check, and the GitHub ruleset in [.github/rulesets/protect-main.json](.github/rulesets/protect-main.json) as the server-side backstop, since local hooks can be bypassed.
   - Prefer failing loudly and early over silently doing the wrong thing.
   - Do not bypass a guardrail (`--no-verify`, force pushes, weakening a check) to get a change through. Fix the cause or ask.
   - Keep secrets and destructive actions behind explicit, deliberate steps.

## Current scope

The initial skeleton intentionally does not implement Lemmy authentication, Lemmy API calls, persistence, scheduling, retries, or production deployment. Preserve that boundary unless a change explicitly expands the scope.

An RPC layer is being added: the API is declared in protobuf under `proto/` and served with Connect-RPC. Generated Go (`backend/internal/gen`) and TypeScript (`frontend/src/gen`) code is committed and produced by `just generate`; `just generate-check` fails on drift. Never hand-edit generated files.

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
