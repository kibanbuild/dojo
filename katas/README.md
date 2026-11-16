# Example katas

This folder holds example katas that show the format. The real catalog lives in a private
repository, because a public setup or solution would let learners pass without learning.

A kata is a directory `<belt>/<name>/`:

| File | Purpose |
| --- | --- |
| `kata.yaml` | id, title, belt, summary, the ticket (`brief`) and `hints` |
| `setup.sh` | breaks the lab; runs as root in a fresh lab |
| `check.sh` | verifies the final state after a reboot; prints one `PASS` or `FAIL` line per requirement and exits non-zero if anything fails |
| `solution.sh` | reference fix for the end-to-end test (it never ships in the binary) |
| `wrong-*.sh` | plausible but wrong fixes the end-to-end test makes sure the check rejects |

Only `kata.yaml`, `setup.sh` and `check.sh` are embedded in the `dojo` binary. Solutions and wrong
fixes never are.

Checks should test behaviour where they can (can this user create a file here?) rather than one
exact answer (is the mode 2770?), so any correct fix passes. The lab runs Ubuntu 26.04, whose Rust
coreutils `test` ignores supplementary groups: run permission checks as another user through
bash's builtin `test`.

The content in this folder is licensed under CC BY-NC-SA 4.0 (see `LICENSE`).
