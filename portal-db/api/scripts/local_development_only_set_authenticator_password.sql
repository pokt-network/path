-- ============================================================================
-- Set Authenticator Password for Local Development
-- ============================================================================
-- This script sets the password for the PostgREST authenticator role.
-- This is separate from the role creation to follow the principle of 
-- configuring passwords outside of the main schema migrations.
--
-- See this file details on Postgres schema authentication and authorization: 
-- https://github.com/pokt-network/path/blob/main/portal-db/schema/002_postgrest_init.sql
--
-- ⚠️  LOCAL DEVELOPMENT ONLY - NOT FOR PRODUCTION USE
-- 
-- In production environments, database passwords should be managed through:
-- - Environment-specific secrets management (e.g., Kubernetes secrets)
-- - Cloud provider secret managers (e.g., GCP Secret Manager, AWS Secrets Manager)
-- - Proper credential rotation and security policies
--
-- This script is only intended for local development convenience.
-- ============================================================================

-- Set password for the authenticator role (local development only)
ALTER ROLE authenticator WITH PASSWORD 'authenticator_password';

-- Verify the role exists and has the correct configuration
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticator') THEN
        RAISE EXCEPTION 'authenticator role does not exist';
    END IF;
    
    RAISE NOTICE 'LOCAL DEV: authenticator role password has been set for local development';
    RAISE NOTICE 'LOCAL DEV: ⚠️  This configuration is for LOCAL DEVELOPMENT ONLY';
END
$$;
