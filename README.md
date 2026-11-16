# Kiban Dojo

Practise real IT work on a lab machine that runs on your own computer. Each kata is a broken
system and a ticket: you fix it from your own terminal, the way the job works.

Work in progress: the first pieces are landing one pull request at a time.

## Develop

You need Go (the version in `go.mod`) and `make`.

```sh
make verify                        # lint for macOS, Linux and Windows, tests with -race, govulncheck
git config core.hooksPath .githooks  # run the same gate before every push
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for how changes land.

## How this is built

Kiban tools are written with AI coding assistants. Every change is
reviewed by a maintainer, covered by tests (including integration
tests against real services), and released through the same CI gates.
If something looks wrong, open an issue: we read and answer them.
