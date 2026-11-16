# Kiban Dojo

Practise real IT work on a lab machine that runs on your own computer. Each kata is a broken
system and a ticket: you fix it from your own terminal, the way the job works.

Work in progress: the first pieces are landing one pull request at a time.

## Use

```sh
make build                 # builds bin/dojo
bin/dojo doctor            # checks that this computer can run the labs
bin/dojo list              # the available katas
bin/dojo start <kata>      # a fresh lab, broken for that kata
bin/dojo status            # the current kata and its brief
bin/dojo hint              # the next hint
bin/dojo shell             # a terminal inside the lab
bin/dojo check             # reboots the lab and verifies your fix (--quick skips the reboot)
bin/dojo clean             # deletes the lab
```

It needs [Lima](https://lima-vm.io/) on macOS or Linux.

## Develop

You need Go (the version in `go.mod`) and `make`.

```sh
make verify                        # lint for macOS, Linux and Windows, tests with -race, govulncheck
make e2e                           # needs Lima; boots a real VM and runs every kata end to end
git config core.hooksPath .githooks  # run the same gate before every push
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for how changes land.

## How this is built

Kiban tools are written with AI coding assistants. Every change is
reviewed by a maintainer, covered by tests (including integration
tests against real services), and released through the same CI gates.
If something looks wrong, open an issue: we read and answer them.
