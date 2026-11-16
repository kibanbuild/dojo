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

## AI-assisted contributions

AI-assisted pull requests are welcome if you understand the change and can explain it in review.
Please don't open bulk or unreviewed pull requests. Security reports must come with steps that
reproduce the problem.

## Reporting security issues

See [SECURITY.md](SECURITY.md). Please don't open public issues for vulnerabilities.
