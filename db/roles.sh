#!/bin/sh
# Turns on login for the service roles and sets their passwords from the environment.
# Locally the migration role is the superuser, which may change log_statement; AWS is settled in M0b.
set -eu
: "${DOJO_API_DB_PASSWORD:?DOJO_API_DB_PASSWORD is empty}"
: "${DOJO_ADMIN_DB_PASSWORD:?DOJO_ADMIN_DB_PASSWORD is empty}"

# \getenv reads the passwords inside psql, so they never appear in process arguments.
psql -v ON_ERROR_STOP=1 --no-psqlrc <<'SQL'
SET log_statement = 'none';
SET log_min_error_statement = 'panic';
\getenv api_pw DOJO_API_DB_PASSWORD
\getenv admin_pw DOJO_ADMIN_DB_PASSWORD
ALTER ROLE dojo_api LOGIN PASSWORD :'api_pw';
ALTER ROLE dojo_admin LOGIN PASSWORD :'admin_pw';
SQL
