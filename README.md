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
bin/dojo mcp               # the mentor server for your AI tool (see below)
```

It needs [Lima](https://lima-vm.io/) on macOS or Linux.

## Let your AI mentor you

`dojo mcp` is an [MCP](https://modelcontextprotocol.io/) server over stdio for your own AI tool.
It lets the AI read your current ticket, the output of your last `dojo check` and the hints you
already opened, so it can explain and point you to documentation. It is read-only and has no way
to reach a solution: the ticket text and the hints you opened are all it can see. It does not
start labs or run commands.

Claude Code:

```sh
claude mcp add dojo -- dojo mcp
```

Any other client that reads an `mcpServers` config (Cursor, VS Code with Copilot, and others):

```json
{
  "mcpServers": {
    "dojo": { "command": "dojo", "args": ["mcp"] }
  }
}
```

Use the full path to the binary if `dojo` is not on your `PATH`.

## Develop

You need Go (the version in `go.mod`) and `make`; the web and API stack also needs Docker with
Compose and the Node version in `web/.nvmrc`.

```sh
make dev                           # the stack plus the web dev server on http://localhost:4321
make down                          # stops it, keeping the data
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
