#!/bin/sh
# Runs once on first Postgres start (docker-entrypoint-initdb.d). Creates the
# least-privilege application login role the backend connects as, so RLS applies
# (the superuser used by migrations bypasses RLS). The password comes from the
# environment — never committed. Table/function GRANTs are applied by the
# migrations (which run as the superuser).
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    DO \$do\$
    BEGIN
        IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'identityhub_app') THEN
            CREATE ROLE identityhub_app LOGIN PASSWORD '${APP_DB_PASSWORD}';
        ELSE
            ALTER ROLE identityhub_app LOGIN PASSWORD '${APP_DB_PASSWORD}';
        END IF;
    END
    \$do\$;
EOSQL
