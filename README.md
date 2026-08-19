# Tchebet

A pari-mutuel sports-betting prototype. Users bet SIM/NÃO (yes/no) on questions ("Time X vai vencer o Time Y?"), odds move with the pool, and winners split the losing pool minus a 3% house commission.

## Stack

Go + Gin, Templ + Htmx + Alpine.js + Tailwind (standalone CLI, no Node), Postgres, Redis, all orchestrated with `docker-compose.yml`.

## Architecture

Two independent Go services (no shared `go.work`, no shared module):

- **`bet-backend/`** — the pari-mutuel engine. Owns markets, pools, bets, and user balances. Never receives traffic directly from a browser — its docker-compose port isn't published to the host. Talks to Postgres (`tchebet_betting`) and Redis (live odds cache/pub-sub).
- **`general-backend/`** — everything user-facing. Sanitizes and proxies every request to `bet-backend` (defense in depth beyond `bet-backend`'s own validation), and owns accounts/sessions/the whole frontend. Has its own Postgres database (`tchebet_general`, same Postgres server, second logical DB) holding just one table — `accounts`. Markets/bets are never duplicated there; always fetched live from `bet-backend`.

`GET /markets/:id` and a couple of other paths are shared by both a raw JSON API (used by `bet-backend`'s original test suite) and an HTML page — content-negotiated on the `Accept` header rather than living at two different paths.

## Running it

```bash
podman compose up -d
```

Brings up Postgres, Redis, `bet-backend` (internal network only), and `general-backend` on `localhost:8081`. First registered account becomes admin automatically for this prototype — admins can create, lock, resolve, and cancel markets at `/admin/markets`.

`docker-compose.yml` bakes in dev-only Postgres credentials and a `SESSION_SECRET` — fine for local use, both must be overridden before deploying anywhere real.

To rebuild after changing code:

```bash
podman compose build bet-backend general-backend
podman compose up -d
```

## Testing

Each service has its own test suite, and they run differently because `bet-backend`'s port isn't published to the host.

**bet-backend** — its tests call `service.*` directly (no HTTP), so they can run straight from the host against the published Postgres/Redis ports:

```bash
podman compose up -d postgres redis
cd bet-backend && DB_HOST=localhost REDIS_HOST=localhost:6379 go test -v ./tests/...
```

**general-backend** — its tests exercise the real HTTP router against a live `bet-backend`, which means they need to run inside the compose network (a one-shot service gated behind the `test` profile so it never starts on a normal `up`):

```bash
podman compose up -d postgres redis bet-backend
podman compose --profile test build general-backend-tests   # after dependency/Dockerfile changes
podman compose --profile test run --rm general-backend-tests
```

Sanitize-package unit tests have no dependencies and can also run directly: `cd general-backend && go test ./sanitize/...`.

## Local frontend dev (outside Docker)

Needs the `templ` CLI and the Tailwind standalone CLI on `PATH` (both pinned to the versions in `general-backend/Dockerfile`):

```bash
cd general-backend
templ generate
tailwindcss -i ./static/css/input.css -o ./static/css/tailwind.css --minify
```

`bet-backend` has no host-published port, so `general-backend` run outside Docker can't reach it directly — either temporarily publish `bet-backend`'s port for local iteration, or just run the whole stack via compose and rebuild.
