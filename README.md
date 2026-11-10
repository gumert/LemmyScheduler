# LemmyScheduler

LemmyScheduler is a locally hosted application for preparing content and publishing it to Lemmy at a scheduled time. It is intended for people who have content ready but cannot reliably post during high-engagement windows.

## Stack

- `backend/`: Go HTTP service.
- `frontend/`: Next.js web application using pnpm.
- `Tiltfile`: local orchestration for the two services.
- `flake.nix`: reproducible Nix development shell.
- Deployment target: a container running on a Synology NAS.

## Getting started

Install [Nix](https://nixos.org/download) (with flakes enabled) and [direnv](https://direnv.net/) (`brew install direnv`), then from the repo root:

```sh
direnv allow
tilt up
```

`direnv allow` loads the project's Nix dev shell automatically (see [.envrc](.envrc)). Without direnv, run `nix develop` manually instead before using `tilt`, `just`, `go`, or `pnpm`.

`tilt up` starts PostgreSQL, the backend and the frontend, and opens a local web UI you can use to reach them.

## Local database

`tilt up` runs PostgreSQL 17 from the Nix shell (not a container) on `127.0.0.1:5433`, and creates the `lemmyscheduler` database.

- Connection string: `postgres://lemmyscheduler@127.0.0.1:5433/lemmyscheduler?sslmode=disable`
- Port `5433` avoids clashing with a Postgres already running on the default port `5432`.
- Data is kept in `target/postgres/` (git-ignored). Set `POSTGRES_DATA_DIR` to use another directory.
- Authentication is `trust` and the server listens on loopback only, so there is no password to manage. Do not expose this instance beyond your machine.
- The major version is pinned in [flake.nix](flake.nix). A data directory only opens with the version that created it, so changing the pin needs `pg_upgrade` or a reset.
- To reset the database, stop `tilt up` with Ctrl-C and run `rm -rf target/postgres`.
- `just tilt-check` (part of `just check`) uses a temporary data directory, and needs ports 8080, 3000 and 5433 free.

Then install the git hooks once per clone:

```sh
just install-hooks
```

They run fast local checks on each commit and refuse commits to `main`. See [docs/pre-commit-runbook.md](docs/pre-commit-runbook.md).

## Generating the API

The API is declared in protobuf under `proto/` and served with
[Connect](https://connectrpc.com). Generated Go (`backend/internal/gen`) and
TypeScript (`frontend/src/gen`) code is committed. After changing a `.proto`
file, run `just generate` and commit the result. `just check` (and the
pre-commit hook) runs `just proto-lint` and `just generate-check`, which fail if
the committed code is out of date. The plugins come from the Nix shell, so
generation works offline. Do not hand-edit generated files.

## Configuration

| Variable | Used by | Default | Purpose |
| --- | --- | --- | --- |
| `LISTEN_ADDR` | backend | `:8080` | Address the backend listens on. |
| `CORS_ALLOWED_ORIGIN` | backend | `http://localhost:3000` | The single browser origin allowed to call the API. Only an exact match is echoed back (never `*`), and preflights from other origins get a 403. CORS only controls what browsers expose; it is not authentication. |
| `NEXT_PUBLIC_API_URL` | frontend | `http://localhost:8080` | Base URL of the backend, read only in `frontend/src/api/client.ts`. |

`NEXT_PUBLIC_API_URL` is inlined at build time, so a production build needs it
set when `next build` runs; changing it afterwards has no effect.

`tilt up` sets all three explicitly. If CORS becomes a problem, the planned
fallback is a Next `rewrites()` proxy: point the client base URL at `/api` so
the browser only talks to the frontend's origin.

## Contributing

- Keep pull requests small and focused.
- Never commit to `main`. Work on a branch and open a pull request; `main` only accepts rebase merges. Use `git pull --rebase` and `git rebase main` rather than merge commits (`just install-hooks` configures this).
- Develop inside the Nix shell (`direnv allow` or `nix develop`) — don't rely on globally installed Go, Node.js, pnpm, or Tilt. The Nix shell is the single source of truth for tool versions; `flake.lock` pins them.
- Run `just check` before opening a pull request.
- Add or update tests with behavioral changes.
- Treat generated code as disposable: document its source and avoid hand-editing generated files.

## License

This project is distributed under the GNU General Public License v3. See [LICENSE](LICENSE).
