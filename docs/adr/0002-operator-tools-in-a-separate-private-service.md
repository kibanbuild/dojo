# ADR 0002: Operator tools run in a separate private service with its own database role

Status: accepted, 2026-11-21. Decided by the project on 2026-11-21, with security as the first
criterion. Replaces the review queue at `/review` in the public app and the "`dojo-admin`
commands run with database access" of the first technical design.

## Context

Operators confirm exam passes, resolve appeals, look up a learner, comp hours or Supporter, void
an exam attempt, suspend or delete an account, and switch `system_flags`. Each of these
changes something a learner or an attacker would want changed: a role pass, a plan, a cap, the
pass gate that stands between the rubric reviewer and prompt injection.

The first design put the review queue in the public SPA behind `learners.is_operator`, and every
other task in a command-line tool with database access. That leaves three weaknesses:

- Operator code ships in the public API binary and its routes are reachable from the internet. A
  single authorization bug, or a stolen operator session, is enough.
- The public API's database user can do everything an operator can. Any SQL injection or logic
  bug in the API can set `is_operator`, publish a grade, or switch off the review of passes.
- A command-line tool with direct database access leaves no record of who did what and why.

## Decision

1. **Separate binary.** `cmd/dojo-admin` in this repo, reusing `internal/server/service`. No
   operator route, handler or template is compiled into `cmd/dojo-api`, and no admin UI is in
   the public SPA bundle. The admin UI is server-rendered HTML from the admin binary.
2. **Own database role.** Migrations create `dojo_api` (the public API) and `dojo_admin` with
   column-level `INSERT` and `UPDATE` grants. `dojo_api` cannot write the operator list, the audit
   log, comp grants, grade overrides, appeal decisions, role passes, the publication columns of an
   exam attempt or `system_flags`, and a trigger stops it moving an attempt into `published` or
   `voided`. Those writes happen only inside `SECURITY DEFINER` functions. `dojo_api` may call
   four of them: one publishes a grade and refuses a pass while `pass_review_required` is on, one
   trips the monthly AI spend cap, one deletes an account (the S1 path), one marks a queued email
   sent. This is the
   real gain: a compromised public API cannot promote itself, write a role pass or move a grade
   past review.
3. **Off the internet.** The admin service runs in a private subnet with no load balancer, is not
   an origin of CloudFront, and is reached only through an ECS Exec port-forward over SSM (IAM with
   MFA, the port-forwarding document only, `ecs:ExecuteCommand` denied so no shell). The edge
   answers a real 404 for `/admin*` and `/review`. ECS desired count is 0 and is raised
   only while it is in use. Locally it is one more compose service bound to `127.0.0.1`.
4. **Identity checked on every request.** GitHub sign-in through its own OAuth App, an
   `operators` table the API cannot read, admin sessions that expire after one hour with no
   sliding, 2FA required on the GitHub account: the `two_factor_authentication` field of
   `GET /user` must be `true`, or sign-in is refused (technical design section 3.7).
5. **Every action audited.** Destructive actions need a confirm step and a written reason. Every
   operator action writes an append-only `admin_audit` row (who, when, what, why, before and
   after) in the same transaction as the change.

Tailscale was the alternative to SSM. SSM won: it needs no new vendor, uses the IAM and MFA we
already have and keeps the project all-in on AWS.

## Consequences

- One more service, one more image and one more OAuth App. It costs nothing while scaled to 0.
- Operator work needs the AWS CLI, an MFA session and a port-forward first. That friction is
  intended. The operator service sends no email and cannot reach the runner: it queues mail in an
  outbox the API sends, and machine teardown through the existing teardown jobs.
- Grants must be kept in step with every new table: each migration states what `dojo_api` and
  `dojo_admin` may do on it, and an integration test asserts that `dojo_api` is refused on the
  operator-only writes.
- Billing tables stay writable by `dojo_api`, because Stripe webhooks land there. A compromised
  API could still forge an hour pack. Comps go to a separate admin-only table so they are told
  apart and audited.
- Reversing this (operator screens back in the public app) is easy; pulling operator code out of a
  public binary and splitting a shared database user later is not. That is why it is decided now.
