#!/usr/bin/env bash
if [ "$(stat -c %G /srv/shared)" = staff ] && [ -w /srv/shared ]; then
  echo "PASS  staff can write to /srv/shared"
else
  echo "FAIL  staff can write to /srv/shared"
  exit 1
fi
