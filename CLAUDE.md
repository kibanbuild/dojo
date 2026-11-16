---
project:
  category: open-source
  phase: 1
  phase_since: 2026-11-16
  done_line: "A first learner installs dojo, fixes white/permissions-01 from the README alone and sees it pass"
  waiting: "the first learner"
  monthly_cost: 0
  next_gate: define
  close_if: "local labs are too heavy for typical student PCs and Codespaces cannot cover the gap"
---

# dojo

CLI that runs Kiban Dojo katas on a local lab VM (Lima on macOS/Linux, WSL2 on Windows).

- Gate: `make verify` (same as CI). Real-VM tests: `make e2e` once the lab backend lands.
- Small PRs, one concern each, one open at a time; the next branches from the new `main`.
- `main` is protected by a ruleset: PRs only, squash, signed commits, linear history.

## Workflows

- One file per component and purpose: `<component>-<purpose>.yml`, named `'<Component>: <Purpose>'`
  (`go-tests.yml` is `Go: Tests`, a future `web-tests.yml` is `Web: Tests`). Repo-wide checks use
  `ci-*`, `pr-*` and `find-secrets`.
- `permissions: {}` at the top, the minimum per job; `if: github.repository == 'kibanbuild/dojo'`;
  Harden Runner with `egress-policy: block` on Linux jobs; actions pinned by SHA with the tag in a comment.
- Job `name:` values are the required checks in the `main` ruleset: renaming a job means updating it.
- Required checks cannot be path-filtered: a skipped required check blocks the PR forever. When a
  second component arrives, filter inside the job (or add a small "changes" job), not with `on.paths`.
