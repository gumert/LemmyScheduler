# Ports are defined once so that the services and their readiness probes can
# never disagree.
BACKEND_PORT = 8080
FRONTEND_PORT = 3000
# Not 5432, so this does not collide with a Postgres already running on the
# developer's machine.
POSTGRES_PORT = 5433

# Postgres runs from the Nix shell as a plain process, not a container. Its data
# lives in POSTGRES_DATA_DIR (default target/postgres). Values are passed
# explicitly so the running stack, the readiness probe and the just recipes agree.
postgres_env = {
    'POSTGRES_DATA_DIR': os.getenv('POSTGRES_DATA_DIR', 'target/postgres'),
    'POSTGRES_PORT': str(POSTGRES_PORT),
}

# Initialises the data directory once, serves Postgres, and is ready once it
# accepts connections. Dependents wait on the probe, because Tilt does not
# restart a failed serve_cmd.
local_resource(
    'postgres',
    cmd='just db-init',
    env=postgres_env,
    serve_cmd='just db-serve',
    serve_env=postgres_env,
    readiness_probe=probe(
        period_secs=2,
        # Explicit role and database: the defaults are the OS user, which has no
        # role, and a database that does not exist yet, so Postgres logs FATAL on
        # every probe.
        exec=exec_action(['pg_isready', '-h', '127.0.0.1', '-p', str(POSTGRES_PORT), '-U', 'lemmyscheduler', '-d', 'postgres']),
    ),
)

# Creates the application database once Postgres is ready. Idempotent, so it runs
# on every `tilt up`.
local_resource(
    'postgres-db',
    cmd='just db-create',
    env=postgres_env,
    resource_deps=['postgres'],
)

# Tilt runs commands from the repository root, but the Go module lives in
# backend/, so the backend must be started with `go -C backend`. LISTEN_ADDR is
# set explicitly so a value exported in a developer's shell cannot move the
# backend away from the port the readiness probe checks. CORS_ALLOWED_ORIGIN is
# the frontend's origin, built from the same port constant as its probe.
local_resource(
    'backend',
    serve_cmd='go -C backend run ./cmd/server',
    serve_env={
        'LISTEN_ADDR': ':%d' % BACKEND_PORT,
        'CORS_ALLOWED_ORIGIN': 'http://localhost:%d' % FRONTEND_PORT,
    },
    deps=['backend'],
    # The backend starts only once the database exists.
    resource_deps=['postgres-db'],
    readiness_probe=probe(
        period_secs=2,
        http_get=http_get_action(port=BACKEND_PORT, path='/healthz'),
    ),
)

# `next dev` writes to frontend/.next (and may rewrite tsconfig.json and
# next-env.d.ts) while it runs. Tilt does not read .gitignore, so watching the
# whole frontend/ directory restarts the dev server every time it starts: a
# crash loop. Next.js handles source changes itself (HMR), so only the
# dependency manifests are watched; a change re-installs and restarts.
# The install command lives in the justfile so there is one definition of it.
# NEXT_PUBLIC_API_URL points the browser at the backend; it must match the
# backend's port.
local_resource(
    'frontend',
    cmd='just frontend-install',
    serve_cmd='pnpm --dir frontend dev --port %d' % FRONTEND_PORT,
    serve_env={'NEXT_PUBLIC_API_URL': 'http://localhost:%d' % BACKEND_PORT},
    deps=['frontend/package.json', 'frontend/pnpm-lock.yaml'],
    links=[link('http://localhost:%d' % FRONTEND_PORT, 'Frontend')],
    readiness_probe=probe(
        period_secs=2,
        http_get=http_get_action(port=FRONTEND_PORT, path='/'),
    ),
)
