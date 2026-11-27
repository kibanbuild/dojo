---
project:
  category: fast-money
  phase: 4
  phase_since: 2026-11-20
  done_line: "A first learner finishes Foundations and Support L1 and passes the Support L1 work-day exam on a hosted machine, by 2027-03-31"
  waiting: "the first learner (FP ASIR/SMR students)"
  monthly_cost: 0
  board: "https://github.com/orgs/kibanbuild/projects/2"
  next_gate: "M2: tickets on local labs mirrored on the web (M1 sign-in and operator skeleton done 2026-11-23)"
  close_if: "no learner passes Support L1 by 2027-03-31, or local labs fail on typical student PCs and hosted machines cost more than the budget allows"
---

# dojo

CLI that runs Kiban Dojo katas on a local lab VM (Lima on macOS/Linux, WSL2 on Windows).

- Gate: `make verify` (same as CI). Real-VM tests: `make e2e` (needs Lima, boots a real VM, not in `verify`; CI runs it in `Go: E2E`).
- Small PRs, one concern each, one open at a time; the next branches from the new `main`.
- `main` is protected by a ruleset: PRs only, squash, signed commits, linear history.

## Workflows

- One file per component and purpose: `<component>-<purpose>.yml`, named `'<Component>: <Purpose>'`
  (`go-tests.yml` is `Go: Tests`, a future `web-tests.yml` is `Web: Tests`). `CI:` audits the
  workflows themselves (zizmor, actionlint); `Tools:` names repo-wide tools (gitleaks, PR title).
- Setup steps are inline for now. A shared `.github/actions/<component>-setup` composite returns once
  actionlint accepts the `$/` self-repository syntax that zizmor asks for (actionlint 1.7.12 rejects it).
- `permissions: {}` at the top, the minimum per job; `if: github.repository == 'kibanbuild/dojo' || github.repository == 'kibanyadev/dojo'` (the kibanbuild half is removed once the transfer to kibanyadev is done);
  Harden Runner with `egress-policy: block` on Linux jobs; actions pinned by SHA with the tag in a comment.
- Job `name:` values are the required checks in the `main` ruleset: renaming a job means updating it.
- Required checks cannot be path-filtered: a skipped required check blocks the PR forever. When a
  second component arrives, skip steps inside the job when its files did not change, using our own
  `git diff` (not tj-actions/changed-files, compromised in March 2025), never `on.paths`.
