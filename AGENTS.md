# Repository guidance

## Project shape

LemmyScheduler is a locally hosted, self-hosted hobby application for preparing content and publishing it to Lemmy on a schedule. It has a Go backend (Connect-RPC over PostgreSQL), a Next.js frontend, Nix development tooling, and Tilt orchestration. The eventual deployment target is a container on a Synology NAS.

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

## Product direction

The frontend is an inbox-style list of scheduled posts: open one to edit it, create new ones, and apply bulk actions (delete, pause, delay). The backend stores posts in PostgreSQL and publishes them to a single Lemmy account at their scheduled time. Lemmy login, Lemmy API calls, scheduling and retries are in scope; build them incrementally, one focused PR at a time.

A post moves through `draft`, `scheduled`, `paused`, `posting`, `posted`, `needs_review`, `failed`. Only `draft`, `scheduled` and `paused` are user-editable; the rest are scheduler-owned and read-only to users except through explicit recovery actions.

Posting safety rules apply to all scheduling and Lemmy code. The worst failure is spamming Lemmy, so prefer missing a post over duplicating one:

- A post is claimed in the database before Lemmy is called. A post left mid-flight by a crash is never retried automatically; it needs human review.
- Overdue posts after downtime or failure are never burst-posted. Missing the posting window delays the queue instead.
- A global rate limit and a persisted pause (circuit breaker) apply regardless of schedule.
- Only one poster runs at a time.

Deliberate non-goals, so nobody adds them speculatively: recurring posts, multiple Lemmy accounts, 2FA-protected Lemmy accounts, remote or multi-user hosting, a durable-execution engine such as Temporal, database backups, and persisting Lemmy session tokens (the backend logs in when needed and keeps the token in memory). The Lemmy password is stored in the database in plain text: this is a single-user, locally hosted app, and the API must never return it, and no log line or error message may contain it or a session token.

The Lemmy API client lives in `backend/internal/lemmyclient`: API v3 (Lemmy 0.19) and v4 (Lemmy 1.0+), standard library only, never follows redirects, and every call has a timeout. Add an endpoint only when a handoff needs it. The single account is stored in the `lemmy_account` table behind `lemmyaccount.Store`. API findings are in [docs/lemmy-api-findings.md](docs/lemmy-api-findings.md).

User-uploaded images are stored as `bytea` in their own table, capped in size, and kept out of list queries. They are uploaded to Lemmy only immediately before posting, never at draft time.

Production deployment (the NAS container) is a planned, separate piece of work; do not couple earlier changes to it.

The API is declared in protobuf under `proto/` and served with Connect-RPC. Generated Go (`backend/internal/gen`) and TypeScript (`frontend/src/gen`) code is committed and produced by `just generate`; `just generate-check` fails on drift. Never hand-edit generated files.

## Database conventions

- Migrations are append-only: never edit one that has been applied; add a new numbered migration. Every `up` needs a `down` (a test enforces this), and down migrations are never run automatically.
- `DATABASE_URL` is required and has no default. Do not add a fallback to localhost, and keep credentials out of the repo.
- Put database-touching code behind small injected seams and test it with hand-written fakes. Fakes cannot validate SQL, so queries whose correctness matters (for example claiming a post for publishing) may also have tests that run against a real Postgres. Gate those on `TEST_DATABASE_URL` so `just check` still works without a database. `just tilt-check` also exercises real migrations and needs ports 8080, 3000 and 5433 free; it uses a throwaway Postgres directory.
- Paginate large lists with keyset pagination (a sort key plus `id`, opaque page token), not offset, so lists stay fast as history grows.

## Frontend conventions

- Keep the UI clean and minimal: plain CSS in `frontend/app/globals.css`, no UI kit or CSS framework, and no decoration that doesn't help the task.
- Validate input on change, not only on save. Show a specific inline message next to the field and disable the actions that need valid input. Mirror backend rules in a pure helper, but treat the server's response as authoritative and show its messages inline too.
- Make mutually exclusive inputs mutually exclusive in the UI: lock the other input and say why. A Lemmy post shares a URL or a title image, not both.
- Show distinct loading, empty and error states, and put a timeout on every API call.
- Put logic in pure helpers under `frontend/src/` and test them with Vitest (`just frontend-test`). Components are thin and are not unit tested.
- `frontend/src/api/client.ts` is the only module that builds API URLs.
- Show times in the user's local zone with the zone name; send UTC to the API.
- Next.js generates `frontend/AGENTS.md` when the dev server runs. It is disabled with `agentRules: false` in `next.config.ts`; this file is the single source of agent guidance.

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
