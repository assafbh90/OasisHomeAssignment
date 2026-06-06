DROP FUNCTION IF EXISTS find_user_for_login(TEXT);
DROP FUNCTION IF EXISTS find_api_token_by_hash(BYTEA);

DROP TABLE IF EXISTS created_tickets;
DROP TABLE IF EXISTS integration_credentials;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;

DROP FUNCTION IF EXISTS app_current_tenant();
DROP TYPE IF EXISTS connection_status;

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'identityhub_app') THEN
        EXECUTE 'DROP OWNED BY identityhub_app';
        DROP ROLE identityhub_app;
    END IF;
END $$;
