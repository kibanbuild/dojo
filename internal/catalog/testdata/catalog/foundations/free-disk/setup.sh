#!/usr/bin/env bash
set -eu
head -c 1048576 /dev/zero > /var/tmp/big.bin
