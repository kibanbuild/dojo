# Example catalog

This folder holds an example catalog that shows the format. The real catalog lives in a private
repository, because a public setup or solution would let learners pass without learning.

`catalog.yaml` at the root lists the `paths`, `roles` and `path_roles` (which roles a path has, in
what order). A ticket is a directory `<role>/<ticket>/`:

| File | Purpose |
| --- | --- |
| `ticket.yaml` | id, title, role, order, est_minutes, tags, the task statement (`brief`) and 1 to 3 `hints`; unknown keys are an error |
| `setup.sh` | breaks the lab; runs as root in a fresh lab |
| `check.sh` | verifies the final state after a reboot; prints one `PASS` or `FAIL` line per requirement and exits non-zero if anything fails |
| `files/` | optional extra files for the lab, shipped in the ticket bundle |
| `solution.sh` | reference fix for the end-to-end test (it never ships) |
| `wrong-fix/*.sh` | plausible but wrong fixes the end-to-end test makes sure the check rejects |

Only `catalog.yaml`, `ticket.yaml`, `setup.sh` and `check.sh` are embedded in the `dojo` binary.
Solutions and wrong fixes never are, and a ticket bundle holds only `ticket.yaml`, `setup.sh`,
`check.sh` and `files/`. The rules and limits are in `docs/technical-design.md`, section 3.8, and
are enforced by `internal/catalog`.

Everything under `files/` is shipped byte for byte, so the catalog repo must commit it with LF line
endings. Put this in the catalog repo's `.gitattributes`:

```
* text=auto eol=lf
```

`ticket.yaml`, `setup.sh` and `check.sh` are normalised to LF when the bundle is built. A file name
under `files/` must be printable ASCII and must not differ from another only by letter case, and
nothing under `files/` may start with `wrong` or `solution`: the build refuses them.

Checks should test behaviour where they can (can this user create a file here?) rather than one
exact answer (is the mode 2770?), so any correct fix passes. The lab runs Ubuntu 26.04, whose Rust
coreutils `test` ignores supplementary groups: run permission checks as another user through
bash's builtin `test`.

The content in this folder is licensed under CC BY-NC-SA 4.0 (see `LICENSE`).
