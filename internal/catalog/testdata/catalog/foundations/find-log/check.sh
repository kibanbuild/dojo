#!/usr/bin/env bash
if [ "$(cat /root/log-path 2>/dev/null)" = /var/log/app/app.log ]; then
  echo "PASS  /root/log-path names the log"
else
  echo "FAIL  /root/log-path names the log"
  exit 1
fi
