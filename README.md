# Kiban Dojo

Practise real IT work on a lab machine that runs on your own computer. Each ticket is a broken
system and a task: you fix it from your own terminal, the way the job works.

Work in progress: the first pieces are landing one pull request at a time.

## Use

```sh
make build                 # builds bin/dojo
bin/dojo doctor            # checks that this computer can run the labs
bin/dojo start <role>/<ticket>  # a fresh lab, broken for that ticket; sets up "ssh dojo" (--key PATH picks the SSH key)
bin/dojo status            # the current ticket and its brief
bin/dojo hint              # the next hint
bin/dojo ssh               # a terminal inside the lab
bin/dojo check             # reboots the lab and verifies your fix (--quick skips the reboot)
bin/dojo stop              # stops the lab and deletes the lab machine
bin/dojo mcp               # the mentor server for your AI tool (see below)
bin/dojo login             # signs in with GitHub (device code); --force logs in again
bin/dojo logout            # revokes this computer's login; --local only forgets it here
bin/dojo telemetry         # on, off or status of the usage stats (see Telemetry)
```

It needs [Lima](https://lima-vm.io/) on macOS or Linux.

## Sign in

`dojo login` needs a Dojo account, made on the website first. It shows a code and a GitHub address:
open the address, enter the code and approve. The CLI then exchanges GitHub's answer for its own
token (GitHub's token is used once and never stored) and saves it in `DOJO_HOME` (`token`, mode 0600).
`dojo logout` revokes that token on the server and deletes the file; if the server cannot be reached
the file stays, and `dojo logout --local` forgets it on this computer anyway.

The API address and the OAuth client id have no production default until the public domain exists, so
set them (or pass `--api-url`, `--client-id`; `--github-url` defaults to `https://github.com`). Against
the local stack (`make dev`):

```sh
export DOJO_API_URL=http://localhost:4321      # the web dev server proxies /v1 to the API
export DOJO_GITHUB_URL=http://localhost:8081   # the fake GitHub
export DOJO_GITHUB_CLIENT_ID=dojo-web-local
bin/dojo login   # approve the code at http://localhost:8081/login/device
```

## Telemetry

The CLI shares usage stats, on by default. The first command you run prints a notice on
stderr saying so, and sends nothing; stats start with the next command.

What is sent, per command: its name (like `check`), its result (`ok`, `failed`, `error` or
`canceled`) and how long it took in milliseconds, plus the CLI version and your OS once per batch.
Never arguments, file names, paths, lab output or anything you type. Events are kept 12 months in
the EU. When you are logged in they are tied to your account; otherwise they are not tied to anyone. `dojo mcp` and
`dojo telemetry` are not counted. Nothing is sent until an API is configured (`DOJO_API_URL`).

Events wait in a small queue file in `DOJO_HOME` (`telemetry-queue.json`, mode 0600, at most 100) and
go out in batches of up to 20 when a command ends, within two seconds; a slow or failing API never
slows or fails the command.

```sh
dojo telemetry off      # never send, drop the queue; when logged in, also turns it off on your account
dojo telemetry on       # turn it back on (and on your account when logged in)
dojo telemetry status   # what it is now and what is sent
```

`dojo login` reads the account setting: if you turned usage stats off on the website, they are off
here too. `DOJO_TELEMETRY=off` or `DO_NOT_TRACK=1` in the environment turns them off for that
run whatever the setting says; the environment can only turn them off, never on.

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
make e2e                           # needs Lima; boots a real VM and runs every ticket end to end
git config core.hooksPath .githooks  # run the same gate before every push
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for how changes land.

## How this is built

Kiban tools are written with AI coding assistants. Every change is
reviewed by a maintainer, covered by tests (including integration
tests against real services), and released through the same CI gates.
If something looks wrong, open an issue: we read and answer them.
