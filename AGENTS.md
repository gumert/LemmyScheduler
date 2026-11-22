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
- A failing test is information, not an obstacle. When an existing test fails, first decide whether the code or the test is wrong. If the change you made was meant to alter that behavior, update the test and say to the user after you've completed implementation. If it was not, fix the code. If you cannot tell, stop and ask the user before touching the test. Never weaken, skip or delete a test just to get a green run. Tests marked `// SAFETY TEST:` follow the stricter rule under the posting safety rules.
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

A post moves through `draft`, `scheduled`, `paused`, `posting`, `posted`, `needs_review`, `failed`. Only `draft`, `scheduled` and `paused` are user-editable; the rest are scheduler-owned and read-only to users except through explicit recovery actions. The edit and delete rules are in [docs/architecture.md](docs/architecture.md).

Posting safety rules apply to all scheduling and Lemmy code. The worst failure is spamming Lemmy, so prefer missing a post over duplicating one:

- A post is claimed in the database before Lemmy is called. A post left mid-flight by a crash is never retried automatically; it needs human review.
- Each scheduled post gets one automatic attempt. Claim before publish-time work; only confirmed success with a valid Lemmy post identity becomes `posted`, while every other outcome becomes `needs_review`. Never retry or reschedule automatically, even if failure happened before a request was sent.
- Overdue posts after downtime or failure are never burst-posted. Missing the posting window delays the queue instead.
- A global rate limit and a persisted pause (circuit breaker) apply regardless of schedule.
- Only one poster runs at a time.

Safety tests are specifications. A test that guards one of these rules, or the secret-handling rule below, starts with the comment `// SAFETY TEST:` and a one-line statement of the invariant (details in [docs/testing.md](docs/testing.md)). If one fails, the implementation has drifted: fix the code. Never edit, skip, loosen or delete such a test to get a change through; if you believe the invariant itself is wrong, stop and ask.

Deliberate non-goals, so nobody adds them speculatively: recurring posts, multiple Lemmy accounts, 2FA-protected Lemmy accounts, remote or multi-user hosting, a durable-execution engine such as Temporal, database backups, and persisting Lemmy session tokens (the backend logs in when needed and keeps the token in memory). The Lemmy password is stored in the database in plain text: this is a single-user, locally hosted app, and the API must never return it, and no log line or error message may contain it or a session token.

Add Lemmy API endpoints only when a handoff needs them. Implementation notes for the client, account storage and images are in [docs/architecture.md](docs/architecture.md); API findings are in [docs/lemmy-api-findings.md](docs/lemmy-api-findings.md). Uploaded images go to Lemmy only immediately before posting, never at draft time.

Production deployment (the NAS container) is a planned, separate piece of work; do not couple earlier changes to it.

The API is declared in protobuf under `proto/`. Generated code is committed and produced by `just generate`; `just generate-check` fails on drift. Never hand-edit generated files.

## Database conventions

- Migrations are append-only: never edit one that has been applied; add a new numbered migration. Every `up` needs a `down` (a test enforces this), and down migrations are never run automatically.
- `DATABASE_URL` is required and has no default. Do not add a fallback to localhost, and keep credentials out of the repo.
- Put database-touching code behind small injected seams and test it with hand-written fakes. Queries whose correctness matters may also have real-Postgres tests gated on `TEST_DATABASE_URL`. See [docs/testing.md](docs/testing.md).
- Paginate large lists with keyset pagination, not offset.

## Frontend conventions

- Keep the UI clean and minimal: plain CSS, no UI kit or CSS framework, and no decoration that doesn't help the task.
- Validate input on change, not only on save. Show a specific inline message next to the field and disable the actions that need valid input. Mirror backend rules in a pure helper, but treat the server's response as authoritative and show its messages inline too.
- Make mutually exclusive inputs mutually exclusive in the UI: lock the other input and say why. A Lemmy post shares a URL or a title image, not both.
- Show distinct loading, empty and error states, and put a timeout on every API call.
- Put logic in pure helpers and test them with Vitest (`just frontend-test`); components stay thin.
- Show times in the user's local zone with the zone name; send UTC to the API.
- File layout and other frontend details are in [docs/architecture.md](docs/architecture.md). This file is the single source of agent guidance.

## Further reading

Read these when your task touches the area; they hold implementation details that this file leaves out.

- [docs/architecture.md](docs/architecture.md): post states and edit rules, the Lemmy client and account, images, generated code, database and frontend layout.
- [docs/testing.md](docs/testing.md): fakes, real-Postgres tests, `just tilt-check`, and the `// SAFETY TEST:` convention.
- [docs/lemmy-api-findings.md](docs/lemmy-api-findings.md): observed Lemmy v3 and v4 API behavior.
- [docs/pre-commit-runbook.md](docs/pre-commit-runbook.md): the pre-commit hooks and why they exist.

### Writing docs

- This file holds rules and invariants that apply to every change. `docs/` holds how things are implemented and why, and should be updated in the same PR as the change it describes.
- Add a new file under `docs/` when a topic needs more than a short paragraph, or is only relevant to one area of the code. Prefer extending an existing doc when the topic fits.
- When you add, rename or remove a doc, update the list above in the same PR, with a one-line description of when to read it.
- Don't copy content between this file and `docs/`: state it in one place and link to it.
- `.handoffs/` is planning material for in-flight work, not reference. Move anything durable into `docs/` before a handoff is finished.

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
