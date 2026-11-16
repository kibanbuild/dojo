#!/usr/bin/env bash
# Breaks the system for this kata. Runs as root inside the lab VM.
set -euo pipefail

printf 'Welcom to teh dojo\n' > /etc/motd
chmod 666 /etc/motd
