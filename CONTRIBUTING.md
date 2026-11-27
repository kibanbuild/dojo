# Contributing

Thanks for helping. Katas, fixes and ideas are all welcome.

## How changes land

- Open an issue first for anything bigger than a small fix, so we can agree on the shape.
- Keep pull requests small: one concern each, readable in 10 to 15 minutes. Tests travel with the
  code they cover.
- `main` only changes through pull requests, merged by squash. Commits must be signed (SSH or
  GPG); see [GitHub's guide](https://docs.github.com/en/authentication/managing-commit-signature-verification).
- Commit messages and PR titles follow [Conventional Commits](https://www.conventionalcommits.org):
  `feat(cli): ...`, `fix(kata): ...`, `chore(ci): ...`.
- Run `make verify` before pushing. `git config core.hooksPath .githooks` does it for you.
- `make e2e` boots a real Lima VM (minutes, needs Lima installed) and is not part of `verify`.
  It reads the catalog tree (`catalog.yaml` plus `<role>/<ticket>/ticket.yaml`) from `katas/`, or from the directory in `DOJO_KATAS`.
  CI runs it in the `Go: E2E` workflow.

## API and local stack

The API (`cmd/dojo-api`, code in `internal/server/`) is a separate binary from the CLI. `compose.yaml`
runs Postgres, a one-off `migrate` job (goose, SQL in `db/migrations/`) and the API; more pieces join it
as milestones land. Needs Docker with Compose.
The migrations create two `NOLOGIN` roles, `dojo_api` and `dojo_admin`; a one-off `db-roles` job then
turns on login and sets their passwords from `DOJO_API_DB_PASSWORD` and `DOJO_ADMIN_DB_PASSWORD`. Every
new table migration must grant to both roles explicitly. The API connects as `dojo_api`, and every
table PR adds its grant rows (allowed and refused) to `grantCases` in `internal/server/grants_integration_test.go`.

- `make test-integration` starts that stack as the isolated project `dojo-it` from a clean state, runs
  the tests tagged `integration`, and always removes the containers and volumes afterwards. It uses the
  fixed local values in `.env.example`, so a clean checkout needs no `.env`.
- `make prune` runs the nightly job (`db/jobs/nightly.sql`: telemetry and audit retention) once against the dev stack, as the
  migration role; the AWS schedule comes in M0b.
- `make dev` is the everyday loop: it starts the stack in the background (`docker compose up`, project
  `dojo`), then the Astro dev server on `http://localhost:4321` with live reload. That server proxies
  `/v1/*` to the edge, so the page calls the API on its own origin, as in production. It needs the Node
  version in `web/.nvmrc` (`fnm install && fnm use` picks it up). `compose.dev.yaml` (loaded only by
  `make dev`) sets `PUBLIC_ORIGIN=http://localhost:4321` and registers that callback on the fake GitHub
  client, so sign-in works from the Astro origin; the integration run keeps `:8080`. Ctrl-C stops only the dev server;
  `make down` stops the stack and keeps
  the database volume; `docker compose down -v` also drops the data. A local `.env`, when present,
  replaces the fixed values.
- To run the stack by hand, `cp .env.example .env` and `docker compose up -d --build --wait`. The API
  listens on a free port on `127.0.0.1` (`docker compose port api 8080`); set `API_PORT` to pin it.
  Postgres is not published to the host.
- The operator service (`cmd/dojo-admin`, code in `internal/admin/`, ADR 0002) is the `admin` compose service,
  published only on `127.0.0.1:8090` and never behind Caddy. It answers only requests whose `Host` is
  `127.0.0.1:8090` (421 otherwise); set `ADMIN_PORT` to move the host port. `make dev` and
  `make test-integration` start it. A test in `internal/admin` fails if `cmd/dojo-api` imports it.
- Object storage (ticket bundles) is the `s3` compose service, an S3-compatible RustFS container, with the private
  bucket `dojo-catalog` created by the one-off `s3-init`. Code goes through `internal/storage` (`Put`, `PresignGet`,
  `Stat`), which also talks to real S3 in `pre` and `pro`. Config, all `DOJO_`-prefixed because GitHub Actions
  exports unprefixed generic names into compose:
  `DOJO_S3_ENDPOINT` (what the process connects to; empty means AWS), `DOJO_S3_PUBLIC_ENDPOINT` (what presigned
  URLs carry; must be reachable by whoever downloads, so `http://localhost:9000` locally while the API uses
  `http://s3:9000`), `DOJO_S3_REGION`, `DOJO_S3_BUCKET`, `DOJO_S3_ACCESS_KEY` and `DOJO_S3_SECRET_KEY` (both or
  neither; neither means the task role), `DOJO_S3_PATH_STYLE` (true for the stand-in). Local defaults exist for all
  of them; `pre` and `pro` require bucket and region and https endpoints. The host port is loopback only
  (`DOJO_S3_PORT`, default 9000; the integration run takes a random one). Change the port and the public endpoint together.
- Email is the `mailpit` compose service (Mailpit: SMTP in, web inbox out), standing in for Amazon SES; the sender in
  `internal/mail` speaks SMTP to either. The API reads `DOJO_SMTP_HOST` (empty turns the sender off), `DOJO_SMTP_PORT`,
  `DOJO_SMTP_USERNAME` and `DOJO_SMTP_PASSWORD` (credentials require STARTTLS) and `DOJO_EMAIL_DOMAIN` (mail goes out as
  `carmen@` that domain); compose sets Mailpit and `dojo.test`. Read the mail at the web inbox on `http://localhost:8025`
  (`MAILPIT_PORT` moves it; the integration run takes a random port). A new learner's welcome mail (E1) shows up there
  within a few seconds of the first sign-in. Learner reports (`POST /v1/reports`) are forwarded to `DOJO_REPORTS_TO`
  (`reports@dojo.test` in compose; required in `pre` and `pro`).
- `make admin-bootstrap LOGIN=ada` makes that GitHub login the first operator of the dev stack, so you can
  sign in to the operator service locally (the fake GitHub knows `ada`, `bob` and `cy`; sign-in needs a
  user with two-factor on: `ada` or `cy`, not `bob`). It runs `dojo-admin bootstrap` as a one-off with the migration
  role and refuses once an active operator exists. To start over, `docker compose down -v` drops the data.
- The single local origin is Caddy (`edge`) on `http://localhost:8080`: `/v1/*` goes to the API,
  everything else to the built site. `make test-integration` also checks both through it.
- Database access goes through sqlc: after editing `db/migrations/` or `internal/server/repo/queries/`,
  run `make sqlc` and commit the result. `make verify` fails when the generated code is stale.

## Web

The public site lives in `web/` (Astro, static output, Tailwind 4). It uses pnpm only, with exact
versions, install scripts off and a committed lockfile; every new dependency needs a reason in its PR.

- With the Node version in `web/.nvmrc`: `cd web && corepack enable && pnpm install --frozen-lockfile && pnpm build`.
  `make dev` (above) runs the dev server with the API behind it; the stack bakes the build into the
  `edge` image.
- `pnpm audit --audit-level high` and OSV-Scanner on `web/pnpm-lock.yaml` run in the `Web: Tests` workflow.

## AI-assisted contributions

AI-assisted pull requests are welcome if you understand the change and can explain it in review.
Please don't open bulk or unreviewed pull requests. Security reports must come with steps that
reproduce the problem.

## Reporting security issues

See [SECURITY.md](SECURITY.md). Please don't open public issues for vulnerabilities.
