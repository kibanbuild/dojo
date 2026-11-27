# Plan: v1

Done-line: a first learner finishes Foundations and Support L1 and passes the Support L1 work-day
exam on a hosted machine, by 2027-03-31.

The design is `docs/technical-design.md`; its section 8 describes each milestone. This file holds
the order, the target dates and the status. Tracked on the [kibanyadev GitHub Project](https://github.com/orgs/kibanyadev/projects/3).

## How we work

- Small PRs: one concern each, one open at a time, tests and docs in the same PR. Each goes through
  a builder, a review and the merge gate (CI green, review PASS, branch clean).
- Everything is built and tested on the local stack first: one `docker compose up` runs every
  piece, with a local stand-in for each external service. Integration tests run against that
  stack, never mocks.
- AWS comes after the local build (M0b), before launch.

## Before M0a

- Land the CLI work in flight: keep the last check output and write the mentor files into the
  lab (6d), then `dojo mcp` (6e).
- WSL2 backend (7) after a test on a real Windows PC; needed before onboarding (M3).

## Build track

| # | Milestone | Target | Status |
|---|---|---|---|
| M0a | Local hello world: compose (Caddy, web, API `/v1/health`, Postgres, migrations), CI | 2026-11-29 | done |
| M1 | Sign-in: GitHub web and device flow (fake GitHub locally), `/v1/me`, CLI telemetry, consent; operator service skeleton: DB roles, admin sign-in, audit (ADR 0002) | 2026-12-13 | |
| M2 | Tickets on local labs mirrored on the web: catalog, bundles, CLI verbs, SSE, Today, Path, ticket page with written hints, public pages; operator A2 learner lookup | 2026-12-27 | |
| M3 | Onboarding O0 to O5, `dojo doctor` checks, welcome email (Mailpit) | 2027-01-03 | |
| M4 | Between tickets: writing tasks, quizzes, simulated feedback round, practice, inbox, belt marks, PWA | 2027-01-17 | |
| M5a | Runner and pool on the local container backend, queue, H1 | 2027-01-24 | |
| M5b | SSH gateway, browser terminal, hosted hours and quotas; operator A3 system switches | 2027-01-31 | |
| M6a | Exam clock, work-day screens and states, submit, results, role pass, track record; operator void | 2027-02-14 | |
| M6b | Checker spike, outside-the-guest checks, rubric, passes pending review, operator A1 review queue, appeals | 2027-02-28 | |
| M7 | AI hint button on the local AI stand-in, caps and output guard | 2027-03-07 | |
| M8 | Stripe in test mode (live keys after the tax decision and the GDPR review); operator comps | 2027-03-14 | |
| M0b | AWS: Organization with two accounts, OpenTofu, OIDC, CloudFront, ECS, RDS, `pre`, the private operator service, CLI installer, EC2 backend and isolation test, Bedrock and SES region checks, spend alarms | 2027-03-21 | |
| M9 | Launch: `pro`, domain, SES production access, legal text, live Stripe, a11y and UX fixes, first school | 2027-03-28 | |

## Content track (in parallel)

| # | Content | Needed by |
|---|---|---|
| C1 | Foundations tickets and the first Support L1 tickets, each with a wrong-fix test | M2 |
| C2 | All 12 Support L1 tickets, writing tasks, quizzes, the feedback round | M4 |
| C3 | The exam pool (four parts, variants per attempt) and the rubrics | M6a |

## Risks

- Content volume, not code, sets the date.
- The first real learner should start Foundations by mid-February 2027 to finish in time.
- December holidays fall between M2 and M3.
- M0b is late by choice: AWS surprises (quotas, Bedrock region, SES access) land close to launch.
  Keep its scope to what the local stack already proves.
