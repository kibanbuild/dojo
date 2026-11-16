#!/usr/bin/env bash
# Verifies the final state. Runs as root inside the lab VM, after a reboot.
# Prints one PASS/FAIL line per requirement; exits non-zero if any fails.
set -uo pipefail

fails=0
pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1"; fails=$((fails + 1)); }

# One line, with or without a trailing newline (grep -c counts a last line without one).
if [ "$(cat /etc/motd 2>/dev/null)" = "Welcome to the dojo" ] && [ "$(grep -c '' /etc/motd)" -eq 1 ]; then
  pass "/etc/motd says exactly: Welcome to the dojo"
else
  fail "/etc/motd says exactly: Welcome to the dojo"
fi

mode=$(stat -c %a /etc/motd 2>/dev/null || echo 0)
if [ "$(stat -c %U /etc/motd 2>/dev/null)" = root ] && (( (8#$mode & 0444) == 0444 && (8#$mode & 0022) == 0 && (8#$mode & 07000) == 0 )); then
  pass "everyone can read /etc/motd, only root can change it"
else
  fail "everyone can read /etc/motd, only root can change it"
fi

exit $(( fails > 0 ))
