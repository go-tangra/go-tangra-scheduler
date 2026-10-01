-- +goose Up
-- Server-side list sorting (go-tangra specs/032-server-side-tables): the
-- history table filters and sorts by status within a tenant. Task sorts are
-- served by the existing (tenant_id, lower(name)) / (tenant_id, updated_at)
-- indexes and the small per-tenant task counts.
CREATE INDEX IF NOT EXISTS scheduler_executions_status ON scheduler_executions (tenant_id, status, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS scheduler_executions_status;
