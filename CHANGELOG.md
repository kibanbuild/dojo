# Changelog

All notable changes are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

### Added

- Build tooling: `make verify` (lint for macOS, Linux and Windows, tests with the race detector,
  govulncheck), CI, CodeQL, Dependabot and a pre-push hook.
- Local progress store for the CLI (`internal/state`).
