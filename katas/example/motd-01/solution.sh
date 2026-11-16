#!/usr/bin/env bash
# Reference solution, used only by the end-to-end test. Never embedded in the binary.
set -euo pipefail

printf 'Welcome to the dojo\n' > /etc/motd
chown root:root /etc/motd
chmod 644 /etc/motd
