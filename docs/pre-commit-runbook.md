# Pre-commit Runbook

This repository ships an opt-in Git hook that runs fast local checks before
each commit. It is not enabled automatically because Git does not allow
repositories to configure hooks on a contributor's behalf without an explicit,
one-time opt-in. Run `just install-hooks` once in every clone.

## One-time setup

From inside the Nix development shell:

```sh
just install-hooks
```

This runs `pre-commit install`, which writes a hook into your local
`.git/hooks`, and sets two local git options so history stays linear:

- `pull.rebase true`: `git pull` rebases instead of creating a merge commit.
- `merge.ff only`: `git merge` refuses to create a merge commit.

It must be repeated after a fresh `git clone`. The
[pre-commit](https://pre-commit.com) tool is provided by the Nix development
shell and prints a green `Passed` or red `Failed` line for each check. The
first run downloads the shared hooks from GitHub, so it needs network access.

## What the hook checks

On every `git commit`, the hooks in
[.pre-commit-config.yaml](../.pre-commit-config.yaml) run:

- `no-commit-to-branch` fails when the current branch is `main`. Create a
  branch (`git switch -c my-change`) and open a pull request instead.
- `end-of-file-fixer`, `trailing-whitespace`, `check-merge-conflict`, and
  `detect-private-key` catch common hygiene mistakes and leaked secrets.
- `just format-check` fails if any Go file under `backend` is not
  `gofmt`-formatted. `just format` fixes it.
- `just vet` runs `go vet` on the Go backend.
- `just test` runs the Go backend tests.
- `just build` compiles the Go backend, catching build breaks before
  they reach a PR.
- `just lint` runs ESLint against the frontend.
- `just typecheck` runs the TypeScript compiler against the frontend.
- `just tilt-check` runs `tilt ci`, which boots the backend and frontend
  exactly as `tilt up` does and fails if either fails to build, crashes, or
  never becomes healthy. This catches orchestration bugs (wrong working
  directory, restart loops, bad probes) that unit tests, `go build`, and
  ESLint cannot. It is slow, so the hook only runs it when staged files
  include the `Tiltfile`, `justfile`, `flake.nix`, `backend/`, or frontend
  dependency/config/app files. It needs ports 8080 and 3000 free, so stop any
  running `tilt up` first.

These checks were chosen because they catch the most common review friction
(formatting, compile errors, lint nits, and broken local startup) before a PR
is opened.

## What the hook does NOT check

`just frontend-build` is intentionally left out of the pre-commit hook to keep
commits fast. The full suite, via `just check`, still runs in CI
(see [.github/workflows/ci.yml](../.github/workflows/ci.yml)) and is
required before a PR can merge. CI also runs `just audit`, which fails on
high-severity advisories in production frontend dependencies.

## Bypassing the hook

For local work-in-progress commits, the hook can be skipped with:

```sh
git commit --no-verify
```

CI is not affected by this flag and will still run the full `just check`
suite, so skipped issues will surface there if not fixed beforehand. Local
hooks can always be bypassed, so the server-side rules below are what actually
protect `main`.

## Server-side protection of main

[.github/rulesets/protect-main.json](../.github/rulesets/protect-main.json)
describes a GitHub ruleset for the default branch. It requires a pull request,
requires the `checks` CI job to pass on an up-to-date branch, requires linear
history, allows only the rebase merge method, and blocks force pushes and
deletion. A repository admin applies it once:

```sh
gh api repos/gumert/LemmyScheduler/rulesets --input .github/rulesets/protect-main.json
```

Rulesets are not applied by merging the file, so re-run (or update the ruleset
in the GitHub settings UI) when the JSON changes.

## Updating the hook

Edit [.pre-commit-config.yaml](../.pre-commit-config.yaml) directly. No
reinstall step is required unless the hook itself needs regenerating
(`just install-hooks`). To run all hooks without committing, use
`pre-commit run --all-files`.
