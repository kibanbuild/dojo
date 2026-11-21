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
  It reads the catalog from `katas/`, or from the directory in `DOJO_KATAS`.
  CI runs it in the `Go: E2E` workflow.

## API and local stack

The API (`cmd/dojo-api`, code in `internal/server/`) is a separate binary from the CLI. `compose.yaml`
runs Postgres, a one-off `migrate` job (goose, SQL in `db/migrations/`) and the API; more pieces join it
as milestones land. Needs Docker with Compose.
The migrations create two `NOLOGIN` roles, `dojo_api` and `dojo_admin`; a one-off `db-roles` job then
turns on login and sets their passwords from `DOJO_API_DB_PASSWORD` and `DOJO_ADMIN_DB_PASSWORD`. Every
new table migration must grant to both roles explicitly.

- `make test-integration` starts that stack as the isolated project `dojo-it` from a clean state, runs
  the tests tagged `integration`, and always removes the containers and volumes afterwards. It uses the
  fixed local values in `.env.example`, so a clean checkout needs no `.env`.
- `make dev` is the everyday loop: it starts the stack in the background (`docker compose up`, project
  `dojo`), then the Astro dev server on `http://localhost:4321` with live reload. That server proxies
  `/v1/*` to the edge, so the page calls the API on its own origin, as in production. It needs the Node
  version in `web/.nvmrc` (`fnm install && fnm use` picks it up). Ctrl-C stops only the dev server;
  `make down` stops the stack and keeps
  the database volume; `docker compose down -v` also drops the data. A local `.env`, when present,
  replaces the fixed values.
- To run the stack by hand, `cp .env.example .env` and `docker compose up -d --build --wait`. The API
  listens on a free port on `127.0.0.1` (`docker compose port api 8080`); set `API_PORT` to pin it.
  Postgres is not published to the host.
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
