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

`tilt up` starts the backend and frontend and opens a local web UI you can use to reach both.

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

## Contributing

- Keep pull requests small and focused.
- Never commit to `main`. Work on a branch and open a pull request; `main` only accepts rebase merges. Use `git pull --rebase` and `git rebase main` rather than merge commits (`just install-hooks` configures this).
- Develop inside the Nix shell (`direnv allow` or `nix develop`) — don't rely on globally installed Go, Node.js, pnpm, or Tilt. The Nix shell is the single source of truth for tool versions; `flake.lock` pins them.
- Run `just check` before opening a pull request.
- Add or update tests with behavioral changes.
- Treat generated code as disposable: document its source and avoid hand-editing generated files.

## License

This project is distributed under the GNU General Public License v3. See [LICENSE](LICENSE).
