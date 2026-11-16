# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

### Added

- Build tooling: `make verify` (lint for macOS, Linux and Windows, tests with the race detector,
  govulncheck), CI, CodeQL, Dependabot and a pre-push hook.
- Local progress store for the CLI (`internal/state`).
- Kata format (`kata.yaml`, `setup.sh`, `check.sh`; `solution.sh` and `wrong-*.sh` for the
  end-to-end test), its loader (`internal/kata`, strict YAML) and an embedded catalog that never
  carries solutions.
- Lab backend interface and the Lima backend (`internal/lab`), plus `make e2e`, an end-to-end
  test that boots a real VM and runs every kata's solution and wrong fixes against its check.
- Example kata `example/motd-01`, licensed CC BY-NC-SA 4.0 like everything in `katas/`.
- The `dojo` command (`internal/cli`, `cmd/dojo`): `list`, `start`, `status`, `hint`, `shell`, `check` (with `--quick`) and `clean`, plus `make build`.
