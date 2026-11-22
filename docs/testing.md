# Testing notes

How tests are organized, and the details behind the testing rules in [AGENTS.md](../AGENTS.md).

## Seams and fakes

Database-touching code sits behind small injected seams and is tested with hand-written fakes. Fakes cannot validate SQL, so queries whose correctness matters (for example claiming a post for publishing) also have tests that run against a real Postgres. Those are gated on `TEST_DATABASE_URL`, so `just check` still works without a database.

`just tilt-check` also exercises real migrations. It needs ports 8080, 3000 and 5433 free and uses a throwaway Postgres directory.

## Safety tests

A test that guards a safety invariant starts with `// SAFETY TEST:` followed by a one-line statement of the invariant. For a check inside a larger test, put the marker on the block it covers.

Invariants covered by this marker:

- Exclusive publisher ownership, claiming a post with one durable attempt, startup recovery, and status guards on user-facing writes.
- Ambiguous/failed attempts becoming `needs_review` with an atomic persisted pause; stale results cannot overwrite recovery.
- A confirmed post requires a usable Lemmy identity; attempt rows survive deletion of their post.
- URL and title-image exclusivity holds under concurrent writes and is validated by the database migration.
- Account-generation checks prevent stale login results, and transient login failures do not change authentication health.
- Never burst-posting overdue posts.
- Dry-run making no database writes and no Lemmy calls.
- Secret handling: passwords and tokens never leave the backend, and credentials are never forwarded to another host.

Find them with `grep -rn "SAFETY TEST" backend`. When one fails, fix the code. Changing one needs the user's explicit approval, as stated in AGENTS.md.
