#!/usr/bin/env bash
# A plausible but wrong fix: the message is right, the permissions are left as they were.
set -euo pipefail

printf 'Welcome to the dojo\n' > /etc/motd
