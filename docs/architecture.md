# Architecture notes

Implementation details that [AGENTS.md](../AGENTS.md) deliberately leaves out. The rules and invariants stay in AGENTS.md; this file explains how they are realized. Update it when the implementation changes.

## Post states

A post moves through `draft`, `scheduled`, `paused`, `posting`, `posted`, `needs_review`, `failed`.

| Status | Owner | User may edit | User may delete |
|---|---|---|---|
| `draft`, `scheduled`, `paused` | user | yes | yes |
| `posting` | scheduler | no | no |
| `posted`, `needs_review`, `failed` | scheduler | no | yes |

Scheduler-owned posts are read-only to users except through explicit recovery actions. The rules live in `backend/internal/scheduledposts` (`CanEdit`, `CanDelete`, `CanTransition`, and the bulk eligibility helpers). The frontend mirrors them in `frontend/src/status.ts`; the server is authoritative.

Database statements that change user-facing rows carry a status guard (`WHERE status IN (...)`), so a row the scheduler is claiming can't be changed or deleted by a concurrent user request. See [the 08a handoff](../.handoffs/08a-scheduler-safety-foundation.md).

The backend holds a session-level PostgreSQL advisory lock on a dedicated connection before recovery or serving. Recovery changes every `posting` row to `needs_review`, closes its open attempt, and pauses the queue in one transaction. Claims and finalization use the same lock-holding connection; a lost connection stops the server rather than reconnecting as owner. Claims create one `started` `post_attempts` audit row before any publish-time work. Failure or interruption is never automatically retried.

Migration 0005 makes URL and title image mutually exclusive and adds persistent account health, pause, and generation state. The [README database section](../README.md#local-database) documents the offline legacy-conflict check and deliberate recovery after a dirty migration.

## Lemmy client and account

- The client lives in `backend/internal/lemmyclient`. It supports API v3 (Lemmy 0.19) and v4 (Lemmy 1.0+), uses only the standard library, never follows redirects, and gives every call a timeout.
- Add an endpoint only when a handoff needs it. No generic wrapper and no generated client.
- The session token is held in memory only. The client logs in lazily and, on a 401, logs in again once and retries once.
- The single account is stored in the `lemmy_account` table behind `lemmyaccount.Store`. The password is stored in plain text by design and is write-only: no RPC, log line or error message contains it or a token.
- Each account save increments its generation. Lazy login results update only the generation they started with, and transient failures do not clear a known authentication failure. The persistent queue pause is independent of credential replacement.
- API findings are in [lemmy-api-findings.md](lemmy-api-findings.md).

## Images

User-uploaded images are stored as `bytea` in their own table, capped in size, and kept out of list queries. They are uploaded to Lemmy only immediately before posting, never at draft time. Previews are served by ordinary HTTP routes, not Connect RPCs.

## API and generated code

The API is declared in protobuf under `proto/` and served with Connect-RPC. Generated Go (`backend/internal/gen`) and TypeScript (`frontend/src/gen`) code is committed and produced by `just generate`; `just generate-check` fails on drift.

## Database

- Lists use keyset pagination (a sort key plus `id`, opaque page token), not offset, so they stay fast as history grows.
- Migrations live in `backend/migrations/` and are applied by the backend at startup. A test enforces that every `up` has a `down`.

## Frontend

- Plain CSS in `frontend/app/globals.css`; no UI kit or CSS framework.
- `frontend/src/api/client.ts` is the only module that builds API URLs.
- Logic lives in pure helpers under `frontend/src/` and is tested with Vitest (`just frontend-test`). Components are thin and not unit tested.
- Times are shown in the user's local zone with the zone name; UTC is sent to the API.
- Next.js generates `frontend/AGENTS.md` when the dev server runs. It is disabled with `agentRules: false` in `next.config.ts`, so AGENTS.md at the root stays the single source of agent guidance.
