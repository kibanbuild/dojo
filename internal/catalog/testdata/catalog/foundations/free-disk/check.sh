#!/usr/bin/env bash
if [ ! -e /var/tmp/big.bin ]; then
  echo "PASS  /var/tmp/big.bin is gone"
else
  echo "FAIL  /var/tmp/big.bin is gone"
  exit 1
fi
