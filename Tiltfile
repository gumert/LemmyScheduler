# Ports are defined once so that the services and their readiness probes can
# never disagree.
BACKEND_PORT = 8080
FRONTEND_PORT = 3000

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
