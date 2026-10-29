# AGENTS.md

Guidance for AI coding agents working on the Zedu backend. Human-facing workflow lives in
[`CONTRIBUTING.md`](./CONTRIBUTING.md); detailed engineering standards live in [`AGENT.md`](./AGENT.md).
Where they conflict, this file wins for agent behaviour, and an explicit user instruction overrides everything.

## Project overview

Zedu backend (`telex_be`, module `github.com/hngprojects/telex_be`) is a Go 1.24 / Gin service. It is a
layered monolith backed by PostgreSQL (GORM), Redis, MongoDB, MinIO, Elasticsearch, RabbitMQ,
Firebase/Centrifugo and Stripe.

Requests flow through a fixed path:

```
pkg/router  ->  pkg/controller/<feature>  ->  services/<feature>  ->  pkg/repository/storage
```

## Repository layout

- `main.go` — boot sequence: config, storage connections, queues, cron jobs, migrations, router.
- `pkg/controller/<feature>/` — Gin handlers: bind, validate, call the service, build the response.
- `services/<feature>/` — business logic. One package per feature (63+ exist).
- `internal/models/` — domain structs, DTOs and schema mappings. Migrations in `internal/models/migrations/`.
- `pkg/repository/storage/` — storage adapters: `postgresql/`, `elastic/`, `redis/`, `minio/`, `mongodb/`, `typesense/`.
- `internal/config/` — env loading (`env.go`), Viper setup, `BaseConfig`.
- `utility/` — logger, response builders, generic helpers.
- `tests/test_<feature>/` — **all** tests. Never place tests inside `internal/` or `pkg/`.
- `cronjobs/`, `migrations/`, `scripts/`, `static/swagger.yaml`.

## Build, run and test

```sh
cp app-sample.env app.env          # first time only; app.env is gitignored
docker compose -f docker-compose.dev.yml up --build   # full local stack (make start-dev)
go run .                           # run against services in app.env
go build ./...                     # compile check
go vet ./...
go test ./... -p 1 -timeout 600s   # -p 1 avoids GORM auto-migrate deadlocks
gofmt -l . && goimports -l .       # formatting check
```

Database migrations (uses `DB_URL` from `app.env`):

```sh
make migrate-safe-up    # fix dirty state then apply
make migrate-up
make migrate-down
```

Before finishing any task, run `go build ./...` and `go test ./tests/... -p 1 -timeout 600s` and fix
failures. Do not report success on a red build.

## Architecture rules

- Add a feature across all layers: `pkg/controller/<feature>`, `services/<feature>`, models in
  `internal/models/`, and register routes in `pkg/router`.
- Use the PostgreSQL wrappers in `pkg/repository/storage/postgresql` (`CheckExists`, `SelectOneFromDb`,
  `CreateOneRecord`, `UpdateFields`, `DeleteRecordFromDb`) instead of ad-hoc queries.
- Use the Elastic wrappers and reference `models.ThreadIndexName` / `models.MessageIndexName`
  (prefixed by `ELASTIC_INDEX_PREFIX`); do not hardcode index names.

## Organisation ID (`org_id`) rule

- The active organisation for an authenticated request **must** come from the signed JWT claims —
  never the URL path parameter. Read it via `userClaims["org_id"]` (`common.GetAllUserClaims(c)` /
  `middleware.GetUserClaims(c, db, "org_id")`); do **not** source it with `c.Param("org_id")`.
- The claim is authoritative; the path value is client-controlled and can disagree with the token.
  Where a route still carries `:org_id`, a mismatched value must be rejected (404), not acted upon.
- Exceptions: middleware that deliberately validates the path organisation against the token
  (`pkg/middleware/permission.go`), and unauthenticated routes with no claims.
- Enforced in CI: a PR that adds `c.Param("org_id")` under `pkg/controller/` fails the
  `org_id convention` workflow (`.github/workflows/org-id-convention.yml`).

## Code style

- Idiomatic, `gofmt`-clean Go. No comments that restate the code, and **never** numbered comments
  (`// 1. Parse payload`). Comment only non-obvious business rules or edge cases.
- Functions take **at most 3 positional parameters**; group more into a param/options struct
  (e.g. `models.IDS`, feature request DTOs).
- Handlers bind with `c.ShouldBindJSON`, validate with `base.Validator.Struct`, and must respond via
  `utility.BuildSuccessResponse` / `utility.BuildErrorResponse` with standard status codes.
- Read auth claims from middleware context (`userClaims` / `middleware.GetUserClaims`).
- Never concatenate raw SQL; always use GORM parameterized clauses.
- Never hardcode secrets, URLs, ports or keys. Register new settings in `internal/config/env.go`
  (`BaseConfig`) and document them in `app-sample.env`.

## Testing

- Every test file lives in `tests/test_<feature>/` with package `test_<feature>`
  (e.g. `package test_message`). Centralised by design — do not add tests elsewhere.
- Add or update tests for any behaviour you change, even when not explicitly asked.
- Run the suite with `-p 1` as shown above; many packages share database fixtures.

## Bootcamp workflow

- Integration branch is `dev`. Promotion branch is `central-staging`. **Never push directly** to either —
  open a PR.
- Branch name: `<type>/<TICKET-ID>-<short-desc>` (e.g. `feat/ZED-142-channel-pins`).
- Conventional commits only: `type(scope): description` (e.g. `fix: guard nil thread on DM list`).
- Teams may open a batch/consolidated PR from their fork that combines the team's work.
- Fill the PR template completely — the ticket ID is mandatory.
- Database changes follow **expand → contract**: ship additive migrations first, remove later.
- The following files are **protected** and need lead approval before changing:
  `.github/`, `Dockerfile`, `Dockerfile.dev`, `docker-compose*.yml`, `Makefile`, `.air.toml`,
  `AGENTS.md`, `AGENT.md`, `CONTRIBUTING.md`, `internal/config/`.

## Security

- Validate and sanitise all external input before use.
- Keep secrets out of source, logs and commits; `app.env` must never be committed.
- Treat auth, payments (Stripe) and credential handling as sensitive — flag anything touching them.

## PR and commit instructions for agents

- Keep diffs minimal and focused on the ticket; do not drive-by refactor.
- Run the build and tests above before reporting completion; report the exact commands you ran.
- Do not touch protected files silently — call it out and wait for confirmation.
- Reference code as `path/to/file.go:line` so reviewers can navigate.
- Do not commit or push unless the user explicitly asks.

## References

- [`CONTRIBUTING.md`](./CONTRIBUTING.md) — bootcamp workflow, branch/PR process (source of truth).
- [`AGENT.md`](./AGENT.md) — detailed engineering standards and layering conventions.
- [`dev-setup.md`](./dev-setup.md) — local Docker setup and troubleshooting.
